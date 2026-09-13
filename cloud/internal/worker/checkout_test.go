package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stagingRecorderRunner fakes git for the non-empty-workspace clone path. It
// records the working directory of the clone (the staging root) and populates
// the clone destination with a .git dir and a tracked file so the merge and
// origin validation succeed.
type stagingRecorderRunner struct {
	cloneURL   string
	stagingDir string
}

func (r *stagingRecorderRunner) Run(_ context.Context, dir string, _ map[string]string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "clone" {
		r.stagingDir = dir
		dest := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("# repo\n"), 0o644); err != nil {
			return "", err
		}
		return "", nil
	}
	// validateOrigin: `remote get-url origin`
	return r.cloneURL, nil
}

// A workspace the coding agent has already written into (non-empty, no .git)
// must check out even when its parent is the provider's durable root that the
// worker user cannot write to. The fix stages inside the workspace itself, so
// the staging dir must live under the workspace, never under its parent.
func TestCloneIntoNonEmptyWorkspaceStagesInsideWorkspace(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "repository")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	// The agent wrote a file before checkout ran — this is what forces the
	// non-empty (staging) path.
	if err := os.WriteFile(filepath.Join(workspace, ".claude"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &stagingRecorderRunner{cloneURL: "https://github.com/acme/repo.git"}
	if err := PrepareCheckout(context.Background(), runner, workspace,
		CheckoutGrantResponse{CloneURL: "https://github.com/acme/repo.git"}); err != nil {
		t.Fatalf("PrepareCheckout: %v", err)
	}

	if r := runner.stagingDir; !strings.HasPrefix(filepath.Clean(r), filepath.Clean(workspace)+string(filepath.Separator)) {
		t.Fatalf("staging dir %q is not inside the workspace %q (would fail on a durable root the worker cannot write)", r, workspace)
	}
	// The clone's tracked files were merged in and the agent's file preserved.
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		t.Fatalf("cloned .git not merged into workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatalf("cloned file not merged into workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".claude")); err != nil {
		t.Fatalf("agent file not preserved: %v", err)
	}
	// The hidden staging dir is cleaned up before returning.
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ao-checkout-") {
			t.Fatalf("staging dir %q was left behind in the workspace", e.Name())
		}
	}
}
