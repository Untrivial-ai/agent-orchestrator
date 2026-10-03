package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLocalGitRefsIncludesCustomBranchHead(t *testing.T) {
	workspace := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "--quiet")
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "-m", "test")
	run("branch", "docs/custom")
	refs, err := localGitRefs(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range refs {
		if ref.Branch == "docs/custom" && len(ref.SHA) == 40 {
			found = true
		}
	}
	if !found {
		t.Fatalf("custom branch missing from %+v", refs)
	}
}
