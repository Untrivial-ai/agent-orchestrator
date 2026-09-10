package authprobe

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCLIStatus_Mocked(t *testing.T) {
	tests := []struct {
		name       string
		mockOutput string
		mockError  error
		wantStatus ports.AgentAuthStatus
		wantError  bool
	}{
		{
			name:       "authorized status check",
			mockOutput: "User is logged in and authenticated",
			wantStatus: ports.AgentAuthStatusAuthorized,
		},
		{
			name:       "unauthorized status check",
			mockOutput: "You are not logged in",
			wantStatus: ports.AgentAuthStatusUnauthorized,
		},
		{
			name:       "unknown status check with exit error",
			mockOutput: "command not found or invalid syntax",
			mockError:  errors.New("exit status 1"),
			wantStatus: ports.AgentAuthStatusUnknown,
		},
		{
			name:       "unknown status check with success but unrecognized output",
			mockOutput: "some random output here",
			wantStatus: ports.AgentAuthStatusUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore CmdRunner
			oldCmdRunner := CmdRunner
			defer func() { CmdRunner = oldCmdRunner }()

			CmdRunner = func(ctx context.Context, name string, arg ...string) ([]byte, error) {
				return []byte(tt.mockOutput), tt.mockError
			}

			status, err := CLIStatus(context.Background(), "mockbinary", [][]string{{"auth", "status"}})
			if (err != nil) != tt.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tt.wantStatus {
				t.Errorf("CLIStatus() = %v, want %v", status, tt.wantStatus)
			}
		})
	}
}

func TestCLIStatusRequiresExplicitCommands(t *testing.T) {
	oldCmdRunner := CmdRunner
	defer func() { CmdRunner = oldCmdRunner }()
	called := false
	CmdRunner = func(ctx context.Context, name string, arg ...string) ([]byte, error) {
		called = true
		return []byte("authenticated"), nil
	}

	status, err := CLIStatus(context.Background(), "mockbinary", nil)
	if err != nil {
		t.Fatalf("CLIStatus: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
	if called {
		t.Fatal("CLIStatus ran a command without explicit adapter commands")
	}
}

func TestCLIStatusTimeoutDegradesToUnknown(t *testing.T) {
	oldCmdRunner := CmdRunner
	defer func() { CmdRunner = oldCmdRunner }()
	CmdRunner = func(ctx context.Context, name string, arg ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	status, err := CLIStatus(context.Background(), "mockbinary", [][]string{{"auth", "status"}})
	if err != nil {
		t.Fatalf("CLIStatus: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func TestStatusFromTextExplicitFalseKeys(t *testing.T) {
	tests := []string{
		`{ "authenticated": false }`,
		`{ "authorized": false }`,
		`authenticated=false`,
		`authorized: false`,
		`{ "logged_in": false }`,
		`{ "loggedIn": false }`,
	}

	for _, out := range tests {
		t.Run(out, func(t *testing.T) {
			if got := StatusFromText(out); got != ports.AgentAuthStatusUnauthorized {
				t.Fatalf("StatusFromText(%q) = %q, want %q", out, got, ports.AgentAuthStatusUnauthorized)
			}
		})
	}
}

func TestStatusFromTextExplicitTrueKeys(t *testing.T) {
	tests := []string{
		`{ "authenticated": true }`,
		`{ "authorized": true }`,
		`{ "loggedIn": true }`,
	}

	for _, out := range tests {
		t.Run(out, func(t *testing.T) {
			if got := StatusFromText(out); got != ports.AgentAuthStatusAuthorized {
				t.Fatalf("StatusFromText(%q) = %q, want %q", out, got, ports.AgentAuthStatusAuthorized)
			}
		})
	}
}
