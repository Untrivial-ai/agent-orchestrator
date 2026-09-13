package scm

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPoll_WorkspaceLegacyBranches(t *testing.T) {
	for _, origin := range []string{"", "https://github.com/o/r.git"} {
		for _, branch := range []string{"ao/p-1", "ao/p-1-2"} {
			t.Run(origin+"/"+branch, func(t *testing.T) {
				store := testStoreWithSession()
				store.sessions[0].Metadata.Branch = branch
				store.projects["p"] = domain.ProjectRecord{ID: "p", Kind: domain.ProjectKindWorkspace, RepoOriginURL: origin}
				store.workspaceRepos = map[string][]domain.WorkspaceRepoRecord{"p": {{Name: "api", RelativePath: "api", RepoOriginURL: "https://github.com/o/api.git"}}}
				prObs := testObs(12)
				prObs.Repo = "o/api"
				prObs.PR.URL = "https://github.com/o/api/pull/12"
				prObs.PR.HTMLURL = prObs.PR.URL
				prObs.PR.SourceBranch = branch + "-billing"
				prObs.PR.HeadRepo = "o/api"
				prObs.PR.TargetBranch = "main"
				provider := &fakeProvider{
					repoGuards:   map[string]ports.SCMGuardResult{prKey(testAPIRepo, 0): {ETag: "api-v1"}},
					openPRs:      map[string][]ports.SCMPRObservation{prKey(testAPIRepo, 0): {prObs.PR}},
					observations: map[string]ports.SCMObservation{prKey(testAPIRepo, 12): prObs},
				}
				lc := &fakeLifecycle{}
				obs := newTestObserver(store, provider, lc, time.Unix(1, 0).UTC())
				if err := obs.Poll(context.Background()); err != nil {
					t.Fatal(err)
				}
				if len(store.writes) == 0 || len(lc.observed) != 1 {
					t.Fatalf("workspace PR missing: writes=%+v, lifecycle=%+v", store.writes, lc.observed)
				}
				for _, write := range store.writes {
					if write.pr.SessionID != "p-1" || write.pr.Repo != "o/api" || write.pr.SourceBranch != branch+"-billing" {
						t.Fatalf("wrong attribution: %+v", write.pr)
					}
				}
			})
		}
	}
}

func TestPoll_WorkspaceRepositoryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		kind                          domain.ProjectKind
		origin, childOrigin, headRepo string
		want                          bool
	}{
		{"workspace root origin", domain.ProjectKindWorkspace, "https://github.com/o/api.git", "", "o/api", true},
		{"registered child", domain.ProjectKindWorkspace, "", "https://github.com/o/api.git", "o/api", true},
		{"unregistered child", domain.ProjectKindWorkspace, "https://github.com/o/r.git", "", "o/api", false},
		{"foreign fork head", domain.ProjectKindWorkspace, "", "https://github.com/o/api.git", "other/api", false},
		{"unknown head", domain.ProjectKindWorkspace, "", "https://github.com/o/api.git", "", false},
		{"single repo excludes registry", domain.ProjectKindSingleRepo, "", "https://github.com/o/api.git", "o/api", false},
		{"scratch excludes registry", domain.ProjectKindScratch, "", "https://github.com/o/api.git", "o/api", false},
		{"single repo excludes hyphen siblings", domain.ProjectKindSingleRepo, "https://github.com/o/api.git", "", "o/api", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testStoreWithSession()
			store.sessions[0].Metadata.Branch = "ao/p-1-2"
			store.projects["p"] = domain.ProjectRecord{ID: "p", Kind: tc.kind, RepoOriginURL: tc.origin}
			if tc.childOrigin != "" {
				store.workspaceRepos = map[string][]domain.WorkspaceRepoRecord{"p": {{Name: "api", RelativePath: "api", RepoOriginURL: tc.childOrigin}}}
			}
			prObs := testObs(12)
			prObs.Repo = "o/api"
			prObs.PR.URL = "https://github.com/o/api/pull/12"
			prObs.PR.HTMLURL = prObs.PR.URL
			prObs.PR.SourceBranch = "ao/p-1-2-fix"
			prObs.PR.HeadRepo = tc.headRepo
			provider := &fakeProvider{
				repoGuards:   map[string]ports.SCMGuardResult{prKey(testAPIRepo, 0): {ETag: "api-v1"}},
				openPRs:      map[string][]ports.SCMPRObservation{prKey(testAPIRepo, 0): {prObs.PR}},
				observations: map[string]ports.SCMObservation{prKey(testAPIRepo, 12): prObs},
			}
			obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
			if err := obs.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if (len(store.writes) > 0) != tc.want || (len(provider.fetchBatches) > 0) != tc.want {
				t.Fatalf("writes = %+v, fetches = %+v, want attributed %v", store.writes, provider.fetchBatches, tc.want)
			}
		})
	}
}

func TestMatchSession_WorkspaceOwnership(t *testing.T) {
	workspace := func(id, branch string) sessionRepo {
		return sessionRepo{session: domain.SessionRecord{ID: domain.SessionID(id)}, branch: branch, workspace: true}
	}
	bare := workspace("p-1", "ao/p-1")
	collision := workspace("p-1", "ao/p-1-2")
	next := workspace("p-1-2", "ao/p-1-2")
	for _, tc := range []struct {
		name, source string
		candidates   []sessionRepo
		want         domain.SessionID
	}{
		{"bare hyphen sibling", "ao/p-1-fix", []sessionRepo{bare}, "p-1"},
		{"collision suffix sibling", "ao/p-1-2-fix", []sessionRepo{collision}, "p-1"},
		{"exact", "ao/p-1", []sessionRepo{bare}, "p-1"},
		{"stacked descendant", "ao/p-1/topic/stack", []sessionRepo{bare}, "p-1"},
		{"root slash sibling", "ao/p-1/fix", []sessionRepo{workspace("p-1", "ao/p-1/root")}, "p-1"},
		{"custom exact", "feature/a", []sessionRepo{workspace("p-1", "feature/a")}, "p-1"},
		{"custom stack", "feature/a/stack", []sessionRepo{workspace("p-1", "feature/a")}, "p-1"},
		{"custom sibling excluded", "feature/b", []sessionRepo{workspace("p-1", "feature/a")}, ""},
		{"custom hyphen excluded", "feature/a-fix", []sessionRepo{workspace("p-1", "feature/a")}, ""},
		{"unvalidated AO branch excluded", "ao/other-fix", []sessionRepo{workspace("p-1", "ao/other")}, ""},
		{"topic hyphen excluded", "ao/p-1-topic-fix", []sessionRepo{workspace("p-1", "ao/p-1-topic")}, ""},
		{"padded collision excluded", "ao/p-1-02-fix", []sessionRepo{workspace("p-1", "ao/p-1-02")}, ""},
		{"next session excluded", "ao/p-10-fix", []sessionRepo{bare}, ""},
		{"empty topic excluded", "ao/p-1-", []sessionRepo{bare}, ""},
		{"single repo excluded", "ao/p-1-fix", []sessionRepo{{session: bare.session, branch: bare.branch}}, ""},
		{"exact owner beats hyphen", "ao/p-1-fix", []sessionRepo{bare, workspace("explicit", "ao/p-1-fix")}, "explicit"},
		{"longest hyphen owner", "ao/p-1-2-fix", []sessionRepo{bare, next}, "p-1-2"},
		{"longest stacked owner", "ao/p-1-fix/stack", []sessionRepo{bare, workspace("explicit", "ao/p-1-fix")}, "explicit"},
		{"ambiguous exact", "ao/p-1-2", []sessionRepo{collision, next}, ""},
		{"ambiguous hyphen", "ao/p-1-2-fix", []sessionRepo{bare, collision, next}, ""},
		{"duplicate scan same owner", "ao/p-1-fix", []sessionRepo{bare, bare}, "p-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 2; i++ {
				got, ok := matchSession(tc.candidates, tc.source)
				if ok != (tc.want != "") || (ok && got.session.ID != tc.want) {
					t.Fatalf("matched %q (%v), want %q", got.session.ID, ok, tc.want)
				}
				for a, b := 0, len(tc.candidates)-1; a < b; a, b = a+1, b-1 {
					tc.candidates[a], tc.candidates[b] = tc.candidates[b], tc.candidates[a]
				}
			}
		})
	}
}
