package commandcode

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusFromOutputVerifiedIsAuthorized(t *testing.T) {
	if got := commandCodeAuthStatusFromOutput("Authentication verified"); got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized", got)
	}
}

func TestAuthStatusFromOutputUnauthorized(t *testing.T) {
	for _, out := range []string{"Authentication required", "not logged in", "No credentials found"} {
		if got := commandCodeAuthStatusFromOutput(out); got != ports.AgentAuthStatusUnauthorized {
			t.Fatalf("status(%q) = %q, want unauthorized", out, got)
		}
	}
}

func TestAuthStatusFromOutputNotVerifiedIsUnauthorized(t *testing.T) {
	if got := commandCodeAuthStatusFromOutput("Not verified"); got != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = %q, want unauthorized", got)
	}
}

func TestAuthStatusFromOutputUnknown(t *testing.T) {
	if got := commandCodeAuthStatusFromOutput("command not configured"); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", got)
	}
}

func TestAuthStatusRunsStatusProbe(t *testing.T) {
	original := authprobe.CmdRunner
	t.Cleanup(func() { authprobe.CmdRunner = original })

	var gotName string
	var gotArgs []string
	authprobe.CmdRunner = func(_ context.Context, name string, arg ...string) ([]byte, error) {
		gotName = name
		gotArgs = arg
		return []byte("Authentication verified"), nil
	}

	status, err := commandCodeAuthStatus(context.Background(), "/usr/local/bin/cmd")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized", status)
	}
	if gotName != "/usr/local/bin/cmd" || !reflect.DeepEqual(gotArgs, []string{"status"}) {
		t.Fatalf("probe = (%q, %#v), want (/usr/local/bin/cmd, [status])", gotName, gotArgs)
	}
}

func TestAuthStatusProbeFailureStaysUnknown(t *testing.T) {
	original := authprobe.CmdRunner
	t.Cleanup(func() { authprobe.CmdRunner = original })
	authprobe.CmdRunner = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("boom")
	}

	status, err := commandCodeAuthStatus(context.Background(), "/usr/local/bin/cmd")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", status)
	}
}

func TestAuthStatusEmptyBinaryIsUnknown(t *testing.T) {
	status, err := commandCodeAuthStatus(context.Background(), "  ")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", status)
	}
}
