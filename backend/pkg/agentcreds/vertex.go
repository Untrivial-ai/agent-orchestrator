package agentcreds

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// vertexScope is the OAuth scope a Vertex probe needs.
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

// googleTokenURL is where a signed JWT is exchanged for an access token.
const googleTokenURL = "https://oauth2.googleapis.com/token"

// vertexRequest builds the Vertex AI probe.
//
// Vertex publishes Claude through Anthropic's publisher namespace, so the
// model-listing endpoint is scoped to a project and region and returns
// Vertex-format IDs (claude-opus-4-5@20251101). As with Bedrock, the listing
// call is both the validation and the only correct catalog source.
func (v *Validator) vertexRequest(ctx context.Context, cred Credential) (requestSpec, error) {
	region := strings.TrimSpace(cred.Region)
	project := strings.TrimSpace(cred.Project)
	if region == "" || project == "" {
		return requestSpec{}, fmt.Errorf("agentcreds: Vertex needs a project and a region")
	}

	token := strings.TrimSpace(cred.Secret)
	switch cred.Kind {
	case KindGoogleAccessToken:
		// GOOGLE_OAUTH_ACCESS_TOKEN: already an access token.
	case KindGoogleServiceAccount:
		// A service-account key is not a bearer token. It must be signed into
		// a JWT and exchanged for one before anything can be probed.
		exchanged, err := v.googleAccessTokenFromServiceAccount(ctx, cred.Secret)
		if err != nil {
			return requestSpec{}, err
		}
		token = exchanged
	default:
		return requestSpec{}, fmt.Errorf("agentcreds: credential kind %q cannot authenticate to Vertex", cred.Kind)
	}
	if token == "" {
		return requestSpec{}, fmt.Errorf("agentcreds: no Vertex access token from %s", cred.Source)
	}

	base := firstNonEmpty(cred.BaseURL, v.endpoint.vertex, fmt.Sprintf("https://%s-aiplatform.googleapis.com", region))
	endpoint := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/anthropic/models",
		strings.TrimRight(base, "/"), url.PathEscape(project), url.PathEscape(region))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return requestSpec{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return requestSpec{
		request: request, parseModels: parseVertexModels,
		// Authenticating to Google says nothing about Claude entitlement on
		// this project, so an empty publisher list is not a pass.
		requireModels: true, label: "Vertex AI",
	}, nil
}

// parseVertexModels reads the publisher-model list.
func parseVertexModels(body []byte) ([]Model, error) {
	var payload struct {
		PublisherModels []struct {
			Name      string `json:"name"`
			VersionID string `json:"versionId"`
		} `json:"publisherModels"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(payload.PublisherModels)+len(payload.Models))
	appendModel := func(name string) {
		// Names arrive fully qualified: publishers/anthropic/models/claude-…
		if index := strings.LastIndex(name, "/"); index >= 0 {
			name = name[index+1:]
		}
		if isClaudeModelID(name) {
			// Vertex's publisher listing carries no reasoning levels either.
			models = append(models, Model{ID: name})
		}
	}
	for _, model := range payload.PublisherModels {
		appendModel(model.Name)
	}
	for _, model := range payload.Models {
		appendModel(model.Name)
	}
	return models, nil
}

// serviceAccountKey is the subset of a Google service-account JSON file the
// exchange needs.
type serviceAccountKey struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
	ProjectID    string `json:"project_id"`
}

// googleAccessTokenFromServiceAccount performs the two-legged OAuth exchange:
// build a claim set, sign it with the key's RSA private key, POST it as a JWT
// bearer grant, and read back an access token.
//
// Only the "service_account" file type is handled. The other three types a
// GOOGLE_APPLICATION_CREDENTIALS path can point at — authorized_user,
// external_account, and impersonated_service_account — are credential-chain
// cases, deliberately out of scope here and delegated to the gcloud CLI.
func (v *Validator) googleAccessTokenFromServiceAccount(ctx context.Context, keyJSON string) (string, error) {
	var key serviceAccountKey
	if err := json.Unmarshal([]byte(keyJSON), &key); err != nil {
		return "", fmt.Errorf("agentcreds: parse service account key: %w", err)
	}
	if key.Type != "service_account" {
		return "", fmt.Errorf("agentcreds: credential file type %q is a credential-chain case, not a service account key", key.Type)
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return "", fmt.Errorf("agentcreds: service account key is missing client_email or private_key")
	}
	tokenURL := firstNonEmpty(key.TokenURI, v.endpoint.sts, googleTokenURL)
	if v.endpoint.sts != "" {
		tokenURL = v.endpoint.sts
	}

	now := v.now().UTC()
	claims := map[string]any{
		"iss":   key.ClientEmail,
		"scope": vertexScope,
		"aud":   tokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion, err := signRS256JWT(key.PrivateKey, claims)
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := v.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("agentcreds: exchange service account key: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := readLimited(response)
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		// A rejected key exchange is a rejected credential, and it must
		// surface as such rather than as an unknown transport failure.
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusBadRequest {
			return "", fmt.Errorf("%w: Google rejected the service account key: %s",
				ErrInvalidCredential, providerErrorMessage(body))
		}
		return "", fmt.Errorf("agentcreds: token exchange returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("agentcreds: decode token response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("agentcreds: token exchange returned no access token")
	}
	return payload.AccessToken, nil
}

// signRS256JWT builds and signs a compact JWT with an RSA private key.
func signRS256JWT(privateKeyPEM string, claims map[string]any) (string, error) {
	key, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64URL(header) + "." + base64URL(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("agentcreds: sign assertion: %w", err)
	}
	return signingInput + "." + base64URL(signature), nil
}

// parseRSAPrivateKey accepts both PKCS#1 and PKCS#8 PEM blocks, since Google
// has issued keys in both encodings over time.
func parseRSAPrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("agentcreds: service account private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("agentcreds: parse service account private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("agentcreds: service account key is not an RSA key")
	}
	return key, nil
}

func base64URL(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func readLimited(response *http.Response) ([]byte, error) {
	return readAllLimited(response, maxBodyBytes)
}

var _ = context.Background
