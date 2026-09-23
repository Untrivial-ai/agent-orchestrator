package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

func TestWorkspacePRDiscoveryAndClaimPersist(t *testing.T) {
	ctx := context.Background()
	f := newSCMFixture(t, "ao/octo-1-2")
	project := domain.ProjectRecord{ID: "octo", Kind: domain.ProjectKindWorkspace, Path: t.TempDir(), RegisteredAt: f.now}
	if err := f.store.UpsertWorkspaceProject(ctx, project, []domain.WorkspaceRepoRecord{{Name: "hello", RelativePath: "hello", RepoOriginURL: scmTestOriginURL, RegisteredAt: f.now}}); err != nil {
		t.Fatal(err)
	}
	// A repository registered to another workspace must not authorize a claim.
	if err := f.store.UpsertWorkspaceProject(ctx, domain.ProjectRecord{ID: "other", Kind: domain.ProjectKindWorkspace, Path: t.TempDir(), RegisteredAt: f.now}, []domain.WorkspaceRepoRecord{{Name: "hello", RelativePath: "hello", RepoOriginURL: "https://github.com/other/hello", RegisteredAt: f.now}}); err != nil {
		t.Fatal(err)
	}
	observed := failingSCMObservation("https://github.com/octocat/hello/pull/1", 1, "sha1", "build failed")
	observed.PR.SourceBranch = "ao/octo-1-2-fix"
	observed.PR.HeadRepo = "octocat/hello"
	f.provider.detected[observed.PR.SourceBranch] = observed.PR
	f.provider.observations[1] = observed
	if err := f.observer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	prs, err := f.store.ListPRsBySession(ctx, f.session.ID)
	if err != nil || len(prs) != 1 || prs[0].URL != observed.PR.URL || prs[0].SourceBranch != observed.PR.SourceBranch {
		t.Fatalf("automatically attributed PRs = %+v, error = %v", prs, err)
	}

	claimed := failingSCMObservation("https://github.com/octocat/hello/pull/2", 2, "sha2", "build failed")
	claimed.PR.SourceBranch = "existing-custom-branch"
	f.provider.observations[2] = claimed
	svc := sessionsvc.NewWithDeps(sessionsvc.Deps{Store: f.store, SCM: f.provider, PRClaimer: f.store})
	result, err := svc.ClaimPR(ctx, f.session.ID, claimed.PR.URL, sessionsvc.ClaimPROptions{})
	if err != nil || len(result.PRs) != 2 || result.PRs[0].URL != claimed.PR.URL {
		t.Fatalf("claimed PRs = %+v, error = %v", result.PRs, err)
	}
	if _, err := svc.ClaimPR(ctx, f.session.ID, "https://github.com/other/hello/pull/2", sessionsvc.ClaimPROptions{}); !errors.Is(err, sessionsvc.ErrProjectMismatch) {
		t.Fatalf("claim across workspace registry = %v, want ErrProjectMismatch", err)
	}
	prs, err = f.store.ListPRsBySession(ctx, f.session.ID)
	if err != nil || len(prs) != 2 {
		t.Fatalf("persisted PRs = %+v, error = %v", prs, err)
	}
}
