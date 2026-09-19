package authutil

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func cloudDeps(t *testing.T, env map[string]string) Dependencies {
	t.Helper()
	if env == nil {
		env = make(map[string]string)
	}
	if env["HOME"] == "" {
		env["HOME"] = t.TempDir()
	}
	return Dependencies{
		Getenv: func(key string) string { return env[key] },
		Now:    func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) },
		GOOS:   "linux",
		Run:    func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("CLI unavailable") },
	}
}

func assertCloud(t *testing.T, got Evidence, status ports.AgentAuthStatus, source string) {
	t.Helper()
	if got.Status != status || got.Source != source {
		t.Fatalf("cloud evidence = %#v, want %s from %q", got, status, source)
	}
}

func TestAWSEnvironmentEvidence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		env    map[string]string
		status ports.AgentAuthStatus
		source string
	}{
		{"pair", map[string]string{"AWS_ACCESS_KEY_ID": "fixture-access", "AWS_SECRET_ACCESS_KEY": "fixture-secret"}, "configured", "aws-environment"},
		{"bearer", map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "fixture-token"}, "configured", "aws-bearer-token"},
		{"partial pair", map[string]string{"AWS_ACCESS_KEY_ID": "fixture-access"}, "unknown", ""},
		{"empty", nil, "unknown", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertCloud(t, AWSEvidence(context.Background(), cloudDeps(t, tt.env)), tt.status, tt.source)
		})
	}
}

func TestAWSSelectedSharedProfiles(t *testing.T) {
	for _, tt := range []struct {
		name, profile, credentials, config string
		status                             ports.AgentAuthStatus
	}{
		{"default", "", "[default]\naws_access_key_id=fixture-access\naws_secret_access_key=fixture-secret\n", "", "configured"},
		{"named", "work", "[work]\naws_access_key_id=fixture-access\naws_secret_access_key=fixture-secret\n", "", "configured"},
		{"config profile", "work", "", "[profile work]\naws_access_key_id=fixture-access\naws_secret_access_key=fixture-secret\n", "configured"},
		{"role source", "role", "[source]\naws_access_key_id=fixture-access\naws_secret_access_key=fixture-secret\n", "[profile role]\nrole_arn=arn:aws:iam::123:role/test\nsource_profile=source\n", "configured"},
		{"wrong profile", "work", "[other]\naws_access_key_id=fixture-access\naws_secret_access_key=fixture-secret\n", "", "unknown"},
		{"partial", "", "[default]\naws_access_key_id=fixture-access\n", "", "unknown"},
		{"role cycle", "a", "", "[profile a]\nrole_arn=role-a\nsource_profile=b\n[profile b]\nrole_arn=role-b\nsource_profile=a\n", "unknown"},
		{"process configuration alone", "", "", "[default]\ncredential_process=never-execute-this\n", "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"HOME": t.TempDir(), "AWS_PROFILE": tt.profile}
			writeFixture(t, filepath.Join(env["HOME"], ".aws", "credentials"), tt.credentials)
			writeFixture(t, filepath.Join(env["HOME"], ".aws", "config"), tt.config)
			got := AWSEvidence(context.Background(), cloudDeps(t, env))
			source := ""
			if tt.status == "configured" {
				source = "aws-profile"
			}
			assertCloud(t, got, tt.status, source)
		})
	}
}

func TestAWSFileOverridesAndWebIdentity(t *testing.T) {
	root := t.TempDir()
	credentials := filepath.Join(root, "custom-credentials")
	config := filepath.Join(root, "custom-config")
	token := filepath.Join(root, "web-token")
	writeFixture(t, credentials, "[work]\naws_access_key_id=fixture-access\naws_secret_access_key=fixture-secret\n")
	writeFixture(t, config, "")
	env := map[string]string{"AWS_DEFAULT_PROFILE": "work", "AWS_SHARED_CREDENTIALS_FILE": credentials, "AWS_CONFIG_FILE": config}
	assertCloud(t, AWSEvidence(context.Background(), cloudDeps(t, env)), "configured", "aws-profile")
	writeFixture(t, token, "fixture-web-token")
	env = map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": token, "AWS_ROLE_ARN": "arn:aws:iam::123:role/test"}
	assertCloud(t, AWSEvidence(context.Background(), cloudDeps(t, env)), "configured", "aws-web-identity")
	delete(env, "AWS_ROLE_ARN")
	assertCloud(t, AWSEvidence(context.Background(), cloudDeps(t, env)), "unknown", "")
	writeFixture(t, config, "[default]\nrole_arn=arn:aws:iam::123:role/test\nweb_identity_token_file="+token+"\n")
	assertCloud(t, AWSEvidence(context.Background(), cloudDeps(t, map[string]string{"AWS_CONFIG_FILE": config})), "configured", "aws-profile")
}

func TestAWSSSOCache(t *testing.T) {
	for _, tt := range []struct {
		name, session, expiry, token string
		status                       ports.AgentAuthStatus
	}{
		{"legacy valid", "", "2026-09-20T12:00:00Z", "fixture-token", "configured"},
		{"session valid", "work", "2026-09-20T12:00:00Z", "fixture-token", "configured"},
		{"legacy UTC expiry", "", "2026-09-20T12:00:00UTC", "fixture-token", "configured"},
		{"expired", "", "2026-09-18T12:00:00Z", "fixture-token", "unknown"},
		{"empty token", "", "2026-09-20T12:00:00Z", "", "unknown"},
		{"invalid expiry", "", "not-time", "fixture-token", "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			start := "https://example.awsapps.com/start"
			config := "[default]\nsso_account_id=123456789012\nsso_role_name=Developer\nsso_start_url=" + start + "\nsso_region=us-east-1\n"
			key := start
			if tt.session != "" {
				config = "[default]\nsso_account_id=123456789012\nsso_role_name=Developer\nsso_session=work\n[sso-session work]\nsso_start_url=" + start + "\nsso_region=us-east-1\n"
				key = tt.session
			}
			writeFixture(t, filepath.Join(root, ".aws", "config"), config)
			hash := sha1.Sum([]byte(key))
			cache, err := json.Marshal(map[string]string{"startUrl": start, "region": "us-east-1", "accessToken": tt.token, "expiresAt": tt.expiry})
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(root, ".aws", "sso", "cache", hex.EncodeToString(hash[:])+".json"), string(cache))
			source := ""
			if tt.status == "configured" {
				source = "aws-profile"
			}
			assertCloud(t, AWSEvidence(context.Background(), cloudDeps(t, map[string]string{"HOME": root})), tt.status, source)
		})
	}
}

func TestAWSInjectedRoleAndMetadataLoader(t *testing.T) {
	for _, name := range []string{"role", "metadata"} {
		t.Run(name, func(t *testing.T) {
			deps := cloudDeps(t, nil)
			deps.LoadAWS = func(ctx context.Context) (CloudCredential, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("loader missing deadline")
				}
				return CloudCredential{AccessKeyID: "fixture-access", SecretAccessKey: "fixture-secret", ExpiresAt: deps.Now().Add(time.Hour)}, nil
			}
			assertCloud(t, AWSEvidence(context.Background(), deps), "configured", "aws-chain")
		})
	}
}

func TestCloudLoaderFailuresAreUnknown(t *testing.T) {
	for _, tt := range []struct {
		name       string
		credential CloudCredential
		err        error
		timeout    bool
	}{
		{"empty", CloudCredential{}, nil, false},
		{"partial pair", CloudCredential{AccessKeyID: "fixture-access"}, nil, false},
		{"expired", CloudCredential{Token: "fixture-token", ExpiresAt: time.Unix(1, 0)}, nil, false},
		{"failure", CloudCredential{Token: "fixture-token"}, errors.New("fixture-secret"), false},
		{"timeout", CloudCredential{}, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps := cloudDeps(t, nil)
			deps.Timeout = time.Millisecond
			loader := func(ctx context.Context) (CloudCredential, error) {
				if tt.timeout {
					<-ctx.Done()
					return CloudCredential{}, ctx.Err()
				}
				return tt.credential, tt.err
			}
			deps.LoadAWS, deps.LoadGoogleADC, deps.LoadAzure = loader, loader, loader
			assertCloud(t, AWSEvidence(context.Background(), deps), "unknown", "")
			assertCloud(t, GoogleADCEvidence(context.Background(), deps), "unknown", "")
			assertCloud(t, AzureEvidence(context.Background(), deps), "unknown", "")
		})
	}
}

func TestGoogleADCCredentialSchemas(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	serviceAccount := map[string]any{"type": "service_account", "private_key": keyPEM, "client_email": "fixture@example.iam.gserviceaccount.com", "token_uri": "https://oauth2.googleapis.com/token"}
	validService, err := json.Marshal(serviceAccount)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, body string
		status     ports.AgentAuthStatus
	}{
		{"service account", string(validService), "configured"},
		{"authorized user", "{\"type\":\"authorized_user\",\"client_id\":\"fixture-client\",\"client_secret\":\"fixture-secret\",\"refresh_token\":\"fixture-refresh\"}", "configured"},
		{"external account", "{\"type\":\"external_account\",\"audience\":\"//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/provider\",\"subject_token_type\":\"urn:ietf:params:oauth:token-type:jwt\",\"token_url\":\"https://sts.googleapis.com/v1/token\",\"credential_source\":{\"url\":\"http://127.0.0.1/token\"}}", "configured"},
		{"empty", "", "unknown"},
		{"malformed", "{\"type\":", "unknown"},
		{"incomplete service", "{\"type\":\"service_account\",\"client_email\":\"fixture@example.test\"}", "unknown"},
		{"invalid key", "{\"type\":\"service_account\",\"private_key\":\"fixture-not-pem\",\"client_email\":\"fixture@example.test\",\"token_uri\":\"https://oauth2.googleapis.com/token\"}", "unknown"},
		{"incomplete user", "{\"type\":\"authorized_user\",\"client_id\":\"fixture-client\",\"client_secret\":\"fixture-secret\"}", "unknown"},
		{"incomplete external", "{\"type\":\"external_account\",\"audience\":\"audience\",\"subject_token_type\":\"jwt\",\"token_url\":\"https://sts.googleapis.com/v1/token\"}", "unknown"},
		{"unrelated token", "{\"access_token\":\"fixture-token\"}", "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "adc.json")
			writeFixture(t, path, tt.body)
			source := ""
			if tt.status == "configured" {
				source = "google-adc-file"
			}
			assertCloud(t, GoogleADCEvidence(context.Background(), cloudDeps(t, map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": path})), tt.status, source)
		})
	}
}

func TestGoogleADCDefaultPathsAndFallback(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			root := t.TempDir()
			env := map[string]string{"HOME": root, "APPDATA": filepath.Join(root, "appdata"), "GOOGLE_APPLICATION_CREDENTIALS": filepath.Join(root, "malformed.json")}
			writeFixture(t, env["GOOGLE_APPLICATION_CREDENTIALS"], "{")
			path := filepath.Join(root, ".config", "gcloud", "application_default_credentials.json")
			if goos == "windows" {
				path = filepath.Join(env["APPDATA"], "gcloud", "application_default_credentials.json")
			}
			writeFixture(t, path, "{\"type\":\"authorized_user\",\"client_id\":\"fixture-client\",\"client_secret\":\"fixture-secret\",\"refresh_token\":\"fixture-refresh\"}")
			deps := cloudDeps(t, env)
			deps.GOOS = goos
			assertCloud(t, GoogleADCEvidence(context.Background(), deps), "configured", "google-adc-file")
		})
	}
	deps := cloudDeps(t, nil)
	deps.LoadGoogleADC = func(context.Context) (CloudCredential, error) { return CloudCredential{Token: "fixture-token"}, nil }
	assertCloud(t, GoogleADCEvidence(context.Background(), deps), "configured", "google-adc-chain")
}

func TestAzureEnvironmentAndIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		env    map[string]string
		status ports.AgentAuthStatus
		source string
	}{
		{"openai key", map[string]string{"AZURE_OPENAI_API_KEY": "fixture-key"}, "configured", "azure-api-key"},
		{"azure key", map[string]string{"AZURE_API_KEY": "fixture-key"}, "configured", "azure-api-key"},
		{"service principal", map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "client", "AZURE_CLIENT_SECRET": "fixture-secret"}, "configured", "azure-service-principal"},
		{"partial principal", map[string]string{"AZURE_CLIENT_ID": "client"}, "unknown", ""},
		{"managed identity", map[string]string{"IDENTITY_ENDPOINT": "http://127.0.0.1/token", "IDENTITY_HEADER": "fixture-header"}, "configured", "azure-managed-identity"},
		{"legacy managed identity", map[string]string{"MSI_ENDPOINT": "http://127.0.0.1/token", "MSI_SECRET": "fixture-secret"}, "configured", "azure-managed-identity"},
		{"partial identity", map[string]string{"IDENTITY_ENDPOINT": "http://127.0.0.1/token"}, "unknown", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertCloud(t, AzureEvidence(context.Background(), cloudDeps(t, tt.env)), tt.status, tt.source)
		})
	}
	path := filepath.Join(t.TempDir(), "identity-token")
	writeFixture(t, path, "fixture-token")
	assertCloud(t, AzureEvidence(context.Background(), cloudDeps(t, map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "client", "AZURE_FEDERATED_TOKEN_FILE": path})), "configured", "azure-workload-identity")
}

func TestAzureCLITokenAndLoader(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     ports.AgentAuthStatus
	}{
		{"valid", "{\"accessToken\":\"fixture-token\",\"expires_on\":\"1789905600\",\"tokenType\":\"Bearer\",\"tenant\":\"tenant\",\"subscription\":\"subscription\"}", "configured"},
		{"expired", "{\"accessToken\":\"fixture-token\",\"expires_on\":\"1\"}", "unknown"},
		{"missing token", "{\"expires_on\":\"1789905600\"}", "unknown"},
		{"malformed", "{\"accessToken\":", "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps := cloudDeps(t, nil)
			deps.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "az" || !reflect.DeepEqual(args, []string{"account", "get-access-token", "--resource", "https://cognitiveservices.azure.com/", "--output", "json"}) {
					t.Fatalf("Azure CLI = %q %q", name, args)
				}
				return []byte(tt.body), nil
			}
			source := ""
			if tt.status == "configured" {
				source = "azure-cli"
			}
			assertCloud(t, AzureEvidence(context.Background(), deps), tt.status, source)
		})
	}
	deps := cloudDeps(t, nil)
	deps.LoadAzure = func(context.Context) (CloudCredential, error) { return CloudCredential{Token: "fixture-token"}, nil }
	assertCloud(t, AzureEvidence(context.Background(), deps), "configured", "azure-chain")
}

func TestAzureDefaultDoesNotExecuteCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable fixture uses a Unix shebang")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	script := "#!/bin/sh\nprintf ran > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\nprintf '%s' '{\"accessToken\":\"fixture-token\",\"expires_on\":\"1789905600\"}'\n"
	if err := os.WriteFile(filepath.Join(root, "az"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	deps := cloudDeps(t, nil)
	deps.Run = nil
	got := AzureEvidence(context.Background(), deps)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("default cloud discovery executed a CLI")
	}
	assertCloud(t, got, "unknown", "")
}
