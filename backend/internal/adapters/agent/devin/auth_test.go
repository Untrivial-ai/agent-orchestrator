package devin

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusAuthorizedFromDocumentedAPIKey(t *testing.T) {
	t.Setenv("DEVIN_API_KEY", "cog_test")
	got, err := (&Plugin{resolvedBinary: "devin"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAuthStatusUsesBoundedDevinSpecificStatusTimeout(t *testing.T) {
	t.Setenv("DEVIN_API_KEY", "")
	previous := authprobe.CmdRunner
	t.Cleanup(func() { authprobe.CmdRunner = previous })
	authprobe.CmdRunner = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
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

	got, err := (&Plugin{resolvedBinary: "devin"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAuthStatusUsesDevinNativeStatus(t *testing.T) {
	t.Setenv("DEVIN_API_KEY", "")
	previous := authprobe.CmdRunner
	t.Cleanup(func() { authprobe.CmdRunner = previous })

	tests := []struct {
		name   string
		output string
		err    error
		want   ports.AgentAuthStatus
	}{
		{name: "logged in", output: "Logged in (via Devin).", want: ports.AgentAuthStatusAuthorized},
		{name: "logged out", output: "You are not logged in.", err: errors.New("exit status 1"), want: ports.AgentAuthStatusUnauthorized},
		{name: "unrecognized", output: "Authentication state unavailable.", want: ports.AgentAuthStatusUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			authprobe.CmdRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "devin" || !reflect.DeepEqual(args, []string{"auth", "status"}) {
					t.Fatalf("command = %q %#v, want devin auth status", name, args)
				}
				return []byte(tc.output), tc.err
			}

			got, err := (&Plugin{resolvedBinary: "devin"}).AuthStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("AuthStatus = %q, want %q", got, tc.want)
			}
		})
	}
}
