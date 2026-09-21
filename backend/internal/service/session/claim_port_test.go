package session

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
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

func TestClaimPRReviewCompletenessFollowsReviewFetch(t *testing.T) {
	newDeps := func(scm fakeSCM, claimer *fakePRClaimer) Deps {
		st := newFakeStore()
		st.sessions["demo-1"] = domain.SessionRecord{ID: "demo-1", ProjectID: "demo", Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: "/ws"}}
		st.projects["demo"] = domain.ProjectRecord{ID: "demo", RepoOriginURL: "https://github.com/acme/repo"}
		return Deps{Store: st, SCM: scm, PRClaimer: claimer}
	}
	obs := ports.SCMObservation{
		Fetched: true, Provider: "github", Host: "github.com", Repo: "acme/repo",
		PR: ports.SCMPRObservation{URL: "https://github.com/acme/repo/pull/7", Number: 7},
	}

	t.Run("partial fetch claims partial certainty", func(t *testing.T) {
		claimer := &fakePRClaimer{}
		scm := fakeSCM{obs: obs, review: ports.SCMReviewObservation{Partial: true}}
		svc := NewWithDeps(newDeps(scm, claimer))
		if _, err := svc.ClaimPR(context.Background(), "demo-1", "7", ClaimPROptions{}); err != nil {
			t.Fatal(err)
		}
		if claimer.gotMode != ports.ReviewWriteMerge {
			t.Fatalf("mode = %v, want merge", claimer.gotMode)
		}
		if !claimer.gotPR.ReviewPartial || claimer.gotPR.ReviewObservedAt.IsZero() {
			t.Fatalf("partial claim must store (observed, partial=true), got (%v, %t)",
				claimer.gotPR.ReviewObservedAt, claimer.gotPR.ReviewPartial)
		}
	})

	t.Run("full fetch claims exact certainty", func(t *testing.T) {
		claimer := &fakePRClaimer{}
		scm := fakeSCM{obs: obs, review: ports.SCMReviewObservation{}}
		svc := NewWithDeps(newDeps(scm, claimer))
		if _, err := svc.ClaimPR(context.Background(), "demo-1", "7", ClaimPROptions{}); err != nil {
			t.Fatal(err)
		}
		if claimer.gotMode != ports.ReviewWriteReplace {
			t.Fatalf("mode = %v, want replace", claimer.gotMode)
		}
		if claimer.gotPR.ReviewPartial || claimer.gotPR.ReviewObservedAt.IsZero() {
			t.Fatalf("full claim must store (observed, partial=false), got (%v, %t)",
				claimer.gotPR.ReviewObservedAt, claimer.gotPR.ReviewPartial)
		}
	})

	t.Run("failed review fetch offers no certainty", func(t *testing.T) {
		claimer := &fakePRClaimer{}
		scm := fakeSCM{obs: obs, reviewErr: errors.New("review window unavailable")}
		svc := NewWithDeps(newDeps(scm, claimer))
		if _, err := svc.ClaimPR(context.Background(), "demo-1", "7", ClaimPROptions{}); err != nil {
			t.Fatal(err)
		}
		if claimer.gotMode != ports.ReviewWritePreserve {
			t.Fatalf("mode = %v, want preserve", claimer.gotMode)
		}
		if !claimer.gotPR.ReviewObservedAt.IsZero() || claimer.gotPR.ReviewPartial {
			t.Fatalf("claim with failed review fetch must pass the zero pair so the upsert keeps stored certainty, got (%v, %t)",
				claimer.gotPR.ReviewObservedAt, claimer.gotPR.ReviewPartial)
		}
	})
}
