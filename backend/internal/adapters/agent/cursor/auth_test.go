package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Fixtures follow Cursor 2026.09.15-d2fe57e's commands/status.ts. Without
// userInfo, authenticated can mean that the getMe provider request failed.
func TestCursorCLIAuthStatusJSON(t *testing.T) {
	for _, tt := range []struct {
		name, out string
		err       error
		want      ports.AgentAuthStatus
	}{
		{"validated account", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"userInfo":{"email":"user@example.com","userId":123}}`, nil, ports.AgentAuthStatusAuthorized},
		{"unvalidated tokens", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"message":"Logged in (unable to fetch user details)"}`, nil, ports.AgentAuthStatusConfigured},
		{"signed out", `{"status":"unauthenticated","isAuthenticated":false,"hasAccessToken":false,"hasRefreshToken":false,"message":"Not logged in"}`, nil, ports.AgentAuthStatusUnauthorized},
		{"partial credentials", `{"status":"partially-authenticated","isAuthenticated":false,"hasAccessToken":true,"hasRefreshToken":false,"message":"Partially authenticated (missing refresh token)"}`, nil, ports.AgentAuthStatusConfigured},
		{"missing fields", `{"status":"authenticated"}`, nil, ports.AgentAuthStatusUnknown},
		{"inconsistent fields", `{"status":"authenticated","isAuthenticated":false,"hasAccessToken":true,"hasRefreshToken":true,"userInfo":{"email":"user@example.com"}}`, nil, ports.AgentAuthStatusUnknown},
		{"unknown version", `{"status":"new-session-format","isAuthenticated":true}`, nil, ports.AgentAuthStatusUnknown},
		{"nested negative text", `{"diagnostic":{"message":"not logged in","authenticated":false}}`, nil, ports.AgentAuthStatusUnknown},
		{"malformed negative text", `{"status":"unauthenticated","message":"not logged in"`, nil, ports.AgentAuthStatusUnknown},
		{"generic positive text", "Logged in as user@example.com", nil, ports.AgentAuthStatusUnknown},
		{"invalid field type", `{"status":"unauthenticated","isAuthenticated":"false","hasAccessToken":false,"hasRefreshToken":false}`, nil, ports.AgentAuthStatusUnknown},
		{"command error", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"userInfo":{"email":"user@example.com"}}`, errors.New("failed with secret"), ports.AgentAuthStatusUnknown},
		{"native error", `{"status":"error","message":"Status check error: unauthorized"}`, errors.New("exit status 1"), ports.AgentAuthStatusUnknown},
		{"timeout", `{"status":"unauthenticated","isAuthenticated":false,"hasAccessToken":false,"hasRefreshToken":false}`, context.DeadlineExceeded, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run := stubCursorAuthCommand(t, []byte(tt.out), tt.err)
			got, err := cursorCLIAuthStatus(context.Background(), "cursor-agent", ports.AgentAuthCheck{}, run)
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCursorCLIAuthStatusValidatedAccountFields(t *testing.T) {
	for _, tt := range []struct {
		name, userInfo string
		want           ports.AgentAuthStatus
	}{
		{"email", `{"email":"user@example.com"}`, ports.AgentAuthStatusAuthorized},
		{"user id", `{"userId":123}`, ports.AgentAuthStatusAuthorized},
		{"first name", `{"firstName":"Casey"}`, ports.AgentAuthStatusAuthorized},
		{"last name", `{"lastName":"Example"}`, ports.AgentAuthStatusAuthorized},
		{"team id", `{"teamId":456}`, ports.AgentAuthStatusAuthorized},
		{"created at", `{"createdAt":"2026-09-19T00:00:00Z"}`, ports.AgentAuthStatusAuthorized},
		{"empty string is present", `{"firstName":""}`, ports.AgentAuthStatusAuthorized},
		{"zero user id is present", `{"userId":0}`, ports.AgentAuthStatusAuthorized},
		{"empty object", `{}`, ports.AgentAuthStatusConfigured},
		{"null fields", `{"email":null,"userId":null,"firstName":null,"lastName":null,"teamId":null,"createdAt":null}`, ports.AgentAuthStatusConfigured},
		{"unrelated field", `{"token":"not-account-evidence"}`, ports.AgentAuthStatusConfigured},
		{"wrong user id type", `{"email":"user@example.com","userId":"user_123"}`, ports.AgentAuthStatusUnknown},
		{"wrong team id type", `{"email":"user@example.com","teamId":"456"}`, ports.AgentAuthStatusUnknown},
		{"wrong created at type", `{"email":"user@example.com","createdAt":123}`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"userInfo":` + tt.userInfo + `}`
			run := stubCursorAuthCommand(t, []byte(out), nil)
			got, err := cursorCLIAuthStatus(context.Background(), "cursor-agent", ports.AgentAuthCheck{}, run)
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCursorAuthStatusConfiguredKey(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "test-key")
	run := func(context.Context, ports.AgentAuthCheck, string, ...string) ([]byte, error) {
		t.Fatal("must not probe a different browser credential")
		return nil, nil
	}
	got, err := (&Plugin{resolvedBinary: "cursor-agent", authRunner: run}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, err = %v", got, err)
	}
}

func TestCursorAuthStatusForScopedKey(t *testing.T) {
	for _, tt := range []struct {
		name, inherited string
		check           ports.AgentAuthCheck
		want            ports.AgentAuthStatus
	}{
		{"flag", "", ports.AgentAuthCheck{Args: []string{"cursor-agent", "--api-key", "scoped-key", "prompt"}}, ports.AgentAuthStatusConfigured},
		{"equals flag", "", ports.AgentAuthCheck{Args: []string{"cursor-agent", "--api-key=scoped-key"}}, ports.AgentAuthStatusConfigured},
		{"environment", "", ports.AgentAuthCheck{Env: map[string]string{"CURSOR_API_KEY": "scoped-key"}}, ports.AgentAuthStatusConfigured},
		{"cleared inherited key", "inherited-key", ports.AgentAuthCheck{Env: map[string]string{"CURSOR_API_KEY": ""}}, ports.AgentAuthStatusUnknown},
		{"flag after separator", "", ports.AgentAuthCheck{Args: []string{"cursor-agent", "--", "--api-key", "not-a-key"}}, ports.AgentAuthStatusUnknown},
		{"missing flag value", "", ports.AgentAuthCheck{Args: []string{"cursor-agent", "--api-key", "--print"}}, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CURSOR_API_KEY", tt.inherited)
			run := stubCursorAuthCommand(t, []byte(`{}`), nil)
			checker, ok := any(&Plugin{resolvedBinary: "cursor-agent", authRunner: run}).(ports.AgentScopedAuthChecker)
			if !ok {
				t.Fatal("Cursor does not check scoped credentials")
			}
			got, err := checker.AuthStatusFor(context.Background(), tt.check)
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCursorAuthStatusForScopesNativeProbe(t *testing.T) {
	workspace := t.TempDir()
	run := func(ctx context.Context, check ports.AgentAuthCheck, name string, args ...string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("status probe has no deadline")
		}
		if name != "cursor-agent" || !reflect.DeepEqual(args, []string{"status", "--format", "json"}) {
			t.Fatalf("unexpected command: %s %#v", name, args)
		}
		if check.WorkingDir != workspace || !reflect.DeepEqual(check.Env, map[string]string{"AO_TEST_OVERLAY": "scoped", "AO_TEST_MASK": ""}) {
			t.Fatalf("probe scope = %#v", check)
		}
		return []byte(`{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"userInfo":{"email":"user@example.com"}}`), nil
	}
	t.Setenv("CURSOR_API_KEY", "")

	got, err := (&Plugin{resolvedBinary: "cursor-agent", authRunner: run}).AuthStatusFor(context.Background(), ports.AgentAuthCheck{
		WorkingDir: workspace,
		Env: map[string]string{
			"AO_TEST_OVERLAY": "scoped",
			"AO_TEST_MASK":    "",
		},
	})
	if err != nil || got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, err = %v; want %q", got, err, ports.AgentAuthStatusAuthorized)
	}
}

func TestCursorAuthStatusIgnoresUndocumentedAuthInfo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CURSOR_API_KEY", "")
	if err := os.Mkdir(filepath.Join(home, ".cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".cursor", "cli-config.json"), []byte(`{"authInfo":{"accessToken":"test-token","refreshToken":"test-refresh"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := stubCursorAuthCommand(t, []byte(`{}`), nil)
	got, err := (&Plugin{resolvedBinary: "cursor-agent", authRunner: run}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, err = %v", got, err)
	}
}

func TestCursorAuthStatusCanceled(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "test-key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := (&Plugin{resolvedBinary: "cursor-agent"}).AuthStatus(ctx)
	if got != ports.AgentAuthStatusUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("status = %q, err = %v", got, err)
	}
}

func stubCursorAuthCommand(t *testing.T, out []byte, err error) authprobe.ScopedCmdRunner {
	t.Helper()
	return func(ctx context.Context, _ ports.AgentAuthCheck, name string, arg ...string) ([]byte, error) {
		if name != "cursor-agent" || !reflect.DeepEqual(arg, []string{"status", "--format", "json"}) {
			t.Fatalf("unexpected command: %s %#v", name, arg)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("status probe has no deadline")
		}
		return out, err
	}
}
