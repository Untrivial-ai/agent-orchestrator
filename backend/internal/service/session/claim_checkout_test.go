package session

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClaimPRMetadataOnlyPreservesWorkspace(t *testing.T) {
	for _, branch := range []string{"ao/mer-1/root", "pr-topic"} {
		t.Run(branch, func(t *testing.T) {
			repo := newWorkspaceRepo(t)
			runGit(t, repo, "branch", "ao/mer-1/root")
			runGit(t, repo, "checkout", "-b", "pr-topic")
			runGit(t, repo, "commit", "--allow-empty", "-m", "PR head")
			prHead := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			runGit(t, repo, "checkout", branch)
			beforeHead := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			if (beforeHead == prHead) != (branch == "pr-topic") {
				t.Fatal("fixture must distinguish the PR head from the worker base")
			}

			st := newFakeStore()
			st.sessions["mer-1"] = domain.SessionRecord{
				ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
				Metadata: domain.SessionMetadata{WorkspacePath: repo, Branch: branch},
			}
			st.projects["mer"] = domain.ProjectRecord{ID: "mer", RepoOriginURL: "https://github.com/acme/repo"}
			st.pr["mer-1"] = domain.PRFacts{URL: "https://github.com/acme/repo/pull/7", Number: 7}
			claimer := &fakePRClaimer{}
			svc := NewWithDeps(Deps{Store: st, PRClaimer: claimer, SCM: fakeSCM{obs: ports.SCMObservation{
				Fetched: true, Provider: "github", Host: "github.com", Repo: "acme/repo",
				PR: ports.SCMPRObservation{URL: "https://github.com/acme/repo/pull/7", Number: 7, SourceBranch: "pr-topic", HeadSHA: prHead},
			}}})
			res, err := svc.ClaimPR(context.Background(), "mer-1", "7", ClaimPROptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !claimer.called || len(res.PRs) != 1 || res.BranchChanged {
				t.Fatalf("metadata-only claim: called=%t result=%+v", claimer.called, res)
			}
			if got := strings.TrimSpace(runGit(t, repo, "branch", "--show-current")); got != branch {
				t.Fatalf("workspace branch = %q, want unchanged %q", got, branch)
			}
			if got := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")); got != beforeHead {
				t.Fatalf("workspace HEAD = %q, want unchanged %q", got, beforeHead)
			}
			if got := runGit(t, repo, "status", "--porcelain"); got != "" {
				t.Fatalf("claim modified workspace: %s", got)
			}
		})
	}
}
