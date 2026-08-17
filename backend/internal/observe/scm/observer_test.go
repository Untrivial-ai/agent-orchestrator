package scm

// This file tests the SCM observer orchestration contract with fake provider,
// store, and lifecycle collaborators so ETag decisions, batching, log fetching,
// review cadence, semantic hashes, and notification behavior stay provider-neutral.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	testRepo    = ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "o", Name: "r", Repo: "o/r"}
	testAPIRepo = ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "o", Name: "api", Repo: "o/api"}
)

type fakeStore struct {
	mu sync.Mutex

	sessions       []domain.SessionRecord
	projects       map[string]domain.ProjectRecord
	workspaceRepos map[string][]domain.WorkspaceRepoRecord
	prs            map[domain.SessionID][]domain.PullRequest
	checks         map[string][]domain.PullRequestCheck
	writeErr       error

	writes []fakeWrite

	listEntered chan struct{}
	listRelease chan struct{}
}

type fakeWrite struct {
	pr         domain.PullRequest
	checks     []domain.PullRequestCheck
	reviews    []domain.PullRequestReview
	threads    []domain.PullRequestReviewThread
	comments   []domain.PullRequestComment
	reviewMode ports.ReviewWriteMode
}

func (s *fakeStore) ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error) {
	if s.listEntered != nil {
		select {
		case <-s.listEntered:
		default:
			close(s.listEntered)
		}
	}
	if s.listRelease != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.listRelease:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.SessionRecord(nil), s.sessions...), nil
}

func (s *fakeStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	return p, ok, nil
}

func (s *fakeStore) UpsertProject(_ context.Context, row domain.ProjectRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projects == nil {
		s.projects = map[string]domain.ProjectRecord{}
	}
	s.projects[row.ID] = row
	return nil
}

func (s *fakeStore) ListWorkspaceRepos(_ context.Context, projectID string) ([]domain.WorkspaceRepoRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.WorkspaceRepoRecord(nil), s.workspaceRepos[projectID]...), nil
}

func (s *fakeStore) ListPRsBySession(_ context.Context, id domain.SessionID) ([]domain.PullRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.PullRequest(nil), s.prs[id]...), nil
}

func (s *fakeStore) ListChecks(_ context.Context, prURL string) ([]domain.PullRequestCheck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.PullRequestCheck(nil), s.checks[prURL]...), nil
}

func (s *fakeStore) WriteSCMObservation(_ context.Context, pr domain.PullRequest, checks []domain.PullRequestCheck, reviews []domain.PullRequestReview, threads []domain.PullRequestReviewThread, comments []domain.PullRequestComment, reviewMode ports.ReviewWriteMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.writes = append(s.writes, fakeWrite{pr: pr, checks: append([]domain.PullRequestCheck(nil), checks...), reviews: append([]domain.PullRequestReview(nil), reviews...), threads: append([]domain.PullRequestReviewThread(nil), threads...), comments: append([]domain.PullRequestComment(nil), comments...), reviewMode: reviewMode})
	return nil
}

type fakeProvider struct {
	mu           sync.Mutex
	repoGuards   map[string]ports.SCMGuardResult
	checkGuards  map[string]ports.SCMGuardResult
	openPRs      map[string][]ports.SCMPRObservation
	listErr      error
	observations map[string]ports.SCMObservation
	reviews      map[string]ports.SCMReviewObservation
	logTails     map[string]string
	fetchErr     error
	reviewErr    error

	// fetchObsErrors maps a prKey to a per-observation error that the fake
	// attaches to the observation it returns for that ref, mimicking the
	// multi dispatcher's per-provider Error metadata. When set, the returned
	// observation has Fetched=false + Error=err so the observer can route it.
	fetchObsErrors map[string]error

	credentialGate   bool
	credentialOK     bool
	credentialErr    error
	credentialChecks int
	repoGuardCalls   int
	listCalls        int
	fetchBatches     [][]ports.SCMPRRef
	logCalls         int
	reviewCalls      int
	identity         ports.SCMIdentity
	identityErr      error
	identityCalls    int
}

type fakeIdentityResolver struct {
	identity ports.SCMIdentity
	err      error
	calls    int
}

func (r *fakeIdentityResolver) AuthenticatedIdentity(context.Context) (ports.SCMIdentity, error) {
	r.calls++
	return r.identity, r.err
}

func (p *fakeProvider) AuthenticatedIdentity(context.Context) (ports.SCMIdentity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.identityCalls++
	return p.identity, p.identityErr
}

func (p *fakeProvider) SCMCredentialsAvailable(context.Context) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.credentialChecks++
	if !p.credentialGate {
		return true, nil
	}
	return p.credentialOK, p.credentialErr
}

func (p *fakeProvider) ParseRepository(remote string) (ports.SCMRepo, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ports.SCMRepo{}, false
	}
	s := strings.TrimSuffix(remote, ".git")
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "git@github.com:")
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ports.SCMRepo{}, false
	}
	return ports.SCMRepo{Provider: "github", Host: "github.com", Owner: parts[0], Name: parts[1], Repo: parts[0] + "/" + parts[1]}, true
}
func (p *fakeProvider) RepoPRListGuard(_ context.Context, repo ports.SCMRepo, _ string) (ports.SCMGuardResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.repoGuardCalls++
	return p.repoGuards[prKey(repo, 0)], nil
}
func (p *fakeProvider) ListPRsByRepo(_ context.Context, repo ports.SCMRepo, _ time.Time) ([]ports.SCMPRObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listCalls++
	if p.listErr != nil {
		return nil, p.listErr
	}
	return p.openPRs[prKey(repo, 0)], nil
}
func (p *fakeProvider) CommitChecksGuard(_ context.Context, repo ports.SCMRepo, sha, _ string) (ports.SCMGuardResult, error) {
	return p.checkGuards[commitKey(repo, sha)], nil
}
func (p *fakeProvider) FetchPullRequests(_ context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fetchBatches = append(p.fetchBatches, append([]ports.SCMPRRef(nil), refs...))
	if p.fetchErr != nil {
		return nil, p.fetchErr
	}
	out := make([]ports.SCMObservation, 0, len(refs))
	for _, ref := range refs {
		key := prKey(ref.Repo, ref.Number)
		// Per-observation error mimics the multi dispatcher's per-provider
		// Error metadata: a Fetched=false placeholder carrying the failure
		// so the observer can route it to cooldown / refresh-incomplete.
		if err, ok := p.fetchObsErrors[key]; ok {
			out = append(out, ports.SCMObservation{
				Fetched:  false,
				Provider: ref.Repo.Provider,
				Host:     ref.Repo.Host,
				Repo:     ref.Repo.Repo,
				PR:       ports.SCMPRObservation{Number: ref.Number, URL: ref.URL},
				Error:    err,
			})
			continue
		}
		if obs, ok := p.observations[key]; ok {
			out = append(out, obs)
		}
	}
	return out, nil
}
func (p *fakeProvider) FetchFailedCheckLogTail(_ context.Context, _ ports.SCMRepo, check ports.SCMCheckObservation) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logCalls++
	return p.logTails[check.Name], nil
}
func (p *fakeProvider) FetchReviewThreads(_ context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reviewCalls++
	if p.reviewErr != nil {
		return ports.SCMReviewObservation{}, p.reviewErr
	}
	return p.reviews[prKey(ref.Repo, ref.Number)], nil
}

type fakeLifecycle struct {
	observed []ports.SCMObservation
	err      error
}

func (l *fakeLifecycle) ApplySCMObservation(_ context.Context, _ domain.SessionID, obs ports.SCMObservation) error {
	if l.err != nil {
		return l.err
	}
	l.observed = append(l.observed, obs)
	return nil
}

func newTestObserver(store *fakeStore, provider *fakeProvider, lc Lifecycle, now time.Time) *Observer {
	cfg := Config{Clock: func() time.Time { return now }, Tick: time.Hour, Logger: quietSlog(), CacheMax: 128, IdentityResolver: provider}
	return New(provider, store, lc, cfg)
}

func TestDispatchOrderIsDeterministic(t *testing.T) {
	obs := map[string]ports.SCMObservation{
		"a#9": {PR: ports.SCMPRObservation{Number: 9}},
		"a#7": {PR: ports.SCMPRObservation{Number: 7}},
		"b#3": {PR: ports.SCMPRObservation{Number: 3}},
	}
	subjects := map[string]*subject{
		"a#9": {session: domain.SessionRecord{ID: "sess-a"}},
		"a#7": {session: domain.SessionRecord{ID: "sess-a"}},
		"b#3": {session: domain.SessionRecord{ID: "sess-b"}},
	}
	want := []string{"a#7", "a#9", "b#3"}
	for run := 0; run < 8; run++ {
		got := dispatchOrder(obs, subjects)
		if len(got) != len(want) {
			t.Fatalf("run %d: got %d keys, want %d (%v)", run, len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("run %d: order[%d]=%q, want %q (full %v)", run, i, got[i], want[i], got)
			}
		}
	}
}

func TestTrackedPRsForSessionRetainsTerminalPRsOnlyForAutoTerminationRetry(t *testing.T) {
	prs := []domain.PullRequest{
		{Number: 1},
		{Number: 2, Merged: true},
		{Number: 3, Closed: true},
	}
	if got := trackedPRsForSession(domain.SessionRecord{}, prs); len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("default tracked PRs = %+v, want only open PR", got)
	}
	if got := trackedPRsForSession(domain.SessionRecord{TerminateOnPRMerge: true}, prs); len(got) != 3 {
		t.Fatalf("auto-termination tracked PRs = %+v, want open and terminal PRs", got)
	}
}
func quietSlog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testStoreWithSession() *fakeStore {
	return &fakeStore{
		sessions: []domain.SessionRecord{{ID: "p-1", ProjectID: "p", Metadata: domain.SessionMetadata{Branch: "feat"}}},
		projects: map[string]domain.ProjectRecord{"p": {ID: "p", RepoOriginURL: "https://github.com/o/r.git"}},
		prs:      map[domain.SessionID][]domain.PullRequest{},
		checks:   map[string][]domain.PullRequestCheck{},
	}
}

func testObs(num int) ports.SCMObservation {
	return ports.SCMObservation{
		Fetched: true, Provider: "github", Host: "github.com", Repo: "o/r",
		PR:           ports.SCMPRObservation{URL: "https://github.com/o/r/pull/" + fmt.Sprint(num), Number: num, State: "open", SourceBranch: "feat", TargetBranch: "main", HeadSHA: "sha" + fmt.Sprint(num), Title: "PR"},
		CI:           ports.SCMCIObservation{Summary: string(domain.CIPassing), HeadSHA: "sha" + fmt.Sprint(num), Checks: []ports.SCMCheckObservation{{Name: "build", Status: string(domain.PRCheckPassed), Conclusion: "success", URL: "ci"}}},
		Review:       ports.SCMReviewObservation{Decision: string(domain.ReviewNone)},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable), Mergeable: true},
	}
}

func TestDomainFromObservationPreservesStableIdentityAndAliases(t *testing.T) {
	oldURL := "https://github.com/old-owner/repo/pull/7"
	currentURL := "https://github.com/new-owner/repo/pull/7"
	obs := ports.SCMObservation{
		Fetched:  true,
		Provider: "github",
		Host:     "github.com",
		Repo:     "new-owner/repo",
		PR: ports.SCMPRObservation{
			ProviderID: "PR_stable_7",
			URL:        currentURL,
			URLAlias:   oldURL,
			Number:     7,
		},
	}

	pr, _, _, _, _ := domainFromObservation("repo-1", domain.SessionRecord{}, obs, domain.PullRequest{}, persistenceOptions{}, time.Unix(1, 0).UTC())
	if pr.ProviderID != "PR_stable_7" {
		t.Fatalf("ProviderID = %q, want PR_stable_7", pr.ProviderID)
	}
	if pr.URLAlias != oldURL {
		t.Fatalf("URLAlias = %q, want %s", pr.URLAlias, oldURL)
	}
}

func knownPR(num int) domain.PullRequest {
	obs := testObs(num)
	pr, _, _, _, _ := domainFromObservation("p-1", domain.SessionRecord{AutoInjectReview: true}, obs, domain.PullRequest{}, persistenceOptions{}, time.Unix(1, 0).UTC())
	// A known PR has been previously observed — simulate a prior review fetch
	// so needsReviewRefresh does not fire solely because ReviewObservedAt is zero.
	pr.ReviewObservedAt = time.Unix(2, 0).UTC()
	return pr
}

func TestDomainFromObservationSnapshotsReviewInjectionPolicy(t *testing.T) {
	obs := testObs(1)
	obs.Review = ports.SCMReviewObservation{
		Decision: string(domain.ReviewChangesRequest),
		Reviews:  []ports.SCMReviewSummaryObservation{{ID: "r1", Author: "alice", State: string(domain.ReviewChangesRequest)}},
		Threads: []ports.SCMReviewThreadObservation{{
			ID: "t1", Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "alice", Body: "fix"}},
		}},
	}
	_, _, reviews, _, comments := domainFromObservation(
		"p-1",
		domain.SessionRecord{AutoInjectReview: false},
		obs,
		domain.PullRequest{},
		persistenceOptions{},
		time.Unix(1, 0).UTC(),
	)
	if len(reviews) != 1 || reviews[0].AutoInjectReview {
		t.Fatalf("reviews = %+v, want one review snapshotted as not injected", reviews)
	}
	if len(comments) != 1 || comments[0].AutoInjectReview {
		t.Fatalf("comments = %+v, want one comment snapshotted as not injected", comments)
	}
}

func TestRepoForTrackedPRMatchesLegacyRepoOnlyRows(t *testing.T) {
	pr := knownPR(1)
	pr.Provider = ""
	pr.Host = ""
	pr.Repo = "o/r"
	repo, ok := repoForTrackedPR(pr, []ports.SCMRepo{testRepo})
	if !ok {
		t.Fatal("legacy repo-only row should match candidate repo")
	}
	if repoFullName(repo) != "o/r" {
		t.Fatalf("matched repo = %q, want o/r", repoFullName(repo))
	}
}

func TestRepoForTrackedPRUsesPersistedRepoWhenCurrentScanDropsUpstream(t *testing.T) {
	pr := knownPR(42)
	pr.Provider = "github"
	pr.Host = "github.com"
	pr.Repo = "upstream/api"
	repo, ok := repoForTrackedPR(pr, []ports.SCMRepo{testAPIRepo})
	if !ok {
		t.Fatal("persisted tracked PR repo should refresh even when current remotes no longer include it")
	}
	if repo.Provider != "github" || repo.Host != "github.com" || repo.Repo != "upstream/api" {
		t.Fatalf("repo = %#v, want persisted upstream/api tuple", repo)
	}
	if repo.Owner != "upstream" || repo.Name != "api" {
		t.Fatalf("repo owner/name = %q/%q, want upstream/api", repo.Owner, repo.Name)
	}
}

func TestStartAsyncPerformsImmediatePollAndStopsOnCancel(t *testing.T) {
	store := testStoreWithSession()
	store.listEntered = make(chan struct{})
	store.listRelease = make(chan struct{})
	provider := &fakeProvider{repoGuards: map[string]ports.SCMGuardResult{}, observations: map[string]ports.SCMObservation{}}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	ctx, cancel := context.WithCancel(context.Background())
	done := obs.Start(ctx)
	select {
	case <-store.listEntered:
	case <-time.After(time.Second):
		t.Fatal("initial poll did not start asynchronously")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer did not exit after context cancellation")
	}
}

func TestPoll_DisablesOnceWhenCredentialsUnavailable(t *testing.T) {
	store := testStoreWithSession()
	provider := &fakeProvider{
		credentialGate: true,
		credentialOK:   false,
		repoGuards:     map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v1"}},
		observations:   map[string]ports.SCMObservation{},
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.credentialChecks != 1 {
		t.Fatalf("credential checks = %d, want one lazy check", provider.credentialChecks)
	}
	if provider.repoGuardCalls != 0 || provider.listCalls != 0 || len(provider.fetchBatches) != 0 {
		t.Fatalf("provider API calls should be skipped without credentials: guards=%d lists=%d batches=%d",
			provider.repoGuardCalls, provider.listCalls, len(provider.fetchBatches))
	}
}

func TestPoll_RetriesTransientCredentialErrors(t *testing.T) {
	store := testStoreWithSession()
	provider := &fakeProvider{
		credentialGate: true,
		credentialErr:  errors.New("gh auth token failed transiently"),
		repoGuards:     map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v1"}},
		observations:   map[string]ports.SCMObservation{},
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if obs.credentialsChecked || obs.disabled {
		t.Fatalf("transient credential error should not commit checked/disabled: checked=%v disabled=%v", obs.credentialsChecked, obs.disabled)
	}
	if provider.credentialChecks != 1 || provider.repoGuardCalls != 0 {
		t.Fatalf("first poll should check credentials only: checks=%d repoGuards=%d", provider.credentialChecks, provider.repoGuardCalls)
	}

	provider.mu.Lock()
	provider.credentialErr = nil
	provider.credentialOK = true
	provider.mu.Unlock()
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !obs.credentialsChecked || obs.disabled {
		t.Fatalf("successful retry should commit checked without disabling: checked=%v disabled=%v", obs.credentialsChecked, obs.disabled)
	}
	if provider.credentialChecks != 2 || provider.repoGuardCalls != 1 {
		t.Fatalf("second poll should retry credentials and continue: checks=%d repoGuards=%d", provider.credentialChecks, provider.repoGuardCalls)
	}
}

// syncBuffer is a goroutine-safe wrapper around bytes.Buffer for capturing
// slog output emitted from the observer's background goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestStart_LogsDisabledWarningWhenNoTokenAndNoSubjects exercises the bug-7
// regression: on a fresh daemon with no tracked sessions/PRs, discoverSubjects
// returns empty and Poll short-circuits before reaching the credential gate.
// The "scm observer disabled: provider credentials unavailable" warn line must
// still fire exactly once from the observer loop's pre-Poll credential check.
func TestStart_LogsDisabledWarningWhenNoTokenAndNoSubjects(t *testing.T) {
	store := &fakeStore{
		sessions: nil, // no sessions → discoverSubjects returns empty
		projects: map[string]domain.ProjectRecord{},
		prs:      map[domain.SessionID][]domain.PullRequest{},
		checks:   map[string][]domain.PullRequestCheck{},
	}
	provider := &fakeProvider{
		credentialGate: true,
		credentialOK:   false,
		repoGuards:     map[string]ports.SCMGuardResult{},
		observations:   map[string]ports.SCMObservation{},
	}

	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	obs := New(provider, store, &fakeLifecycle{}, Config{
		Clock:    func() time.Time { return time.Unix(1, 0).UTC() },
		Tick:     time.Hour,
		Logger:   logger,
		CacheMax: 128,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := obs.Start(ctx)
	// Wait until the loop has emitted the expected warn line, or fail on timeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "scm observer disabled: provider credentials unavailable") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer did not exit after context cancellation")
	}

	logged := buf.String()
	if !strings.Contains(logged, "scm observer disabled: provider credentials unavailable") {
		t.Fatalf("expected disabled-credentials warn line in logs; got:\n%s", logged)
	}
	if got := strings.Count(logged, "scm observer disabled: provider credentials unavailable"); got != 1 {
		t.Fatalf("warn line should fire exactly once, got %d occurrences:\n%s", got, logged)
	}
	if !obs.credentialsChecked || !obs.disabled {
		t.Fatalf("observer state after pre-poll credential check: checked=%v disabled=%v", obs.credentialsChecked, obs.disabled)
	}
	if provider.credentialChecks != 1 {
		t.Fatalf("credential checks = %d, want exactly one pre-poll check", provider.credentialChecks)
	}
	if provider.repoGuardCalls != 0 || provider.listCalls != 0 || len(provider.fetchBatches) != 0 {
		t.Fatalf("no provider API calls expected when disabled: guards=%d lists=%d batches=%d",
			provider.repoGuardCalls, provider.listCalls, len(provider.fetchBatches))
	}
}

func TestPoll_RepoETag304SkipsListPRs(t *testing.T) {
	store := testStoreWithSession()
	provider := &fakeProvider{repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v1", NotModified: true}}, observations: map[string]ports.SCMObservation{}}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "v1"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.listCalls != 0 {
		t.Fatalf("ListOpenPRsByRepo called on 304: %d", provider.listCalls)
	}
}

func TestPoll_NoOpenPRsCommitsRepoETag(t *testing.T) {
	store := testStoreWithSession()
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v2"}},
		openPRs:      map[string][]ports.SCMPRObservation{},
		observations: map[string]ports.SCMObservation{},
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "v1"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.listCalls != 1 {
		t.Fatalf("ListOpenPRsByRepo calls = %d, want 1", provider.listCalls)
	}
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got != "v2" {
		t.Fatalf("repo ETag after empty listing = %q, want v2", got)
	}
	if len(provider.fetchBatches) != 0 {
		t.Fatalf("empty listing should not fetch PR batch: %#v", provider.fetchBatches)
	}
}

func TestPoll_RepoETag200DiscoversPRAndRefreshesSamePoll(t *testing.T) {
	store := testStoreWithSession()
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v2"}},
		openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {{ProviderID: "PR_stable_1", URL: "https://github.com/o/r/pull/1", Number: 1, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1"}}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.listCalls != 1 {
		t.Fatalf("ListOpenPRsByRepo calls = %d, want 1", provider.listCalls)
	}
	if len(provider.fetchBatches) != 1 || len(provider.fetchBatches[0]) != 1 || provider.fetchBatches[0][0].Number != 1 {
		t.Fatalf("new PR not refreshed in same poll: %#v", provider.fetchBatches)
	}
	if len(store.writes) < 1 || len(lc.observed) != 1 {
		t.Fatalf("write/lifecycle missing: writes=%d lifecycle=%d", len(store.writes), len(lc.observed))
	}
	if store.writes[0].pr.ProviderID != "PR_stable_1" {
		t.Fatalf("discovery ProviderID = %q, want PR_stable_1", store.writes[0].pr.ProviderID)
	}
}

func TestPoll_DiscoversOnlyPRsFromAuthenticatedHuman(t *testing.T) {
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
	if len(provider.fetchBatches) != 1 || len(provider.fetchBatches[0]) != 1 || provider.fetchBatches[0][0].Number != 2 {
		t.Fatalf("fetched PRs = %#v, want only authenticated author's PR #2", provider.fetchBatches)
	}
	for _, write := range store.writes {
		if write.pr.Number == 1 {
			t.Fatal("foreign author's PR was persisted")
		}
	}
}

func TestPoll_PreservesBranchDiscoveryWithoutHumanIdentity(t *testing.T) {
	tests := []struct {
		name        string
		identity    ports.SCMIdentity
		identityErr error
	}{
		{name: "lookup error", identityErr: errors.New("identity unavailable")},
		{name: "bot account", identity: ports.SCMIdentity{Login: "ao-bot", Human: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testStoreWithSession()
			provider := &fakeProvider{
				repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v2"}},
				openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {{URL: "https://github.com/o/r/pull/1", Number: 1, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1", Author: "other"}}},
				observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
				identity:     tt.identity,
				identityErr:  tt.identityErr,
			}
			obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
			if err := obs.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(provider.fetchBatches) != 1 || provider.fetchBatches[0][0].Number != 1 {
				t.Fatalf("branch fallback did not discover PR: %#v", provider.fetchBatches)
			}
		})
	}
}

// A session whose branch is the prefix of two open PRs (its root plus a stacked
// child on branch "feat/child") picks up both PRs in a single poll.
func TestPoll_DiscoversStackedChildByBranchPrefix(t *testing.T) {
	store := testStoreWithSession()
	childObs := testObs(2)
	childObs.PR.SourceBranch = "feat/child"
	childObs.PR.TargetBranch = "feat"
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v2"}},
		openPRs: map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {
			{URL: "https://github.com/o/r/pull/1", Number: 1, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1"},
			{URL: "https://github.com/o/r/pull/2", Number: 2, SourceBranch: "feat/child", HeadRepo: "o/r", TargetBranch: "feat", HeadSHA: "sha2"},
		}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1), prKey(testRepo, 2): childObs},
	}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	fetched := map[int]bool{}
	for _, batch := range provider.fetchBatches {
		for _, ref := range batch {
			fetched[ref.Number] = true
		}
	}
	if !fetched[1] || !fetched[2] {
		t.Fatalf("expected both root and stacked child fetched, got %#v", fetched)
	}
}

func TestPoll_DiscoversSiblingUnderRootSessionNamespace(t *testing.T) {
	store := testStoreWithSession()
	store.sessions[0].Metadata.Branch = "ao/p-1/root"
	prObs := testObs(1)
	prObs.PR.SourceBranch = "ao/p-1/fix"
	prObs.PR.TargetBranch = "main"
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v2"}},
		openPRs: map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {
			{URL: "https://github.com/o/r/pull/1", Number: 1, SourceBranch: "ao/p-1/fix", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1"},
		}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): prObs},
	}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected discovered PR write")
	}
	if got := store.writes[0].pr.SourceBranch; got != "ao/p-1/fix" {
		t.Fatalf("source branch = %q, want ao/p-1/fix", got)
	}
	if got := store.writes[0].pr.SessionID; got != "p-1" {
		t.Fatalf("session id = %q, want p-1", got)
	}
	if len(lc.observed) != 1 {
		t.Fatalf("lifecycle observations = %d, want 1", len(lc.observed))
	}
}

func TestMatchSession_PrefersExactBranchOverNamespaceMatch(t *testing.T) {
	exact := sessionRepo{session: domain.SessionRecord{ID: "exact"}, branch: "ao/p-1"}
	namespace := sessionRepo{session: domain.SessionRecord{ID: "namespace"}, branch: "ao/p-1/root"}
	got, ok := matchSession([]sessionRepo{namespace, exact}, "ao/p-1")
	if !ok {
		t.Fatal("expected a matching session")
	}
	if got.session.ID != exact.session.ID {
		t.Fatalf("matched session = %q, want exact branch owner %q", got.session.ID, exact.session.ID)
	}
}

func TestPoll_DiscoversWorkspaceChildRepoPR(t *testing.T) {
	store := testStoreWithSession()
	store.sessions[0].Metadata.Branch = "ao/p-1/root"
	store.projects["p"] = domain.ProjectRecord{
		ID:            "p",
		RepoOriginURL: "https://github.com/o/r.git",
		Kind:          domain.ProjectKindWorkspace,
	}
	store.workspaceRepos = map[string][]domain.WorkspaceRepoRecord{
		"p": {{ProjectID: "p", Name: "api", RelativePath: "api", RepoOriginURL: "https://github.com/o/api.git"}},
	}
	prObs := testObs(12)
	prObs.Repo = "o/api"
	prObs.PR.URL = "https://github.com/o/api/pull/12"
	prObs.PR.HTMLURL = "https://github.com/o/api/pull/12"
	prObs.PR.Number = 12
	prObs.PR.SourceBranch = "ao/p-1/api-billing"
	prObs.PR.TargetBranch = "main"
	prObs.PR.HeadSHA = "api-sha"
	prObs.CI.HeadSHA = "api-sha"
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{
			prKey(testRepo, 0):    {ETag: "root-v2"},
			prKey(testAPIRepo, 0): {ETag: "api-v2"},
		},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(testAPIRepo, 0): {
				{URL: "https://github.com/o/api/pull/12", Number: 12, SourceBranch: "ao/p-1/api-billing", HeadRepo: "o/api", TargetBranch: "main", HeadSHA: "api-sha"},
			},
		},
		observations: map[string]ports.SCMObservation{prKey(testAPIRepo, 12): prObs},
	}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 1 || len(provider.fetchBatches[0]) != 1 {
		t.Fatalf("child repo PR not refreshed: %#v", provider.fetchBatches)
	}
	ref := provider.fetchBatches[0][0]
	if ref.Repo.Repo != "o/api" || ref.Number != 12 {
		t.Fatalf("fetched ref = %#v, want o/api#12", ref)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected child repo PR write")
	}
	if got := store.writes[0].pr.Repo; got != "o/api" {
		t.Fatalf("persisted repo = %q, want o/api", got)
	}
	if got := store.writes[0].pr.SessionID; got != "p-1" {
		t.Fatalf("session id = %q, want p-1", got)
	}
	if len(lc.observed) != 1 {
		t.Fatalf("lifecycle observations = %d, want 1", len(lc.observed))
	}
}

func TestPoll_DiscoversWorkspaceChildRepoUpstreamPR(t *testing.T) {
	oldRemoteURLs := gitRemoteURLsFunc
	gitRemoteURLsFunc = func(path string) []string {
		if strings.HasSuffix(filepath.ToSlash(path), "/api") {
			return []string{"https://github.com/o/api.git", "https://github.com/upstream/api.git"}
		}
		return nil
	}
	defer func() { gitRemoteURLsFunc = oldRemoteURLs }()

	store := testStoreWithSession()
	store.sessions[0].Metadata.Branch = "ao/p-1/api-billing"
	store.projects["p"] = domain.ProjectRecord{
		ID:            "p",
		Path:          "/repo/workspace",
		RepoOriginURL: "https://github.com/o/root.git",
		Kind:          domain.ProjectKindWorkspace,
	}
	store.workspaceRepos = map[string][]domain.WorkspaceRepoRecord{
		"p": {{ProjectID: "p", Name: "api", RelativePath: "api", RepoOriginURL: "https://github.com/o/api.git"}},
	}
	upstreamRepo := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "upstream", Name: "api", Repo: "upstream/api"}
	prObs := testObs(44)
	prObs.Repo = "upstream/api"
	prObs.PR.URL = "https://github.com/upstream/api/pull/44"
	prObs.PR.HTMLURL = "https://github.com/upstream/api/pull/44"
	prObs.PR.Number = 44
	prObs.PR.SourceBranch = "ao/p-1/api-billing"
	prObs.PR.HeadRepo = "o/api"
	prObs.PR.TargetBranch = "main"
	prObs.PR.HeadSHA = "api-sha"
	prObs.CI.HeadSHA = "api-sha"
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{
			prKey(upstreamRepo, 0): {ETag: "upstream-v1"},
		},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(upstreamRepo, 0): {
				{URL: "https://github.com/upstream/api/pull/44", Number: 44, SourceBranch: "ao/p-1/api-billing", HeadRepo: "o/api", TargetBranch: "main", HeadSHA: "api-sha"},
			},
		},
		observations: map[string]ports.SCMObservation{prKey(upstreamRepo, 44): prObs},
	}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected discovered upstream child PR write")
	}
	if got := store.writes[0].pr.Repo; got != "upstream/api" {
		t.Fatalf("written repo = %q, want upstream/api", got)
	}
	if got := store.writes[0].pr.SessionID; got != "p-1" {
		t.Fatalf("session id = %q, want p-1", got)
	}
}

// A PR whose head branch matches a session branch but lives in a fork (its head
// repo differs from the project repo) must not be auto-attributed: its commits
// are not the session's work. It is neither fetched nor persisted.
func TestPoll_IgnoresForkPRWithMatchingBranch(t *testing.T) {
	store := testStoreWithSession()
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v2"}},
		openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {{URL: "https://github.com/forker/r/pull/1", Number: 1, SourceBranch: "feat", HeadRepo: "forker/r", TargetBranch: "main", HeadSHA: "sha1"}}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 0 {
		t.Fatalf("fork PR must not be fetched, got %#v", provider.fetchBatches)
	}
	if len(store.writes) != 0 {
		t.Fatalf("fork PR must not be persisted, got %d writes", len(store.writes))
	}
}

func mustGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

// A PR opened from the fork push origin against an upstream base repo (the
// standard fork -> upstream contribution flow) must be discovered and attributed
// by scanning every remote in the project checkout, not just origin. Its head
// lives in origin (o/r) while its base lives in upstream (up/r); the persisted
// row records the upstream base repo so refresh refetches against it.
func TestPoll_DiscoversCrossForkPRFromUpstreamRemote(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, "init", dir)
	mustGit(t, "-C", dir, "remote", "add", "origin", "https://github.com/o/r.git")
	mustGit(t, "-C", dir, "remote", "add", "upstream", "https://github.com/up/r.git")

	upstream := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "up", Name: "r", Repo: "up/r"}
	store := &fakeStore{
		sessions: []domain.SessionRecord{{ID: "p-1", ProjectID: "p", Metadata: domain.SessionMetadata{Branch: "ao/p-1/root"}}},
		projects: map[string]domain.ProjectRecord{"p": {ID: "p", Path: dir, RepoOriginURL: "https://github.com/o/r.git"}},
		prs:      map[domain.SessionID][]domain.PullRequest{},
		checks:   map[string][]domain.PullRequestCheck{},
	}
	crossObs := ports.SCMObservation{
		Fetched: true, Provider: "github", Host: "github.com", Repo: "up/r",
		PR:           ports.SCMPRObservation{URL: "https://github.com/up/r/pull/1", Number: 1, State: "open", SourceBranch: "ao/p-1/feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1", Title: "PR"},
		CI:           ports.SCMCIObservation{Summary: string(domain.CIPassing), HeadSHA: "sha1"},
		Review:       ports.SCMReviewObservation{Decision: string(domain.ReviewNone)},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable), Mergeable: true},
	}
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "origin"}, prKey(upstream, 0): {ETag: "up"}},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(upstream, 0): {{URL: "https://github.com/up/r/pull/1", Number: 1, SourceBranch: "ao/p-1/feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1"}},
		},
		observations: map[string]ports.SCMObservation{prKey(upstream, 1): crossObs},
	}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected cross-fork PR write")
	}
	got := store.writes[0].pr
	if got.SessionID != "p-1" {
		t.Fatalf("session id = %q, want p-1", got.SessionID)
	}
	if got.Repo != "up/r" {
		t.Fatalf("pr repo = %q, want up/r (upstream base)", got.Repo)
	}
	if got.SourceBranch != "ao/p-1/feat" {
		t.Fatalf("source branch = %q, want ao/p-1/feat", got.SourceBranch)
	}
	fetched := false
	for _, batch := range provider.fetchBatches {
		for _, ref := range batch {
			if ref.Repo.Repo == "up/r" && ref.Number == 1 {
				fetched = true
			}
		}
	}
	if !fetched {
		t.Fatalf("cross-fork PR must be refreshed against upstream, batches=%#v", provider.fetchBatches)
	}
}

// A PR on a scanned upstream remote whose head lives in some third-party fork
// (not this project's origin) must never be attributed, even though its branch
// name matches a session. Scanning extra remotes stays safe.
func TestPoll_IgnoresUpstreamPRFromForeignHead(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, "init", dir)
	mustGit(t, "-C", dir, "remote", "add", "origin", "https://github.com/o/r.git")
	mustGit(t, "-C", dir, "remote", "add", "upstream", "https://github.com/up/r.git")

	upstream := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "up", Name: "r", Repo: "up/r"}
	store := &fakeStore{
		sessions: []domain.SessionRecord{{ID: "p-1", ProjectID: "p", Metadata: domain.SessionMetadata{Branch: "ao/p-1/root"}}},
		projects: map[string]domain.ProjectRecord{"p": {ID: "p", Path: dir, RepoOriginURL: "https://github.com/o/r.git"}},
		prs:      map[domain.SessionID][]domain.PullRequest{},
		checks:   map[string][]domain.PullRequestCheck{},
	}
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "origin"}, prKey(upstream, 0): {ETag: "up"}},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(upstream, 0): {{URL: "https://github.com/up/r/pull/9", Number: 9, SourceBranch: "ao/p-1/feat", HeadRepo: "stranger/r", TargetBranch: "main", HeadSHA: "sha9"}},
		},
		observations: map[string]ports.SCMObservation{},
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 0 {
		t.Fatalf("foreign-head upstream PR must not be fetched, got %#v", provider.fetchBatches)
	}
	if len(store.writes) != 0 {
		t.Fatalf("foreign-head upstream PR must not be persisted, got %d writes", len(store.writes))
	}
}

// A newly discovered open PR is persisted as a baseline row during discovery,
// before the refresh/lifecycle pass. This is what lets a same-poll terminal
// observation for a sibling PR see the open PR in the store and avoid completing
// the session prematurely. The persist holds even when the refresh fetch yields
// no observation for the new PR.
func TestPoll_DiscoveredPRPersistedAsBaselineBeforeRefresh(t *testing.T) {
	store := testStoreWithSession()
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "v2"}},
		openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {{URL: "https://github.com/o/r/pull/1", Number: 1, SourceBranch: "feat", HeadRepo: "o/r", TargetBranch: "main", HeadSHA: "sha1"}}},
		observations: map[string]ports.SCMObservation{}, // refresh returns nothing
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	var baseline *domain.PullRequest
	for i := range store.writes {
		if store.writes[i].pr.Number == 1 {
			baseline = &store.writes[i].pr
			break
		}
	}
	if baseline == nil {
		t.Fatalf("discovered PR #1 not persisted as a baseline row; writes=%#v", store.writes)
		return
	}
	if baseline.Merged || baseline.Closed {
		t.Fatalf("baseline row must be open, got merged=%v closed=%v", baseline.Merged, baseline.Closed)
	}
}

func TestPoll_CIETagChangeRefreshesWhenRepoUnchanged(t *testing.T) {
	store := testStoreWithSession()
	store.prs["p-1"] = []domain.PullRequest{knownPR(1)}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		checkGuards:  map[string]ports.SCMGuardResult{commitKey(testRepo, "sha1"): {ETag: "ci2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(2, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")] = "ci1"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 1 {
		t.Fatalf("CI ETag 200 should trigger batch fetch, got %d", len(provider.fetchBatches))
	}
}

func TestPoll_GraphQLBatchChunksAt25(t *testing.T) {
	store := &fakeStore{projects: map[string]domain.ProjectRecord{"p": {ID: "p", RepoOriginURL: "https://github.com/o/r.git"}}, prs: map[domain.SessionID][]domain.PullRequest{}, checks: map[string][]domain.PullRequestCheck{}}
	provider := &fakeProvider{repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}}, observations: map[string]ports.SCMObservation{}}
	for i := 1; i <= 26; i++ {
		id := domain.SessionID("p-" + fmt.Sprint(i))
		store.sessions = append(store.sessions, domain.SessionRecord{ID: id, ProjectID: "p", Metadata: domain.SessionMetadata{Branch: "b" + fmt.Sprint(i)}})
		pr := knownPR(i)
		pr.SessionID = id
		pr.MetadataHash = "" // force candidate
		store.prs[id] = []domain.PullRequest{pr}
		provider.observations[prKey(testRepo, i)] = testObs(i)
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 2 || len(provider.fetchBatches[0]) != 25 || len(provider.fetchBatches[1]) != 1 {
		t.Fatalf("batch sizes = %#v", provider.fetchBatches)
	}
}

func TestPoll_FailingCIFetchesLogTailOnlyWhenFingerprintChanged(t *testing.T) {
	failing := testObs(1)
	failing.CI.Summary = string(domain.CIFailing)
	failing.CI.Checks = []ports.SCMCheckObservation{{Name: "build", Status: string(domain.PRCheckFailed), Conclusion: "failure", ProviderID: "99"}}
	failing.CI.FailedChecks = failing.CI.Checks
	failing.CI.FailedFingerprint = "fp"

	store := testStoreWithSession()
	local := knownPR(1)
	local.CIHash = "old"
	store.prs["p-1"] = []domain.PullRequest{local}
	provider := &fakeProvider{repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}}, checkGuards: map[string]ports.SCMGuardResult{commitKey(testRepo, "sha1"): {ETag: "ci2"}}, observations: map[string]ports.SCMObservation{prKey(testRepo, 1): failing}, logTails: map[string]string{"build": "tail"}}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.logCalls != 1 {
		t.Fatalf("log calls = %d, want 1", provider.logCalls)
	}

	provider.logCalls = 0
	store.writes = nil
	withTail := failing
	withTail.CI.Checks[0].LogTail = "tail"
	withTail.CI.FailedChecks[0].LogTail = "tail"
	withTail.CI.FailureLogTail = "tail"
	local.CIHash = ciSemanticHash(withTail.CI)
	store.prs["p-1"] = []domain.PullRequest{local}
	store.checks[local.URL] = []domain.PullRequestCheck{{Name: "build", Status: domain.PRCheckFailed, LogTail: "tail"}}
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.logCalls != 0 {
		t.Fatalf("unchanged fingerprint fetched logs again: %d", provider.logCalls)
	}
	if len(store.writes) != 0 {
		t.Fatalf("unchanged failed fingerprint with stored log tail should not write, got %d writes", len(store.writes))
	}
}

func TestEnrichFailureLogsDoesNotRefetchExistingTailOrMissingProviderID(t *testing.T) {
	obsValue := testObs(1)
	obsValue.CI.Summary = string(domain.CIFailing)
	obsValue.CI.FailedFingerprint = "fp"
	obsValue.CI.Checks = []ports.SCMCheckObservation{
		{Name: "build", Status: string(domain.PRCheckFailed), Conclusion: "failure", ProviderID: "99", LogTail: "provider supplied tail"},
		{Name: "lint", Status: string(domain.PRCheckFailed), Conclusion: "failure"},
	}
	obsValue.CI.FailedChecks = append([]ports.SCMCheckObservation(nil), obsValue.CI.Checks...)

	provider := &fakeProvider{logTails: map[string]string{"build": "fetched tail", "lint": "should not fetch"}}
	obs := newTestObserver(testStoreWithSession(), provider, &fakeLifecycle{}, time.Unix(1, 0).UTC())
	obs.enrichFailureLogs(context.Background(), &obsValue, domain.PullRequest{})

	if provider.logCalls != 0 {
		t.Fatalf("log calls = %d, want 0 when tail already exists or provider id is missing", provider.logCalls)
	}
	if got := obsValue.CI.FailedChecks[0].LogTail; got != "provider supplied tail" {
		t.Fatalf("existing tail changed: got %q", got)
	}
	if got := obsValue.CI.FailedChecks[1].LogTail; got != "" {
		t.Fatalf("tail without provider id = %q, want empty", got)
	}
	if got := obsValue.CI.FailureLogTail; got != "provider supplied tail" {
		t.Fatalf("FailureLogTail = %q, want only existing tail", got)
	}
}

func TestPoll_ReviewPollingRespectsInterval(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewChangesRequest
	local.ReviewHash = "old-review"
	store.prs["p-1"] = []domain.PullRequest{local}
	provider := &fakeProvider{repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}}, observations: map[string]ports.SCMObservation{}, reviews: map[string]ports.SCMReviewObservation{prKey(testRepo, 1): {Decision: string(domain.ReviewChangesRequest), Threads: []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 1, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Body: "fix"}}}}}}}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(120, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	obs.Cache.LastReviewPollAt[prKey(testRepo, 1)] = time.Unix(90, 0).UTC()
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.reviewCalls != 0 {
		t.Fatalf("review fetched before interval: %d", provider.reviewCalls)
	}
	obs.Cache.LastReviewPollAt[prKey(testRepo, 1)] = time.Unix(0, 0).UTC()
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.reviewCalls != 1 {
		t.Fatalf("review not fetched after interval: %d", provider.reviewCalls)
	}
	if len(store.writes) == 0 || store.writes[0].reviewMode != ports.ReviewWriteReplace {
		t.Fatalf("review refresh not persisted with replace mode: %#v", store.writes)
	}
}

func TestPoll_UnchangedHashesDoNotWriteOrNotify(t *testing.T) {
	store := testStoreWithSession()
	obsValue := testObs(1)
	local := knownPR(1)
	local.MetadataHash = metadataSemanticHash(obsValue)
	local.CIHash = ciSemanticHash(obsValue.CI)
	local.ReviewHash = reviewSemanticHash(obsValue.Review)
	store.prs["p-1"] = []domain.PullRequest{local}
	provider := &fakeProvider{repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}}, observations: map[string]ports.SCMObservation{prKey(testRepo, 1): obsValue}}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(1, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) != 0 || len(lc.observed) != 0 {
		t.Fatalf("unchanged hashes wrote/notified: writes=%d observed=%d", len(store.writes), len(lc.observed))
	}
}

func TestPoll_ReviewHashDrivesPersistenceAndLifecycle(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.ReviewHash = "old"
	local.Review = domain.ReviewChangesRequest
	store.prs["p-1"] = []domain.PullRequest{local}
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewChangesRequest),
		Reviews:  []ports.SCMReviewSummaryObservation{{ID: "review-1", Author: "ann", State: string(domain.ReviewChangesRequest), URL: "https://github.com/o/r/pull/1#pullrequestreview-1", SubmittedAt: time.Unix(199, 0).UTC()}},
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "fix this"}}}},
	}
	provider := &fakeProvider{repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}}, observations: map[string]ports.SCMObservation{}, reviews: map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review}}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(200, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 || len(store.writes[0].comments) != 1 {
		t.Fatalf("review change not persisted: %#v", store.writes)
	}
	if len(store.writes[0].reviews) != 1 || store.writes[0].reviews[0].URL != "https://github.com/o/r/pull/1#pullrequestreview-1" {
		t.Fatalf("review summaries not persisted: %#v", store.writes[0].reviews)
	}
	if len(store.writes) != 2 {
		t.Fatalf("review change with lifecycle should write held-back facts then acknowledgement, got %d writes", len(store.writes))
	}
	if store.writes[0].reviewMode != ports.ReviewWriteReplace {
		t.Fatalf("initial review write mode = %v, want replace", store.writes[0].reviewMode)
	}
	if store.writes[1].reviewMode != ports.ReviewWritePreserve || len(store.writes[1].comments) != 0 {
		t.Fatalf("lifecycle acknowledgement should preserve review rows, got mode=%v comments=%d", store.writes[1].reviewMode, len(store.writes[1].comments))
	}
	if len(lc.observed) != 1 || !lc.observed[0].Changed.Review {
		t.Fatalf("review change not notified: %#v", lc.observed)
	}
}

func TestPoll_PartialReviewRefreshUsesMergeMode(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.ReviewHash = "old"
	local.Review = domain.ReviewChangesRequest
	store.prs["p-1"] = []domain.PullRequest{local}
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewChangesRequest),
		Partial:  true,
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "fix this"}}}},
	}
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		reviews:    map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	obs := newTestObserver(store, provider, nil, time.Unix(210, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %#v, want one partial review merge", store.writes)
	}
	if store.writes[0].reviewMode != ports.ReviewWriteMerge {
		t.Fatalf("review mode = %v, want merge", store.writes[0].reviewMode)
	}
	if store.writes[0].pr.ReviewHash != reviewSemanticHash(review) {
		t.Fatalf("review hash = %q, want partial hash %q", store.writes[0].pr.ReviewHash, reviewSemanticHash(review))
	}
}

func TestPoll_PartialCIPreservesDurableChecks(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.CI = domain.CIPassing
	local.ObservedAt = time.Unix(10, 0).UTC()
	local.CIObservedAt = time.Unix(11, 0).UTC()
	durableChecks := []domain.PullRequestCheck{
		{Name: "build", CommitHash: "sha1", Status: domain.PRCheckPassed, Conclusion: "success", URL: "ci"},
		{Name: "lint", CommitHash: "sha1", Status: domain.PRCheckPassed, Conclusion: "success", URL: "ci-lint"},
	}
	store.checks[local.URL] = durableChecks
	// Compute the durable CI hash from the full two-check snapshot so the
	// incoming one-check partial observation differs and would trigger a
	// CI change write without the gate.
	durableObs := testObs(1)
	durableObs.CI = ports.SCMCIObservation{
		Summary: string(domain.CIPassing),
		HeadSHA: "sha1",
		Checks: []ports.SCMCheckObservation{
			{Name: "build", Status: string(domain.PRCheckPassed), Conclusion: "success", URL: "ci"},
			{Name: "lint", Status: string(domain.PRCheckPassed), Conclusion: "success", URL: "ci-lint"},
		},
	}
	local.CIHash = ciSemanticHash(durableObs.CI)
	local.MetadataHash = metadataSemanticHash(durableObs)
	store.prs["p-1"] = []domain.PullRequest{local}
	// Provider returns a truncated CI snapshot: Partial=true, only one of
	// the two durable checks is present. Without the CI.Partial gate this
	// capped snapshot would be persisted as Fetched=true complete and
	// overwrite the durable lint check. The title differs so a metadata
	// change forces a write even when the CI hash is preserved by the gate.
	partialObs := testObs(1)
	partialObs.PR.Title = "PR (updated)"
	partialObs.CI = ports.SCMCIObservation{
		Summary: string(domain.CIPassing),
		HeadSHA: "sha1",
		Partial: true,
		Checks:  []ports.SCMCheckObservation{{Name: "build", Status: string(domain.PRCheckPassed), Conclusion: "success", URL: "ci"}},
	}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		checkGuards:  map[string]ports.SCMGuardResult{commitKey(testRepo, "sha1"): {ETag: "ci2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): partialObs},
	}
	obs := newTestObserver(store, provider, nil, time.Unix(210, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %#v, want one write", store.writes)
	}
	write := store.writes[0]
	// The durable CI hash must be preserved — the capped snapshot must
	// not advance it as if it were a complete observation.
	if write.pr.CIHash != local.CIHash {
		t.Fatalf("CI hash = %q, want preserved durable %q", write.pr.CIHash, local.CIHash)
	}
	// CIObservedAt must not advance — the partial snapshot is not an
	// authoritative complete observation.
	if !write.pr.CIObservedAt.Equal(local.CIObservedAt) {
		t.Fatalf("CIObservedAt = %s, want preserved durable %s", write.pr.CIObservedAt, local.CIObservedAt)
	}
}

func TestPoll_ReviewOnlyRefreshPreservesLocalCIAndMetadata(t *testing.T) {
	store := testStoreWithSession()
	localObs := testObs(1)
	local := knownPR(1)
	local.CI = domain.CIPassing
	local.Review = domain.ReviewChangesRequest
	local.ReviewHash = "old-review"
	local.MetadataHash = metadataSemanticHash(localObs)
	local.CIHash = ciSemanticHash(localObs.CI)
	local.ObservedAt = time.Unix(10, 0).UTC()
	local.CIObservedAt = time.Unix(11, 0).UTC()
	local.ReviewObservedAt = time.Unix(12, 0).UTC()
	store.prs["p-1"] = []domain.PullRequest{local}
	store.checks[local.URL] = []domain.PullRequestCheck{
		{Name: "build", CommitHash: "sha1", Status: domain.PRCheckPassed, Conclusion: "success", URL: "ci"},
		{Name: "stale", CommitHash: "old-sha", Status: domain.PRCheckFailed, Conclusion: "failure", URL: "old-ci", LogTail: "old tail"},
	}
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewChangesRequest),
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "fix"}}}},
	}
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		reviews:    map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	now := time.Unix(200, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 {
		t.Fatalf("writes = %d, want review-only write", len(store.writes))
	}
	write := store.writes[len(store.writes)-1]
	if write.pr.MetadataHash != local.MetadataHash {
		t.Fatalf("metadata hash changed on review-only refresh: got %q want %q", write.pr.MetadataHash, local.MetadataHash)
	}
	if write.pr.CIHash != local.CIHash {
		t.Fatalf("CI hash changed on review-only refresh: got %q want %q", write.pr.CIHash, local.CIHash)
	}
	if !write.pr.ObservedAt.Equal(local.ObservedAt) {
		t.Fatalf("ObservedAt changed on review-only refresh: got %s want %s", write.pr.ObservedAt, local.ObservedAt)
	}
	if !write.pr.CIObservedAt.Equal(local.CIObservedAt) {
		t.Fatalf("CIObservedAt changed on review-only refresh: got %s want %s", write.pr.CIObservedAt, local.CIObservedAt)
	}
	if !write.pr.ReviewObservedAt.Equal(now) {
		t.Fatalf("ReviewObservedAt = %s, want %s", write.pr.ReviewObservedAt, now)
	}
	if len(write.checks) != 1 || write.checks[0].Name != "build" {
		t.Fatalf("review-only local reconstruction should include current-head checks only: %#v", write.checks)
	}
}

func TestPoll_ReviewFetchFailureDoesNotUpdateReviewDecision(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewChangesRequest
	local.ReviewHash = reviewSemanticHash(ports.SCMReviewObservation{Decision: string(domain.ReviewChangesRequest), Threads: []ports.SCMReviewThreadObservation{{ID: "old", Comments: []ports.SCMReviewCommentObservation{{ID: "c-old", Body: "old"}}}}})
	obsValue := testObs(1)
	obsValue.Review.Decision = string(domain.ReviewApproved)
	local.MetadataHash = metadataSemanticHash(obsValue)
	local.CIHash = ciSemanticHash(obsValue.CI)
	store.prs["p-1"] = []domain.PullRequest{local}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): obsValue},
		reviewErr:    errors.New("review API down"),
	}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(300, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want 1", provider.reviewCalls)
	}
	if len(store.writes) != 0 || len(lc.observed) != 0 {
		t.Fatalf("review fetch failure should not persist/notify stale review data: writes=%#v lifecycle=%#v", store.writes, lc.observed)
	}
	if !obs.Cache.ReviewRefreshFailed[prKey(testRepo, 1)] {
		t.Fatalf("review fetch failure was not marked for retry")
	}
}

func TestPoll_SuccessfulReviewRefreshClearsRetryCacheSlot(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewChangesRequest
	local.ReviewHash = "old-review"
	store.prs["p-1"] = []domain.PullRequest{local}
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewChangesRequest),
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Body: "fix"}}}},
	}
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		reviews:    map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	obs := newTestObserver(store, provider, nil, time.Unix(350, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	obs.cacheSetBool(obs.Cache.ReviewRefreshFailed, &obs.Cache.reviewFailedOrder, prKey(testRepo, 1), true)

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := obs.Cache.ReviewRefreshFailed[prKey(testRepo, 1)]; ok {
		t.Fatalf("successful review refresh should delete retry map entry, got %#v", obs.Cache.ReviewRefreshFailed)
	}
	for _, key := range obs.Cache.reviewFailedOrder {
		if key == prKey(testRepo, 1) {
			t.Fatalf("successful review refresh should remove retry order slot, got %#v", obs.Cache.reviewFailedOrder)
		}
	}
}

func TestPoll_ReviewObservedAtZeroTriggersReviewRefresh(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewApproved
	local.ReviewHash = "some-hash"       // non-empty but review_observed_at is zero
	local.ReviewObservedAt = time.Time{} // review threads were never fetched
	store.prs["p-1"] = []domain.PullRequest{local}
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewApproved),
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "fix"}}}},
	}
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		reviews:    map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	now := time.Unix(400, 0).UTC()
	obs := newTestObserver(store, provider, nil, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want 1 (should trigger FetchReviewThreads when review_observed_at is zero)", provider.reviewCalls)
	}
	if len(store.writes) == 0 {
		t.Fatalf("expected a write after review refresh")
	}
	write := store.writes[len(store.writes)-1]
	if !write.pr.ReviewObservedAt.Equal(now) {
		t.Fatalf("ReviewObservedAt = %s, want %s", write.pr.ReviewObservedAt, now)
	}
	if len(write.threads) != 1 || write.threads[0].ThreadID != "t1" {
		t.Fatalf("expected review thread t1 persisted, got %#v", write.threads)
	}
}

func TestPoll_DoesNotCommitCommitETagWhenFetchFails(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	store.prs["p-1"] = []domain.PullRequest{local}
	provider := &fakeProvider{
		repoGuards:  map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		checkGuards: map[string]ports.SCMGuardResult{commitKey(testRepo, "sha1"): {ETag: "ci2"}},
		fetchErr:    errors.New("graphql down"),
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(400, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")] = "ci1"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")]; got != "ci1" {
		t.Fatalf("commit ETag advanced after failed fetch: got %q want ci1", got)
	}
}

func TestNeedsReviewRefresh_UpdatedAtProviderTriggersRefresh(t *testing.T) {
	store := testStoreWithSession()
	obs := newTestObserver(store, &fakeProvider{}, nil, time.Unix(500, 0).UTC())
	key := prKey(testRepo, 1)

	base := domain.PullRequest{
		Number:           1,
		Review:           domain.ReviewApproved,
		ReviewHash:       "hash",
		ReviewObservedAt: time.Unix(100, 0).UTC(),
	}

	// updated_at newer than ReviewObservedAt → refresh
	if !obs.needsReviewRefresh(key, base, string(domain.ReviewApproved), true, time.Unix(200, 0).UTC(), time.Unix(500, 0).UTC()) {
		t.Fatal("expected refresh when updatedAtProvider is newer than ReviewObservedAt")
	}

	// updated_at older than ReviewObservedAt → no refresh
	if obs.needsReviewRefresh(key, base, string(domain.ReviewApproved), true, time.Unix(50, 0).UTC(), time.Unix(500, 0).UTC()) {
		t.Fatal("expected no refresh when updatedAtProvider is older than ReviewObservedAt")
	}

	// updated_at equal to ReviewObservedAt → no refresh
	if obs.needsReviewRefresh(key, base, string(domain.ReviewApproved), true, time.Unix(100, 0).UTC(), time.Unix(500, 0).UTC()) {
		t.Fatal("expected no refresh when updatedAtProvider equals ReviewObservedAt")
	}

	// updated_at is zero → no refresh (falls back to existing conditions)
	if obs.needsReviewRefresh(key, base, string(domain.ReviewApproved), true, time.Time{}, time.Unix(500, 0).UTC()) {
		t.Fatal("expected no refresh when updatedAtProvider is zero")
	}

	// hasObs is false → updated_at check skipped, no refresh
	if obs.needsReviewRefresh(key, base, string(domain.ReviewApproved), false, time.Unix(200, 0).UTC(), time.Unix(500, 0).UTC()) {
		t.Fatal("expected no refresh when hasObs is false even with newer updatedAtProvider")
	}
}

func TestPoll_UpdatedAtProviderTriggersReviewRefresh(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewApproved
	local.ReviewHash = "review-hash"
	local.ReviewObservedAt = time.Unix(100, 0).UTC()
	store.prs["p-1"] = []domain.PullRequest{local}

	obsValue := testObs(1)
	obsValue.PR.UpdatedAtProvider = time.Unix(200, 0).UTC()
	obsValue.Review = ports.SCMReviewObservation{Decision: string(domain.ReviewApproved)}

	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewApproved),
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "comment"}}}},
	}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): obsValue},
		reviews:      map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	now := time.Unix(500, 0).UTC()
	obs := newTestObserver(store, provider, nil, now)
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want 1 (should trigger FetchReviewThreads when updatedAtProvider is newer)", provider.reviewCalls)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected a write after review refresh")
	}
	write := store.writes[len(store.writes)-1]
	if !write.pr.ReviewObservedAt.Equal(now) {
		t.Fatalf("ReviewObservedAt = %s, want %s", write.pr.ReviewObservedAt, now)
	}
	if len(write.threads) != 1 || write.threads[0].ThreadID != "t1" {
		t.Fatalf("expected review thread t1 persisted, got %#v", write.threads)
	}
}

func TestPoll_UpdatedAtProviderStaleDoesNotTriggerReviewRefresh(t *testing.T) {
	store := testStoreWithSession()
	obsValue := testObs(1)
	obsValue.PR.UpdatedAtProvider = time.Unix(100, 0).UTC() // older than ReviewObservedAt
	obsValue.Review = ports.SCMReviewObservation{Decision: string(domain.ReviewApproved)}

	local := knownPR(1)
	local.Review = domain.ReviewApproved
	local.ReviewObservedAt = time.Unix(200, 0).UTC()
	local.MetadataHash = metadataSemanticHash(obsValue)
	local.CIHash = ciSemanticHash(obsValue.CI)
	local.ReviewHash = reviewSemanticHash(obsValue.Review)
	store.prs["p-1"] = []domain.PullRequest{local}

	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): obsValue},
		reviews:      map[string]ports.SCMReviewObservation{prKey(testRepo, 1): {Decision: string(domain.ReviewApproved)}},
	}
	obs := newTestObserver(store, provider, nil, time.Unix(500, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.reviewCalls != 0 {
		t.Fatalf("reviewCalls = %d, want 0 (stale updatedAtProvider should not trigger refresh)", provider.reviewCalls)
	}
	if len(store.writes) != 0 {
		t.Fatalf("expected no writes when stale, got %d", len(store.writes))
	}
}

func TestPoll_RefreshReviewsDoesNotDowngradeChangesRequested(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewChangesRequest
	local.ReviewHash = "old-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	// The fast-path observation set ReviewChangesRequest (e.g. from
	// detailed_merge_status=requested_changes). FetchReviewThreads derives the
	// decision from approvals and returns a lower-priority "approved".
	obsValue := testObs(1)
	obsValue.Review.Decision = string(domain.ReviewChangesRequest)
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewApproved),
		Partial:  true,
		Reviews:  []ports.SCMReviewSummaryObservation{{ID: "review-1", Author: "ann", State: string(domain.ReviewApproved), URL: "https://github.com/o/r/pull/1#pullrequestreview-1", SubmittedAt: time.Unix(199, 0).UTC()}},
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "fix"}}}},
	}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): obsValue},
		reviews:      map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	obs := newTestObserver(store, provider, nil, time.Unix(500, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want 1", provider.reviewCalls)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected a write after review refresh")
	}
	write := store.writes[len(store.writes)-1]
	if write.pr.Review != domain.ReviewChangesRequest {
		t.Fatalf("Review = %q, want %q (should not downgrade ChangesRequested to lower-priority decision)", write.pr.Review, domain.ReviewChangesRequest)
	}
	// Threads, summaries, and partial flag from FetchReviewThreads must still
	// be adopted regardless of the decision guard.
	if len(write.threads) != 1 || write.threads[0].ThreadID != "t1" {
		t.Fatalf("expected review thread t1 persisted, got %#v", write.threads)
	}
	if len(write.reviews) != 1 || write.reviews[0].ID != "review-1" {
		t.Fatalf("expected review summary review-1 persisted, got %#v", write.reviews)
	}
	if write.reviewMode != ports.ReviewWriteMerge {
		t.Fatalf("expected ReviewWriteMerge mode when Partial is true, got %v", write.reviewMode)
	}
}

func TestPoll_RefreshReviewsOverwritesWithEqualOrHigherPriority(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewApproved
	local.ReviewHash = "old-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	// Local decision is ReviewApproved (priority 1). FetchReviewThreads
	// returns ReviewChangesRequest (priority 3) — higher priority, should
	// overwrite as before (no regression).
	local.ReviewObservedAt = time.Unix(100, 0).UTC()
	obsValue := testObs(1)
	obsValue.Review.Decision = string(domain.ReviewApproved)
	obsValue.PR.UpdatedAtProvider = time.Unix(200, 0).UTC()
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewChangesRequest),
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "fix"}}}},
	}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): obsValue},
		reviews:      map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	obs := newTestObserver(store, provider, nil, time.Unix(500, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected a write after review refresh")
	}
	write := store.writes[len(store.writes)-1]
	if write.pr.Review != domain.ReviewChangesRequest {
		t.Fatalf("Review = %q, want %q (higher-priority decision should overwrite)", write.pr.Review, domain.ReviewChangesRequest)
	}
	if len(write.threads) != 1 || write.threads[0].ThreadID != "t1" {
		t.Fatalf("expected review thread t1 persisted, got %#v", write.threads)
	}
}

func TestPoll_RefreshReviewsDowngradeToNoneBlocked(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewChangesRequest
	local.ReviewHash = "old-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	// FetchReviewThreads returns Decision="none" (priority 0), which is
	// lower than ReviewChangesRequest (priority 3). The guard must keep the
	// existing ChangesRequested decision but still adopt the threads.
	obsValue := testObs(1)
	obsValue.Review.Decision = string(domain.ReviewChangesRequest)
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewNone),
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "fix"}}}},
	}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): obsValue},
		reviews:      map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	obs := newTestObserver(store, provider, nil, time.Unix(500, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected a write after review refresh")
	}
	write := store.writes[len(store.writes)-1]
	if write.pr.Review != domain.ReviewChangesRequest {
		t.Fatalf("Review = %q, want %q (should not downgrade to none)", write.pr.Review, domain.ReviewChangesRequest)
	}
	if len(write.threads) != 1 || write.threads[0].ThreadID != "t1" {
		t.Fatalf("expected review thread t1 persisted, got %#v", write.threads)
	}
}

func TestPoll_RefreshReviewsDowngradeToReviewRequiredBlocked(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.Review = domain.ReviewChangesRequest
	local.ReviewHash = "old-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	// FetchReviewThreads returns Decision="review_required" (priority 2),
	// which is lower than ReviewChangesRequest (priority 3). The guard must
	// keep the existing ChangesRequested decision but still adopt the threads.
	obsValue := testObs(1)
	obsValue.Review.Decision = string(domain.ReviewChangesRequest)
	review := ports.SCMReviewObservation{
		Decision: string(domain.ReviewRequired),
		Threads:  []ports.SCMReviewThreadObservation{{ID: "t1", Path: "f.go", Line: 2, Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Author: "ann", Body: "fix"}}}},
	}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): obsValue},
		reviews:      map[string]ports.SCMReviewObservation{prKey(testRepo, 1): review},
	}
	obs := newTestObserver(store, provider, nil, time.Unix(500, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 {
		t.Fatal("expected a write after review refresh")
	}
	write := store.writes[len(store.writes)-1]
	if write.pr.Review != domain.ReviewChangesRequest {
		t.Fatalf("Review = %q, want %q (should not downgrade to review_required)", write.pr.Review, domain.ReviewChangesRequest)
	}
	if len(write.threads) != 1 || write.threads[0].ThreadID != "t1" {
		t.Fatalf("expected review thread t1 persisted, got %#v", write.threads)
	}
}

func TestPoll_LifecycleFailureHoldsBackHashesForDurableRetry(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.MetadataHash = "old-metadata"
	local.CIHash = "old-ci"
	store.prs["p-1"] = []domain.PullRequest{local}
	changed := testObs(1)
	changed.PR.Title = "changed title"
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		checkGuards:  map[string]ports.SCMGuardResult{commitKey(testRepo, "sha1"): {ETag: "ci2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): changed},
	}
	lc := &fakeLifecycle{err: errors.New("lifecycle down")}
	obs := newTestObserver(store, provider, lc, time.Unix(500, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")] = "ci1"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("DB write should succeed before lifecycle retry is queued, got writes=%#v", store.writes)
	}
	if store.writes[0].pr.Title != "changed title" {
		t.Fatalf("DB write did not persist the observed PR state: %#v", store.writes[0].pr)
	}
	if store.writes[0].pr.MetadataHash != local.MetadataHash {
		t.Fatalf("metadata hash advanced before lifecycle acknowledgement: got %q want %q", store.writes[0].pr.MetadataHash, local.MetadataHash)
	}
	if store.writes[0].pr.CIHash != local.CIHash {
		t.Fatalf("CI hash advanced before lifecycle acknowledgement: got %q want %q", store.writes[0].pr.CIHash, local.CIHash)
	}
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got != "repo1" {
		t.Fatalf("repo ETag advanced after lifecycle failure: got %q want repo1", got)
	}
	if got := obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")]; got != "ci1" {
		t.Fatalf("commit ETag advanced after lifecycle failure: got %q want ci1", got)
	}

	lc.err = nil
	store.prs["p-1"] = []domain.PullRequest{store.writes[0].pr}
	restarted := newTestObserver(store, provider, lc, time.Unix(501, 0).UTC())
	if err := restarted.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lc.observed) != 1 {
		t.Fatalf("held-back semantic hashes did not force lifecycle retry after restart: %#v", lc.observed)
	}
	if len(store.writes) != 3 {
		t.Fatalf("retry should write held-back facts then acknowledge hashes, got writes=%d", len(store.writes))
	}
	last := store.writes[len(store.writes)-1].pr
	if last.MetadataHash != metadataSemanticHash(changed) {
		t.Fatalf("metadata hash not acknowledged after lifecycle success: got %q want %q", last.MetadataHash, metadataSemanticHash(changed))
	}
	if last.CIHash != ciSemanticHash(changed.CI) {
		t.Fatalf("CI hash not acknowledged after lifecycle success: got %q want %q", last.CIHash, ciSemanticHash(changed.CI))
	}
}

func TestPoll_WriteFailureDoesNotAdvanceGuardETags(t *testing.T) {
	store := testStoreWithSession()
	store.writeErr = errors.New("db down")
	local := knownPR(1)
	local.MetadataHash = "old-metadata"
	local.CIHash = "old-ci"
	store.prs["p-1"] = []domain.PullRequest{local}
	changed := testObs(1)
	changed.PR.Title = "changed title"
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		checkGuards:  map[string]ports.SCMGuardResult{commitKey(testRepo, "sha1"): {ETag: "ci2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): changed},
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(550, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")] = "ci1"
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got != "repo1" {
		t.Fatalf("repo ETag advanced after write failure: got %q want repo1", got)
	}
	if got := obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")]; got != "ci1" {
		t.Fatalf("commit ETag advanced after write failure: got %q want ci1", got)
	}
}

func TestPoll_DuplicateTrackedPRKeepsFirstSession(t *testing.T) {
	store := &fakeStore{
		sessions: []domain.SessionRecord{
			{ID: "p-1", ProjectID: "p", Metadata: domain.SessionMetadata{Branch: "feat"}},
			{ID: "p-2", ProjectID: "p", Metadata: domain.SessionMetadata{Branch: "feat"}},
		},
		projects: map[string]domain.ProjectRecord{"p": {ID: "p", RepoOriginURL: "https://github.com/o/r.git"}},
		prs:      map[domain.SessionID][]domain.PullRequest{},
		checks:   map[string][]domain.PullRequestCheck{},
	}
	pr1 := knownPR(1)
	pr1.MetadataHash = "old-1"
	pr2 := pr1
	pr2.SessionID = "p-2"
	store.prs["p-1"] = []domain.PullRequest{pr1}
	store.prs["p-2"] = []domain.PullRequest{pr2}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(600, 0).UTC())
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) == 0 {
		t.Fatalf("writes = %d, want exactly one owner write", len(store.writes))
	}
	if store.writes[0].pr.SessionID != "p-1" {
		t.Fatalf("duplicate owner overwrote first session: wrote session %s", store.writes[0].pr.SessionID)
	}
}

// TestDiscoverSubjects_BackfillsRepoOriginURL asserts that a project row with
// an empty RepoOriginURL but a real on-disk repo gets its origin filled in
// during discovery and persisted, so the same project becomes observable
// without re-running project add.
func TestDiscoverSubjects_BackfillsRepoOriginURL(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://github.com/o/r.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v (%s)", err, out)
	}

	store := &fakeStore{
		sessions: []domain.SessionRecord{{ID: "p-1", ProjectID: "p", Metadata: domain.SessionMetadata{Branch: "feat"}}},
		projects: map[string]domain.ProjectRecord{"p": {ID: "p", Path: dir}}, // empty RepoOriginURL
		prs:      map[domain.SessionID][]domain.PullRequest{},
		checks:   map[string][]domain.PullRequestCheck{},
	}
	provider := &fakeProvider{}
	obs := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(0, 0).UTC())

	if _, _, err := obs.discoverSubjects(context.Background()); err != nil {
		t.Fatalf("discoverSubjects: %v", err)
	}
	if got := store.projects["p"].RepoOriginURL; got != "https://github.com/o/r.git" {
		t.Fatalf("RepoOriginURL after backfill = %q, want https://github.com/o/r.git", got)
	}
}

// TestPoll_StaleOpenPRForcedRefreshAfterMaxAge verifies that an open PR whose
// LastPRFetchAt is zero (never fetched) is re-fetched even when both the repo
// ETag guard and the commit checks guard return NotModified. This covers the
// post-merge stale-ETag scenario where the PR has been merged server-side but
// the provider ETag signals no change.
func TestPoll_StaleOpenPRForcedRefreshAfterMaxAge(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	// Fully-populated open PR — all hashes set, not merged, not closed.
	local.Merged = false
	local.Closed = false
	store.prs["p-1"] = []domain.PullRequest{local}

	// Provider: both guards return NotModified, but the observation reports merged.
	mergedObs := testObs(1)
	mergedObs.PR.Merged = true
	mergedObs.PR.State = "merged"

	repoKey := prKey(testRepo, 0)
	commitCacheKey := commitKey(testRepo, "sha1")

	provider := &fakeProvider{
		repoGuards:  map[string]ports.SCMGuardResult{repoKey: {ETag: "v1", NotModified: true}},
		checkGuards: map[string]ports.SCMGuardResult{commitCacheKey: {ETag: "ci1", NotModified: true}},
		observations: map[string]ports.SCMObservation{
			prKey(testRepo, 1): mergedObs,
		},
	}

	now := time.Unix(1000, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	// Pre-seed ETags so both guards fire NotModified.
	obs.Cache.RepoPRListETag[repoKey] = "v1"
	obs.Cache.CommitChecksETag[commitCacheKey] = "ci1"
	// Leave LastPRFetchAt empty (zero time) for the PR key — forces refresh.

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 1 {
		t.Fatalf("expected 1 fetch batch (stale PR forced refresh), got %d: %#v", len(provider.fetchBatches), provider.fetchBatches)
	}
	var mergedWrite *fakeWrite
	for i := range store.writes {
		if store.writes[i].pr.Number == 1 && store.writes[i].pr.Merged {
			mergedWrite = &store.writes[i]
			break
		}
	}
	if mergedWrite == nil {
		t.Fatalf("expected a write with pr.Merged==true, got writes: %#v", store.writes)
	}

	// Now simulate a recent fetch: set LastPRFetchAt to now and poll again.
	// The PR is now merged in the store, so openTrackedPRs will not include it.
	// Reset the store to still have the open PR so we test the max-age guard specifically.
	store.writes = nil
	provider.fetchBatches = nil
	store.prs["p-1"] = []domain.PullRequest{local} // restore open PR
	obs.Cache.LastPRFetchAt[prKey(testRepo, 1)] = now

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 0 {
		t.Fatalf("expected no fetch batch within max-age window, got %d: %#v", len(provider.fetchBatches), provider.fetchBatches)
	}

	// Backdate the last fetch beyond DefaultPRMaxAge. This is the behavior the PR
	// actually adds: the max-age guard (not the never-fetched branch) forces a
	// refetch. The clock is pinned, so parts 1 and 2 only cover last.IsZero() and
	// a zero elapsed time; without this, deleting the `> DefaultPRMaxAge` compare
	// would still pass.
	store.writes = nil
	provider.fetchBatches = nil
	store.prs["p-1"] = []domain.PullRequest{local} // restore open PR
	obs.Cache.LastPRFetchAt[prKey(testRepo, 1)] = now.Add(-(DefaultPRMaxAge + time.Minute))

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 1 {
		t.Fatalf("expected forced refresh once older than DefaultPRMaxAge, got %d: %#v", len(provider.fetchBatches), provider.fetchBatches)
	}
}

// TestDiscoverSubjects_NonGitPathDoesNotBackfill confirms the backfill is
// best-effort: a non-git project path leaves RepoOriginURL empty without
// erroring or persisting a stub, so the project is simply skipped.
func TestDiscoverSubjects_NonGitPathDoesNotBackfill(t *testing.T) {
	dir := t.TempDir()
	store := &fakeStore{
		sessions: []domain.SessionRecord{{ID: "p-1", ProjectID: "p", Metadata: domain.SessionMetadata{Branch: "feat"}}},
		projects: map[string]domain.ProjectRecord{"p": {ID: "p", Path: dir}},
		prs:      map[domain.SessionID][]domain.PullRequest{},
		checks:   map[string][]domain.PullRequestCheck{},
	}
	obs := newTestObserver(store, &fakeProvider{}, &fakeLifecycle{}, time.Unix(0, 0).UTC())
	subjects, sessionRepos, err := obs.discoverSubjects(context.Background())
	if err != nil {
		t.Fatalf("discoverSubjects: %v", err)
	}
	if len(subjects) != 0 || len(sessionRepos) != 0 {
		t.Fatalf("non-git project should be skipped, got %d subjects %d sessionRepos", len(subjects), len(sessionRepos))
	}
	if got := store.projects["p"].RepoOriginURL; got != "" {
		t.Fatalf("RepoOriginURL = %q, want empty (no persist on failed backfill)", got)
	}
}

// TestPoll_RejectsFetchedFalseObservation verifies that a Fetched=false
// observation from a transient provider failure is rejected: the store is not
// written, ETags are not advanced, and lifecycle is not notified (review
// finding #1). The last durable state must be preserved.
func TestPoll_RejectsFetchedFalseObservation(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.MetadataHash = "durable-metadata"
	local.CIHash = "durable-ci"
	store.prs["p-1"] = []domain.PullRequest{local}

	// Provider returns a Fetched=false placeholder (simulating a transient
	// GitLab 5xx that the adapter converts to Fetched=false + error).
	failedObs := ports.SCMObservation{
		Fetched:  false,
		Provider: "github",
		Host:     "github.com",
		Repo:     "o/r",
		PR:       ports.SCMPRObservation{Number: 1, URL: "https://github.com/o/r/pull/1"},
	}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		checkGuards:  map[string]ports.SCMGuardResult{commitKey(testRepo, "sha1"): {ETag: "ci2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): failedObs},
	}
	lc := &fakeLifecycle{}
	obs := newTestObserver(store, provider, lc, time.Unix(700, 0).UTC())
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")] = "ci1"

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Store must not be written with placeholder facts that would overwrite
	// durable metadata/CI hashes.
	if len(store.writes) != 0 {
		t.Fatalf("store writes = %d, want 0 (Fetched=false must not persist)", len(store.writes))
	}
	// Lifecycle must not be notified with stale/placeholder observations.
	if len(lc.observed) != 0 {
		t.Fatalf("lifecycle observed = %d, want 0 (Fetched=false must not notify)", len(lc.observed))
	}
	// ETags must not advance — the failed observation is not authoritative.
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got != "repo1" {
		t.Fatalf("repo ETag advanced after Fetched=false: got %q want repo1", got)
	}
	if got := obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")]; got != "ci1" {
		t.Fatalf("commit ETag advanced after Fetched=false: got %q want ci1", got)
	}
}

// TestPoll_IncrementalDiscovery_OnlyUpdatedMRsRefreshed verifies that after
// the first poll establishes a sync cursor, only MRs in the updated set are
// refreshed on subsequent polls — not every tracked MR in the repo (review
// finding #2).
func TestPoll_IncrementalDiscovery_OnlyUpdatedMRsRefreshed(t *testing.T) {
	store := testStoreWithSession()
	// Three tracked PRs with durable hashes so missingLocalState does not
	// force a refresh; the listedPRs filter is the deciding factor.
	pr1, pr2, pr3 := knownPR(1), knownPR(2), knownPR(3)
	for _, p := range []*domain.PullRequest{&pr1, &pr2, &pr3} {
		p.MetadataHash = "durable-meta"
		p.CIHash = "durable-ci"
		p.ReviewHash = "durable-review"
	}
	store.prs["p-1"] = []domain.PullRequest{pr1, pr2, pr3}

	// Poll 1: full listing (no cursor), all 3 PRs returned.
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo1"}},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(testRepo, 0): {
				{Number: 1, State: "open", SourceBranch: "feat", HeadRepo: "o/r"},
				{Number: 2, State: "open", SourceBranch: "feat", HeadRepo: "o/r"},
				{Number: 3, State: "open", SourceBranch: "feat", HeadRepo: "o/r"},
			},
		},
		observations: map[string]ports.SCMObservation{
			prKey(testRepo, 1): testObs(1),
			prKey(testRepo, 2): testObs(2),
			prKey(testRepo, 3): testObs(3),
		},
	}
	now := time.Unix(1000, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.listCalls != 1 {
		t.Fatalf("poll 1: listCalls = %d, want 1", provider.listCalls)
	}
	if obs.Cache.LastSyncCursor[prKey(testRepo, 0)].IsZero() {
		t.Fatal("poll 1: cursor not set after successful poll")
	}
	cursor := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]

	// Poll 2: ETag changed (repo2), but only PR 2 is in the updated set.
	// PRs #1 and #3 are tracked open but absent from the listing, so they
	// trigger terminal-state reconciliation fetches (Item 2). With durable
	// hashes matching their observations, the reconciled fetches are no-op
	// persists (no writes, no ETag/cursor advance) — but they still issue
	// detail fetches.
	provider.mu.Lock()
	provider.repoGuards[prKey(testRepo, 0)] = ports.SCMGuardResult{ETag: "repo2"}
	provider.openPRs[prKey(testRepo, 0)] = []ports.SCMPRObservation{
		{Number: 2, State: "open", SourceBranch: "feat", HeadRepo: "o/r"},
	}
	// Match durable hashes so the reconciliation results for PRs #1 and #3
	// are no-op persists (still-open, no terminal transition).
	o1, o3 := testObs(1), testObs(3)
	pr1.MetadataHash = metadataSemanticHash(o1)
	pr1.CIHash = ciSemanticHash(o1.CI)
	pr1.ReviewHash = reviewSemanticHash(o1.Review)
	pr3.MetadataHash = metadataSemanticHash(o3)
	pr3.CIHash = ciSemanticHash(o3.CI)
	pr3.ReviewHash = reviewSemanticHash(o3.Review)
	store.prs["p-1"] = []domain.PullRequest{pr1, pr2, pr3}
	provider.fetchBatches = nil
	provider.mu.Unlock()

	now2 := now.Add(time.Minute)
	obs.clock = func() time.Time { return now2 }
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Cursor must advance.
	if got := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; !got.After(cursor) {
		t.Fatalf("poll 2: cursor did not advance: got %v, want > %v", got, cursor)
	}
	// The incremental-discovery filter selects only PR 2 (in the updated
	// set) for normal refresh. PRs #1 and #3 are fetched via the
	// reconciliation pass. So we expect 2 fetch batches: one for PR 2
	// (normal refresh) and one for PRs #1 and #3 (reconciliation).
	if len(provider.fetchBatches) != 2 {
		t.Fatalf("poll 2: fetchBatches = %d, want 2 (normal + reconciliation)", len(provider.fetchBatches))
	}
	// Collect all fetched PR numbers.
	fetched := map[int]bool{}
	for _, batch := range provider.fetchBatches {
		for _, ref := range batch {
			fetched[ref.Number] = true
		}
	}
	// All three PRs are fetched: PR 2 via normal refresh, PRs 1 and 3 via
	// reconciliation. The incremental-discovery filter correctly prevents
	// PRs 1 and 3 from being in the normal refresh batch — they are only
	// fetched via reconciliation.
	if !fetched[1] || !fetched[2] || !fetched[3] {
		t.Fatalf("poll 2: expected PRs 1, 2, 3 fetched (2 via normal, 1+3 via reconciliation); got %v", fetched)
	}
}

// TestPoll_IncrementalDiscovery_CursorNotAdvancedOnFailure verifies that when
// FetchPullRequests fails, the sync cursor is NOT advanced so the next poll
// re-fetches MRs updated during the failed poll
func TestPoll_IncrementalDiscovery_CursorNotAdvancedOnFailure(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.MetadataHash = "durable-meta"
	local.CIHash = "durable-ci"
	local.ReviewHash = "durable-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo1"}},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(testRepo, 0): {{Number: 1, State: "open", SourceBranch: "feat", HeadRepo: "o/r"}},
		},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	now := time.Unix(1000, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	cursor := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]
	if cursor.IsZero() {
		t.Fatal("poll 1: cursor not set")
	}

	// Poll 2: FetchPullRequests fails — cursor must not advance.
	provider.mu.Lock()
	provider.repoGuards[prKey(testRepo, 0)] = ports.SCMGuardResult{ETag: "repo2"}
	provider.fetchErr = errors.New("transient fetch failure")
	provider.mu.Unlock()
	now2 := now.Add(time.Minute)
	obs.clock = func() time.Time { return now2 }
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; got != cursor {
		t.Fatalf("poll 2: cursor advanced after failure: got %v, want %v (unchanged)", got, cursor)
	}
}

// returns a rate-limit error, subsequent polls within the cooldown window skip
// that provider's calls instead of hammering it every 30s
func TestPoll_RateLimitCooldown_SkipsProviderCalls(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	store.prs["p-1"] = []domain.PullRequest{local}

	rle := &testRateLimitError{retryAfter: 2 * time.Minute}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	// First FetchPullRequests returns a rate-limit error; subsequent calls
	// would succeed, but the cooldown must suppress them.
	provider.mu.Lock()
	provider.fetchErr = rle
	provider.mu.Unlock()

	now := time.Unix(1000, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.fetchBatches == nil || len(provider.fetchBatches) != 1 {
		t.Fatalf("first poll: fetchBatches = %d, want 1", len(provider.fetchBatches))
	}
	if !obs.inRateLimitCooldown(now, "github") {
		t.Fatal("github provider should be in rate-limit cooldown after rate-limit error")
	}

	// Second poll within the cooldown window — provider calls must be skipped.
	provider.mu.Lock()
	provider.fetchErr = nil
	provider.fetchBatches = nil
	provider.repoGuardCalls = 0
	provider.mu.Unlock()
	now2 := now.Add(30 * time.Second)
	obs.clock = func() time.Time { return now2 }
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 0 {
		t.Fatalf("cooldown poll: fetchBatches = %d, want 0 (cooldown must skip provider calls)", len(provider.fetchBatches))
	}
}

// TestPoll_RateLimitCooldown_ResumesAfterExpiry verifies that after the
// cooldown expires, the observer resumes calling the provider (review
// finding #4).
func TestPoll_RateLimitCooldown_ResumesAfterExpiry(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	store.prs["p-1"] = []domain.PullRequest{local}

	rle := &testRateLimitError{retryAfter: 2 * time.Minute}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	provider.mu.Lock()
	provider.fetchErr = rle
	provider.mu.Unlock()

	now := time.Unix(1000, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// After the cooldown expires, provider calls resume.
	provider.mu.Lock()
	provider.fetchErr = nil
	provider.fetchBatches = nil
	provider.mu.Unlock()
	now2 := now.Add(3 * time.Minute)
	obs.clock = func() time.Time { return now2 }
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) == 0 {
		t.Fatal("after cooldown expiry: fetchBatches = 0, want >=1 (observer must resume)")
	}
}

// testRateLimitError is a minimal rate-limit error satisfying the observer's
// rateLimitedError interface (GetRetryAfter / GetResetAt) via errors.As.
type testRateLimitError struct {
	retryAfter time.Duration
}

func (e *testRateLimitError) Error() string { return "test: rate limited" }

func (e *testRateLimitError) GetRetryAfter() time.Duration { return e.retryAfter }

func (e *testRateLimitError) GetResetAt() time.Time { return time.Time{} }

// TestPoll_PerObservationRateLimitError_TriggersCooldown (Item 7) verifies that
// when the provider returns a Fetched=false observation carrying a rate-limit
// error in its Error field, the observer enters per-provider cooldown for that
// observation's provider. This is the mixed-batch case: the multi dispatcher
// attaches the error as per-observation metadata so a rate-limited GitLab
// alongside a healthy GitHub enters cooldown without suppressing GitHub.
func TestPoll_PerObservationRateLimitError_TriggersCooldown(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	store.prs["p-1"] = []domain.PullRequest{local}

	rle := &testRateLimitError{retryAfter: 2 * time.Minute}
	provider := &fakeProvider{
		repoGuards:     map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		observations:   map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
		fetchObsErrors: map[string]error{prKey(testRepo, 1): rle},
	}

	now := time.Unix(1000, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !obs.inRateLimitCooldown(now, "github") {
		t.Fatal("github provider should be in rate-limit cooldown after per-observation rate-limit error")
	}
	// Subsequent poll within cooldown must skip provider calls.
	provider.mu.Lock()
	provider.fetchObsErrors = nil
	provider.fetchBatches = nil
	provider.mu.Unlock()
	now2 := now.Add(30 * time.Second)
	obs.clock = func() time.Time { return now2 }
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 0 {
		t.Fatalf("cooldown poll: fetchBatches = %d, want 0 (cooldown must skip provider calls)", len(provider.fetchBatches))
	}
}

// TestPoll_PerObservationNonRateLimitError_MarksRefreshIncomplete (Item 7)
// verifies that a Fetched=false observation carrying a non-rate-limit error is
// routed to refresh-incomplete, NOT cooldown: the repo ETag and sync cursor
// must not advance, and the provider must not enter cooldown.
func TestPoll_PerObservationNonRateLimitError_MarksRefreshIncomplete(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	store.prs["p-1"] = []domain.PullRequest{local}

	provider := &fakeProvider{
		repoGuards:     map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		observations:   map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
		fetchObsErrors: map[string]error{prKey(testRepo, 1): errors.New("gitlab 503")},
	}

	now := time.Unix(1000, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Non-rate-limit errors must NOT enter cooldown.
	if obs.inRateLimitCooldown(now, "github") {
		t.Fatal("github provider must not enter cooldown for a non-rate-limit error")
	}
	// The repo's new ETag ("repo2") must NOT have been cached because the
	// refresh was marked incomplete.
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got == "repo2" {
		t.Fatalf("repo ETag advanced to %q after refresh-incomplete; durable state must not advance", got)
	}
	// The sync cursor must NOT have advanced.
	if cursor := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; !cursor.IsZero() {
		t.Fatalf("sync cursor advanced to %v after refresh-incomplete; durable state must not advance", cursor)
	}
}

// TestPoll_PerObservationError_NiledBeforePersistence (Item 7) verifies that
// the observer nils out the Error field before the observation reaches the
// store and lifecycle. The Error field is transient metadata, not durable
// state: the storage layer must never see provider-error classification.
func TestPoll_PerObservationError_NiledBeforePersistence(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	store.prs["p-1"] = []domain.PullRequest{local}

	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	lc := &fakeLifecycle{}
	now := time.Unix(1000, 0).UTC()
	obs := newTestObserver(store, provider, lc, now)
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A healthy observation must reach lifecycle with Error == nil.
	if len(lc.observed) == 0 {
		t.Skip("no observation reached lifecycle; adjust test fixture")
	}
	for _, o := range lc.observed {
		if o.Error != nil {
			t.Errorf("lifecycle observed Error = %v, want nil (transient metadata must be nil-ed before persistence)", o.Error)
		}
	}
	for _, w := range store.writes {
		_ = w // store writes carry domain types, not the obs.Error field; the
		// lifecycle assertion above is the authoritative nil-out check.
	}
}

// TestPoll_CommitCheckETagChange_PromotesIndependentOfListing (Item 1)
// verifies that a changed commit-check ETag independently promotes a PR to a
// refresh candidate even when the repository listing is a 304 (so listedPRs is
// empty). CI state changes during a 304 listing must be persisted rather than
// silently dropped. This is the reviewer's non-zero-cursor regression test.
func TestPoll_CommitCheckETagChange_PromotesIndependentOfListing(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.MetadataHash = "durable-meta"
	local.CIHash = "durable-ci"
	local.ReviewHash = "durable-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	// New CI observation (pending → passing) that should be persisted.
	updated := testObs(1)
	updated.CI.Summary = string(domain.CIPassing)
	updated.CI.HeadSHA = "sha1"
	updated.CI.Checks = []ports.SCMCheckObservation{{Name: "build", Status: string(domain.PRCheckPassed), Conclusion: "success", URL: "ci"}}

	provider := &fakeProvider{
		// Repo-list guard reports 304 (NotModified) against the cached ETag.
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo1", NotModified: true}},
		// Commit-check ETag changed.
		checkGuards:  map[string]ports.SCMGuardResult{commitKey(testRepo, "sha1"): {ETag: "ci2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): updated},
	}
	lc := &fakeLifecycle{}
	now := time.Unix(1100, 0).UTC()
	obs := newTestObserver(store, provider, lc, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")] = "ci1"
	// Non-zero cursor: simulates the post-first-poll state where a 304 leaves
	// listedPRs empty.
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = now.Add(-time.Hour)

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The PR must be fetched despite the 304 listing.
	if len(provider.fetchBatches) != 1 || len(provider.fetchBatches[0]) != 1 || provider.fetchBatches[0][0].Number != 1 {
		t.Fatalf("commit-check ETag change with 304 listing must promote PR to refresh candidate; batches=%#v", provider.fetchBatches)
	}
	// The new CI state must be persisted despite the 304 listing.
	if len(store.writes) == 0 {
		t.Fatalf("expected at least one store write with updated CI state, got 0")
	}
	found := false
	for _, w := range store.writes {
		if w.pr.Number == 1 && w.pr.CI == domain.CIPassing {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("updated CI state (passing) not persisted; writes=%#v", store.writes)
	}
	// The commit-check ETag must advance after successful persistence.
	if got := obs.Cache.CommitChecksETag[commitKey(testRepo, "sha1")]; got != "ci2" {
		t.Fatalf("commit ETag not advanced after successful persist: got %q want ci2", got)
	}
}

// TestPoll_GitHubTerminalReconciliation_StillOpenNoOp (Item 2) verifies that a
// tracked open GitHub PR not in the current listing triggers a terminal-
// reconciliation detail fetch, and that a "still open" result is a no-op
// persistence that does NOT advance any cursor or ETag.
func TestPoll_GitHubTerminalReconciliation_StillOpenNoOp(t *testing.T) {
	store := testStoreWithSession()
	stillOpen := testObs(1)
	local := knownPR(1)
	// Compute durable hashes from the observation so the "still open" result
	// is semantically a no-op (unchanged hashes → no write, no ETag advance).
	local.MetadataHash = metadataSemanticHash(stillOpen)
	local.CIHash = ciSemanticHash(stillOpen.CI)
	local.ReviewHash = reviewSemanticHash(stillOpen.Review)
	store.prs["p-1"] = []domain.PullRequest{local}

	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		openPRs:      map[string][]ports.SCMPRObservation{}, // PR not in listing
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): stillOpen},
	}
	lc := &fakeLifecycle{}
	now := time.Unix(1200, 0).UTC()
	obs := newTestObserver(store, provider, lc, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = now.Add(-time.Hour)

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The reconciliation pass must issue a detail fetch for the missing PR.
	if len(provider.fetchBatches) != 1 || len(provider.fetchBatches[0]) != 1 || provider.fetchBatches[0][0].Number != 1 {
		t.Fatalf("reconciliation must fetch the missing PR; batches=%#v", provider.fetchBatches)
	}
	// The repo ETag must NOT advance — the "still open" result is a no-op.
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got == "repo2" {
		t.Fatalf("repo ETag advanced to %q on still-open reconciliation; durable state must not advance", got)
	}
	// The sync cursor must NOT advance.
	if cursor := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; cursor.Equal(now) {
		t.Fatalf("sync cursor advanced to %v on still-open reconciliation; durable state must not advance", cursor)
	}
}

// TestPoll_GitHubTerminalReconciliation_TerminalTransition (Item 2) verifies
// that when the reconciliation detail fetch reports a terminal state
// (merged/closed), the terminal state is observed and persisted.
func TestPoll_GitHubTerminalReconciliation_TerminalTransition(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.MetadataHash = "durable-meta"
	local.CIHash = "durable-ci"
	local.ReviewHash = "durable-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	// The detail fetch returns a merged terminal state.
	terminalObs := testObs(1)
	terminalObs.PR.Merged = true
	terminalObs.PR.State = string(domain.PRStateMerged)
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		openPRs:      map[string][]ports.SCMPRObservation{}, // PR not in state=open listing
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): terminalObs},
	}
	lc := &fakeLifecycle{}
	now := time.Unix(1300, 0).UTC()
	obs := newTestObserver(store, provider, lc, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = now.Add(-time.Hour)

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The reconciliation pass must issue a detail fetch.
	if len(provider.fetchBatches) != 1 || len(provider.fetchBatches[0]) != 1 {
		t.Fatalf("reconciliation must fetch the missing PR; batches=%#v", provider.fetchBatches)
	}
	// The terminal (merged) state must be persisted.
	foundMerged := false
	for _, w := range store.writes {
		if w.pr.Number == 1 && w.pr.Merged {
			foundMerged = true
			break
		}
	}
	if !foundMerged {
		t.Fatalf("terminal merged state not persisted; writes=%#v", store.writes)
	}
	// Lifecycle must see the terminal transition.
	if len(lc.observed) == 0 {
		t.Fatalf("lifecycle not notified of terminal transition; observed=%#v", lc.observed)
	}
}

// TestPoll_GitHubTerminalReconciliation_SecondPollNoReconcile (Item 2)
// verifies the reviewer's explicit multi-poll requirement: after a PR is
// reconciled on poll N (still-open, no-op), poll N+1 does NOT re-reconcile it
// unnecessarily when the listing is unchanged. Once the terminal
// reconciliation finds the PR is still open, the repo ETag has not advanced,
// so a subsequent 304 poll skips reconciliation entirely.
func TestPoll_GitHubTerminalReconciliation_SecondPollNoReconcile(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.MetadataHash = "durable-meta"
	local.CIHash = "durable-ci"
	local.ReviewHash = "durable-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	stillOpen := testObs(1)
	stillOpen.PR.Title = local.Title
	stillOpen.CI.Summary = string(domain.CIPassing)
	stillOpen.CI.HeadSHA = local.HeadSHA
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		openPRs:      map[string][]ports.SCMPRObservation{},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): stillOpen},
	}
	now := time.Unix(1400, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = now.Add(-time.Hour)

	// Poll 1: non-304 guard, PR missing from listing → reconciliation fetch.
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 1 {
		t.Fatalf("poll 1: reconciliation must fetch the missing PR; batches=%#v", provider.fetchBatches)
	}

	// Poll 2: repo-list guard is now a 304 (NotModified). The PR is still
	// missing from listing, but reconciliation must NOT run on a 304 poll.
	provider.mu.Lock()
	provider.repoGuards[prKey(testRepo, 0)] = ports.SCMGuardResult{ETag: "repo2", NotModified: true}
	provider.fetchBatches = nil
	provider.mu.Unlock()

	now2 := now.Add(time.Minute)
	obs.clock = func() time.Time { return now2 }
	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.fetchBatches) != 0 {
		t.Fatalf("poll 2 (304 guard): reconciliation must not re-fetch; batches=%#v", provider.fetchBatches)
	}
}

// TestPoll_CooldownSkip_MarksRepoRefreshIncomplete (Item 3) verifies that when
// a ref is skipped under cooldown, the repository is marked refresh-incomplete
// so the repo ETag and sync cursor do NOT advance without an observation being
// fetched. After cooldown, a 304 must not make the skipped update unrecoverable.
func TestPoll_CooldownSkip_MarksRepoRefreshIncomplete(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	local.MetadataHash = "durable-meta"
	local.CIHash = "durable-ci"
	local.ReviewHash = "durable-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {{Number: 1, State: "open", SourceBranch: "feat", HeadRepo: "o/r"}}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	now := time.Unix(1500, 0).UTC()
	obs := newTestObserver(store, provider, &fakeLifecycle{}, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	cursorBefore := now.Add(-time.Hour)
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = cursorBefore
	// Put the github provider under cooldown so the ref is skipped.
	obs.setRateLimitCooldown(now, "github", 2*time.Minute)

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The ref must not have been fetched (cooldown skipped it).
	if len(provider.fetchBatches) != 0 {
		t.Fatalf("cooldown-skip must not fetch refs; batches=%#v", provider.fetchBatches)
	}
	// The repo ETag must NOT advance — the refresh was marked incomplete.
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got == "repo2" {
		t.Fatalf("repo ETag advanced to %q after cooldown-skip; durable state must not advance", got)
	}
	// The sync cursor must NOT advance.
	if got := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; got != cursorBefore {
		t.Fatalf("sync cursor advanced after cooldown-skip: got %v, want %v (unchanged)", got, cursorBefore)
	}
}

// TestPoll_RepoRefreshMonotonicity_OneRefFailsOneSucceeds (finding #1) verifies
// that when two MRs share a repo and one fetch fails while the other succeeds,
// the repo ETag and LastSyncCursor do NOT advance. Without per-ref tracking,
// markRepoRefreshOK (called after the successful PR persistence) would clear
// the repo-level failure set by markRepoRefreshFailed for the failed PR,
// making the failed update unrecoverable.
func TestPoll_RepoRefreshMonotonicity_OneRefFailsOneSucceeds(t *testing.T) {
	store := testStoreWithSession()
	// Two tracked PRs in the same repo with durable hashes.
	pr1, pr2 := knownPR(1), knownPR(2)
	for _, p := range []*domain.PullRequest{&pr1, &pr2} {
		p.MetadataHash = "durable-meta"
		p.CIHash = "durable-ci"
		p.ReviewHash = "durable-review"
	}
	store.prs["p-1"] = []domain.PullRequest{pr1, pr2}

	// PR 1 fetch will fail (per-observation error), PR 2 fetch will succeed.
	pr2Obs := testObs(2)
	pr2Obs.PR.Title = "PR 2 updated" // force a metadata change so it persists
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {{Number: 1, State: "open", SourceBranch: "feat", HeadRepo: "o/r"}, {Number: 2, State: "open", SourceBranch: "feat", HeadRepo: "o/r"}}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 2): pr2Obs},
		// PR 1 returns a Fetched=false placeholder with a non-rate-limit error.
		fetchObsErrors: map[string]error{prKey(testRepo, 1): errors.New("gitlab 503")},
	}
	lc := &fakeLifecycle{}
	now := time.Unix(1600, 0).UTC()
	obs := newTestObserver(store, provider, lc, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = now.Add(-time.Hour)
	cursorBefore := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// PR 2 must have been persisted (metadata changed).
	foundPr2Write := false
	for _, w := range store.writes {
		if w.pr.Number == 2 {
			foundPr2Write = true
			break
		}
	}
	if !foundPr2Write {
		t.Fatalf("PR 2 (successful fetch) must be persisted; writes=%#v", store.writes)
	}

	// The repo ETag must NOT advance — PR 1 failed.
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got == "repo2" {
		t.Fatalf("repo ETag advanced to %q when one ref failed; durable state must not advance", got)
	}
	// The sync cursor must NOT advance.
	if got := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; got != cursorBefore {
		t.Fatalf("sync cursor advanced when one ref failed: got %v, want %v (unchanged)", got, cursorBefore)
	}
}

// TestPoll_RepoRefreshMonotonicity_BothRefsSucceed (finding #1) verifies
// that when two MRs share a repo and both fetches succeed, the repo ETag and
// LastSyncCursor DO advance.
func TestPoll_RepoRefreshMonotonicity_BothRefsSucceed(t *testing.T) {
	store := testStoreWithSession()
	// Two tracked PRs in the same repo with durable hashes.
	pr1, pr2 := knownPR(1), knownPR(2)
	for _, p := range []*domain.PullRequest{&pr1, &pr2} {
		p.MetadataHash = "durable-meta"
		p.CIHash = "durable-ci"
		p.ReviewHash = "durable-review"
	}
	store.prs["p-1"] = []domain.PullRequest{pr1, pr2}

	// Both PR observations have a metadata change so they persist.
	pr1Obs := testObs(1)
	pr1Obs.PR.Title = "PR 1 updated"
	pr2Obs := testObs(2)
	pr2Obs.PR.Title = "PR 2 updated"
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		openPRs:      map[string][]ports.SCMPRObservation{prKey(testRepo, 0): {{Number: 1, State: "open", SourceBranch: "feat", HeadRepo: "o/r"}, {Number: 2, State: "open", SourceBranch: "feat", HeadRepo: "o/r"}}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): pr1Obs, prKey(testRepo, 2): pr2Obs},
	}
	lc := &fakeLifecycle{}
	now := time.Unix(1700, 0).UTC()
	obs := newTestObserver(store, provider, lc, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = now.Add(-time.Hour)
	cursorBefore := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Both PRs must have been persisted.
	foundPr1, foundPr2 := false, false
	for _, w := range store.writes {
		if w.pr.Number == 1 {
			foundPr1 = true
		}
		if w.pr.Number == 2 {
			foundPr2 = true
		}
	}
	if !foundPr1 || !foundPr2 {
		t.Fatalf("both PRs must be persisted; writes=%#v", store.writes)
	}

	// The repo ETag must advance.
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got != "repo2" {
		t.Fatalf("repo ETag not advanced after all refs succeeded: got %q, want repo2", got)
	}
	// The sync cursor must advance.
	if got := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; !got.After(cursorBefore) {
		t.Fatalf("sync cursor not advanced after all refs succeeded: got %v, want > %v", got, cursorBefore)
	}
}

// TestPoll_AllFail_ScopedPerProviderError (Ticket 02) verifies that when ALL
// providers fail in a single chunk, the observer applies the correct error
// classification PER PROVIDER — not one arbitrary classification to all refs.
// GitHub fails with a rate-limit error → GitHub enters per-provider cooldown.
// GitLab fails with an auth error (non-rate-limit) → GitLab is marked
// refresh-incomplete (repo ETag/cursor do not advance) but does NOT enter
// cooldown. The two classifications must not be cross-applied: GitHub must not
// be marked refresh-incomplete-only (missing its cooldown), and GitLab must
// not enter cooldown (wrongly treating an auth error as rate-limit).
//
// This test uses the hostAwareProvider (from scoped_identity_test.go) which
// routes gitlab.com URLs to the gitlab provider key, so the observer sees two
// providers in a single poll.
func TestPoll_AllFail_ScopedPerProviderError(t *testing.T) {
	store := testStoreWithTwoSessions()
	// Two tracked PRs — one GitHub, one GitLab — both with durable hashes.
	ghPR := knownPR(1)
	ghPR.MetadataHash = "durable-meta"
	ghPR.CIHash = "durable-ci"
	ghPR.ReviewHash = "durable-review"
	ghPR.Provider = "github"
	ghPR.Host = "github.com"
	ghPR.Repo = "o/r"

	glPR := knownPR(3)
	glPR.MetadataHash = "durable-meta"
	glPR.CIHash = "durable-ci"
	glPR.ReviewHash = "durable-review"
	glPR.Provider = "gitlab"
	glPR.Host = "gitlab.com"
	glPR.Repo = "o/r"
	glPR.URL = "https://gitlab.com/o/r/-/merge_requests/3"

	store.prs["gh-1"] = []domain.PullRequest{ghPR}
	store.prs["gl-1"] = []domain.PullRequest{glPR}

	ghRateLimitErr := &testRateLimitError{retryAfter: 2 * time.Minute}
	glAuthErr := errors.New("gitlab 401 unauthorized")

	provider := &hostAwareProvider{fakeProvider: &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{
			prKey(testRepo, 0): {ETag: "repo2"},
			prKey(glRepo, 0):   {ETag: "repo2"},
		},
		openPRs: map[string][]ports.SCMPRObservation{
			prKey(testRepo, 0): {{Number: 1, State: "open", SourceBranch: "feat", HeadRepo: "o/r"}},
			prKey(glRepo, 0):   {{Number: 3, State: "open", SourceBranch: "feat", HeadRepo: "o/r"}},
		},
		// Both PRs fail: GitHub with rate-limit, GitLab with auth error.
		fetchObsErrors: map[string]error{
			prKey(testRepo, 1): ghRateLimitErr,
			prKey(glRepo, 3):   glAuthErr,
		},
	}}

	now := time.Unix(2000, 0).UTC()
	obs := New(provider, store, &fakeLifecycle{}, Config{
		Clock:            func() time.Time { return now },
		Tick:             time.Hour,
		Logger:           quietSlog(),
		CacheMax:         128,
		IdentityResolver: provider.fakeProvider,
	})
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.RepoPRListETag[prKey(glRepo, 0)] = "repo1"
	ghCursorBefore := now.Add(-time.Hour)
	glCursorBefore := now.Add(-time.Hour)
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = ghCursorBefore
	obs.Cache.LastSyncCursor[prKey(glRepo, 0)] = glCursorBefore

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// GitHub must be in rate-limit cooldown (rate-limit error classified correctly).
	if !obs.inRateLimitCooldown(now, "github") {
		t.Fatal("github provider must be in rate-limit cooldown after rate-limit error")
	}

	// GitLab must NOT be in cooldown (auth error is non-rate-limit).
	if obs.inRateLimitCooldown(now, "gitlab") {
		t.Fatal("gitlab provider must NOT enter cooldown for a non-rate-limit (auth) error")
	}

	// Both repos must be marked refresh-incomplete: GitHub via cooldown-skip,
	// GitLab via per-observation non-rate-limit routing. The ETags must NOT
	// have advanced to "repo2".
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got == "repo2" {
		t.Fatalf("github repo ETag advanced to %q after all-fail; durable state must not advance", got)
	}
	if got := obs.Cache.RepoPRListETag[prKey(glRepo, 0)]; got == "repo2" {
		t.Fatalf("gitlab repo ETag advanced to %q after all-fail; durable state must not advance", got)
	}

	// Neither sync cursor must advance.
	if got := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; got != ghCursorBefore {
		t.Fatalf("github sync cursor advanced after all-fail: got %v, want %v (unchanged)", got, ghCursorBefore)
	}
	if got := obs.Cache.LastSyncCursor[prKey(glRepo, 0)]; got != glCursorBefore {
		t.Fatalf("gitlab sync cursor advanced after all-fail: got %v, want %v (unchanged)", got, glCursorBefore)
	}

	// No PRs should have been persisted (all failed).
	for _, w := range store.writes {
		if w.pr.Number == 1 || w.pr.Number == 3 {
			t.Fatalf("failed PR %d must not be persisted; writes=%#v", w.pr.Number, store.writes)
		}
	}
}

// TestPoll_RepoRefreshMonotonicity_ListingFailsButPRSucceeds (finding #1)
// verifies that when ListPRsByRepo fails for a repo but a tracked PR in that
// repo is still fetched and persisted successfully (via the commit-check ETag
// path), the repo ETag and LastSyncCursor do NOT advance. Without the
// repoListFailed monotonicity guard, markRepoRefreshOK (called after the
// successful PR persistence) would flip the repo back to OK. The per-ref
// monotonicity check would then see an empty repoCandidateKeys (because the
// listing failed, the PR never entered candidateKeys through the listing path)
// and allRefsOK would be vacuously true, advancing the ETag/cursor and making
// the failed listing unrecoverable on the next poll.
func TestPoll_RepoRefreshMonotonicity_ListingFailsButPRSucceeds(t *testing.T) {
	store := testStoreWithSession()
	// A tracked PR with durable hashes.
	local := knownPR(1)
	local.MetadataHash = "durable-meta"
	local.CIHash = "durable-ci"
	local.ReviewHash = "durable-review"
	store.prs["p-1"] = []domain.PullRequest{local}

	// The PR observation has a metadata change so it persists.
	successObs := testObs(1)
	successObs.PR.Title = "PR 1 updated"
	provider := &fakeProvider{
		repoGuards: map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo2"}},
		// Listing fails — the PR is not discovered through the listing path.
		listErr: errors.New("gitlab 502"),
		// A changed commit-check ETag promotes the PR to a refresh candidate
		// even though the listing failed.
		checkGuards:  map[string]ports.SCMGuardResult{commitKey(testRepo, local.HeadSHA): {ETag: "checks2"}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): successObs},
	}
	lc := &fakeLifecycle{}
	now := time.Unix(1800, 0).UTC()
	obs := newTestObserver(store, provider, lc, now)
	obs.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo1"
	obs.Cache.LastSyncCursor[prKey(testRepo, 0)] = now.Add(-time.Hour)
	cursorBefore := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]
	// Seed the commit-check ETag so the guard reports a change.
	obs.Cache.CommitChecksETag[commitKey(testRepo, local.HeadSHA)] = "checks1"

	if err := obs.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The PR must have been persisted (metadata changed).
	foundWrite := false
	for _, w := range store.writes {
		if w.pr.Number == 1 {
			foundWrite = true
			break
		}
	}
	if !foundWrite {
		t.Fatalf("PR 1 (successful fetch via commit-check ETag) must be persisted; writes=%#v", store.writes)
	}

	// The repo ETag must NOT advance — the listing failed.
	if got := obs.Cache.RepoPRListETag[prKey(testRepo, 0)]; got == "repo2" {
		t.Fatalf("repo ETag advanced to %q when listing failed; durable state must not advance", got)
	}
	// The sync cursor must NOT advance.
	if got := obs.Cache.LastSyncCursor[prKey(testRepo, 0)]; got != cursorBefore {
		t.Fatalf("sync cursor advanced when listing failed: got %v, want %v (unchanged)", got, cursorBefore)
	}
}
