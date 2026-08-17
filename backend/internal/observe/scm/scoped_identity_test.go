package scm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// fakeScopedIdentityResolver implements ports.ScopedIdentityResolver for tests.
// It returns a per-provider identity or error.
type fakeScopedIdentityResolver struct {
	// identities is keyed by identityKey(provider, host) so tests can
	// return different identities for the same provider on different hosts
	// (e.g. gitlab.com vs a self-managed GitLab).
	identities map[string]ports.SCMIdentity
	errs       map[string]error
	calls      []string
}

func (r *fakeScopedIdentityResolver) AuthenticatedIdentityForProvider(_ context.Context, provider, host string) (ports.SCMIdentity, error) {
	r.calls = append(r.calls, provider)
	key := identityKey(provider, host)
	if err, ok := r.errs[key]; ok {
		return ports.SCMIdentity{}, err
	}
	return r.identities[key], nil
}

// hostAwareProvider wraps fakeProvider and overrides ParseRepository to
// route gitlab.com URLs to the gitlab provider key, so multi-provider observer
// tests can exercise per-provider identity resolution.
type hostAwareProvider struct {
	*fakeProvider
}

func (p *hostAwareProvider) ParseRepository(remote string) (ports.SCMRepo, bool) {
	if strings.Contains(remote, "gitlab.com") {
		return glRepo, true
	}
	if strings.Contains(remote, "gitlab.internal") {
		return glSelfRepo, true
	}
	return p.fakeProvider.ParseRepository(remote)
}

// --- Test helpers for multi-provider observer tests ---

var (
	glRepo     = ports.SCMRepo{Provider: "gitlab", Host: "gitlab.com", Owner: "o", Name: "r", Repo: "o/r"}
	glSelfRepo = ports.SCMRepo{Provider: "gitlab", Host: "gitlab.internal", Owner: "o", Name: "r", Repo: "o/r"}
)

// testStoreWithTwoGitLabSessions sets up a store with two sessions — one for
// gitlab.com and one for a self-managed gitlab.internal — both using the "feat"
// branch.
func testStoreWithTwoGitLabSessions() *fakeStore {
	return &fakeStore{
		sessions: []domain.SessionRecord{
			{ID: "gl-com", ProjectID: "gl-com-proj", Metadata: domain.SessionMetadata{Branch: "feat"}},
			{ID: "gl-int", ProjectID: "gl-int-proj", Metadata: domain.SessionMetadata{Branch: "feat"}},
		},
		projects: map[string]domain.ProjectRecord{
			"gl-com-proj": {ID: "gl-com-proj", RepoOriginURL: "https://gitlab.com/o/r.git"},
			"gl-int-proj": {ID: "gl-int-proj", RepoOriginURL: "https://gitlab.internal/o/r.git"},
		},
		prs:    map[domain.SessionID][]domain.PullRequest{},
		checks: map[string][]domain.PullRequestCheck{},
	}
}

// testStoreWithTwoSessions sets up a store with two sessions — one for a GitHub
// repo and one for a GitLab repo — both using the "feat" branch.
func testStoreWithTwoSessions() *fakeStore {
	return &fakeStore{
		sessions: []domain.SessionRecord{
			{ID: "gh-1", ProjectID: "gh-proj", Metadata: domain.SessionMetadata{Branch: "feat"}},
			{ID: "gl-1", ProjectID: "gl-proj", Metadata: domain.SessionMetadata{Branch: "feat"}},
		},
		projects: map[string]domain.ProjectRecord{
			"gh-proj": {ID: "gh-proj", RepoOriginURL: "https://github.com/o/r.git"},
			"gl-proj": {ID: "gl-proj", RepoOriginURL: "https://gitlab.com/o/r.git"},
		},
		prs:    map[domain.SessionID][]domain.PullRequest{},
		checks: map[string][]domain.PullRequestCheck{},
	}
}

// TestPoll_TwoGitLabHostsIdentityResolution verifies that when a
// ScopedIdentityResolver is wired, MRs from gitlab.com and a self-managed
// gitlab.internal host are checked against their own per-host identity.
// A self-managed MR authored by a different account than gitlab.com must not
// be silently dropped (ticket 06).
func TestPoll_TwoGitLabHostsIdentityResolution(t *testing.T) {
	store := testStoreWithTwoGitLabSessions()
	provider := &hostAwareProvider{fakeProvider: &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{
			prKey(glRepo, 0):     {ETag: "v2"},
			prKey(glSelfRepo, 0): {ETag: "v2"},
		},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(glRepo, 0): {
				{URL: "https://gitlab.com/o/r/-/merge_requests/1", Number: 1, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1", Author: "other"},
				{URL: "https://gitlab.com/o/r/-/merge_requests/2", Number: 2, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha2", Author: "comuser"},
			},
			prKey(glSelfRepo, 0): {
				{URL: "https://gitlab.internal/o/r/-/merge_requests/3", Number: 3, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha3", Author: "other"},
				{URL: "https://gitlab.internal/o/r/-/merge_requests/4", Number: 4, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha4", Author: "internaluser"},
			},
		},
		observations: map[string]ports.SCMObservation{
			prKey(glRepo, 2):     testObsGitLab(2),
			prKey(glSelfRepo, 4): testObsGitLabHost(4),
		},
	}}
	scoped := &fakeScopedIdentityResolver{
		identities: map[string]ports.SCMIdentity{
			identityKey("gitlab", "gitlab.com"):      {Login: "comuser", Human: true},
			identityKey("gitlab", "gitlab.internal"): {Login: "internaluser", Human: true},
		},
	}
	obs := New(provider, store, &fakeLifecycle{}, Config{
		Clock:                  func() time.Time { return time.Unix(1, 0).UTC() },
		Tick:                   time.Hour,
		Logger:                 quietSlog(),
		CacheMax:               128,
		ScopedIdentityResolver: scoped,
	})
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	fetched := fetchedNumbers(provider.fetchBatches)

	// gitlab.com: only MR #2 (author "comuser") should be fetched, not MR #1.
	if !fetched[2] || fetched[1] {
		t.Fatalf("gitlab.com fetched = %v, want only MR #2 (comuser's MR)", fetched)
	}

	// gitlab.internal: only MR #4 (author "internaluser") should be fetched, not MR #3.
	if !fetched[4] || fetched[3] {
		t.Fatalf("gitlab.internal fetched = %v, want only MR #4 (internaluser's MR)", fetched)
	}

	// Foreign-author MRs must not be persisted.
	for _, w := range store.writes {
		if w.pr.Number == 1 || w.pr.Number == 3 {
			t.Fatalf("foreign author's MR %d was persisted", w.pr.Number)
		}
	}
}

// TestPoll_PerProviderIdentityResolution verifies that when a
// ScopedIdentityResolver is wired, GitHub PRs are checked against the GitHub
// identity and GitLab PRs against the GitLab identity (finding #7).
func TestPoll_PerProviderIdentityResolution(t *testing.T) {
	store := testStoreWithTwoSessions()
	provider := &hostAwareProvider{fakeProvider: &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{
			prKey(testRepo, 0): {ETag: "v2"},
			prKey(glRepo, 0):   {ETag: "v2"},
		},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(testRepo, 0): {
				{URL: "https://github.com/o/r/pull/1", Number: 1, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1", Author: "other"},
				{URL: "https://github.com/o/r/pull/2", Number: 2, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha2", Author: "octocat"},
			},
			prKey(glRepo, 0): {
				{URL: "https://gitlab.com/o/r/-/merge_requests/3", Number: 3, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha3", Author: "other"},
				{URL: "https://gitlab.com/o/r/-/merge_requests/4", Number: 4, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha4", Author: "gitlabuser"},
			},
		},
		observations: map[string]ports.SCMObservation{
			prKey(testRepo, 2): testObs(2),
			prKey(glRepo, 4):   testObsGitLab(4),
		},
	}}
	scoped := &fakeScopedIdentityResolver{
		identities: map[string]ports.SCMIdentity{
			identityKey("github", "github.com"): {Login: "octocat", Human: true},
			identityKey("gitlab", "gitlab.com"): {Login: "gitlabuser", Human: true},
		},
	}
	obs := New(provider, store, &fakeLifecycle{}, Config{
		Clock:                  func() time.Time { return time.Unix(1, 0).UTC() },
		Tick:                   time.Hour,
		Logger:                 quietSlog(),
		CacheMax:               128,
		ScopedIdentityResolver: scoped,
	})
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Both providers should have been queried for identity.
	if len(scoped.calls) != 2 {
		t.Fatalf("identity resolver calls = %v, want both [github gitlab]", scoped.calls)
	}

	// Verify GitHub: only PR #2 (author "octocat") was fetched, not PR #1 (author "other").
	fetched := fetchedNumbers(provider.fetchBatches)
	if !fetched[2] || fetched[1] {
		t.Fatalf("github fetched = %v, want only PR #2 (octocat's PR)", fetched)
	}

	// Verify GitLab: only PR #4 (author "gitlabuser") was fetched, not PR #3 (author "other").
	if !fetched[4] || fetched[3] {
		t.Fatalf("gitlab fetched = %v, want only PR #4 (gitlabuser's PR)", fetched)
	}

	// Verify foreign PRs were not persisted.
	for _, w := range store.writes {
		if w.pr.Number == 1 || w.pr.Number == 3 {
			t.Fatalf("foreign author's PR %d was persisted", w.pr.Number)
		}
	}
}

// TestPoll_ScopedIdentityFallbackToSingleResolver verifies that when only the
// single-provider IdentityResolver is wired (no ScopedIdentityResolver), the
// existing behavior is preserved: one identity is applied to all PRs.
func TestPoll_ScopedIdentityFallbackToSingleResolver(t *testing.T) {
	store := testStoreWithSession()
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v2"}},
		openPRs: map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {
			{URL: "https://github.com/o/r/pull/1", Number: 1, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1", Author: "other"},
			{URL: "https://github.com/o/r/pull/2", Number: 2, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha2", Author: "ALICE"},
		}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 2): testObs(2)},
	}
	identity := &fakeIdentityResolver{identity: ports.SCMIdentity{Login: "alice", Human: true}}
	obs := New(provider, store, &fakeLifecycle{}, Config{
		Clock:            func() time.Time { return time.Unix(1, 0).UTC() },
		Tick:             time.Hour,
		Logger:           quietSlog(),
		CacheMax:         128,
		IdentityResolver: identity,
	})
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if identity.calls != 1 {
		t.Fatalf("identity resolver calls = %d, want 1", identity.calls)
	}
	fetched := fetchedNumbers(provider.fetchBatches)
	if !fetched[2] || fetched[1] {
		t.Fatalf("fetched = %v, want only PR #2 (alice's PR)", fetched)
	}
}

// TestPoll_ScopedIdentityPartialFailure verifies that when identity resolution
// fails for one provider, PRs from the other provider are still discovered
// against their matching identity (finding #7).
func TestPoll_ScopedIdentityPartialFailure(t *testing.T) {
	store := testStoreWithTwoSessions()
	provider := &hostAwareProvider{fakeProvider: &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{
			prKey(testRepo, 0): {ETag: "v2"},
			prKey(glRepo, 0):   {ETag: "v2"},
		},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(testRepo, 0): {
				{URL: "https://github.com/o/r/pull/1", Number: 1, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1", Author: "octocat"},
			},
			prKey(glRepo, 0): {
				{URL: "https://gitlab.com/o/r/-/merge_requests/3", Number: 3, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha3", Author: "gitlabuser"},
			},
		},
		observations: map[string]ports.SCMObservation{
			prKey(testRepo, 1): testObs(1),
			prKey(glRepo, 3):   testObsGitLab(3),
		},
	}}
	// GitHub identity resolves fine; GitLab identity fails.
	scoped := &fakeScopedIdentityResolver{
		identities: map[string]ports.SCMIdentity{
			identityKey("github", "github.com"): {Login: "octocat", Human: true},
		},
		errs: map[string]error{
			identityKey("gitlab", "gitlab.com"): errors.New("gitlab token expired"),
		},
	}
	obs := New(provider, store, &fakeLifecycle{}, Config{
		Clock:                  func() time.Time { return time.Unix(1, 0).UTC() },
		Tick:                   time.Hour,
		Logger:                 quietSlog(),
		CacheMax:               128,
		ScopedIdentityResolver: scoped,
	})
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// GitHub PR #1 (author "octocat") should be fetched — identity resolved.
	fetched := fetchedNumbers(provider.fetchBatches)
	if !fetched[1] {
		t.Fatalf("github PR #1 should be fetched (identity resolved), got fetched=%v", fetched)
	}

	// GitLab PR #3 should also be fetched — identity resolution failed for
	// GitLab, so PRs from that provider fall back to branch-based discovery
	// (no identity check, so any matching branch is accepted).
	if !fetched[3] {
		t.Fatalf("gitlab PR #3 should be fetched (identity failure → branch-based discovery), got fetched=%v", fetched)
	}

	// Both providers should have been queried.
	if len(scoped.calls) != 2 {
		t.Fatalf("identity resolver calls = %v, want both [github gitlab]", scoped.calls)
	}
}

// --- helpers ---

func testObsGitLabHost(num int) ports.SCMObservation {
	o := testObs(num)
	o.Provider = "gitlab"
	o.Host = "gitlab.internal"
	o.PR.URL = "https://gitlab.internal/o/r/-/merge_requests/" + fmt.Sprint(num)
	return o
}

func testObsGitLab(num int) ports.SCMObservation {
	o := testObs(num)
	o.Provider = "gitlab"
	o.Host = "gitlab.com"
	o.PR.URL = "https://gitlab.com/o/r/-/merge_requests/" + fmt.Sprint(num)
	return o
}

func fetchedNumbers(batches [][]ports.SCMPRRef) map[int]bool {
	m := map[int]bool{}
	for _, batch := range batches {
		for _, ref := range batch {
			m[ref.Number] = true
		}
	}
	return m
}
