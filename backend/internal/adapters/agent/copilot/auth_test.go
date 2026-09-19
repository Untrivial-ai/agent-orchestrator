package copilot

import (
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

func TestCopilotLocalAuthStatusConfiguredWithBYOKProvider(t *testing.T) {
	clearCopilotAuthProbeEnv(t)
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "http://localhost:11434")
	t.Setenv("COPILOT_MODEL", "llama3.2")

	status, ok, err := copilotLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestCopilotLocalAuthStatusUsesCopilotHome(t *testing.T) {
	clearCopilotAuthProbeEnv(t)
	dir := t.TempDir()
	t.Setenv("COPILOT_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"authToken":"oauth-token"}`), 0o600); err != nil {
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
		return []byte("gho_test-token"), nil
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
	for _, name := range []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_API_KEY", "COPILOT_MODEL", "COPILOT_HOME"} {
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
				visited = append(visited, key)
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
			if err := os.WriteFile(path, []byte(`{"authToken":"gho_config"}`), 0o600); err != nil {
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
			keychain, gh := false, false
			d.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				switch name {
				case "/usr/bin/security":
					if !reflect.DeepEqual(args, []string{"find-generic-password", "-s", "copilot-cli", "-w"}) {
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

func TestCopilotAuthStatusDoesNotUseGHWithDifferentScopedEnvironment(t *testing.T) {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_HOST", "GH_CONFIG_DIR", "HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Run(key, func(t *testing.T) {
			d := copilotAuthDependencies(t, map[string]string{key: "inherited-value"})
			scope := ports.AgentAuthCheck{Env: map[string]string{key: ""}}
			status, err := copilotAuthStatus(context.Background(), scope, d)
			if err != nil || status != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = (%q, %v), want unknown", status, err)
			}
		})
	}
}
