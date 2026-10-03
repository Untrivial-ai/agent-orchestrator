package opencodev2

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDataHomeIsSiblingOfUserDataHome(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("XDG_DATA_HOME", parent)
	got, ok := DataHome()
	if !ok || got != filepath.Join(parent, "opencode-v2-home") {
		t.Fatalf("DataHome = (%q, %v)", got, ok)
	}
	t.Setenv("XDG_DATA_HOME", got)
	if again, _ := DataHome(); again != got {
		t.Fatalf("DataHome is not idempotent: %q != %q", again, got)
	}
}

func TestLaunchCommandIsolatesDataHome(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("XDG_DATA_HOME", parent)
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	argv, err := command(context.Background(), "/bin/opencode", ports.LaunchConfig{SessionID: "s1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"env", "XDG_DATA_HOME=" + filepath.Join(parent, "opencode-v2-home"), "/bin/opencode", "--standalone"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %#v, want %#v", argv, want)
		}
	}
}
