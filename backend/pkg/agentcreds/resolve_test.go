package agentcreds

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func envFrom(values map[string]string) Env {
	return func(name string) string { return values[name] }
}

// Precedence, corrected by experiment: CLAUDE_CODE_OAUTH_TOKEN beats
// ANTHROPIC_API_KEY. Getting this backwards means AO confidently validates a
// credential the agent will not send.
func TestFirstPartyPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantSource string
		wantKind   Kind
	}{
		{
			name: "oauth token wins over api key",
			env: map[string]string{
				"CLAUDE_CODE_OAUTH_TOKEN": "oat", "ANTHROPIC_API_KEY": "key", "ANTHROPIC_AUTH_TOKEN": "auth",
			},
			wantSource: "CLAUDE_CODE_OAUTH_TOKEN", wantKind: KindOAuthToken,
		},
		{
			name:       "api key wins over auth token",
			env:        map[string]string{"ANTHROPIC_API_KEY": "key", "ANTHROPIC_AUTH_TOKEN": "auth"},
			wantSource: "ANTHROPIC_API_KEY", wantKind: KindAPIKey,
		},
		{
			name:       "auth token is last of the env sources",
			env:        map[string]string{"ANTHROPIC_AUTH_TOKEN": "auth"},
			wantSource: "ANTHROPIC_AUTH_TOKEN", wantKind: KindAuthToken,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cred, ok := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
				Env: envFrom(tc.env), ConfigDir: t.TempDir(),
			})
			if !ok {
				t.Fatal("expected a credential")
			}
			if cred.Source != tc.wantSource {
				t.Fatalf("source = %q, want %q", cred.Source, tc.wantSource)
			}
			if cred.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", cred.Kind, tc.wantKind)
			}
		})
	}
}

// Source 5, the subscription login. On Linux and Windows this is the only
// path; on macOS it is where a keychain read falls through to.
func TestCredentialsFileIsSourceFive(t *testing.T) {
	dir := t.TempDir()
	content := `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-from-file"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, ok := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
		Env: envFrom(nil), ConfigDir: dir, GOOS: "linux",
	})
	if !ok {
		t.Fatal("expected the credentials file to resolve")
	}
	if cred.Kind != KindOAuthToken || cred.Secret != "sk-ant-oat01-from-file" {
		t.Fatalf("credential = %+v", cred)
	}
	if cred.Source != "credentials-file" {
		t.Fatalf("source = %q, want credentials-file", cred.Source)
	}
}

// Every env source must outrank the file, or AO validates the subscription
// while the agent uses the env var.
func TestEnvBeatsCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"from-file"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, _ := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
		Env: envFrom(map[string]string{"ANTHROPIC_API_KEY": "from-env"}), ConfigDir: dir, GOOS: "linux",
	})
	if cred.Secret != "from-env" {
		t.Fatalf("secret came from %q, want the environment", cred.Source)
	}
}

// Q1's blast radius, pinned: with keychain reads disallowed the resolver keeps
// working and simply loses source 5. Nothing else changes.
func TestKeychainCanBeDisabledWithoutBreakingResolution(t *testing.T) {
	dir := t.TempDir()
	opts := ResolveOptions{Env: envFrom(nil), ConfigDir: dir, GOOS: "darwin", AllowKeychain: false,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("the keychain must not be read when it is disallowed")
			return nil, nil
		}}
	if _, ok := ResolveLocal(context.Background(), ProviderFirstParty, opts); ok {
		t.Fatal("no credential should resolve with no env, no file, and no keychain")
	}

	// Env sources are entirely unaffected by the keychain decision.
	opts.Env = envFrom(map[string]string{"ANTHROPIC_API_KEY": "key"})
	if _, ok := ResolveLocal(context.Background(), ProviderFirstParty, opts); !ok {
		t.Fatal("env-sourced credentials must resolve regardless of keychain policy")
	}
}

// A locked or denied keychain must fall through to the file, never abort
// resolution: the user is very likely signed in and simply has it locked.
func TestKeychainFailureFallsThroughToFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"from-file"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, ok := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
		Env: envFrom(nil), ConfigDir: dir, GOOS: "darwin", AllowKeychain: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("security: SecKeychainSearchCopyNext: User interaction is not allowed.")
		},
	})
	if !ok || cred.Secret != "from-file" {
		t.Fatalf("credential = %+v, want the file fallback", cred)
	}
}

func TestKeychainSuccessIsSourceFive(t *testing.T) {
	cred, ok := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
		Env: envFrom(nil), ConfigDir: t.TempDir(), GOOS: "darwin", AllowKeychain: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-keychain"}}`), nil
		},
	})
	if !ok || cred.Source != "keychain" || cred.Kind != KindOAuthToken {
		t.Fatalf("credential = %+v, want a keychain-sourced oauth token", cred)
	}
}

// Claude Code v2.1.268+ stores the /login managed API key under the "Claude
// Code" keychain service. AO must read it and classify it as an API key so
// the probe sends x-api-key, not Bearer.
func TestKeychainManagedKeyResolvesAsAPIKey(t *testing.T) {
	cred, ok := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
		Env: envFrom(nil), ConfigDir: t.TempDir(), GOOS: "darwin", AllowKeychain: true,
		Runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) >= 3 && args[2] == "Claude Code" {
				return []byte("sk-ant-api03-managed-key"), nil
			}
			return nil, errors.New("security: item not found")
		},
	})
	if !ok {
		t.Fatal("expected the managed key to resolve")
	}
	if cred.Kind != KindAPIKey {
		t.Fatalf("kind = %q, want %q", cred.Kind, KindAPIKey)
	}
	if cred.Source != "keychain" {
		t.Fatalf("source = %q, want keychain", cred.Source)
	}
	if cred.Secret != "sk-ant-api03-managed-key" {
		t.Fatalf("secret = %q, want the managed key", cred.Secret)
	}
}

// When the older "Claude Code-credentials" entry is present but empty ({}) —
// as happens after a v2.1.268 /login — the resolver must fall through to the
// "Claude Code" managed-key service rather than report no credential.
func TestKeychainEmptyCredentialsFallsThroughToManagedKey(t *testing.T) {
	cred, ok := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
		Env: envFrom(nil), ConfigDir: t.TempDir(), GOOS: "darwin", AllowKeychain: true,
		Runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch {
			case len(args) >= 3 && args[2] == "Claude Code-credentials":
				return []byte("{}"), nil
			case len(args) >= 3 && args[2] == "Claude Code":
				return []byte("sk-ant-api03-fallback"), nil
			default:
				return nil, errors.New("security: item not found")
			}
		},
	})
	if !ok {
		t.Fatal("expected the managed-key fallback to resolve")
	}
	if cred.Kind != KindAPIKey {
		t.Fatalf("kind = %q, want %q", cred.Kind, KindAPIKey)
	}
	if cred.Secret != "sk-ant-api03-fallback" {
		t.Fatalf("secret = %q, want the fallback key", cred.Secret)
	}
}

// A setup token (sk-ant-oat*) stored under the "Claude Code" keychain service
// must be classified as KindOAuthToken so the probe sends Bearer, not
// x-api-key. Sending it under x-api-key would be rejected as an invalid API
// key even though the credential is valid.
func TestKeychainSetupTokenFromClassifiedAsOAuth(t *testing.T) {
	cred, ok := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
		Env: envFrom(nil), ConfigDir: t.TempDir(), GOOS: "darwin", AllowKeychain: true,
		Runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch {
			case len(args) >= 3 && args[2] == "Claude Code-credentials":
				return []byte("{}"), nil
			case len(args) >= 3 && args[2] == "Claude Code":
				return []byte("sk-ant-oat01-setup-token"), nil
			default:
				return nil, errors.New("security: item not found")
			}
		},
	})
	if !ok {
		t.Fatal("expected the setup token to resolve")
	}
	if cred.Kind != KindOAuthToken {
		t.Fatalf("kind = %q, want %q", cred.Kind, KindOAuthToken)
	}
	if cred.Source != "keychain" {
		t.Fatalf("source = %q, want keychain", cred.Source)
	}
	if cred.Secret != "sk-ant-oat01-setup-token" {
		t.Fatalf("secret = %q", cred.Secret)
	}
}

// Non-Mac platforms must never invoke the keychain helper at all.
func TestKeychainIsMacOnly(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
				Env: envFrom(nil), ConfigDir: t.TempDir(), GOOS: goos, AllowKeychain: true,
				Runner: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatalf("%s must not read a keychain", goos)
					return nil, nil
				},
			})
		})
	}
}

// The provider gate. Guessing here would send a Bedrock credential to
// api.anthropic.com.
func TestResolveProvider(t *testing.T) {
	tests := []struct {
		name     string
		reported string
		env      map[string]string
		want     Provider
		wantOK   bool
	}{
		{name: "reported wins", reported: "bedrock", want: ProviderBedrock, wantOK: true},
		{name: "vertex", reported: "vertex", want: ProviderVertex, wantOK: true},
		{name: "foundry", reported: "foundry", want: ProviderFoundry, wantOK: true},
		{name: "empty defaults to first party", want: ProviderFirstParty, wantOK: true},
		{
			name: "bedrock inferred from env when the CLI could not be asked",
			env:  map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}, want: ProviderBedrock, wantOK: true,
		},
		{
			name: "base url means gateway",
			env:  map[string]string{"ANTHROPIC_BASE_URL": "https://proxy.internal"},
			want: ProviderGateway, wantOK: true,
		},
		{name: "an unknown provider is not guessed", reported: "some-future-provider", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveProvider(tc.reported, ResolveOptions{Env: envFrom(tc.env)})
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("provider = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveBedrockCredentialShapes(t *testing.T) {
	bearer, ok := ResolveLocal(context.Background(), ProviderBedrock, ResolveOptions{
		Env: envFrom(map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "bt", "AWS_REGION": "us-east-1"}),
	})
	if !ok || bearer.Kind != KindAuthToken || bearer.Region != "us-east-1" {
		t.Fatalf("bearer credential = %+v", bearer)
	}

	static, ok := ResolveLocal(context.Background(), ProviderBedrock, ResolveOptions{
		Env: envFrom(map[string]string{
			"AWS_ACCESS_KEY_ID": "AKIA", "AWS_SECRET_ACCESS_KEY": "secret",
			"AWS_SESSION_TOKEN": "session", "AWS_REGION": "eu-west-1",
		}),
	})
	if !ok || static.Kind != KindAWSSigV4 {
		t.Fatalf("static credential = %+v", static)
	}
	keys, err := parseAWSKeys(static.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if keys.AccessKeyID != "AKIA" || keys.SecretAccessKey != "secret" || keys.SessionToken != "session" {
		t.Fatalf("keys = %+v", keys)
	}

	// Chain-sourced: nothing readable, and that is the correct answer.
	if _, ok := ResolveLocal(context.Background(), ProviderBedrock, ResolveOptions{
		Env: envFrom(map[string]string{"AWS_PROFILE": "sso-profile"}),
	}); ok {
		t.Fatal("an SSO profile must not resolve to a readable credential")
	}
}

func TestResolveVertexReadsProjectFromServiceAccountKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sa.json")
	key := map[string]string{
		"type": "service_account", "client_email": "probe@example.iam.gserviceaccount.com",
		"private_key": "-----BEGIN PRIVATE KEY-----\n-----END PRIVATE KEY-----\n",
		"project_id":  "from-key-file",
	}
	data, _ := json.Marshal(key)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cred, ok := ResolveLocal(context.Background(), ProviderVertex, ResolveOptions{
		Env: envFrom(map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": path}),
	})
	if !ok || cred.Kind != KindGoogleServiceAccount {
		t.Fatalf("credential = %+v", cred)
	}
	if cred.Project != "from-key-file" {
		t.Fatalf("project = %q, want it read from the key file", cred.Project)
	}
}

// Honoring apiKeyHelper means executing a command named in a settings file.
// Claude Code gates that behind workspace trust; AO declines entirely rather
// than making a credential check into a way to run code from a repo.
func TestAPIKeyHelperIsNeverExecuted(t *testing.T) {
	_, ok := ResolveLocal(context.Background(), ProviderFirstParty, ResolveOptions{
		Env:       envFrom(map[string]string{"ANTHROPIC_API_KEY_HELPER": "/bin/echo leak"}),
		ConfigDir: t.TempDir(), GOOS: "linux",
		Runner: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("no command may be executed during resolution, got %q", name)
			return nil, nil
		},
	})
	if ok {
		t.Fatal("a key helper must not produce a credential")
	}
}
