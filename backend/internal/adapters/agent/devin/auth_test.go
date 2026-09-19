package devin

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

func TestAuthStatusConfiguredFromDocumentedAPIKey(t *testing.T) {
	devinTestHome(t)
	t.Setenv("DEVIN_API_KEY", "cog_test")
	runner := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("status unavailable") }
	got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{}, authutil.Dependencies{Getenv: os.Getenv, Run: runner, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAuthStatusUsesBoundedDevinSpecificStatusTimeout(t *testing.T) {
	devinTestHome(t)
	t.Setenv("DEVIN_API_KEY", "")
	runner := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("status probe context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 3*time.Second || remaining > 8*time.Second {
			t.Fatalf("status probe timeout = %v, want > 3s and <= 8s", remaining)
		}
		return []byte("Logged in (via Devin)."), nil
	}

	got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{}, authutil.Dependencies{Getenv: os.Getenv, Run: runner, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAuthStatusUsesDevinNativeStatus(t *testing.T) {
	devinTestHome(t)
	t.Setenv("DEVIN_API_KEY", "")
	tests := []struct {
		name   string
		output string
		err    error
		want   ports.AgentAuthStatus
	}{
		{name: "logged in", output: "Logged in (via Devin).", want: ports.AgentAuthStatusAuthorized},
		{name: "logged out", output: "You are not logged in.", err: errors.New("exit status 1"), want: ports.AgentAuthStatusUnauthorized},
		{name: "unrecognized", output: "Authentication state unavailable.", want: ports.AgentAuthStatusUnknown},
		{name: "incidental positive", output: "Help: Logged in (via Devin). means success", want: ports.AgentAuthStatusUnknown},
		{name: "failed positive", output: "Logged in (via Devin).", err: errors.New("network failed"), want: ports.AgentAuthStatusUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "devin" || !reflect.DeepEqual(args, []string{"auth", "status"}) {
					t.Fatalf("command = %q %#v, want devin auth status", name, args)
				}
				return []byte(tc.output), tc.err
			}

			got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{}, authutil.Dependencies{Getenv: os.Getenv, Run: runner, GOOS: "linux"})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("AuthStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func devinTestHome(t *testing.T) string {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestDevinRejectionPrecedesEnvironmentKey(t *testing.T) {
	devinTestHome(t)
	t.Setenv("DEVIN_API_KEY", "cog_fixture")
	runner := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("You are not logged in."), errors.New("exit status 1")
	}
	got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{}, authutil.Dependencies{Getenv: os.Getenv, Run: runner, GOOS: "linux"})
	if err != nil || got != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = %q, error = %v; want unauthorized", got, err)
	}
}

func TestDevinCredentialFileFallback(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		xdg           bool
		want          ports.AgentAuthStatus
	}{
		{"default path", "windsurf_api_key = 'fixture-token'\ndevin_api_url = 'https://api.devin.ai'\n", false, ports.AgentAuthStatusConfigured},
		{"XDG path", "windsurf_api_key = 'fixture-token'\n", true, ports.AgentAuthStatusConfigured},
		{"URL only", "devin_api_url = 'https://api.devin.ai'\n", false, ports.AgentAuthStatusUnknown},
		{"empty key", "windsurf_api_key = '  '\n", false, ports.AgentAuthStatusUnknown},
		{"malformed", "windsurf_api_key = [", false, ports.AgentAuthStatusUnknown},
		{"nested key", "[unrelated]\nwindsurf_api_key = 'fixture-token'\n", false, ports.AgentAuthStatusUnknown},
		{"unobserved field", "api_key = 'fixture-token'\n", false, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := devinTestHome(t)
			root := filepath.Join(home, ".local", "share")
			if tc.xdg {
				root = filepath.Join(home, "xdg-data")
				t.Setenv("XDG_DATA_HOME", root)
			}
			path := filepath.Join(root, "devin", "credentials.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			runner := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("status unavailable") }
			got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{}, authutil.Dependencies{Getenv: os.Getenv, Run: runner, GOOS: "linux"})
			if err != nil || got != tc.want {
				t.Fatalf("status = %q, error = %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDevinWindowsCredentialsAndPathPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		env            map[string]string
		goos, relative string
	}{
		{"AppData", map[string]string{}, "windows", "roaming/devin/credentials.toml"},
		{"Windows default", map[string]string{}, "windows", "AppData/Roaming/devin/credentials.toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			tc.env["HOME"] = home
			tc.env["USERPROFILE"] = home
			if tc.name == "AppData" {
				tc.env["APPDATA"] = filepath.Join(home, "roaming")
			}
			path := filepath.Join(home, tc.relative)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("windsurf_api_key = 'fixture'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			d := authutil.Dependencies{Getenv: func(key string) string { return tc.env[key] }, GOOS: tc.goos, Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("offline") }}
			got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{}, d)
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("got %q, %v; want configured", got, err)
			}
		})
	}
}

func TestDevinScopedACPEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want ports.AgentAuthStatus
	}{
		{"normal CLI ignores Windsurf key", []string{"devin"}, nil, ports.AgentAuthStatusUnknown},
		{"ACP key", []string{"devin", "acp"}, nil, ports.AgentAuthStatusConfigured},
		{"ACP scoped key", []string{"devin", "acp"}, map[string]string{"WINDSURF_API_KEY": "scoped-fixture"}, ports.AgentAuthStatusConfigured},
		{"cleared ACP key", []string{"devin", "acp"}, map[string]string{"WINDSURF_API_KEY": ""}, ports.AgentAuthStatusUnknown},
		{"prompt is not ACP", []string{"devin", "--", "acp"}, nil, ports.AgentAuthStatusUnknown},
		{"model is not ACP", []string{"devin", "--model", "acp"}, nil, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			d := authutil.Dependencies{Getenv: func(key string) string {
				switch key {
				case "HOME":
					return home
				case "WINDSURF_API_KEY":
					return "fixture"
				}
				return ""
			}, GOOS: "linux", Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("offline") }}
			got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{Env: tc.env, Args: tc.args}, d)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDevinUnknownProbeSafety(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output []byte
		runErr error
	}{
		{"oversize", []byte(strings.Repeat("x", authutil.MaxFileSize) + "Logged in (via Devin)."), nil},
		{"secret error", nil, errors.New("fixture-secret-value")},
		{"negative help text", []byte("Help: You are not logged in. means signed out"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := authutil.Dependencies{Getenv: func(string) string { return "" }, Run: func(context.Context, string, ...string) ([]byte, error) { return tc.output, tc.runErr }}
			got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{}, d)
			if err != nil || got != ports.AgentAuthStatusUnknown {
				t.Fatalf("got %q, %v; want unknown with no secret-bearing error", got, err)
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := devinAuthStatus(ctx, "devin", ports.AgentAuthCheck{}, authutil.Dependencies{Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("ran cancelled command")
			return nil, nil
		}})
		if got != ports.AgentAuthStatusUnknown || !errors.Is(err, context.Canceled) {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("timeout is inconclusive", func(t *testing.T) {
		d := authutil.Dependencies{Timeout: time.Millisecond, Getenv: func(string) string { return "" }, Run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return []byte("You are not logged in."), ctx.Err()
		}}
		got, err := devinAuthStatus(context.Background(), "devin", ports.AgentAuthCheck{}, d)
		if got != ports.AgentAuthStatusUnknown || err != nil {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}
