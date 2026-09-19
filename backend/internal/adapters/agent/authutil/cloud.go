package authutil

import (
	"context"
	"crypto/rsa"
	"crypto/sha1" // #nosec G505 -- AWS defines SSO cache filenames using SHA-1, not as a security primitive.
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CloudCredential is private credential material returned by an injected chain
// loader. Consumers must never log this value or serialize it into a response.
// Either a token or a complete access-key pair establishes configured evidence.
type CloudCredential struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Token           string
	ExpiresAt       time.Time
}

func configured(source string) Evidence {
	return Evidence{Status: ports.AgentAuthStatus("configured"), Source: source}
}

func unknown() Evidence { return Evidence{Status: ports.AgentAuthStatusUnknown} }

func hasValues(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func chainEvidence(ctx context.Context, d Dependencies, loader func(context.Context) (CloudCredential, error), source string) Evidence {
	if loader == nil || ctx.Err() != nil {
		return unknown()
	}
	probeCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	credential, err := loader(probeCtx)
	if err != nil || probeCtx.Err() != nil {
		return unknown()
	}
	if !credential.ExpiresAt.IsZero() && !credential.ExpiresAt.After(d.now()) {
		return unknown()
	}
	if hasValues(credential.Token) || hasValues(credential.AccessKeyID, credential.SecretAccessKey) {
		return configured(source)
	}
	return unknown()
}

func (d Dependencies) home() string {
	if d.goos() == "windows" && d.getenv("USERPROFILE") != "" {
		return d.getenv("USERPROFILE")
	}
	return d.getenv("HOME")
}

// AWSEvidence inspects local default-chain inputs and, when supplied, a bounded
// chain loader for role/metadata/refresh credentials. It never validates a
// provider entitlement or contacts metadata by default.
func AWSEvidence(ctx context.Context, d Dependencies) Evidence {
	if ctx.Err() != nil {
		return unknown()
	}
	if hasValues(d.getenv("AWS_BEARER_TOKEN_BEDROCK")) {
		return configured("aws-bearer-token")
	}
	if hasValues(d.getenv("AWS_ACCESS_KEY_ID"), d.getenv("AWS_SECRET_ACCESS_KEY")) {
		return configured("aws-environment")
	}
	if hasValues(d.getenv("AWS_ROLE_ARN"), d.getenv("AWS_WEB_IDENTITY_TOKEN_FILE")) && nonemptyFile(ctx, d, d.getenv("AWS_WEB_IDENTITY_TOKEN_FILE")) {
		return configured("aws-web-identity")
	}
	profile := d.getenv("AWS_PROFILE")
	if profile == "" {
		profile = d.getenv("AWS_DEFAULT_PROFILE")
	}
	if profile == "" {
		profile = "default"
	}
	credentialsPath, configPath := d.getenv("AWS_SHARED_CREDENTIALS_FILE"), d.getenv("AWS_CONFIG_FILE")
	if root := d.home(); root != "" {
		if credentialsPath == "" {
			credentialsPath = filepath.Join(root, ".aws", "credentials")
		}
		if configPath == "" {
			configPath = filepath.Join(root, ".aws", "config")
		}
	}
	credentials := readINI(ctx, d, credentialsPath)
	config := readINI(ctx, d, configPath)
	if awsProfile(ctx, d, profile, credentials, config, make(map[string]bool)) {
		return configured("aws-profile")
	}
	return chainEvidence(ctx, d, d.LoadAWS, "aws-chain")
}

func awsProfile(ctx context.Context, d Dependencies, profile string, credentials, config map[string]map[string]string, seen map[string]bool) bool {
	if seen[profile] || len(seen) >= 8 || ctx.Err() != nil {
		return false
	}
	seen[profile] = true
	section := profile
	if profile != "default" {
		section = "profile " + profile
	}
	values := make(map[string]string)
	for key, value := range config[section] {
		values[key] = value
	}
	for key, value := range credentials[profile] {
		values[key] = value
	}
	if hasValues(values["aws_access_key_id"], values["aws_secret_access_key"]) {
		return true
	}
	if hasValues(values["role_arn"], values["web_identity_token_file"]) && nonemptyFile(ctx, d, values["web_identity_token_file"]) {
		return true
	}
	if hasValues(values["role_arn"], values["source_profile"]) && awsProfile(ctx, d, values["source_profile"], credentials, config, seen) {
		return true
	}
	if !hasValues(values["sso_account_id"], values["sso_role_name"]) {
		return false
	}
	startURL, region, cacheKey := values["sso_start_url"], values["sso_region"], values["sso_start_url"]
	if session := values["sso_session"]; session != "" {
		startURL, region, cacheKey = config["sso-session "+session]["sso_start_url"], config["sso-session "+session]["sso_region"], session
	}
	if !hasValues(startURL, region, cacheKey, d.home()) {
		return false
	}
	hash := sha1.Sum([]byte(cacheKey)) // #nosec G401 -- Match AWS's existing cache filename; this hash does not authenticate data.
	path := filepath.Join(d.home(), ".aws", "sso", "cache", hex.EncodeToString(hash[:])+".json")
	var cache struct {
		AccessToken string
		ExpiresAt   string
		StartURL    string
		Region      string
	}
	if ReadJSON(ctx, d, path, &cache) != nil || !hasValues(cache.AccessToken) {
		return false
	}
	if cache.StartURL != "" && cache.StartURL != startURL {
		return false
	}
	if cache.Region != "" && cache.Region != region {
		return false
	}
	// Older AWS CLI caches use a literal UTC suffix in place of RFC3339's Z.
	expiryText := cache.ExpiresAt
	if strings.HasSuffix(expiryText, "UTC") {
		expiryText = strings.TrimSuffix(expiryText, "UTC") + "Z"
	}
	expiry, ok := ParseExpiry(expiryText)
	return ok && expiry.After(d.now())
}

// AWS INI parsing deliberately recognizes only selected sections and explicit
// keys. Credential processes and arbitrary values are never executed.
func readINI(ctx context.Context, d Dependencies, path string) map[string]map[string]string {
	result := make(map[string]map[string]string)
	if path == "" {
		return result
	}
	data, err := ReadFile(ctx, d, path)
	if err != nil {
		return result
	}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if result[section] == nil {
				result[section] = make(map[string]string)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			return nil
		}
		result[section][strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return result
}

func nonemptyFile(ctx context.Context, d Dependencies, path string) bool {
	if path == "" {
		return false
	}
	data, err := ReadFile(ctx, d, path)
	return err == nil && strings.TrimSpace(string(data)) != ""
}

// GoogleADCEvidence validates explicit ADC schemas in the configured and
// well-known files, falling back to an optional bounded chain loader.
func GoogleADCEvidence(ctx context.Context, d Dependencies) Evidence {
	if ctx.Err() != nil {
		return unknown()
	}
	paths := []string{d.getenv("GOOGLE_APPLICATION_CREDENTIALS")}
	if dir := d.getenv("CLOUDSDK_CONFIG"); dir != "" {
		paths = append(paths, filepath.Join(dir, "application_default_credentials.json"))
	} else if d.goos() == "windows" {
		if dir := d.getenv("APPDATA"); dir != "" {
			paths = append(paths, filepath.Join(dir, "gcloud", "application_default_credentials.json"))
		}
	} else if root := d.home(); root != "" {
		paths = append(paths, filepath.Join(root, ".config", "gcloud", "application_default_credentials.json"))
	}
	for _, path := range paths {
		if path != "" && validADC(ctx, d, path) {
			return configured("google-adc-file")
		}
	}
	return chainEvidence(ctx, d, d.LoadGoogleADC, "google-adc-chain")
}

func validADC(ctx context.Context, d Dependencies, path string) bool {
	var credential struct {
		Type             string
		PrivateKey       string `json:"private_key"`
		ClientEmail      string `json:"client_email"`
		TokenURI         string `json:"token_uri"`
		ClientID         string `json:"client_id"`
		ClientSecret     string `json:"client_secret"`
		RefreshToken     string `json:"refresh_token"`
		Audience         string
		SubjectTokenType string `json:"subject_token_type"`
		TokenURL         string `json:"token_url"`
		CredentialSource struct {
			File                    string
			URL                     string
			EnvironmentID           string `json:"environment_id"`
			RegionalVerificationURL string `json:"regional_cred_verification_url"`
			RegionURL               string `json:"region_url"`
		} `json:"credential_source"`
	}
	if ReadJSON(ctx, d, path, &credential) != nil {
		return false
	}
	switch credential.Type {
	case "service_account":
		return hasValues(credential.ClientEmail) && strings.Contains(credential.ClientEmail, "@") && validURL(credential.TokenURI) && validRSAPrivateKey(credential.PrivateKey)
	case "authorized_user":
		return hasValues(credential.ClientID, credential.ClientSecret, credential.RefreshToken)
	case "external_account":
		if !hasValues(credential.Audience, credential.SubjectTokenType) || !validURL(credential.TokenURL) {
			return false
		}
		source := credential.CredentialSource
		if source.File != "" {
			return nonemptyFile(ctx, d, source.File)
		}
		if source.EnvironmentID != "" {
			return source.EnvironmentID == "aws1" && validURL(source.URL) && validURL(source.RegionURL) && validURL(source.RegionalVerificationURL)
		}
		return validURL(source.URL)
	}
	return false
}

func validURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Hostname() != ""
}

func validRSAPrivateKey(value string) bool {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return false
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key.Validate() == nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return false
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	return ok && rsaKey.Validate() == nil
}

// AzureEvidence reads explicit Azure credential/identity configuration, then an
// optional chain loader or the official CLI token command through an explicitly
// supplied Run dependency. Defaults never execute a cloud CLI. A returned CLI
// token is configured evidence only; it is not a provider entitlement check.
func AzureEvidence(ctx context.Context, d Dependencies) Evidence {
	if ctx.Err() != nil {
		return unknown()
	}
	if hasValues(d.getenv("AZURE_OPENAI_API_KEY")) || hasValues(d.getenv("AZURE_API_KEY")) {
		return configured("azure-api-key")
	}
	if hasValues(d.getenv("AZURE_TENANT_ID"), d.getenv("AZURE_CLIENT_ID")) {
		if hasValues(d.getenv("AZURE_CLIENT_SECRET")) {
			return configured("azure-service-principal")
		}
		if nonemptyFile(ctx, d, d.getenv("AZURE_FEDERATED_TOKEN_FILE")) {
			return configured("azure-workload-identity")
		}
	}
	if (validURL(d.getenv("IDENTITY_ENDPOINT")) && hasValues(d.getenv("IDENTITY_HEADER"))) || (validURL(d.getenv("MSI_ENDPOINT")) && hasValues(d.getenv("MSI_SECRET"))) {
		return configured("azure-managed-identity")
	}
	if evidence := chainEvidence(ctx, d, d.LoadAzure, "azure-chain"); evidence.Status == ports.AgentAuthStatus("configured") {
		return evidence
	}
	if d.Run == nil {
		return unknown()
	}
	data, err := RunCommand(ctx, d, "az", "account", "get-access-token", "--resource", "https://cognitiveservices.azure.com/", "--output", "json")
	if err != nil {
		return unknown()
	}
	var token struct {
		AccessToken string
		ExpiresOn   any
		ExpiresUnix any `json:"expires_on"`
	}
	if json.Unmarshal(data, &token) != nil || !hasValues(token.AccessToken) {
		return unknown()
	}
	expiry, ok := ParseExpiry(token.ExpiresUnix)
	if !ok {
		expiry, ok = ParseExpiry(token.ExpiresOn)
	}
	if ok && expiry.After(d.now()) {
		return configured("azure-cli")
	}
	return unknown()
}
