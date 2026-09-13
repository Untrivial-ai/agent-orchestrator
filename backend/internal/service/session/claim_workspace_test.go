package session

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type workspaceClaimStore struct {
	*fakeStore
	repos         []domain.WorkspaceRepoRecord
	err           error
	listedProject string
}

func (s *workspaceClaimStore) ListWorkspaceRepos(_ context.Context, projectID string) ([]domain.WorkspaceRepoRecord, error) {
	s.listedProject = projectID
	return s.repos, s.err
}

func TestClaimPRWorkspaceRepositories(t *testing.T) {
	for _, root := range []string{"", "https://github.com/acme/parent.git"} {
		for _, tc := range []struct {
			name, ref, wantRepo string
			wantErr             error
		}{
			{"first child", "https://github.com/acme/api/pull/7", "acme/api", nil},
			{"second child", "https://github.com/acme/web/pull/7", "acme/web", nil},
			{"nested GitLab child", "https://gitlab.example.com:8443/group/sub/api/-/merge_requests/7", "group/sub/api", nil},
			{"unregistered repo", "https://github.com/other/api/pull/7", "", ErrProjectMismatch},
			{"different host", "https://gitlab.other.com:8443/group/sub/api/-/merge_requests/7", "", ErrProjectMismatch},
			{"different port", "https://gitlab.example.com/group/sub/api/-/merge_requests/7", "", ErrProjectMismatch},
			{"different namespace", "https://gitlab.example.com:8443/other/sub/api/-/merge_requests/7", "", ErrProjectMismatch},
			{"different provider", "https://gitlab.com/acme/api/-/merge_requests/7", "", ErrProjectMismatch},
			{"invalid PR URL", "https://github.com/acme/api/-/merge_requests/7", "", ErrInvalidPRRef},
		} {
			t.Run(root+"/"+tc.name, func(t *testing.T) {
				st := &workspaceClaimStore{fakeStore: newFakeStore(), repos: []domain.WorkspaceRepoRecord{
					{RepoOriginURL: ""}, {RepoOriginURL: "/local/repo"},
					{RepoOriginURL: "git@github.com:Acme/API.git"},
					{RepoOriginURL: "https://github.com/acme/web.git"},
					{RepoOriginURL: "ssh://git@gitlab.example.com:8443/group/sub/api.git"},
				}}
				st.projects["ws"] = domain.ProjectRecord{ID: "ws", Kind: domain.ProjectKindWorkspace, RepoOriginURL: root}
				st.sessions["ws-1"] = domain.SessionRecord{ID: "ws-1", ProjectID: "ws", Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: "/ws", Branch: "ao/ws-1-2"}}
				scm, claimer := &claimTargetSCM{}, &fakePRClaimer{}
				svc := NewWithDeps(Deps{Store: st, SCM: scm, PRClaimer: claimer})
				_, err := svc.ClaimPR(context.Background(), "ws-1", tc.ref, ClaimPROptions{})
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ClaimPR error = %v, want %v", err, tc.wantErr)
				}
				if tc.wantErr != nil {
					if len(scm.refs) != 0 || claimer.called {
						t.Fatal("rejected claim reached SCM or storage")
					}
					return
				}
				if st.listedProject != "ws" || len(scm.refs) != 1 || scm.refs[0].Repo.Repo != tc.wantRepo {
					t.Fatalf("project = %q, fetched refs = %+v", st.listedProject, scm.refs)
				}
				if !claimer.called || claimer.gotPR.SessionID != "ws-1" || claimer.gotPR.Repo != tc.wantRepo {
					t.Fatalf("claimed PR = %+v", claimer.gotPR)
				}
			})
		}
	}
}

func TestClaimPRWorkspaceBoundaries(t *testing.T) {
	lookupErr := errors.New("workspace repository lookup failed")
	for _, tc := range []struct {
		name, origin, canonical, ref string
		kind                         domain.ProjectKind
		lookupErr, wantErr           error
		wantLookup                   bool
	}{
		{name: "root URL", kind: domain.ProjectKindWorkspace, origin: "https://github.com/acme/parent", ref: "https://github.com/acme/parent/pull/7"},
		{name: "root number", kind: domain.ProjectKindWorkspace, origin: "https://github.com/acme/parent", ref: "#7"},
		{name: "canonical root", kind: domain.ProjectKindWorkspace, origin: "https://github.com/fork/parent", canonical: "https://github.com/acme/parent", ref: "7"},
		{name: "rootless number is not guessed", kind: domain.ProjectKindWorkspace, ref: "7", wantErr: ErrInvalidPRRef},
		{name: "invalid root does not mask child", kind: domain.ProjectKindWorkspace, origin: "/local/root", ref: "https://github.com/acme/api/pull/7", wantLookup: true},
		{name: "lookup failure", kind: domain.ProjectKindWorkspace, ref: "https://github.com/acme/api/pull/7", lookupErr: lookupErr, wantErr: lookupErr, wantLookup: true},
		{name: "single repo ignores children", kind: domain.ProjectKindSingleRepo, origin: "https://github.com/acme/parent", ref: "https://github.com/acme/api/pull/7", wantErr: ErrProjectMismatch},
		{name: "default kind ignores children", origin: "https://github.com/acme/parent", ref: "https://github.com/acme/api/pull/7", wantErr: ErrProjectMismatch},
		{name: "scratch rejects child", kind: domain.ProjectKindScratch, ref: "https://github.com/acme/api/pull/7", wantErr: ErrSessionNotClaimable},
		{name: "scratch rejects root", kind: domain.ProjectKindScratch, origin: "https://github.com/acme/parent", ref: "7", wantErr: ErrSessionNotClaimable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &workspaceClaimStore{fakeStore: newFakeStore(), err: tc.lookupErr, repos: []domain.WorkspaceRepoRecord{{RepoOriginURL: "https://github.com/acme/api"}}}
			st.projects["ws"] = domain.ProjectRecord{ID: "ws", Kind: tc.kind, RepoOriginURL: tc.origin, Config: domain.ProjectConfig{CanonicalRepoURL: tc.canonical}}
			st.sessions["ws-1"] = domain.SessionRecord{ID: "ws-1", ProjectID: "ws", Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: "/ws"}}
			scm, claimer := &claimTargetSCM{}, &fakePRClaimer{}
			svc := NewWithDeps(Deps{Store: st, SCM: scm, PRClaimer: claimer})
			_, err := svc.ClaimPR(context.Background(), "ws-1", tc.ref, ClaimPROptions{})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if (st.listedProject != "") != tc.wantLookup {
				t.Fatalf("workspace lookup = %q, want lookup %v", st.listedProject, tc.wantLookup)
			}
			if tc.wantErr != nil {
				if len(scm.refs) != 0 || claimer.called {
					t.Fatal("rejected claim reached SCM or storage")
				}
			} else if !claimer.called || len(scm.refs) != 1 {
				t.Fatal("accepted claim did not reach SCM and storage")
			}
		})
	}
}
