package cursor

import (
	"context"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCursorCLIAuthStatusAuthorizedFromStatus(t *testing.T) {
	restore := stubCursorAuthCommand(t, []string{"status"}, []byte("✓ Logged in as user@example.com\n"), nil)
	defer restore()

	status, err := cursorCLIAuthStatus(context.Background(), "cursor-agent")
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusAuthorized)
	}
}

func TestCursorCLIAuthStatusUnknownFromKeychainError(t *testing.T) {
	restore := stubCursorAuthCommand(t, []string{"status"}, []byte("ERROR: SecItemCopyMatching failed -50\n"), assertErr("exit status 139"))
	defer restore()

	status, err := cursorCLIAuthStatus(context.Background(), "cursor-agent")
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

type assertErr string

func (e assertErr) Error() string {
	return string(e)
}

func stubCursorAuthCommand(t *testing.T, wantArgs []string, out []byte, err error) func() {
	t.Helper()
	previous := authprobe.CmdRunner
	authprobe.CmdRunner = func(ctx context.Context, name string, arg ...string) ([]byte, error) {
		if name != "cursor-agent" || !reflect.DeepEqual(arg, wantArgs) {
			t.Fatalf("command = %s %#v, want cursor-agent %#v", name, arg, wantArgs)
		}
		return out, err
	}
	return func() { authprobe.CmdRunner = previous }
}
