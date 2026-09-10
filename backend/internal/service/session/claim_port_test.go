package session

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestClaimPRSSHPortPreserved(t *testing.T) {
	st := newFakeStore()
	st.sessions["demo-1"] = domain.SessionRecord{ID: "demo-1", ProjectID: "demo", Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: "/ws"}}
	st.projects["demo"] = domain.ProjectRecord{ID: "demo", RepoOriginURL: "ssh://git@gitlab.example.com:8443/alice/repo.git"}
	scm := &claimTargetSCM{}
	svc := NewWithDeps(Deps{Store: st, SCM: scm, PRClaimer: &fakePRClaimer{}})
	_, err := svc.ClaimPR(context.Background(), "demo-1", "7", ClaimPROptions{})
	if err != nil {
		t.Fatal(err)
	}
	if scm.refs[0].Repo.Host != "gitlab.example.com:8443" {
		t.Fatalf("claim changed SCM authority to %q; expected explicit origin port 8443", scm.refs[0].Repo.Host)
	}
}
func TestClaimPRHTTPSPortOriginStillClaimable(t *testing.T) {
	st := newFakeStore()
	st.sessions["demo-1"] = domain.SessionRecord{ID: "demo-1", ProjectID: "demo", Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: "/ws"}}
	st.projects["demo"] = domain.ProjectRecord{ID: "demo", RepoOriginURL: "https://gitlab.example.com:8443/alice/repo.git"}
	svc := NewWithDeps(Deps{Store: st, SCM: &claimTargetSCM{}, PRClaimer: &fakePRClaimer{}})
	_, err := svc.ClaimPR(context.Background(), "demo-1", "7", ClaimPROptions{})
	if err != nil {
		t.Fatalf("previously supported HTTPS origin is no longer claimable: %v", err)
	}
}
