package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type claimTargetSCM struct {
	fakeSCM
	refs []ports.SCMPRRef
}

func (f *claimTargetSCM) FetchPullRequests(_ context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	f.refs = refs
	ref := refs[0]
	return []ports.SCMObservation{{Fetched: true, Provider: ref.Repo.Provider, Host: ref.Repo.Host, Repo: ref.Repo.Repo, PR: ports.SCMPRObservation{URL: ref.URL, Number: ref.Number}}}, nil
}

func TestClaimPRExplicitCanonicalRepository(t *testing.T) {
	for _, tc := range []struct {
		name, origin, canonical, ref, wantRepo, wantURL string
		wantErr                                         error
	}{
		{"authenticated checkout origin", "https://git-user:token@github.com/alice/repo.git", "https://github.com/acme/repo", "7", "acme/repo", "https://github.com/acme/repo/pull/7", nil},
		{"gitlab custom port", "https://gitlab.example.com:8443/alice/repo", "https://gitlab.example.com:8443/group/sub/repo", "7", "group/sub/repo", "https://gitlab.example.com:8443/group/sub/repo/-/merge_requests/7", nil},
		{"SSH custom port", "ssh://git@gitlab.example.com:8443/alice/repo.git", "", "7", "alice/repo", "https://gitlab.example.com:8443/alice/repo/-/merge_requests/7", nil},
		{"github upstream URL", "git@github.com:alice/repo.git", "https://github.com/acme/repo", "https://github.com/acme/repo/pull/7", "acme/repo", "https://github.com/acme/repo/pull/7", nil},
		{"github upstream number", "https://github.com/alice/repo", "https://github.com/acme/repo", "7", "acme/repo", "https://github.com/acme/repo/pull/7", nil},
		{"origin remains trusted", "https://github.com/alice/repo", "https://github.com/acme/repo", "https://github.com/alice/repo/pull/7", "alice/repo", "https://github.com/alice/repo/pull/7", nil},
		{"gitlab nested upstream URL", "git@gitlab.com:alice/team/repo.git", "https://gitlab.com/group/subgroup/repo.git", "https://gitlab.com/group/subgroup/repo/-/merge_requests/7", "group/subgroup/repo", "https://gitlab.com/group/subgroup/repo/-/merge_requests/7", nil},
		{"gitlab nested upstream number", "https://gitlab.example.com/alice/repo", "https://gitlab.example.com/group/subgroup/repo", "#7", "group/subgroup/repo", "https://gitlab.example.com/group/subgroup/repo/-/merge_requests/7", nil},
		{"unconfigured upstream", "https://github.com/alice/repo", "", "https://github.com/acme/repo/pull/7", "", "", ErrProjectMismatch},
		{"unrelated remote", "https://github.com/alice/repo", "https://github.com/acme/repo", "https://github.com/other/repo/pull/7", "", "", ErrProjectMismatch},
		{"cross host", "https://gitlab.com/alice/repo", "https://gitlab.com/group/sub/repo", "https://gitlab.example.com/group/sub/repo/-/merge_requests/7", "", "", ErrProjectMismatch},
		{"cross provider", "https://github.com/alice/repo", "https://github.com/acme/repo", "https://gitlab.com/acme/repo/-/merge_requests/7", "", "", ErrProjectMismatch},
		{"full namespace", "https://gitlab.com/alice/repo", "https://gitlab.com/group/sub/repo", "https://gitlab.com/other/sub/repo/-/merge_requests/7", "", "", ErrProjectMismatch},
		{"nested prefix", "https://gitlab.com/alice/repo", "https://gitlab.com/group/sub/repo", "https://gitlab.com/sub/repo/-/merge_requests/7", "", "", ErrProjectMismatch},
		{"wrong provider route", "https://github.com/alice/repo", "https://github.com/acme/repo", "https://github.com/acme/repo/-/merge_requests/7", "", "", ErrInvalidPRRef},
		{"port authority cannot collapse", "https://github.com/alice/repo", "https://github.com/acme/repo", "https://github.com:8443/acme/repo/pull/7", "", "", ErrProjectMismatch},
		{"no identity", "", "", "https://github.com/acme/repo/pull/7", "", "", ErrProjectMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			st.sessions["demo-1"] = domain.SessionRecord{ID: "demo-1", ProjectID: "demo", Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: "/ws"}}
			st.projects["demo"] = domain.ProjectRecord{ID: "demo", RepoOriginURL: tc.origin, Config: domain.ProjectConfig{CanonicalRepoURL: tc.canonical}}
			scm := &claimTargetSCM{}
			claimer := &fakePRClaimer{}
			svc := NewWithDeps(Deps{Store: st, SCM: scm, PRClaimer: claimer})
			_, err := svc.ClaimPR(context.Background(), "demo-1", tc.ref, ClaimPROptions{})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if len(scm.refs) != 0 || claimer.called {
					t.Fatal("rejected claim reached SCM or storage")
				}
				return
			}
			if len(scm.refs) != 1 || scm.refs[0].Repo.Repo != tc.wantRepo || scm.refs[0].URL != tc.wantURL {
				t.Fatalf("SCM target = %+v", scm.refs)
			}
			if !strings.HasPrefix(tc.wantURL, "https://"+scm.refs[0].Repo.Host+"/"+scm.refs[0].Repo.Repo+"/") {
				t.Fatalf("SCM authority lost: %+v", scm.refs[0])
			}
			if !claimer.called || claimer.gotPR.Repo != tc.wantRepo || claimer.gotPR.URL != tc.wantURL {
				t.Fatalf("persisted claim = %+v", claimer.gotPR)
			}
		})
	}
}

func TestClaimPRRejectsInvalidPersistedCanonicalIdentity(t *testing.T) {
	for _, canonical := range []string{"upstream", "https://gitlab.com/acme/repo", "https://evil.ghe.io/acme/repo"} {
		t.Run(canonical, func(t *testing.T) {
			st := newFakeStore()
			st.sessions["demo-1"] = domain.SessionRecord{ID: "demo-1", ProjectID: "demo", Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: "/ws"}}
			// Native imports copy durable config without running the project service's
			// validation. Claims must validate it again before any provider access.
			st.projects["demo"] = domain.ProjectRecord{ID: "demo", RepoOriginURL: "https://github.com/alice/repo", Config: domain.ProjectConfig{CanonicalRepoURL: canonical}}
			scm := &claimTargetSCM{}
			claimer := &fakePRClaimer{}
			svc := NewWithDeps(Deps{Store: st, SCM: scm, PRClaimer: claimer})
			if _, err := svc.ClaimPR(context.Background(), "demo-1", "7", ClaimPROptions{}); err == nil {
				t.Fatal("invalid persisted identity authorized claim")
			}
			if len(scm.refs) != 0 || claimer.called {
				t.Fatal("invalid config reached SCM or storage")
			}
		})
	}
}
