package copilot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCopilotLocalAuthStatusNotApplicableWithLocalOllama(t *testing.T) {
	clearCopilotAuthProbeEnv(t)
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "http://localhost:11434")
	t.Setenv("COPILOT_MODEL", "llama3.2")

	status, ok, err := copilotLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusNotApplicable)
	}
}

func TestCopilotLocalAuthStatusUsesCopilotHome(t *testing.T) {
	clearCopilotAuthProbeEnv(t)
	dir := t.TempDir()
	t.Setenv("COPILOT_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"copilot_tokens":{"https://github.com:fixture":"gho_oauthToken"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	d := copilotAuthDependencies(t, map[string]string{"COPILOT_HOME": dir})
	d.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("unavailable") }
	status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusConfigured)
	}
}

func TestCopilotLocalAuthStatusFallsBackToGHTokenWhenConfigIsMalformed(t *testing.T) {
	clearCopilotAuthProbeEnv(t)
	dir := t.TempDir()
	t.Setenv("COPILOT_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{malformed`), 0o600); err != nil {
		t.Fatal(err)
	}

	d := copilotAuthDependencies(t, map[string]string{"COPILOT_HOME": dir})
	d.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" || !reflect.DeepEqual(args, []string{"auth", "token"}) {
			return nil, errors.New("unexpected auth probe command")
		}
		return []byte("gho_testToken"), nil
	}

	status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusConfigured)
	}
}

func clearCopilotAuthProbeEnv(t *testing.T) {
	t.Helper()
	clearCopilotAuthEnv(t)
	for _, name := range []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_BEARER_TOKEN", "COPILOT_MODEL", "COPILOT_HOME"} {
		t.Setenv(name, "")
	}
}

func TestCopilotAuthStatusTokenPrecedence(t *testing.T) {
	for i, selected := range []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Run(selected, func(t *testing.T) {
			env := map[string]string{selected: "github_pat_fixture"}
			d := copilotAuthDependencies(t, env)
			d.GOOS = "darwin"
			var visited []string
			d.Getenv = func(key string) string {
				if key == "COPILOT_GITHUB_TOKEN" || key == "GH_TOKEN" || key == "GITHUB_TOKEN" {
					visited = append(visited, key)
				}
				return env[key]
			}
			status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || status != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = (%q, %v), want configured", status, err)
			}
			want := []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}[:i+1]
			if !reflect.DeepEqual(visited, want) {
				t.Fatalf("environment lookup order = %v, want %v", visited, want)
			}
		})
	}
}

func TestCopilotAuthStatusRejectsSelectedClassicPAT(t *testing.T) {
	d := copilotAuthDependencies(t, map[string]string{
		"COPILOT_GITHUB_TOKEN": "ghp_unsupported", "GH_TOKEN": "github_pat_fixture",
	})
	status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want unknown", status, err)
	}
}

func TestCopilotAuthStatusGHBeforeConfig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		output   string
		err      error
		wantFile bool
	}{
		{"gh token", "gho_fixture", nil, false},
		{"gh failure", "", errors.New("private command detail"), true},
		{"gh empty", " ", nil, true},
		{"gh classic PAT", "ghp_unsupported", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(`{"copilot_tokens":{"https://github.com:fixture":"gho_config"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			d := copilotAuthDependencies(t, map[string]string{"COPILOT_HOME": dir})
			called, read := false, false
			d.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "gh" || !reflect.DeepEqual(args, []string{"auth", "token"}) {
					t.Fatalf("unexpected command: %s %v", name, args)
				}
				called = true
				return []byte(tc.output), tc.err
			}
			d.ReadFile = func(got string) ([]byte, error) {
				if !called || got != path {
					t.Fatalf("config read before gh or outside selected home: %s", got)
				}
				read = true
				return os.ReadFile(got)
			}
			status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || status != ports.AgentAuthStatusConfigured || !called || read != tc.wantFile {
				t.Fatalf("status = (%q, %v), gh = %v, file = %v; want configured, gh, file=%v", status, err, called, read, tc.wantFile)
			}
		})
	}
}

func TestCopilotAuthStatusIgnoresSessionLogsAndLoginMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-state", "session", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"assistant.message","content":"model response"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"loggedInUsers":{"github.com":{"login":"fixture"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := copilotAuthDependencies(t, map[string]string{"COPILOT_HOME": dir})
	d.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("unavailable") }
	status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want unknown", status, err)
	}
}

func TestCopilotAuthStatusScopedEnvironment(t *testing.T) {
	d := copilotAuthDependencies(t, map[string]string{"COPILOT_GITHUB_TOKEN": "ghp_unsupported"})
	scope := ports.AgentAuthCheck{Env: map[string]string{"COPILOT_GITHUB_TOKEN": "", "GH_TOKEN": "gho_scoped"}}
	status, err := copilotAuthStatus(context.Background(), scope, d)
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want configured", status, err)
	}
}

func TestCopilotAuthStatusCommandTimeoutAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "canceled"}[canceled], func(t *testing.T) {
			d := copilotAuthDependencies(t, map[string]string{})
			d.Timeout = time.Millisecond
			d.Run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			status, err := copilotAuthStatus(ctx, ports.AgentAuthCheck{}, d)
			if status != ports.AgentAuthStatusUnknown || (canceled && !errors.Is(err, context.Canceled)) || (!canceled && err != nil) {
				t.Fatalf("status = (%q, %v), canceled = %v", status, err, canceled)
			}
		})
	}
}

func copilotAuthDependencies(t *testing.T, env map[string]string) authutil.Dependencies {
	t.Helper()
	if _, ok := env["HOME"]; !ok {
		env["HOME"] = t.TempDir()
	}
	return authutil.Dependencies{
		Getenv: func(key string) string { return env[key] }, GOOS: "linux",
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("unexpected credential command")
			return nil, errors.New("unexpected command")
		},
	}
}

func TestCopilotAuthStatusKeychainPrecedesGHAndFallsThrough(t *testing.T) {
	for _, tc := range []struct {
		name          string
		keychainToken string
		keychainErr   error
		ghToken       string
		want          ports.AgentAuthStatus
		wantGH        bool
	}{
		{"keychain token", "gho_keychain", nil, "gho_cli", ports.AgentAuthStatusConfigured, false},
		{"keychain empty", " ", nil, "gho_cli", ports.AgentAuthStatusConfigured, true},
		{"keychain locked", "", errors.New("interaction not allowed"), "gho_cli", ports.AgentAuthStatusConfigured, true},
		{"keychain missing", "", os.ErrNotExist, "gho_cli", ports.AgentAuthStatusConfigured, true},
		{"keychain classic PAT", "ghp_unsupported", nil, "gho_cli", ports.AgentAuthStatusConfigured, true},
		{"all sources fail", "", errors.New("keychain failure with private details"), "", ports.AgentAuthStatusUnknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := copilotAuthDependencies(t, map[string]string{})
			d.GOOS = "darwin"
			dir := filepath.Join(d.Getenv("HOME"), ".copilot")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"last_logged_in_user":{"host":"https://github.com","login":"selected"},"logged_in_users":[{"host":"https://github.com","login":"other"}]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			keychain, gh := false, false
			d.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				switch name {
				case "/usr/bin/security":
					if !reflect.DeepEqual(args, []string{"find-generic-password", "-s", "copilot-cli", "-a", "https://github.com:selected", "-w"}) {
						t.Fatalf("unexpected keychain selectors: %v", args)
					}
					keychain = true
					return []byte(tc.keychainToken), tc.keychainErr
				case "gh":
					if !keychain || !reflect.DeepEqual(args, []string{"auth", "token"}) {
						t.Fatalf("gh called before keychain or with unexpected args: %v", args)
					}
					gh = true
					return []byte(tc.ghToken), nil
				default:
					t.Fatalf("unexpected command %s", name)
					return nil, errors.New("unexpected command")
				}
			}
			status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || status != tc.want || !keychain || gh != tc.wantGH {
				t.Fatalf("status = (%q, %v), keychain = %v, gh = %v; want %q, keychain, gh=%v", status, err, keychain, gh, tc.want, tc.wantGH)
			}
		})
	}
}

func TestCopilotConfigAuthStatusStoredTokenMap(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       ports.AgentAuthStatus
	}{
		{"oauth", `{"store_token_plaintext":true,"copilot_tokens":{"https://github.com:fixture":"gho_stored"}}`, ports.AgentAuthStatusConfigured},
		{"app user token", `{"copilot_tokens":{"https://github.com:fixture":"ghu_stored"}}`, ports.AgentAuthStatusConfigured},
		{"fine grained PAT", `{"copilot_tokens":{"https://github.com:fixture":"github_pat_stored_123"}}`, ports.AgentAuthStatusConfigured},
		{"prefix only", `{"copilot_tokens":{"https://github.com:fixture":"gho_"}}`, ports.AgentAuthStatusUnknown},
		{"empty token", `{"copilot_tokens":{"https://github.com:fixture":" "}}`, ports.AgentAuthStatusUnknown},
		{"classic PAT", `{"copilot_tokens":{"https://github.com:fixture":"ghp_classic"}}`, ports.AgentAuthStatusUnknown},
		{"metadata only", `{"copilot_tokens":{"https://github.com:fixture":{"login":"fixture"}}}`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			status, _, err := copilotConfigAuthStatus(path)
			if err != nil || status != tc.want {
				t.Fatalf("status = (%q, %v), want %q", status, err, tc.want)
			}
		})
	}
}

func TestCopilotAuthStatusUsesGHWithEffectiveScopedEnvironment(t *testing.T) {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_HOST", "GH_CONFIG_DIR", "HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Run(key, func(t *testing.T) {
			d := copilotAuthDependencies(t, map[string]string{key: "inherited-value"})
			scope := ports.AgentAuthCheck{Env: map[string]string{key: ""}}
			called := false
			status, err := copilotAuthStatus(context.Background(), scope, d, func(_ context.Context, name string, args []string, env map[string]string) ([]byte, error) {
				if name != "gh" || !reflect.DeepEqual(args, []string{"auth", "token"}) {
					t.Fatalf("unexpected command %s %v", name, args)
				}
				if got, ok := env[key]; !ok || got != "" {
					t.Fatalf("scoped %s not applied", key)
				}
				called = true
				return []byte("gho_scopedToken"), nil
			})
			if err != nil || status != ports.AgentAuthStatusConfigured || !called {
				t.Fatalf("status = (%q, %v), called=%v; want configured", status, err, called)
			}
		})
	}
}

func TestCopilotAuthStatusBYOKProviderRequirements(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, provider, key, bearer string
		want                                  ports.AgentAuthStatus
	}{
		{"local Ollama", "http://localhost:11434", "openai", "", "", ports.AgentAuthStatusNotApplicable},
		{"remote requires key", "https://api.openai.com/v1", "openai", "", "", ports.AgentAuthStatusUnknown},
		{"remote key", "https://api.openai.com/v1", "openai", "fixture-key", "", ports.AgentAuthStatusConfigured},
		{"bearer", "https://resource.openai.azure.com", "azure", "", "fixture-bearer", ports.AgentAuthStatusConfigured},
		{"anthropic requires key", "https://api.anthropic.com", "anthropic", "", "", ports.AgentAuthStatusUnknown},
		{"unrecognized provider", "https://example.test", "unknown", "fixture-key", "", ports.AgentAuthStatusUnknown},
		{"local unknown service", "http://localhost:8000", "openai", "", "", ports.AgentAuthStatusUnknown},
		{"invalid endpoint", "not-a-url", "openai", "fixture-key", "", ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := copilotAuthDependencies(t, map[string]string{"GH_TOKEN": "gho_unrelated", "COPILOT_PROVIDER_BASE_URL": tc.endpoint, "COPILOT_MODEL": "fixture-model", "COPILOT_PROVIDER_TYPE": tc.provider, "COPILOT_PROVIDER_API_KEY": tc.key, "COPILOT_PROVIDER_BEARER_TOKEN": tc.bearer})
			status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || status != tc.want {
				t.Fatalf("status=(%q, %v), want %q", status, err, tc.want)
			}
		})
	}
}

func TestCopilotRejectsUndocumentedCredentials(t *testing.T) {
	for _, body := range []string{
		`{"authToken":"gho_legacy"}`, `{"accessToken":"gho_legacy"}`, `{"token":"gho_legacy"}`,
		`{"copilot_tokens":{"not-an-account":"gho_stored"}}`,
		`{"copilot_tokens":{"https://github.com:fixture":"arbitrary-text"}}`,
		`{"copilot_tokens":{"https://github.com:fixture":"gho_bad token"}}`,
		`{"last_logged_in_user":{"host":"https://github.com","login":"selected"},"copilot_tokens":{"https://github.com:other":"gho_other"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			status, _, err := copilotConfigAuthStatus(path)
			if err != nil || status != ports.AgentAuthStatusUnknown {
				t.Fatalf("status=(%q, %v), want unknown", status, err)
			}
		})
	}
}

func TestCopilotAuthStatusDoesNotReadUnselectedKeychainAccounts(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"last_logged_in_user":{"host":"https://github.com","login":""}}`,
		`{"last_logged_in_user":{"host":"https://github.com","login":"selected\nother"}}`,
		`{"last_logged_in_user":{"host":"https://user:password@github.com","login":"selected"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			d := copilotAuthDependencies(t, map[string]string{})
			d.GOOS = "darwin"
			dir := filepath.Join(d.Getenv("HOME"), ".copilot")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			d.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "gh" {
					t.Fatal("read keychain without a valid selected account")
				}
				return nil, errors.New("not configured")
			}
			status, err := copilotAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || status != ports.AgentAuthStatusUnknown {
				t.Fatalf("status=(%q, %v), want unknown", status, err)
			}
		})
	}
}

func TestCopilotBoundedEnvironmentRunner(t *testing.T) {
	// Exercise the actual process boundary with this test executable only;
	// no installed gh/security/agent binary is invoked.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"env", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			env := map[string]string{"AO_COPILOT_TEST_HELPER": mode, "GH_CONFIG_DIR": "scoped-config", "GH_TOKEN": ""}
			out, err := copilotRunWithEnv(ctx, executable, []string{"-test.run=^TestCopilotCommandHelper$"}, env)
			if mode == "env" {
				if err != nil || string(out) != "gho_fixture" {
					t.Fatalf("scoped runner failed: %v", err)
				}
			} else if err == nil || len(out) != 0 {
				t.Fatal("oversized subprocess output was not rejected")
			}
		})
	}
}

func TestCopilotCommandHelper(t *testing.T) {
	switch os.Getenv("AO_COPILOT_TEST_HELPER") {
	case "env":
		if os.Getenv("GH_CONFIG_DIR") != "scoped-config" || os.Getenv("GH_TOKEN") != "" {
			os.Exit(2)
		}
		_, _ = os.Stdout.Write([]byte("gho_fixture"))
		os.Exit(0)
	case "oversized":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), authutil.MaxFileSize+1))
		os.Exit(0)
	}
}
