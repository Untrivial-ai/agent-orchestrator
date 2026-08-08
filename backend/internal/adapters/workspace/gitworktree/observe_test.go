package gitworktree

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestParseObservedWorkspaceChanges(t *testing.T) {
	changes, staged, untracked := parseObservedWorkspaceChanges(" M tracked.go\nA  staged.go\n?? new file.md\n", 2)
	if !staged || !untracked {
		t.Fatalf("flags = staged %v, untracked %v; want both true", staged, untracked)
	}
	want := []ports.WorkspaceChange{
		{Path: "tracked.go", Status: " M"},
		{Path: "staged.go", Status: "A "},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
}

func TestObserveWorkspaceReturnsBoundedGitFacts(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "project", "session-1")
	if err := os.MkdirAll(workspacePath, 0o750); err != nil {
		t.Fatal(err)
	}
	physicalRoot, err := physicalAbs(root)
	if err != nil {
		t.Fatal(err)
	}
	physicalWorkspace, err := physicalAbs(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	w := &Workspace{
		binary:      "git",
		managedRoot: physicalRoot,
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch args[2] {
			case "rev-parse":
				return []byte("abc123\n"), nil
			case "branch":
				return []byte("feature/switch\n"), nil
			case "status":
				return []byte(" M manager.go\n?? notes.md\n"), nil
			case "log":
				return []byte("abc123\x1fimplement switch\x1f2026-08-04T10:00:00Z\x1e"), nil
			default:
				t.Fatalf("unexpected git args: %#v", args)
				return nil, nil
			}
		},
	}

	got, err := w.ObserveWorkspace(context.Background(), ports.WorkspaceInfo{Path: physicalWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != "abc123" || got.Branch != "feature/switch" || !got.Dirty || !got.Untracked || got.Staged {
		t.Fatalf("observation = %#v", got)
	}
	if len(got.Commits) != 1 || got.Commits[0].Subject != "implement switch" {
		t.Fatalf("commits = %#v", got.Commits)
	}
}
