package gitworktree

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestFreshPreparedBranchSkipsRemoteCollision(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	mainSHA := gitOutput(t, git, repo, "rev-parse", "refs/heads/main")
	runGit(t, git, repo, "update-ref", "refs/remotes/origin/ao/sess/root", mainSHA)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "proj", SessionID: "sess", Branch: "ao/sess/root", FreshBranch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Branch != "ao/sess/root-2" {
		t.Fatalf("prepared branch = %q, want fresh suffix", info.Branch)
	}
	if got := gitOutput(t, git, info.Path, "rev-parse", "HEAD"); got != mainSHA {
		t.Fatalf("prepared HEAD = %q, want main %q", got, mainSHA)
	}
}
