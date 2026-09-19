package auggie

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const auggieValidSession = `{"accessToken":"test-access-token","tenantURL":"https://tenant.api.augmentcode.com/","scopes":["email"]}`

func auggieTestDeps(t *testing.T, env map[string]string) authutil.Dependencies {
	t.Helper()
	home := t.TempDir()
	return authutil.Dependencies{
		Getenv: func(key string) string {
			if key == "HOME" || key == "USERPROFILE" {
				return home
			}
			return env[key]
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("Auggie local check ran a command")
			return nil, nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC) },
	}
}

func auggieTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuggieTypedSessionEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, session string
		want          ports.AgentAuthStatus
	}{
		{"browser", auggieValidSession, ports.AgentAuthStatusConfigured},
		{"service account", `{"accessToken":"service-account-token","tenantURL":"https://tenant.api.augmentcode.com","scopes":["read","write"]}`, ports.AgentAuthStatusConfigured},
		{"token only", `{"token":"secret"}`, ports.AgentAuthStatusUnknown},
		{"missing tenant", `{"accessToken":"secret","scopes":["email"]}`, ports.AgentAuthStatusUnknown},
		{"invalid tenant", `{"accessToken":"secret","tenantURL":"not a URL","scopes":["email"]}`, ports.AgentAuthStatusUnknown},
		{"missing scopes", `{"accessToken":"secret","tenantURL":"https://tenant.api.augmentcode.com"}`, ports.AgentAuthStatusUnknown},
		{"invalid scopes type", `{"accessToken":"secret","tenantURL":"https://tenant.api.augmentcode.com","scopes":"email"}`, ports.AgentAuthStatusUnknown},
		{"invalid scope item", `{"accessToken":"secret","tenantURL":"https://tenant.api.augmentcode.com","scopes":[true]}`, ports.AgentAuthStatusUnknown},
		{"wrong token type", `{"accessToken":123,"tenantURL":"https://tenant.api.augmentcode.com","scopes":["email"]}`, ports.AgentAuthStatusUnknown},
		{"nested credential", `{"other":` + auggieValidSession + `}`, ports.AgentAuthStatusUnknown},
		{"malformed", `{"accessToken":"secret"`, ports.AgentAuthStatusUnknown},
		{"opaque env", "some-token", ports.AgentAuthStatusUnknown},
		{"empty", "", ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := auggieTestDeps(t, map[string]string{"AUGMENT_SESSION_AUTH": tt.session})
			got, err := auggieAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAuggieExpiryDoesNotInventARefreshPath(t *testing.T) {
	for _, tt := range []struct {
		name, claims, extra string
		want                ports.AgentAuthStatus
	}{
		{"future", `{"exp":1900000000}`, "", ports.AgentAuthStatusConfigured},
		{"expired", `{"exp":1600000000}`, "", ports.AgentAuthStatusUnauthorized},
		{"unix epoch is expired", `{"exp":0}`, "", ports.AgentAuthStatusUnauthorized},
		{"refresh field is not a native session refresh path", `{"exp":1600000000}`, `,"refreshToken":"unsupported"`, ports.AgentAuthStatusUnauthorized},
		{"malformed expiry", `{"exp":"tomorrow"}`, "", ports.AgentAuthStatusUnknown},
		{"null expiry", `{"exp":null}`, "", ports.AgentAuthStatusUnknown},
		{"no expiry", `{}`, "", ports.AgentAuthStatusConfigured},
	} {
		t.Run(tt.name, func(t *testing.T) {
			token := "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(tt.claims)) + ".c2lnbmF0dXJl"
			d := auggieTestDeps(t, map[string]string{"AUGMENT_SESSION_AUTH": `{"accessToken":"` + token + `","tenantURL":"https://tenant.api.augmentcode.com","scopes":["email"]` + tt.extra + `}`})
			got, err := auggieAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAuggieMalformedJWTIsNotSessionEvidence(t *testing.T) {
	d := auggieTestDeps(t, map[string]string{"AUGMENT_SESSION_AUTH": `{"accessToken":"not-json.eyJleHAiOjE5MDAwMDAwMDB9.c2ln","tenantURL":"https://tenant.api.augmentcode.com","scopes":["email"]}`})
	got, err := auggieAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAuggieExpiredScopedSessionOverridesValidEnvAndFile(t *testing.T) {
	d := auggieTestDeps(t, map[string]string{"AUGMENT_SESSION_AUTH": auggieValidSession})
	auggieTestFile(t, filepath.Join(d.Getenv("HOME"), ".augment", "session.json"), auggieValidSession)
	expired := `{"accessToken":"eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE2MDAwMDAwMDB9.c2ln","tenantURL":"https://tenant.api.augmentcode.com","scopes":["email"]}`
	got, err := auggieAuthStatus(context.Background(), ports.AgentAuthCheck{Args: []string{"--augment-session-json", expired}}, d)
	if err != nil || got != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAuggieSessionPrecedenceAndScopedPaths(t *testing.T) {
	for _, tt := range []struct {
		name, arg, env string
		file           bool
		want           ports.AgentAuthStatus
	}{
		{"inline", auggieValidSession, "bad", false, ports.AgentAuthStatusConfigured},
		{"relative path", "session.json", "bad", false, ports.AgentAuthStatusConfigured},
		{"malformed flag falls through to env", "{broken", auggieValidSession, false, ports.AgentAuthStatusConfigured},
		{"missing path falls through to env", "missing.json", auggieValidSession, false, ports.AgentAuthStatusConfigured},
		{"malformed env falls through to file", "", "bad", true, ports.AgentAuthStatusConfigured},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := auggieTestDeps(t, map[string]string{"AUGMENT_SESSION_AUTH": tt.env})
			workspace := t.TempDir()
			auggieTestFile(t, filepath.Join(workspace, "session.json"), auggieValidSession)
			if tt.file {
				auggieTestFile(t, filepath.Join(d.Getenv("HOME"), ".augment", "session.json"), auggieValidSession)
			}
			check := ports.AgentAuthCheck{WorkingDir: workspace}
			if tt.arg != "" {
				check.Args = []string{"auggie", "--augment-session-json", tt.arg}
			}
			got, err := auggieAuthStatus(context.Background(), check, d)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAuggieScopedInputWinsAndPromptFlagsAreIgnored(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		env  map[string]string
		want ports.AgentAuthStatus
	}{
		{"equals flag", []string{"--augment-session-json=" + auggieValidSession}, nil, ports.AgentAuthStatusConfigured},
		{"after separator", []string{"--", "--augment-session-json", auggieValidSession}, nil, ports.AgentAuthStatusUnknown},
		{"scoped env", nil, map[string]string{"AUGMENT_SESSION_AUTH": auggieValidSession}, ports.AgentAuthStatusConfigured},
		{"explicit empty scoped env", nil, map[string]string{"AUGMENT_SESSION_AUTH": ""}, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inherited := ""
			if tt.name == "explicit empty scoped env" {
				inherited = auggieValidSession
			}
			d := auggieTestDeps(t, map[string]string{"AUGMENT_SESSION_AUTH": inherited})
			got, err := auggieAuthStatus(context.Background(), ports.AgentAuthCheck{Args: tt.args, Env: tt.env}, d)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAuggieCancellationAndUnreadableFileAreSafe(t *testing.T) {
	d := auggieTestDeps(t, nil)
	d.Lstat = func(string) (os.FileInfo, error) {
		return nil, errors.New("credential-containing path must not escape")
	}
	got, err := auggieAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("got %q, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = auggieAuthStatus(ctx, ports.AgentAuthCheck{}, d)
	if got != ports.AgentAuthStatusUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %q, %v", got, err)
	}
}
