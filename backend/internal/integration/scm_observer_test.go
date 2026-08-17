// This file is the end-to-end regression guard for the SCM observer lane wired
// in PR #114 (issue #108). It wires a real sqlite.Store, a real lifecycle.Manager
// with a recording messenger spy, and a canned observe/scm.Provider into the
// real observe/scm.Observer, then drives Observer.Poll directly (never the
// ticker) to assert the full observation -> reducer -> store -> messenger path.
// Provider/store/lifecycle unit coverage already live in their own packages;
// this file's job is to catch wiring regressions only an integration view can
// see — e.g. a nil messenger, a wrong RepoOriginURL plumbing, or a dedup
// signature that does not persist across polls.
package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	scmobserve "github.com/aoagents/agent-orchestrator/backend/internal/observe/scm"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

var scmTestRepo = ports.SCMRepo{
	Provider: "github",
	Host:     "github.com",
	Owner:    "octocat",
	Name:     "hello",
	Repo:     "octocat/hello",
}

const scmTestOriginURL = "https://github.com/octocat/hello.git"

// scmMessengerSpy is a minimal lifecycle.messenger that records every nudge so
// tests can assert exactly which lifecycle reactions fired and what they sent.
type scmMessengerSpy struct {
	mu   sync.Mutex
	sent []scmCapturedNudge
}

type scmCapturedNudge struct {
	session domain.SessionID
	body    string
}

func (s *scmMessengerSpy) Send(_ context.Context, id domain.SessionID, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, scmCapturedNudge{session: id, body: msg})
	return nil
}

func (s *scmMessengerSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *scmMessengerSpy) snapshot() []scmCapturedNudge {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]scmCapturedNudge(nil), s.sent...)
}

// cannedSCMProvider satisfies observe/scm.Provider with hand-built observations
// keyed by branch (for ListOpenPRsByRepo) and by PR number (for everything else,
// since every test case uses scmTestRepo). It is the integration-package analog
// of observer_test.go's fakeProvider: the SCM adapter has its own httptest-based
// coverage, so this test holds the provider constant and exercises every other
// layer end-to-end.
type cannedSCMProvider struct {
	mu sync.Mutex

	parsedRepo   ports.SCMRepo
	detected     map[string]ports.SCMPRObservation
	observations map[int]ports.SCMObservation
	reviews      map[int]ports.SCMReviewObservation
}

func newCannedSCMProvider() *cannedSCMProvider {
	return &cannedSCMProvider{
		parsedRepo:   scmTestRepo,
		detected:     map[string]ports.SCMPRObservation{},
		observations: map[int]ports.SCMObservation{},
		reviews:      map[int]ports.SCMReviewObservation{},
	}
}

func (p *cannedSCMProvider) ParseRepository(remote string) (ports.SCMRepo, bool) {
	if strings.TrimSpace(remote) == "" {
		return ports.SCMRepo{}, false
	}
	return p.parsedRepo, true
}

func (p *cannedSCMProvider) RepoPRListGuard(_ context.Context, _ ports.SCMRepo, _ string) (ports.SCMGuardResult, error) {
	return ports.SCMGuardResult{ETag: "repo-etag"}, nil
}

func (p *cannedSCMProvider) ListPRsByRepo(_ context.Context, _ ports.SCMRepo, _ time.Time) ([]ports.SCMPRObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ports.SCMPRObservation, 0, len(p.detected))
	for _, pr := range p.detected {
		out = append(out, pr)
	}
	return out, nil
}

func (p *cannedSCMProvider) CommitChecksGuard(_ context.Context, _ ports.SCMRepo, _, _ string) (ports.SCMGuardResult, error) {
	return ports.SCMGuardResult{ETag: "commit-etag"}, nil
}

func (p *cannedSCMProvider) FetchPullRequests(_ context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ports.SCMObservation, 0, len(refs))
	for _, ref := range refs {
		if obs, ok := p.observations[ref.Number]; ok {
			out = append(out, obs)
		}
	}
	return out, nil
}

func (p *cannedSCMProvider) FetchFailedCheckLogTail(_ context.Context, _ ports.SCMRepo, _ ports.SCMCheckObservation) (string, error) {
	// Observations in this test always carry their LogTail inline, so the
	// observer's failed-log enrichment short-circuits without calling here.
	// Returning the empty string keeps the contract honest if a future case
	// drops the inline tail.
	return "", nil
}

func (p *cannedSCMProvider) FetchReviewThreads(_ context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reviews[ref.Number], nil
}

// scmFixture bundles the live collaborators a single SCM observer scenario
// needs. Every test case constructs its own fixture against a fresh tmpdir DB
// so writes/lifecycle/messenger state never leak between cases.
type scmFixture struct {
	store    *sqlite.Store
	lcm      *lifecycle.Manager
	spy      *scmMessengerSpy
	provider *cannedSCMProvider
	observer *scmobserve.Observer
	session  domain.SessionRecord
	now      time.Time
}

type lifecycleMarkTerminator struct {
	lcm *lifecycle.Manager
}

func (t lifecycleMarkTerminator) Kill(ctx context.Context, id domain.SessionID) (bool, error) {
	return true, t.lcm.MarkTerminated(ctx, id)
}

type failOnceTerminator struct {
	lcm   *lifecycle.Manager
	calls int
}

func (t *failOnceTerminator) Kill(ctx context.Context, id domain.SessionID) (bool, error) {
	t.calls++
	if t.calls == 1 {
		return false, errors.New("transient runtime teardown failure")
	}
	return true, t.lcm.MarkTerminated(ctx, id)
}

func newSCMFixture(t *testing.T, branch string) *scmFixture {
	t.Helper()
	ctx := context.Background()

	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:            "octo",
		Path:          t.TempDir(),
		DisplayName:   "octo",
		RepoOriginURL: scmTestOriginURL,
		RegisteredAt:  now,
	}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	sess, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID:    "octo",
		Kind:         domain.KindWorker,
		Metadata:     domain.SessionMetadata{Branch: branch, WorkspacePath: "/ws/octo"},
		AutoInjectCI: true,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	spy := &scmMessengerSpy{}
	lcm := lifecycle.New(store, spy)
	lcm.SetCompletionTerminator(lifecycleMarkTerminator{lcm: lcm})
	provider := newCannedSCMProvider()
	observer := scmobserve.New(provider, store, lcm, scmobserve.Config{
		Tick:   time.Hour,
		Clock:  func() time.Time { return now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &scmFixture{
		store:    store,
		lcm:      lcm,
		spy:      spy,
		provider: provider,
		observer: observer,
		session:  sess,
		now:      now,
	}
}

func (f *scmFixture) enableTerminateOnPRMerge(t *testing.T) {
	t.Helper()
	ok, err := f.store.SetSessionTerminateOnPRMerge(context.Background(), f.session.ID, true, f.now)
	if err != nil || !ok {
		t.Fatalf("enable terminate-on-merge: ok=%v err=%v", ok, err)
	}
	f.session.TerminateOnPRMerge = true
}

func failingSCMObservation(prURL string, num int, headSHA, logTail string) ports.SCMObservation {
	failed := ports.SCMCheckObservation{
		Name:       "build",
		Status:     string(domain.PRCheckFailed),
		Conclusion: "failure",
		URL:        "https://github.com/octocat/hello/runs/9001",
		ProviderID: "9001",
		LogTail:    logTail,
	}
	return ports.SCMObservation{
		Fetched:  true,
		Provider: "github", Host: "github.com", Repo: "octocat/hello",
		PR: ports.SCMPRObservation{
			URL:          prURL,
			HTMLURL:      prURL,
			Number:       num,
			State:        string(domain.PRStateOpen),
			SourceBranch: "feat/x",
			TargetBranch: "main",
			HeadSHA:      headSHA,
			Title:        "Found a bug",
		},
		CI: ports.SCMCIObservation{
			Summary:           string(domain.CIFailing),
			HeadSHA:           headSHA,
			FailedFingerprint: "fp-build",
			Checks:            []ports.SCMCheckObservation{failed},
			FailedChecks:      []ports.SCMCheckObservation{failed},
			FailureLogTail:    logTail,
		},
		Review:       ports.SCMReviewObservation{Decision: string(domain.ReviewNone)},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeBlocked), Blockers: []string{"ci_failing"}},
	}
}

func mergedSCMObservation(prURL string, num int, headSHA string) ports.SCMObservation {
	return ports.SCMObservation{
		Fetched:  true,
		Provider: "github", Host: "github.com", Repo: "octocat/hello",
		PR: ports.SCMPRObservation{
			URL:          prURL,
			HTMLURL:      prURL,
			Number:       num,
			State:        string(domain.PRStateMerged),
			Merged:       true,
			SourceBranch: "feat/x",
			TargetBranch: "main",
			HeadSHA:      headSHA,
			Title:        "Ship it",
		},
		CI:           ports.SCMCIObservation{Summary: string(domain.CIPassing), HeadSHA: headSHA},
		Review:       ports.SCMReviewObservation{Decision: string(domain.ReviewApproved)},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable), Mergeable: true},
	}
}

// TestSCMObserverEndToEnd is the wiring regression guard for issue #109. It
// drives Observer.Poll against a real sqlite.Store + real lifecycle.Manager so
// the observation -> reducer -> store -> messenger pipeline the daemon runs in
// production stays connected end-to-end after PR #114.
func TestSCMObserverEndToEnd(t *testing.T) {
	t.Run("CI failing observation persists rows, nudges once, and is idempotent on re-poll", func(t *testing.T) {
		ctx := context.Background()
		f := newSCMFixture(t, "feat/x")
		const (
			prURL   = "https://github.com/octocat/hello/pull/42"
			headSHA = "deadbeef"
			logTail = "setup\nsetup\nFAILED: build broke\n"
		)
		f.provider.detected["feat/x"] = ports.SCMPRObservation{
			URL: prURL, Number: 42, SourceBranch: "feat/x", HeadRepo: scmTestRepo.Repo, TargetBranch: "main", HeadSHA: headSHA,
		}
		f.provider.observations[42] = failingSCMObservation(prURL, 42, headSHA, logTail)

		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}

		// PR row reflects the observation: provider-neutral identity columns,
		// failing CI roll-up, and persisted semantic hashes.
		pr, ok, err := f.store.GetPR(ctx, prURL)
		if err != nil || !ok {
			t.Fatalf("GetPR after Poll: ok=%v err=%v", ok, err)
		}
		if pr.SessionID != f.session.ID {
			t.Fatalf("PR.SessionID = %q, want %q", pr.SessionID, f.session.ID)
		}
		if pr.Number != 42 || pr.HeadSHA != headSHA {
			t.Fatalf("PR identity wrong: %+v", pr)
		}
		if pr.Provider != "github" || pr.Host != "github.com" || pr.Repo != "octocat/hello" {
			t.Fatalf("provider-neutral columns wrong: %+v", pr)
		}
		if pr.CI != domain.CIFailing {
			t.Fatalf("PR.CI = %q, want %q", pr.CI, domain.CIFailing)
		}
		if pr.MetadataHash == "" || pr.CIHash == "" {
			t.Fatalf("semantic hashes not persisted: metadata=%q ci=%q", pr.MetadataHash, pr.CIHash)
		}

		// pr_checks rows are a transactional mirror of the observation's CI.Checks.
		checks, err := f.store.ListChecks(ctx, prURL)
		if err != nil {
			t.Fatalf("ListChecks: %v", err)
		}
		if len(checks) != 1 {
			t.Fatalf("pr_checks rows = %d, want 1: %+v", len(checks), checks)
		}
		got := checks[0]
		if got.Name != "build" || got.Status != domain.PRCheckFailed || got.CommitHash != headSHA || got.LogTail != logTail {
			t.Fatalf("pr_checks row mismatch: %+v", got)
		}

		// Exactly one nudge reached the messenger, containing the log tail the
		// agent needs to fix CI.
		msgs := f.spy.snapshot()
		if len(msgs) != 1 {
			t.Fatalf("messenger captured %d nudges, want 1: %+v", len(msgs), msgs)
		}
		nudge := msgs[0]
		if nudge.session != f.session.ID {
			t.Fatalf("nudge addressed to session %q, want %q", nudge.session, f.session.ID)
		}
		if !strings.Contains(nudge.body, "CI is failing") {
			t.Fatalf("nudge body missing CI-failure cue: %q", nudge.body)
		}
		if !strings.Contains(nudge.body, logTail) {
			t.Fatalf("nudge body missing log tail %q: %q", logTail, nudge.body)
		}

		// Persisted dedup signature proves the lifecycle wrote its
		// nudge-acknowledgement state through, so a daemon restart would not
		// re-fire the same nudge against the same observation.
		sigBeforeSecondPoll, err := f.store.GetPRLastNudgeSignature(ctx, prURL)
		if err != nil {
			t.Fatalf("GetPRLastNudgeSignature: %v", err)
		}
		if sigBeforeSecondPoll == "" {
			t.Fatalf("last_nudge_signature not persisted after first nudge")
		}

		// A second identical Poll must produce zero additional nudges. This
		// exercises the hash-match short-circuit in prepareForPersistence —
		// the production fallback the observer relies on when the upstream
		// ETag guard misses. The ETag-driven 304 short-circuit on the same
		// SHA is covered by the unit tests in observe/scm/observer_test.go
		// (Poll_RepoETag304SkipsListPRs, Poll_CIETagChangeRefreshesWhenRepoUnchanged).
		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("second Poll: %v", err)
		}
		if got := f.spy.count(); got != 1 {
			t.Fatalf("nudges after idempotent re-poll = %d, want 1", got)
		}
		sigAfterSecondPoll, err := f.store.GetPRLastNudgeSignature(ctx, prURL)
		if err != nil {
			t.Fatalf("GetPRLastNudgeSignature after re-poll: %v", err)
		}
		if sigAfterSecondPoll != sigBeforeSecondPoll {
			t.Fatalf("idempotent re-poll mutated last_nudge_signature: before=%q after=%q", sigBeforeSecondPoll, sigAfterSecondPoll)
		}
	})

	t.Run("Merged observation terminates the session and sends no nudge", func(t *testing.T) {
		ctx := context.Background()
		f := newSCMFixture(t, "feat/x")
		f.enableTerminateOnPRMerge(t)
		const (
			prURL   = "https://github.com/octocat/hello/pull/77"
			headSHA = "cafef00d"
		)
		f.provider.detected["feat/x"] = ports.SCMPRObservation{
			URL: prURL, Number: 77, SourceBranch: "feat/x", HeadRepo: scmTestRepo.Repo, TargetBranch: "main", HeadSHA: headSHA, Merged: true,
		}
		f.provider.observations[77] = mergedSCMObservation(prURL, 77, headSHA)

		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}

		rec, ok, err := f.store.GetSession(ctx, f.session.ID)
		if err != nil || !ok {
			t.Fatalf("GetSession: ok=%v err=%v", ok, err)
		}
		if !rec.IsTerminated {
			t.Fatalf("merged observation should MarkTerminated the session: %+v", rec)
		}
		if got := f.spy.count(); got != 0 {
			t.Fatalf("merged observation must not nudge, got %d msgs: %+v", got, f.spy.snapshot())
		}
	})

	t.Run("Merged teardown failure remains live and retries on the next poll", func(t *testing.T) {
		ctx := context.Background()
		f := newSCMFixture(t, "feat/x")
		f.enableTerminateOnPRMerge(t)
		terminator := &failOnceTerminator{lcm: f.lcm}
		f.lcm.SetCompletionTerminator(terminator)
		const prURL = "https://github.com/octocat/hello/pull/78"
		f.provider.detected["feat/x"] = ports.SCMPRObservation{
			URL: prURL, Number: 78, SourceBranch: "feat/x", HeadRepo: scmTestRepo.Repo, TargetBranch: "main", HeadSHA: "deadbeef", Merged: true,
		}
		f.provider.observations[78] = mergedSCMObservation(prURL, 78, "deadbeef")

		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("first Poll: %v", err)
		}
		if rec, _, _ := f.store.GetSession(ctx, f.session.ID); rec.IsTerminated {
			t.Fatalf("failed teardown hid a live session: %+v", rec)
		}
		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("second Poll: %v", err)
		}
		if terminator.calls != 2 {
			t.Fatalf("terminator calls = %d, want 2", terminator.calls)
		}
		if rec, _, _ := f.store.GetSession(ctx, f.session.ID); !rec.IsTerminated {
			t.Fatalf("successful retry left session live: %+v", rec)
		}
	})

	t.Run("Branch with no open PR writes nothing and sends no nudge", func(t *testing.T) {
		ctx := context.Background()
		f := newSCMFixture(t, "feat/quiet")
		// No entry in provider.detected — ListOpenPRsByRepo returns an empty list,
		// the production "no PR yet" signal.

		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}

		prs, err := f.store.ListPRsBySession(ctx, f.session.ID)
		if err != nil {
			t.Fatalf("ListPRsBySession: %v", err)
		}
		if len(prs) != 0 {
			t.Fatalf("no PR should be persisted for a quiet branch: %+v", prs)
		}
		if got := f.spy.count(); got != 0 {
			t.Fatalf("quiet branch must not nudge, got %d msgs: %+v", got, f.spy.snapshot())
		}
	})
}

// openSCMObservation builds an open-PR observation with caller-chosen branches
// and mergeability, CI passing and no review. The multi-PR cases drive the stack
// model (target/source branch pairs) and the completion rule, so branches must
// be configurable rather than the fixed feat/x->main the single-PR helpers bake in.
func openSCMObservation(prURL string, num int, headSHA, src, tgt string, merge domain.Mergeability) ports.SCMObservation {
	mo := ports.SCMMergeabilityObservation{State: string(merge)}
	switch merge {
	case domain.MergeMergeable:
		mo.Mergeable = true
	case domain.MergeConflicting:
		mo.Conflict = true
		mo.Blockers = []string{"conflicts"}
	}
	return ports.SCMObservation{
		Fetched:  true,
		Provider: "github", Host: "github.com", Repo: "octocat/hello",
		PR: ports.SCMPRObservation{
			URL:          prURL,
			HTMLURL:      prURL,
			Number:       num,
			State:        string(domain.PRStateOpen),
			SourceBranch: src,
			TargetBranch: tgt,
			HeadSHA:      headSHA,
			Title:        "wip",
		},
		CI:           ports.SCMCIObservation{Summary: string(domain.CIPassing), HeadSHA: headSHA},
		Review:       ports.SCMReviewObservation{Decision: string(domain.ReviewNone)},
		Mergeability: mo,
	}
}

// mergedSCMObservationBranches is mergedSCMObservation with caller-chosen
// branches so a stacked child (feat/x/auth -> feat/x) can be merged distinctly
// from the root (feat/x -> main).
func mergedSCMObservationBranches(prURL string, num int, headSHA, src, tgt string) ports.SCMObservation {
	o := mergedSCMObservation(prURL, num, headSHA)
	o.PR.SourceBranch = src
	o.PR.TargetBranch = tgt
	return o
}

// detectedPR is the open-PR-list discovery shape: the observer attributes a
// listed PR to a session by source-branch prefix, so only identity + branches
// matter here.
func detectedPR(prURL string, num int, src, tgt, headSHA string) ports.SCMPRObservation {
	return ports.SCMPRObservation{URL: prURL, HTMLURL: prURL, Number: num, SourceBranch: src, HeadRepo: scmTestRepo.Repo, TargetBranch: tgt, HeadSHA: headSHA}
}

// TestSCMObserverMultiPREndToEnd is the functional regression guard for the
// multi-PR-per-session feature. It drives the real store + lifecycle + observer
// through the three behaviours the feature adds on top of the single-PR lane:
// branch-prefix attribution of several PRs to one session, the "all PRs
// merged/closed and at least one merged" completion bar, and the stacked-child
// merge-conflict nudge suppression. The SCM provider is canned (its own httptest
// coverage lives in observe/scm), so every other layer runs for real.
func TestSCMObserverMultiPREndToEnd(t *testing.T) {
	t.Run("one session owns its root and stacked child PRs from a single repo list", func(t *testing.T) {
		ctx := context.Background()
		f := newSCMFixture(t, "feat/x")
		const (
			rootURL  = "https://github.com/octocat/hello/pull/101"
			childURL = "https://github.com/octocat/hello/pull/102"
		)
		// Root PR on the session branch, plus a stacked child whose source branch
		// descends from it (feat/x/auth). matchSession claims both for the one
		// session: the child by the "branch/..." stacking convention.
		f.provider.detected["feat/x"] = detectedPR(rootURL, 101, "feat/x", "main", "sha-root")
		f.provider.detected["feat/x/auth"] = detectedPR(childURL, 102, "feat/x/auth", "feat/x", "sha-child")
		f.provider.observations[101] = openSCMObservation(rootURL, 101, "sha-root", "feat/x", "main", domain.MergeMergeable)
		f.provider.observations[102] = openSCMObservation(childURL, 102, "sha-child", "feat/x/auth", "feat/x", domain.MergeBlocked)

		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}

		prs, err := f.store.ListPRsBySession(ctx, f.session.ID)
		if err != nil {
			t.Fatalf("ListPRsBySession: %v", err)
		}
		if len(prs) != 2 {
			t.Fatalf("one session should own both discovered PRs, got %d: %+v", len(prs), prs)
		}
		byURL := map[string]domain.PullRequest{}
		for _, pr := range prs {
			if pr.SessionID != f.session.ID {
				t.Fatalf("PR %q attributed to %q, want %q", pr.URL, pr.SessionID, f.session.ID)
			}
			byURL[pr.URL] = pr
		}
		// The branch pair is what the stack model is derived from, so it must be
		// persisted by the observer write path (not just discovered).
		if byURL[rootURL].SourceBranch != "feat/x" || byURL[rootURL].TargetBranch != "main" {
			t.Fatalf("root branch pair lost: %+v", byURL[rootURL])
		}
		if byURL[childURL].SourceBranch != "feat/x/auth" || byURL[childURL].TargetBranch != "feat/x" {
			t.Fatalf("child branch pair lost: %+v", byURL[childURL])
		}
		if got := f.spy.count(); got != 0 {
			t.Fatalf("clean PRs must not nudge, got %d: %+v", got, f.spy.snapshot())
		}
	})

	t.Run("session stays alive while a stacked PR is open and terminates once all are merged", func(t *testing.T) {
		ctx := context.Background()
		f := newSCMFixture(t, "feat/x")
		f.enableTerminateOnPRMerge(t)
		const (
			rootURL  = "https://github.com/octocat/hello/pull/201"
			childURL = "https://github.com/octocat/hello/pull/202"
		)
		f.provider.detected["feat/x"] = detectedPR(rootURL, 201, "feat/x", "main", "sha-root")
		f.provider.detected["feat/x/auth"] = detectedPR(childURL, 202, "feat/x/auth", "feat/x", "sha-child")
		f.provider.observations[201] = openSCMObservation(rootURL, 201, "sha-root", "feat/x", "main", domain.MergeMergeable)
		f.provider.observations[202] = openSCMObservation(childURL, 202, "sha-child", "feat/x/auth", "feat/x", domain.MergeBlocked)

		// Poll 1: both PRs open and tracked. The session is live.
		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll 1: %v", err)
		}
		if rec, _, _ := f.store.GetSession(ctx, f.session.ID); rec.IsTerminated {
			t.Fatal("session terminated with two open PRs")
		}

		// Poll 2: the root merges while the child stays open. One merged PR does
		// not satisfy the completion bar while another PR is still open.
		f.provider.observations[201] = mergedSCMObservationBranches(rootURL, 201, "sha-root", "feat/x", "main")
		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll 2: %v", err)
		}
		rootPR, ok, err := f.store.GetPR(ctx, rootURL)
		if err != nil || !ok {
			t.Fatalf("GetPR root: ok=%v err=%v", ok, err)
		}
		if !rootPR.Merged {
			t.Fatalf("root PR should be persisted merged: %+v", rootPR)
		}
		if rec, _, _ := f.store.GetSession(ctx, f.session.ID); rec.IsTerminated {
			t.Fatal("session terminated while the stacked child PR is still open")
		}

		// Poll 3: the child merges too. Now every PR is merged/closed and at least
		// one merged, so the session completes and terminates.
		f.provider.observations[202] = mergedSCMObservationBranches(childURL, 202, "sha-child", "feat/x/auth", "feat/x")
		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll 3: %v", err)
		}
		rec, ok, err := f.store.GetSession(ctx, f.session.ID)
		if err != nil || !ok {
			t.Fatalf("GetSession: ok=%v err=%v", ok, err)
		}
		if !rec.IsTerminated {
			t.Fatalf("session should terminate once all PRs are merged: %+v", rec)
		}
		if got := f.spy.count(); got != 0 {
			t.Fatalf("merge-driven completion must not nudge, got %d: %+v", got, f.spy.snapshot())
		}
	})

	t.Run("stacked child blocked by an open parent is exempt from the rebase nudge", func(t *testing.T) {
		ctx := context.Background()
		f := newSCMFixture(t, "feat/x")
		const (
			rootURL  = "https://github.com/octocat/hello/pull/301"
			childURL = "https://github.com/octocat/hello/pull/302"
		)
		f.provider.detected["feat/x"] = detectedPR(rootURL, 301, "feat/x", "main", "sha-root")
		f.provider.detected["feat/x/auth"] = detectedPR(childURL, 302, "feat/x/auth", "feat/x", "sha-child")
		// Poll 1 establishes both rows (open, mergeable) so the stack relationship
		// is durable before conflicts appear, making the poll-2 reaction order
		// independent of map iteration.
		f.provider.observations[301] = openSCMObservation(rootURL, 301, "sha-root", "feat/x", "main", domain.MergeMergeable)
		f.provider.observations[302] = openSCMObservation(childURL, 302, "sha-child", "feat/x/auth", "feat/x", domain.MergeMergeable)
		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll 1: %v", err)
		}
		if got := f.spy.count(); got != 0 {
			t.Fatalf("clean establishing poll must not nudge, got %d: %+v", got, f.spy.snapshot())
		}

		// Poll 2: both PRs now report merge conflicts. Only the bottom of the
		// stack (the root, targeting main) is eligible for the rebase nudge; the
		// child targets feat/x, the still-open root's source branch, so it is
		// expected to conflict against its parent until the parent merges and is
		// suppressed.
		f.provider.observations[301] = openSCMObservation(rootURL, 301, "sha-root", "feat/x", "main", domain.MergeConflicting)
		f.provider.observations[302] = openSCMObservation(childURL, 302, "sha-child", "feat/x/auth", "feat/x", domain.MergeConflicting)
		if err := f.observer.Poll(ctx); err != nil {
			t.Fatalf("Poll 2: %v", err)
		}

		msgs := f.spy.snapshot()
		if len(msgs) != 1 {
			t.Fatalf("exactly one PR (the stack bottom) should be nudged, got %d: %+v", len(msgs), msgs)
		}
		if msgs[0].session != f.session.ID {
			t.Fatalf("nudge addressed to %q, want %q", msgs[0].session, f.session.ID)
		}
		if !strings.Contains(msgs[0].body, "merge conflicts") {
			t.Fatalf("nudge body missing merge-conflict cue: %q", msgs[0].body)
		}

		// The persisted dedup signature must be the root's, never the child's —
		// proving the child was suppressed at the reaction layer, not merely
		// deduped after sending.
		rootSig, err := f.store.GetPRLastNudgeSignature(ctx, rootURL)
		if err != nil {
			t.Fatalf("GetPRLastNudgeSignature root: %v", err)
		}
		if rootSig == "" {
			t.Fatal("root PR should have a persisted nudge signature")
		}
		childSig, err := f.store.GetPRLastNudgeSignature(ctx, childURL)
		if err != nil {
			t.Fatalf("GetPRLastNudgeSignature child: %v", err)
		}
		if childSig != "" {
			t.Fatalf("stacked child must not record a nudge signature: %q", childSig)
		}
	})
}
