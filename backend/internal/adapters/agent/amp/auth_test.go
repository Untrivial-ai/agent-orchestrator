package amp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func ampTestDeps(t *testing.T, env map[string]string) authutil.Dependencies {
	t.Helper()
	home := t.TempDir()
	return authutil.Dependencies{
		Getenv: func(key string) string {
			if key == "HOME" || key == "USERPROFILE" {
				return home
			}
			return env[key]
		},
		Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("offline") },
		Now: func() time.Time { return time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC) },
	}
}

func ampTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAmpProbeFirstAndAuthoritative(t *testing.T) {
	for _, tt := range []struct {
		name, output string
		fail         bool
		want         ports.AgentAuthStatus
	}{
		// The provider supplies displayText; successful usage completion is
		// authoritative even when that text contains no fixed identity marker.
		{"provider validation completed with no identity line", "\n", false, ports.AgentAuthStatusAuthorized},
		{"provider validation completed with empty display text", "", false, ports.AgentAuthStatusAuthorized},
		{"native signed out", "Error: You must be logged in to view usage. Run `amp login` first.\n", true, ports.AgentAuthStatusUnauthorized},
		{"quoted native error is inconclusive", "Example: Error: You must be logged in to view usage. Run `amp login` first.", true, ports.AgentAuthStatusConfigured},
		{"unrecognized auth error", "Error: Authentication required. Please run 'amp login'.", true, ports.AgentAuthStatusConfigured},
		{"unsupported version", "error: unknown command 'usage'", true, ports.AgentAuthStatusConfigured},
		{"failure with success text", "Signed in as user@example.test", true, ports.AgentAuthStatusConfigured},
		{"generic positive on failure", "Authenticated successfully", true, ports.AgentAuthStatusConfigured},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := ampTestDeps(t, map[string]string{"AMP_API_KEY": "sgamp_test_access_token"})
			probed := false
			getenv := d.Getenv
			d.Getenv = func(key string) string {
				if key == "AMP_API_KEY" && !probed {
					t.Error("local credentials read before usage probe")
				}
				return getenv(key)
			}
			d.Run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				probed = true
				if binary != "/test/amp" || !reflect.DeepEqual(args, []string{"usage", "--no-color"}) {
					t.Fatalf("unexpected invocation: %s %v", binary, args)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("probe has no deadline")
				}
				if tt.fail {
					return []byte(tt.output), errors.New("failure with sensitive output")
				}
				return []byte(tt.output), nil
			}
			got, err := ampAuthStatus(context.Background(), "/test/amp", ports.AgentAuthCheck{}, d)
			if err != nil || got != tt.want || !probed {
				t.Fatalf("status = %q, err = %v, probed = %v, want %q", got, err, probed, tt.want)
			}
		})
	}
}

func TestAmpEnvironmentTokenShape(t *testing.T) {
	for _, tt := range []struct {
		name, key string
		want      ports.AgentAuthStatus
	}{
		{"access token", "sgamp_user_01J012345678901234567890123_0123456789abcdef", ports.AgentAuthStatusConfigured},
		{"trimmed", "  sgamp_test_access_token  ", ports.AgentAuthStatusConfigured},
		{"prefix only", "sgamp_", ports.AgentAuthStatusUnknown},
		{"whitespace", "sgamp_bad token", ports.AgentAuthStatusUnknown},
		{"unrecognized", "amp-key", ports.AgentAuthStatusUnknown},
		{"short lived", "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE5MDAwMDAwMDB9.signature", ports.AgentAuthStatusUnknown},
		{"absent", "", ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := ampTestDeps(t, map[string]string{"AMP_API_KEY": tt.key})
			got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{}, d)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAmpSecretsOnlySelectedOfficialField(t *testing.T) {
	for _, tt := range []struct {
		name, content string
		want          ports.AgentAuthStatus
	}{
		{"official", `{"apiKey@https://ampcode.com/":"sgamp_test_access_token"}`, ports.AgentAuthStatusConfigured},
		{"generic access token", `{"accessToken":"secret"}`, ports.AgentAuthStatusUnknown},
		{"unrelated", `{"github-access-token@https://ampcode.com/":"secret","mcp-oauth-token@https://ampcode.com/":"secret"}`, ports.AgentAuthStatusUnknown},
		{"other server", `{"apiKey@https://other.test/":"sgamp_other"}`, ports.AgentAuthStatusUnknown},
		{"nested", `{"credentials":{"apiKey@https://ampcode.com/":"sgamp_secret"}}`, ports.AgentAuthStatusUnknown},
		{"empty", `{}`, ports.AgentAuthStatusUnknown},
		{"empty credential", `{"apiKey@https://ampcode.com/":" "}`, ports.AgentAuthStatusUnknown},
		{"wrong type", `{"apiKey@https://ampcode.com/":true}`, ports.AgentAuthStatusUnknown},
		{"malformed", `{"apiKey@https://ampcode.com/":"secret",`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := ampTestDeps(t, nil)
			ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".local", "share", "amp", "secrets.json"), tt.content)
			got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{}, d)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAmpJSONCSettingsSelectServerWithoutSupplyingCredentials(t *testing.T) {
	for _, filename := range []string{"settings.json", "settings.jsonc"} {
		t.Run(filename, func(t *testing.T) {
			d := ampTestDeps(t, nil)
			ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".config", "amp", filename), "{\n// select the existing server\n\"amp.url\": \"https://enterprise.test/\", /* comment */\n\"amp.apiKey\": \"ignored\",\n}")
			ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".local", "share", "amp", "secrets.json"), `{"apiKey@https://enterprise.test/":"sgamp_enterprise"}`)
			got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{}, d)
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
	for _, key := range []string{"amp.apiKey", "amp.api_key", "apiKey", "api_key"} {
		t.Run(key, func(t *testing.T) {
			d := ampTestDeps(t, nil)
			ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".config", "amp", "settings.json"), `{"`+key+`":"sgamp_should_not_count"}`)
			got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{}, d)
			if err != nil || got != ports.AgentAuthStatusUnknown {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestAmpTimeoutFallbackAndCancellation(t *testing.T) {
	for _, hasKey := range []bool{false, true} {
		d := ampTestDeps(t, nil)
		if hasKey {
			d = ampTestDeps(t, map[string]string{"AMP_API_KEY": "sgamp_test_access_token"})
		}
		d.Timeout = time.Millisecond
		d.Run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
		got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{}, d)
		want := ports.AgentAuthStatusUnknown
		if hasKey {
			want = ports.AgentAuthStatusConfigured
		}
		if err != nil || got != want {
			t.Fatalf("timeout got %q, %v; want %q", got, err, want)
		}
	}
	d := ampTestDeps(t, map[string]string{"AMP_API_KEY": "sgamp_test_access_token"})
	d.Run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("canceled probe ran")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := ampAuthStatus(ctx, "amp", ports.AgentAuthCheck{}, d)
	if got != ports.AgentAuthStatusUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled got %q, %v", got, err)
	}
}

func TestAmpOversizedProbeIsInconclusive(t *testing.T) {
	d := ampTestDeps(t, nil)
	d.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Signed in as user@example.test\n" + strings.Repeat("x", authutil.MaxFileSize)), nil
	}
	got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{}, d)
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAmpMalformedSettingsDoNotSelectPartiallyDecodedServer(t *testing.T) {
	d := ampTestDeps(t, nil)
	ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".config", "amp", "settings.json"), `{"amp.url":"https://enterprise.test/", /* unterminated`)
	ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".local", "share", "amp", "secrets.json"), `{"apiKey@https://enterprise.test/":"sgamp_enterprise"}`)
	got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{}, d)
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAmpScopedSettingsAndEnvironment(t *testing.T) {
	d := ampTestDeps(t, map[string]string{"AMP_API_KEY": "sgamp_inherited"})
	workspace := t.TempDir()
	ampTestFile(t, filepath.Join(workspace, "custom.jsonc"), `{"amp.url":"https://enterprise.test/",}`)
	ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".local", "share", "amp", "secrets.json"), `{"apiKey@https://enterprise.test/":"sgamp_enterprise"}`)
	d.Run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"usage", "--no-color", "--settings-file", "custom.jsonc"}) {
			t.Fatalf("unexpected args: %v", args)
		}
		return nil, errors.New("offline")
	}
	got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{WorkingDir: workspace, Args: []string{"--settings-file=custom.jsonc"}, Env: map[string]string{"AMP_API_KEY": ""}}, d)
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAmpSelectedInvalidEnvDoesNotFallBackToStoredCredentials(t *testing.T) {
	for _, tt := range []struct {
		name, inherited string
		scoped          map[string]string
		want            ports.AgentAuthStatus
	}{
		{"invalid", "invalid", nil, ports.AgentAuthStatusUnknown},
		{"session token", "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE5MDAwMDAwMDB9.signature", nil, ports.AgentAuthStatusUnknown},
		{"whitespace selects environment", " \t", nil, ports.AgentAuthStatusUnknown},
		{"scoped invalid masks inherited", "sgamp_inherited", map[string]string{"AMP_API_KEY": "invalid"}, ports.AgentAuthStatusUnknown},
		{"empty permits stored credentials", "", nil, ports.AgentAuthStatusConfigured},
		{"scoped empty permits stored credentials", "invalid", map[string]string{"AMP_API_KEY": ""}, ports.AgentAuthStatusConfigured},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := ampTestDeps(t, map[string]string{"AMP_API_KEY": tt.inherited})
			ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".local", "share", "amp", "secrets.json"), `{"apiKey@https://ampcode.com/":"sgamp_access"}`)
			got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{Env: tt.scoped}, d)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAmpMalformedJSONFallsBackToJSONCSettings(t *testing.T) {
	d := ampTestDeps(t, nil)
	ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".config", "amp", "settings.json"), `{broken`)
	ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".config", "amp", "settings.jsonc"), `{"amp.url":"https://enterprise.test/",}`)
	ampTestFile(t, filepath.Join(d.Getenv("HOME"), ".local", "share", "amp", "secrets.json"), `{"apiKey@https://enterprise.test/":"sgamp_enterprise"}`)
	got, err := ampAuthStatus(context.Background(), "amp", ports.AgentAuthCheck{}, d)
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("got %q, %v", got, err)
	}
}
