package gitworktree

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestImportWorkspaceUsesLocalBaseWithoutRemoteCalls(t *testing.T) {
	git := requireGit(t)
	dir := t.TempDir()
	repo := setupOriginClone(t, git, dir)
	runGit(t, git, repo, "branch", "import-source", "origin/main")
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(dir, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatal(err)
	}
	runner := ws.run
	remoteCalls := 0
	ws.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		for _, arg := range args {
			if arg == "ls-remote" || arg == "fetch" {
				remoteCalls++
				return nil, fmt.Errorf("unexpected remote call: %s", arg)
			}
		}
		return runner(ctx, binary, args...)
	}
	base, err := ws.ResolveLocalDefaultBranch(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, branch := range []string{"import-source", "import-fresh"} {
		_, err := ws.Create(context.Background(), ports.WorkspaceConfig{ProjectID: "proj", SessionID: domain.SessionID(fmt.Sprintf("session-%d", i)), Branch: branch, BaseRef: base.BaseRef})
		if err != nil {
			t.Fatal(err)
		}
	}
	if remoteCalls != 0 {
		t.Fatalf("listing/importing local history must not probe or fetch remotes; got %d calls", remoteCalls)
	}
}

func TestDefaultBranchResolutionKeepsLiveRemoteHeadForOrdinarySpawns(t *testing.T) {
	git := requireGit(t)
	dir := t.TempDir()
	repo := setupOriginClone(t, git, dir)
	origin := filepath.Join(dir, "origin.git")
	runGit(t, git, origin, "branch", "trunk", "main")
	runGit(t, git, origin, "symbolic-ref", "HEAD", "refs/heads/trunk")
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(dir, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatal(err)
	}
	local, err := ws.ResolveLocalDefaultBranch(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if local.Branch != "main" {
		t.Fatalf("import default = %q, want cached main", local.Branch)
	}
	live, err := ws.ResolveDefaultBranch(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if live.Branch != "trunk" || live.BaseRef != "refs/remotes/origin/trunk" {
		t.Fatalf("ordinary spawn default = %#v, want live origin/trunk", live)
	}
}
