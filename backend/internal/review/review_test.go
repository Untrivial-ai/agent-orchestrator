package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// --- fakes ---

type fakeStore struct {
	review                *domain.Review
	reviews               map[domain.ReviewerHarness]domain.Review
	runs                  []domain.ReviewRun
	listAllReviewRunHits  int
	agentSessionUpdates   []struct{ id, agentSessionID string }
	reviewerConfigUpdates []struct {
		sessionID domain.SessionID
		harness   domain.ReviewerHarness
		config    domain.AgentConfig
	}
	upsertErr     error
	upsertErrCall int
	upsertCalls   int
	// insertErr, when set, makes the next InsertReviewRun model a concurrent
	// writer that already recorded a run for this commit: it records that
	// winner (so a follow-up GetReviewRunBySessionAndSHA finds it) and returns
	// insertErr instead of recording the caller's run.
	insertErr              error
	insertErrWinnerAtFront bool
}

func (f *fakeStore) UpsertReview(_ context.Context, r domain.Review) error {
	f.upsertCalls++
	if f.upsertErr != nil && f.upsertCalls == f.upsertErrCall {
		return f.upsertErr
	}
	cp := r
	f.review = &cp
	if f.reviews == nil {
		f.reviews = make(map[domain.ReviewerHarness]domain.Review)
	}
	if existing, ok := f.reviews[r.Harness]; ok {
		cp.ID = existing.ID
		cp.CreatedAt = existing.CreatedAt
		if cp.AgentSessionID == "" {
			cp.AgentSessionID = existing.AgentSessionID
		}
		f.review = &cp
	}
	f.reviews[r.Harness] = cp
	return nil
}
func (f *fakeStore) SetSessionReviewerConfig(_ context.Context, id domain.SessionID, harness domain.ReviewerHarness, config domain.AgentConfig, _ time.Time) (bool, error) {
	f.reviewerConfigUpdates = append(f.reviewerConfigUpdates, struct {
		sessionID domain.SessionID
		harness   domain.ReviewerHarness
		config    domain.AgentConfig
	}{sessionID: id, harness: harness, config: config})
	return true, nil
}
func (f *fakeStore) GetReviewBySession(_ context.Context, _ domain.SessionID) (domain.Review, bool, error) {
	if f.review == nil {
		return domain.Review{}, false, nil
	}
	return *f.review, true, nil
}
func (f *fakeStore) GetReviewBySessionAndHarness(_ context.Context, _ domain.SessionID, harness domain.ReviewerHarness) (domain.Review, bool, error) {
	if f.reviews != nil {
		review, ok := f.reviews[harness]
		return review, ok, nil
	}
	if f.review != nil && f.review.Harness == harness {
		return *f.review, true, nil
	}
	return domain.Review{}, false, nil
}
func (f *fakeStore) ListReviewsBySession(_ context.Context, _ domain.SessionID) ([]domain.Review, error) {
	if f.reviews != nil {
		out := make([]domain.Review, 0, len(f.reviews))
		for _, review := range f.reviews {
			out = append(out, review)
		}
		return out, nil
	}
	if f.review == nil {
		return nil, nil
	}
	return []domain.Review{*f.review}, nil
}
func (f *fakeStore) ClearReviewerHandle(_ context.Context, id domain.SessionID) error {
	if f.review != nil && f.review.SessionID == id {
		f.review.ReviewerHandleID = ""
	}
	for harness, review := range f.reviews {
		if review.SessionID == id {
			review.ReviewerHandleID = ""
			f.reviews[harness] = review
		}
	}
	return nil
}
func (f *fakeStore) ClearReviewerHandleByHarness(_ context.Context, _ domain.SessionID, harness domain.ReviewerHarness) error {
	if f.review != nil && f.review.Harness == harness {
		f.review.ReviewerHandleID = ""
	}
	if f.reviews != nil {
		review, ok := f.reviews[harness]
		if !ok {
			return nil
		}
		review.ReviewerHandleID = ""
		f.reviews[harness] = review
	}
	return nil
}
func (f *fakeStore) InsertReviewRun(_ context.Context, r domain.ReviewRun) error {
	if f.insertErr != nil {
		winner := r
		winner.ID = "winner-" + r.ID
		if f.insertErrWinnerAtFront {
			f.runs = append([]domain.ReviewRun{winner}, f.runs...)
		} else {
			f.runs = append(f.runs, winner)
		}
		return f.insertErr
	}
	// Mirrors idx_review_run_session_pr_sha_harness. Harness is part of the key so
	// a second reviewer on the same commit is a distinct pass, not a duplicate.
	for _, existing := range f.runs {
		if existing.SessionID == r.SessionID &&
			existing.PRURL == r.PRURL &&
			existing.TargetSHA == r.TargetSHA &&
			existing.Harness == r.Harness &&
			existing.TargetSHA != "" &&
			existing.Status == domain.ReviewRunRunning &&
			existing.Verdict == domain.VerdictNone {
			return domain.ErrDuplicateReviewRun
		}
	}
	f.runs = append(f.runs, r)
	return nil
}
func (f *fakeStore) UpdateReviewAgentSessionID(_ context.Context, id, agentSessionID string) (bool, error) {
	f.agentSessionUpdates = append(f.agentSessionUpdates, struct{ id, agentSessionID string }{id: id, agentSessionID: agentSessionID})
	if f.review != nil && f.review.ID == id {
		f.review.AgentSessionID = agentSessionID
	}
	for harness, review := range f.reviews {
		if review.ID == id {
			review.AgentSessionID = agentSessionID
			f.reviews[harness] = review
		}
	}
	return true, nil
}

func (f *fakeStore) UpdateReviewRunResult(_ context.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, githubReviewID string, autoInjectReview bool) (bool, error) {
	for i := range f.runs {
		if f.runs[i].ID == id {
			if f.runs[i].Status != domain.ReviewRunRunning {
				return false, nil
			}
			f.runs[i].Status = status
			f.runs[i].Verdict = verdict
			f.runs[i].Body = body
			f.runs[i].GithubReviewID = githubReviewID
			f.runs[i].AutoInjectReview = autoInjectReview
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeStore) SupersedeStaleRunningReviewRuns(_ context.Context, sessionID domain.SessionID, prURL, targetSHA, body string) (int64, error) {
	var n int64
	for i := range f.runs {
		if f.runs[i].SessionID == sessionID && f.runs[i].PRURL == prURL && f.runs[i].TargetSHA != targetSHA && f.runs[i].Status == domain.ReviewRunRunning && f.runs[i].Verdict == domain.VerdictNone {
			f.runs[i].Status = domain.ReviewRunFailed
			f.runs[i].Body = body
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) CancelRunningReviewRunsBySession(_ context.Context, sessionID domain.SessionID, body string) (int64, error) {
	var n int64
	for i := range f.runs {
		if f.runs[i].SessionID == sessionID && f.runs[i].Status == domain.ReviewRunRunning && f.runs[i].Verdict == domain.VerdictNone {
			f.runs[i].Status = domain.ReviewRunCancelled
			f.runs[i].Body = body
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) CancelRunningReviewRunsBySessionAndHarness(_ context.Context, sessionID domain.SessionID, harness domain.ReviewerHarness, body string) (int64, error) {
	var n int64
	for i := range f.runs {
		if f.runs[i].SessionID == sessionID && (f.runs[i].Harness == harness || f.runs[i].Harness == "") && f.runs[i].Status == domain.ReviewRunRunning && f.runs[i].Verdict == domain.VerdictNone {
			f.runs[i].Status = domain.ReviewRunCancelled
			f.runs[i].Body = body
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) GetReviewRun(_ context.Context, id string) (domain.ReviewRun, bool, error) {
	for _, r := range f.runs {
		if r.ID == id {
			return r, true, nil
		}
	}
	return domain.ReviewRun{}, false, nil
}
func (f *fakeStore) GetReviewRunBySessionPRAndSHA(_ context.Context, sessionID domain.SessionID, prURL, sha string) (domain.ReviewRun, bool, error) {
	for i := len(f.runs) - 1; i >= 0; i-- {
		if f.runs[i].SessionID == sessionID && f.runs[i].PRURL == prURL && f.runs[i].TargetSHA == sha {
			return f.runs[i], true, nil
		}
	}
	return domain.ReviewRun{}, false, nil
}
func (f *fakeStore) GetReviewRunBySessionPRSHAAndHarness(_ context.Context, sessionID domain.SessionID, prURL, sha string, harness domain.ReviewerHarness) (domain.ReviewRun, bool, error) {
	for i := len(f.runs) - 1; i >= 0; i-- {
		if f.runs[i].SessionID == sessionID && f.runs[i].PRURL == prURL && f.runs[i].TargetSHA == sha && f.runs[i].Harness == harness {
			return f.runs[i], true, nil
		}
	}
	return domain.ReviewRun{}, false, nil
}
func (f *fakeStore) ListReviewRunsBySession(_ context.Context, _ domain.SessionID) ([]domain.ReviewRun, error) {
	f.listAllReviewRunHits++
	return f.runs, nil
}
func (f *fakeStore) ListRunningReviewRunsBySession(_ context.Context, sessionID domain.SessionID) ([]domain.ReviewRun, error) {
	out := make([]domain.ReviewRun, 0)
	for _, run := range f.runs {
		if run.SessionID == sessionID && run.Status == domain.ReviewRunRunning && run.Verdict == domain.VerdictNone {
			out = append(out, run)
		}
	}
	return out, nil
}

type fakeSessions struct {
	rec domain.SessionRecord
	ok  bool
}

func (f fakeSessions) GetSession(_ context.Context, _ domain.SessionID) (domain.SessionRecord, bool, error) {
	return f.rec, f.ok, nil
}

type fakePRs struct{ prs []domain.PullRequest }

func (f fakePRs) ListPRsBySession(_ context.Context, _ domain.SessionID) ([]domain.PullRequest, error) {
	return f.prs, nil
}

type fakeProjects struct{ cfg domain.ProjectConfig }

func (f fakeProjects) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	return domain.ProjectRecord{ID: id, Config: f.cfg}, true, nil
}

type fakeLauncher struct {
	handle           string
	agentSessionID   string
	alive            bool
	reusable         bool
	reusableSet      bool
	spawnErr         error
	notifyErr        error
	spawned          bool
	restored         bool
	spawnCount       int
	notified         bool
	cancelled        bool
	cancelErr        error
	aliveErr         error
	gotSpec          LaunchSpec
	gotHandle        string
	cancelledHandle  string
	cancelledHarness domain.ReviewerHarness
	destroyed        bool
	destroyedHandle  string
	destroyErr       error
	destroyErrCall   int
	destroyCalls     int
	specs            []LaunchSpec
	handles          []string
	aliveChecked     bool
	preflightErr     error
	preflighted      bool
	spawnStarted     chan struct{}
	unblockSpawn     <-chan struct{}
	destroyCalled    chan string
}

func (f *fakeLauncher) Spawn(_ context.Context, spec LaunchSpec) (LaunchResult, error) {
	f.spawned = true
	f.spawnCount++
	f.gotSpec = spec
	f.specs = append(f.specs, spec)
	if f.spawnStarted != nil {
		close(f.spawnStarted)
	}
	if f.unblockSpawn != nil {
		<-f.unblockSpawn
	}
	if f.spawnErr != nil {
		return LaunchResult{}, f.spawnErr
	}
	return LaunchResult{HandleID: f.handle, AgentSessionID: f.agentSessionID}, nil
}
func (f *fakeLauncher) RestoreTerminal(_ context.Context, spec LaunchSpec) (LaunchResult, error) {
	f.restored = true
	f.gotSpec = spec
	f.specs = append(f.specs, spec)
	if f.spawnErr != nil {
		return LaunchResult{}, f.spawnErr
	}
	return LaunchResult{HandleID: f.handle, AgentSessionID: f.agentSessionID}, nil
}
func (f *fakeLauncher) Notify(_ context.Context, handleID string, spec LaunchSpec) error {
	f.notified = true
	f.gotHandle = handleID
	f.gotSpec = spec
	f.handles = append(f.handles, handleID)
	f.specs = append(f.specs, spec)
	return f.notifyErr
}
func (f *fakeLauncher) Alive(_ context.Context, _ string) (bool, error) {
	f.aliveChecked = true
	return f.alive || f.spawned || f.restored, f.aliveErr
}
func (f *fakeLauncher) Reusable(domain.ReviewerHarness) bool {
	if f.reusableSet {
		return f.reusable
	}
	return true
}
func (f *fakeLauncher) Cancel(_ context.Context, handleID string, harness domain.ReviewerHarness) error {
	f.cancelled = true
	f.cancelledHandle = handleID
	f.cancelledHarness = harness
	return f.cancelErr
}
func (f *fakeLauncher) Destroy(_ context.Context, handleID string) error {
	f.destroyed = true
	f.destroyedHandle = handleID
	f.destroyCalls++
	if f.destroyCalled != nil {
		f.destroyCalled <- handleID
	}
	if f.destroyErr != nil && (f.destroyErrCall == 0 || f.destroyErrCall == f.destroyCalls) {
		return f.destroyErr
	}
	return nil
}
func (f *fakeLauncher) Preflight(_ context.Context, _ domain.ReviewerHarness, _ string) error {
	f.preflighted = true
	return f.preflightErr
}

func liveWorker() domain.SessionRecord {
	return domain.SessionRecord{
		ID:                "mer-1",
		ProjectID:         "mer",
		Kind:              domain.KindWorker,
		Harness:           domain.HarnessClaudeCode,
		Metadata:          domain.SessionMetadata{WorkspacePath: "/ws/mer-1"},
		AutoReviewEnabled: true,
		AutoInjectReview:  true,
		Activity: domain.Activity{
			State:          domain.ActivityIdle,
			LastActivityAt: time.Unix(-60, 0).UTC(),
		},
	}
}

func newEngineForTest(store Store, sessions Sessions, prs PRs, projects Projects, launcher Launcher) *Engine {
	ids := 0
	return New(Deps{
		Store: store, Sessions: sessions, PRs: prs, Projects: projects, Launcher: launcher,
		Clock: func() time.Time { return time.Unix(0, 0).UTC() },
		NewID: func() string { ids++; return "id-" + string(rune('0'+ids)) },
	})
}

func prAt(sha string) fakePRs {
	return fakePRs{prs: []domain.PullRequest{{URL: "https://github.com/o/r/pull/1", Number: 1, HeadSHA: sha}}}
}

// --- tests ---

func TestTriggerSpawnsNewReviewerAndRecordsRunAfterLaunch(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || res.ReviewerHandleID != "review-mer-1" {
		t.Fatalf("result = %+v", res)
	}
	if !launcher.spawned || launcher.notified {
		t.Fatalf("expected spawn (no live reviewer): %+v", launcher)
	}
	if res.Run.TargetSHA != "sha1" || res.Run.Status != domain.ReviewRunRunning || res.Run.Harness != domain.ReviewerClaudeCode {
		t.Fatalf("run = %+v", res.Run)
	}
	if launcher.gotSpec.RunID != res.Run.ID || launcher.gotSpec.BatchID != res.Run.BatchID {
		t.Fatalf("launch spec ids = batch %q run %q, want batch %q run %q", launcher.gotSpec.BatchID, launcher.gotSpec.RunID, res.Run.BatchID, res.Run.ID)
	}
	if len(store.runs) != 1 || store.review == nil || store.review.ReviewerHandleID != "review-mer-1" {
		t.Fatalf("persisted review=%+v runs=%+v", store.review, store.runs)
	}
}

func TestRestoreReviewerNoopsWithoutReviewHistory(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.RestoreReviewer(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("RestoreReviewer: %v", err)
	}
	if res.Restored || launcher.restored {
		t.Fatalf("expected no reviewer restore without review history: res=%+v launcher=%+v", res, launcher)
	}
}

func TestRestoreReviewerRestoresDeadReviewerFromHistory(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs:   []domain.ReviewRun{{ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved}},
	}
	launcher := &fakeLauncher{alive: false, handle: "review-mer-1"}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.RestoreReviewer(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("RestoreReviewer: %v", err)
	}
	if !res.Restored || res.ReviewerHandleID != "review-mer-1" || !launcher.restored {
		t.Fatalf("expected reviewer terminal restore: res=%+v launcher=%+v", res, launcher)
	}
	if launcher.gotSpec.ProjectID != "mer" || launcher.gotSpec.WorkerID != "mer-1" || launcher.gotSpec.Harness != domain.ReviewerCodex {
		t.Fatalf("restore spec = %+v", launcher.gotSpec)
	}
	if len(launcher.gotSpec.PreviousRuns) != 1 || launcher.gotSpec.PreviousRuns[0].ID != "run-1" {
		t.Fatalf("previous runs passed to restore = %+v", launcher.gotSpec.PreviousRuns)
	}
	if store.review.ReviewerHandleID != "review-mer-1" {
		t.Fatalf("stored reviewer handle = %q", store.review.ReviewerHandleID)
	}
}

func TestRestoreCodexReviewerDoesNotApplyAnotherHarnessProjectConfig(t *testing.T) {
	store := &fakeStore{
		reviews: map[domain.ReviewerHarness]domain.Review{
			domain.ReviewerCodex: {
				ID:               "rev-codex",
				SessionID:        "mer-1",
				ProjectID:        "mer",
				Harness:          domain.ReviewerCodex,
				ReviewerHandleID: "codex-pane",
				AgentSessionID:   "codex-native",
			},
		},
	}
	projects := fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{
		Harness:     domain.ReviewerOpenCode,
		AgentConfig: domain.AgentConfig{Model: "opencode-model"},
	}}}}
	launcher := &fakeLauncher{alive: true, handle: "codex-restored"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), projects, launcher)

	stopped, err := eng.SuspendCodexReviewer(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("SuspendCodexReviewer: %v", err)
	}
	if !stopped {
		t.Fatal("SuspendCodexReviewer stopped = false, want true for stale live Codex pane")
	}
	if err := eng.RestoreCodexReviewer(context.Background(), "mer-1"); err != nil {
		t.Fatalf("RestoreCodexReviewer: %v", err)
	}
	if !launcher.restored || launcher.gotSpec.Harness != domain.ReviewerCodex {
		t.Fatalf("restore spec = %+v, want retained Codex reviewer restored", launcher.gotSpec)
	}
	if !launcher.gotSpec.AgentConfig.IsZero() {
		t.Fatalf("restored Codex config = %+v, want no OpenCode project config", launcher.gotSpec.AgentConfig)
	}
	if launcher.gotSpec.AgentSessionID != "codex-native" || !launcher.gotSpec.RequireNativeHistory {
		t.Fatalf("restore spec = %+v, want exact retained Codex native history", launcher.gotSpec)
	}
}

func TestCancelInterruptsReviewerAndCancelsRunningRuns(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{
			{ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
			{ID: "run-2", ReviewID: "rev-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/2", TargetSHA: "sha2", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved},
		},
	}
	launcher := &fakeLauncher{}
	prs := fakePRs{prs: []domain.PullRequest{
		{URL: "https://github.com/o/r/pull/1", Number: 1, HeadSHA: "sha1"},
		{URL: "https://github.com/o/r/pull/2", Number: 2, HeadSHA: "sha2"},
	}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prs, fakeProjects{}, launcher)

	res, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !launcher.cancelled || launcher.cancelledHandle != "review-mer-1" {
		t.Fatalf("launcher cancel = %v handle=%q", launcher.cancelled, launcher.cancelledHandle)
	}
	if launcher.cancelledHarness != domain.ReviewerCodex {
		t.Fatalf("cancel harness = %q, want codex", launcher.cancelledHarness)
	}
	if len(res.CancelledRuns) != 1 || res.CancelledRuns[0].ID != "run-1" {
		t.Fatalf("cancelled runs = %+v", res.CancelledRuns)
	}
	if store.runs[0].Status != domain.ReviewRunCancelled || !strings.Contains(store.runs[0].Body, "cancelled") {
		t.Fatalf("run not marked cancelled: %+v", store.runs[0])
	}
	if store.runs[1].Status != domain.ReviewRunComplete {
		t.Fatalf("non-running run was changed: %+v", store.runs[1])
	}
	if store.listAllReviewRunHits != 1 {
		t.Fatalf("full review run list calls = %d, want 1 for final plan refresh only", store.listAllReviewRunHits)
	}
	if res.Reviews[0].Status == ReviewStateRunning {
		t.Fatalf("review state still running: %+v", res.Reviews[0])
	}
}

func TestCancelTargetsRunningReviewerHarness(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "codex-pane"},
		reviews: map[domain.ReviewerHarness]domain.Review{
			domain.ReviewerCodex:    {ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "codex-pane"},
			domain.ReviewerOpenCode: {ID: "rev-open", SessionID: "mer-1", Harness: domain.ReviewerOpenCode, ReviewerHandleID: "opencode-pane"},
		},
		runs: []domain.ReviewRun{
			{ID: "run-open", ReviewID: "rev-open", SessionID: "mer-1", Harness: domain.ReviewerOpenCode, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
			{ID: "run-codex", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, PRURL: "https://github.com/o/r/pull/2", TargetSHA: "sha2", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved},
		},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	launcher := &fakeLauncher{}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if launcher.cancelledHandle != "opencode-pane" || launcher.cancelledHarness != domain.ReviewerOpenCode {
		t.Fatalf("cancelled handle=%q harness=%q, want opencode pane", launcher.cancelledHandle, launcher.cancelledHarness)
	}
	if len(res.CancelledRuns) != 1 || res.CancelledRuns[0].ID != "run-open" {
		t.Fatalf("cancelled runs = %+v", res.CancelledRuns)
	}
	if store.runs[0].Status != domain.ReviewRunCancelled {
		t.Fatalf("opencode run not cancelled: %+v", store.runs[0])
	}
	if store.runs[1].Status != domain.ReviewRunComplete {
		t.Fatalf("codex run was changed: %+v", store.runs[1])
	}
}

func TestCancelDoesNotInterruptIdleReviewer(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex,
			PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if launcher.cancelled {
		t.Fatalf("idle reviewer should not be interrupted: %+v", launcher)
	}
	if len(res.CancelledRuns) != 0 {
		t.Fatalf("cancelled runs = %+v, want none", res.CancelledRuns)
	}
	if res.ReviewerHandleID != "review-mer-1" {
		t.Fatalf("reviewer handle = %q, want review-mer-1", res.ReviewerHandleID)
	}
}

func TestCancelMarksRunsCancelledWhenReviewerHandleIsGone(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1",
			PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning,
		}},
	}
	launcher := &fakeLauncher{cancelErr: errors.New("runtime: session not found")}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !launcher.cancelled {
		t.Fatal("expected launcher cancellation to be attempted")
	}
	if got := store.runs[0]; got.Status != domain.ReviewRunCancelled {
		t.Fatalf("run not marked cancelled after stale handle: %+v", got)
	}
	if len(res.CancelledRuns) != 1 || res.CancelledRuns[0].ID != "run-1" {
		t.Fatalf("cancelled runs = %+v", res.CancelledRuns)
	}
}

func TestCancelKeepsRunsRunningWhenReviewerCancelFailsAndHandleIsAlive(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1",
			PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning,
		}},
	}
	launcher := &fakeLauncher{alive: true, cancelErr: errors.New("interrupt failed")}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.Cancel(context.Background(), "mer-1"); err == nil {
		t.Fatal("Cancel err = nil, want interrupt failure")
	}
	if got := store.runs[0]; got.Status != domain.ReviewRunRunning {
		t.Fatalf("run should remain running when reviewer is still alive: %+v", got)
	}
}

func TestRestoreReviewerUsesSelectedHarnessSessionAndKillsOtherActivePane(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "codex-pane", AgentSessionID: "codex-native"},
		reviews: map[domain.ReviewerHarness]domain.Review{
			domain.ReviewerCodex:    {ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "codex-pane", AgentSessionID: "codex-native"},
			domain.ReviewerOpenCode: {ID: "rev-open", SessionID: "mer-1", ProjectID: "mer", Harness: domain.ReviewerOpenCode, AgentSessionID: "opencode-native"},
		},
		runs: []domain.ReviewRun{
			{ID: "codex-run", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved},
			{ID: "opencode-run", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerOpenCode, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved},
		},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerOpenCode
	launcher := &fakeLauncher{alive: true, handle: "opencode-pane", agentSessionID: "opencode-native"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.RestoreReviewer(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("RestoreReviewer: %v", err)
	}
	if !res.Restored || res.ReviewerHandleID != "opencode-pane" || !launcher.restored {
		t.Fatalf("expected opencode reviewer restore: res=%+v launcher=%+v", res, launcher)
	}
	if !launcher.destroyed || launcher.destroyedHandle != "codex-pane" {
		t.Fatalf("expected active codex pane to be destroyed: %+v", launcher)
	}
	if launcher.gotSpec.Harness != domain.ReviewerOpenCode || launcher.gotSpec.AgentSessionID != "opencode-native" {
		t.Fatalf("restore spec = %+v", launcher.gotSpec)
	}
	if len(launcher.gotSpec.PreviousRuns) != 1 || launcher.gotSpec.PreviousRuns[0].ID != "opencode-run" {
		t.Fatalf("previous runs = %+v, want only opencode history", launcher.gotSpec.PreviousRuns)
	}
	if store.review.Harness != domain.ReviewerOpenCode || store.review.ReviewerHandleID != "opencode-pane" || store.review.AgentSessionID != "opencode-native" {
		t.Fatalf("active review row = %+v, want restored opencode", store.review)
	}
}

func TestSwitchReviewerRestartsLivePaneWhenOnlyConfigChanges(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "codex-pane", AgentSessionID: "codex-native"},
		reviews: map[domain.ReviewerHarness]domain.Review{
			domain.ReviewerCodex: {ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "codex-pane", AgentSessionID: "codex-native"},
		},
		runs: []domain.ReviewRun{{ID: "codex-run", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-old"}
	launcher := &fakeLauncher{alive: true, handle: "codex-pane-2"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.SwitchReviewer(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{Model: "gpt-new"})
	if err != nil {
		t.Fatalf("SwitchReviewer: %v", err)
	}
	if !launcher.destroyed || launcher.destroyedHandle != "codex-pane" {
		t.Fatalf("expected active pane destroyed: %+v", launcher)
	}
	if !launcher.restored || launcher.gotSpec.AgentConfig.Model != "gpt-new" || launcher.gotSpec.AgentSessionID != "" {
		t.Fatalf("restore spec = %+v, want restarted with new config and no native session", launcher.gotSpec)
	}
	if len(store.agentSessionUpdates) != 1 || store.agentSessionUpdates[0].agentSessionID != "" {
		t.Fatalf("agent session updates = %+v, want cleared native session", store.agentSessionUpdates)
	}
	if res.ReviewerHandleID != "codex-pane-2" || res.ReviewerHarness != domain.ReviewerCodex {
		t.Fatalf("result = %+v", res)
	}
}

func TestSwitchReviewerKeepsDefaultReviewerInheritanceWhenSavingConfig(t *testing.T) {
	store := &fakeStore{}
	worker := liveWorker()
	launcher := &fakeLauncher{handle: "review-mer-2"}
	projects := fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{
		Harness: domain.ReviewerClaudeCode,
	}}}}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), projects, launcher)

	res, err := eng.SwitchReviewer(context.Background(), "mer-1", "", domain.AgentConfig{Model: "claude-3.7"})
	if err != nil {
		t.Fatalf("SwitchReviewer: %v", err)
	}
	if len(store.reviewerConfigUpdates) != 1 {
		t.Fatalf("reviewer config updates = %+v, want one", store.reviewerConfigUpdates)
	}
	if got := store.reviewerConfigUpdates[0]; got.harness != "" || got.config.Model != "claude-3.7" {
		t.Fatalf("persisted reviewer config = %+v, want default reviewer + model", got)
	}
	if res.ReviewerHarness != domain.ReviewerClaudeCode {
		t.Fatalf("result reviewer harness = %q, want claude-code", res.ReviewerHarness)
	}
	worker.ReviewerHarness = store.reviewerConfigUpdates[0].harness
	worker.ReviewerConfig = store.reviewerConfigUpdates[0].config
	eng.projects = fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{
		Harness: domain.ReviewerOpenCode,
	}}}}
	selected, selectedConfig, err := eng.reviewerSelection(context.Background(), worker)
	if err != nil {
		t.Fatalf("reviewerSelection after project change: %v", err)
	}
	if selected != domain.ReviewerOpenCode || selectedConfig.Model != "claude-3.7" {
		t.Fatalf("selection after project reviewer change = (%q, %+v), want inherited opencode config", selected, selectedConfig)
	}
}

func TestTerminateReviewerDestroysPaneAndCancelsRunningRuns(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1",
			Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone,
		}},
	}
	launcher := &fakeLauncher{}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.TerminateReviewer(context.Background(), "mer-1", "cancelled by worker termination")
	if err != nil {
		t.Fatalf("TerminateReviewer: %v", err)
	}
	if !launcher.destroyed || launcher.destroyedHandle != "review-mer-1" {
		t.Fatalf("launcher destroy = %v handle=%q", launcher.destroyed, launcher.destroyedHandle)
	}
	if launcher.cancelled {
		t.Fatal("TerminateReviewer must hard-destroy, not adapter-cancel")
	}
	if res.ReviewerHandleID != "review-mer-1" || len(res.CancelledRuns) != 1 {
		t.Fatalf("terminate result = %+v", res)
	}
	if store.runs[0].Status != domain.ReviewRunCancelled || store.runs[0].Body != "cancelled by worker termination" {
		t.Fatalf("run after terminate = %+v", store.runs[0])
	}
	if store.review.ReviewerHandleID != "" {
		t.Fatalf("reviewer handle after terminate = %q, want cleared", store.review.ReviewerHandleID)
	}
	list, err := eng.List(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("List after terminate: %v", err)
	}
	if list.ReviewerHandleID != "" {
		t.Fatalf("list reviewer handle after terminate = %q, want empty", list.ReviewerHandleID)
	}
}

func TestTerminateReviewerWaitsForInFlightTriggerSpawn(t *testing.T) {
	spawnStarted := make(chan struct{})
	unblockSpawn := make(chan struct{})
	destroyCalled := make(chan string, 1)
	store := &fakeStore{}
	launcher := &fakeLauncher{
		handle:        "review-mer-1",
		spawnStarted:  spawnStarted,
		unblockSpawn:  unblockSpawn,
		destroyCalled: destroyCalled,
	}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	triggerDone := make(chan error, 1)
	go func() {
		_, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{})
		triggerDone <- err
	}()
	<-spawnStarted

	terminateDone := make(chan error, 1)
	go func() {
		_, err := eng.TerminateReviewer(context.Background(), "mer-1", "cancelled by worker termination")
		terminateDone <- err
	}()

	select {
	case handleID := <-destroyCalled:
		t.Fatalf("TerminateReviewer destroyed %q before trigger spawn completed", handleID)
	case <-time.After(25 * time.Millisecond):
	}
	close(unblockSpawn)
	if err := <-triggerDone; err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := <-terminateDone; err != nil {
		t.Fatalf("TerminateReviewer: %v", err)
	}
	if handleID := <-destroyCalled; handleID != "review-mer-1" {
		t.Fatalf("destroyed handle = %q, want review-mer-1", handleID)
	}
	if store.review == nil || store.review.ReviewerHandleID != "" {
		t.Fatalf("review row after terminate = %+v, want cleared handle", store.review)
	}
	if len(store.runs) != 1 || store.runs[0].Status != domain.ReviewRunCancelled {
		t.Fatalf("runs after terminate = %+v, want cancelled trigger run", store.runs)
	}
}

func TestTerminateReviewerCancelsRunningRunsWithoutReviewerHandle(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1",
			Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone,
		}},
	}
	launcher := &fakeLauncher{}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.TerminateReviewer(context.Background(), "mer-1", "")
	if err != nil {
		t.Fatalf("TerminateReviewer: %v", err)
	}
	if launcher.destroyed {
		t.Fatal("destroy should not run without a reviewer handle")
	}
	if len(res.CancelledRuns) != 1 {
		t.Fatalf("cancelled runs = %d, want 1", len(res.CancelledRuns))
	}
	if store.runs[0].Status != domain.ReviewRunCancelled || store.runs[0].Body != "cancelled by worker session lifecycle" {
		t.Fatalf("run after terminate = %+v", store.runs[0])
	}
}

func TestTerminateReviewerNoopsWhenNoReviewHistory(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.TerminateReviewer(context.Background(), "mer-1", ""); err != nil {
		t.Fatalf("TerminateReviewer: %v", err)
	}
	if launcher.destroyed {
		t.Fatal("destroy should not run without a reviewer handle")
	}
}

func TestTriggerConcurrentSameWorkerSpawnsOnce(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	const n = 8
	var wg sync.WaitGroup
	results := make([]TriggerResult, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
		}(i)
	}
	wg.Wait()

	created := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Trigger[%d]: %v", i, errs[i])
		}
		if results[i].Created {
			created++
		}
	}
	if created != 1 {
		t.Errorf("Created=true count = %d, want exactly 1", created)
	}
	if launcher.spawnCount != 1 {
		t.Errorf("reviewer spawn count = %d, want 1", launcher.spawnCount)
	}
	if len(store.runs) != 1 {
		t.Errorf("recorded review runs = %d, want 1", len(store.runs))
	} else if !store.runs[0].AutoInjectReview {
		t.Error("review run did not snapshot the enabled session injection policy")
	}
}

func TestTriggerSnapshotsDisabledInjectionPolicy(t *testing.T) {
	store := &fakeStore{}
	worker := liveWorker()
	worker.AutoInjectReview = false
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{handle: "review-mer-1"})

	result, err := eng.Trigger(context.Background(), worker.ID, "", domain.AgentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.AutoInjectReview || len(store.runs) != 1 || store.runs[0].AutoInjectReview {
		t.Fatalf("review run injection snapshot = result:%v stored:%+v, want disabled", result.Run.AutoInjectReview, store.runs)
	}
}

func TestTriggerFallsBackToExistingRunOnUniqueConflict(t *testing.T) {
	// The idempotency check passes (no run yet), but the insert loses to a
	// concurrent writer the unique index already accepted.
	store := &fakeStore{insertErr: domain.ErrDuplicateReviewRun}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created {
		t.Fatalf("expected Created=false on unique conflict: %+v", res)
	}
	if res.Run.TargetSHA != "sha1" || !strings.HasPrefix(res.Run.ID, "winner-") {
		t.Fatalf("expected the recorded winner run, got %+v", res.Run)
	}
	if launcher.spawnCount != 0 {
		t.Fatalf("reviewer should not launch after unique conflict: %+v", launcher)
	}
}

func TestTriggerDuplicateFallbackUsesRequestedHarness(t *testing.T) {
	store := &fakeStore{
		insertErr:              domain.ErrDuplicateReviewRun,
		insertErrWinnerAtFront: true,
		runs: []domain.ReviewRun{{
			ID: "other-harness-run", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created {
		t.Fatalf("expected Created=false on unique conflict: %+v", res)
	}
	if res.Run.Harness != domain.ReviewerCodex || !strings.HasPrefix(res.Run.ID, "winner-") {
		t.Fatalf("expected duplicate fallback to return the codex winner, got %+v", res.Run)
	}
	if launcher.spawnCount != 0 {
		t.Fatalf("reviewer should not launch after unique conflict: %+v", launcher)
	}
}

func TestTriggerIsIdempotentForSameCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created || res.Run.ID != "run-1" || res.ReviewerHandleID != "review-mer-1" {
		t.Fatalf("expected reuse of existing run: %+v", res)
	}
	if launcher.spawned || launcher.notified {
		t.Fatalf("should not launch for an already-reviewed commit: %+v", launcher)
	}
	if len(store.runs) != 1 {
		t.Fatalf("should not insert another run: %+v", store.runs)
	}
}

// Choosing a different reviewer is a request for a second opinion on this exact
// commit. Before this, an approved commit was skipped before the harness was
// even consulted, so the picker looked broken precisely when a user would reach
// for it: pick another agent, nothing happens.
func TestTriggerRunsAnotherHarnessOnAnAlreadyApprovedCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{alive: true}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created {
		t.Fatalf("a different harness should start a new pass: %+v", res)
	}
	if len(store.runs) != 2 {
		t.Fatalf("expected a second run for the other harness, got %d: %+v", len(store.runs), store.runs)
	}
	if res.Run.Harness != domain.ReviewerCodex {
		t.Fatalf("new run should record the requested harness, got %q", res.Run.Harness)
	}
}

func TestTriggerHarnessOverrideDoesNotRestartRunningReviewJustBecauseResolvedConfigIsNonEmpty(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "codex-pane"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunRunning, Verdict: domain.VerdictNone,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, handle: "codex-pane"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created {
		t.Fatalf("different harness should still create a second-opinion run: %+v", res)
	}
	if len(store.runs) != 2 {
		t.Fatalf("runs = %+v, want original running pass plus new second-opinion run", store.runs)
	}
	if got := store.runs[0]; got.Status != domain.ReviewRunRunning || got.Harness != domain.ReviewerClaudeCode {
		t.Fatalf("original run = %+v, want unchanged running claude-code pass", got)
	}
	if got := store.runs[1]; got.Harness != domain.ReviewerCodex || got.Status != domain.ReviewRunRunning {
		t.Fatalf("new run = %+v, want running codex second-opinion pass", got)
	}
	if launcher.notified {
		t.Fatalf("different harness should spawn a second-opinion reviewer, not notify existing pane: %+v", launcher)
	}
	if !launcher.spawned {
		t.Fatalf("expected a new reviewer process for the different harness: %+v", launcher)
	}
}

// The project default must not re-review an approved commit on every trigger.
// Only an explicit pick counts as asking for a second opinion.
func TestTriggerWithoutOverrideStillSkipsAnApprovedCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{alive: true}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created || len(store.runs) != 1 {
		t.Fatalf("no override should still reuse the existing pass: created=%v runs=%+v", res.Created, store.runs)
	}
}

func TestTriggerWithPersistedReviewerConfigStillReusesSameCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created || res.Run.ID != "run-1" || len(store.runs) != 1 {
		t.Fatalf("same persisted config should reuse existing pass: %+v runs=%+v", res, store.runs)
	}
	if launcher.spawned || launcher.notified {
		t.Fatalf("should not relaunch for unchanged persisted reviewer config: %+v", launcher)
	}
}

func TestTriggerWithPersistedReviewerConfigAndSameHarnessOverrideStillReusesSameCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created || res.Run.ID != "run-1" || len(store.runs) != 1 {
		t.Fatalf("same persisted config with same harness override should reuse existing pass: %+v runs=%+v", res, store.runs)
	}
	if launcher.spawned || launcher.notified {
		t.Fatalf("should not relaunch for unchanged persisted reviewer config with same harness override: %+v", launcher)
	}
}

// Re-picking the harness that already reviewed this commit is not a second
// opinion, so it must still reuse rather than run the same agent twice.
func TestTriggerWithSameHarnessOverrideStillReuses(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{alive: true}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created || len(store.runs) != 1 {
		t.Fatalf("same harness should reuse: created=%v runs=%+v", res.Created, store.runs)
	}
}

func TestTriggerConfigOnlyOverrideUsesResolvedHarnessAndConfig(t *testing.T) {
	store := &fakeStore{
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{handle: "review-mer-2"}
	projects := fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{
		Harness:     domain.ReviewerClaudeCode,
		AgentConfig: domain.AgentConfig{Model: "claude-old"},
	}}}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), projects, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{Model: "claude-new"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created {
		t.Fatalf("config-only override should force a new pass: %+v", res)
	}
	if res.Run.Harness != domain.ReviewerClaudeCode {
		t.Fatalf("run harness = %q, want resolved claude-code", res.Run.Harness)
	}
	if got := launcher.gotSpec.AgentConfig; got.Model != "claude-new" {
		t.Fatalf("spawn config = %+v, want model override preserved", got)
	}
}

func TestTriggerConfigOnlyOverrideMergesResolvedConfig(t *testing.T) {
	store := &fakeStore{
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{handle: "review-mer-2"}
	projects := fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{
		Harness:     domain.ReviewerClaudeCode,
		AgentConfig: domain.AgentConfig{Model: "claude-old", Permissions: domain.PermissionModeBypassPermissions},
	}}}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), projects, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{Model: "claude-new"}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if got := launcher.gotSpec.AgentConfig; got.Model != "claude-new" || got.Permissions != domain.PermissionModeBypassPermissions {
		t.Fatalf("spawn config = %+v, want merged override plus inherited permissions", got)
	}
}

func TestTriggerSameHarnessOverrideMergesResolvedConfig(t *testing.T) {
	store := &fakeStore{
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	launcher := &fakeLauncher{handle: "review-mer-2"}
	projects := fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{
		Harness:     domain.ReviewerClaudeCode,
		AgentConfig: domain.AgentConfig{Model: "claude-old", Permissions: domain.PermissionModeBypassPermissions},
	}}}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), projects, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "claude-new"}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if got := launcher.gotSpec.AgentConfig; got.Model != "claude-new" || got.Permissions != domain.PermissionModeBypassPermissions {
		t.Fatalf("spawn config = %+v, want explicit same-harness override merged with inherited permissions", got)
	}
}

func TestReviewerSelectionMergesSessionConfigWithProjectReviewerConfig(t *testing.T) {
	worker := liveWorker()
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	eng := newEngineForTest(&fakeStore{}, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{
		Harness:     domain.ReviewerClaudeCode,
		AgentConfig: domain.AgentConfig{Permissions: domain.PermissionModeBypassPermissions},
	}}}}, &fakeLauncher{})

	harness, config, err := eng.reviewerSelection(context.Background(), worker)
	if err != nil {
		t.Fatalf("reviewerSelection: %v", err)
	}
	if harness != domain.ReviewerClaudeCode {
		t.Fatalf("harness = %q, want claude-code", harness)
	}
	if config.Model != "gpt-5" || config.Permissions != domain.PermissionModeBypassPermissions {
		t.Fatalf("config = %+v, want merged session override + project permissions", config)
	}
}

func TestTriggerConfigOverrideRestartsAliveReviewerPane(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-2"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "gpt-5-mini"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created {
		t.Fatalf("expected new run for config override: %+v", res)
	}
	if !launcher.destroyed || launcher.destroyedHandle != "review-mer-1" {
		t.Fatalf("expected old reviewer pane destroyed after replacement launch: %+v", launcher)
	}
	if !launcher.spawned || launcher.notified {
		t.Fatalf("config override should relaunch, not notify existing pane: %+v", launcher)
	}
	if got := launcher.gotSpec.AgentConfig.Model; got != "gpt-5-mini" {
		t.Fatalf("spawn config model = %q, want gpt-5-mini", got)
	}
	if store.review.AgentSessionID != "" {
		t.Fatalf("replacement should clear stale native session id when launch reports none, got %q", store.review.AgentSessionID)
	}
}

func TestTriggerConfigOverrideRollbackUpsertFailureKeepsReplacementHandleTracked(t *testing.T) {
	store := &fakeStore{
		review:        &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		upsertErr:     errors.New("rollback failed"),
		upsertErrCall: 3,
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-2", destroyErr: errors.New("destroy old failed"), destroyErrCall: 1}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "gpt-5-mini"}); err == nil || !strings.Contains(err.Error(), "rollback review row") {
		t.Fatalf("Trigger error = %v, want rollback review row failure", err)
	}
	if store.review == nil || store.review.ReviewerHandleID != "review-mer-2" || store.review.AgentSessionID != "" {
		t.Fatalf("stored review row = %+v, want replacement handle to remain authoritative", store.review)
	}
	if launcher.destroyCalls != 1 || launcher.destroyedHandle != "review-mer-1" {
		t.Fatalf("launcher destroy calls = %d handle=%q, want only failed old-handle destroy", launcher.destroyCalls, launcher.destroyedHandle)
	}
}

func TestTriggerConfigOverrideDestroyPreviousFailureRestoresOldNativeSessionID(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-2", destroyErr: errors.New("destroy old failed"), destroyErrCall: 1}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "gpt-5-mini"}); err == nil || !strings.Contains(err.Error(), "destroy previous reviewer") {
		t.Fatalf("Trigger error = %v, want destroy previous reviewer failure", err)
	}
	if store.review == nil || store.review.ReviewerHandleID != "review-mer-1" || store.review.AgentSessionID != "native-reviewer-1" {
		t.Fatalf("stored review row = %+v, want rollback to old handle and native session id", store.review)
	}
}

func TestTriggerConfigOverrideFinalUpsertFailurePreservesStoredReviewerHandle(t *testing.T) {
	store := &fakeStore{
		review:        &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		upsertErr:     errors.New("write failed"),
		upsertErrCall: 2,
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-2"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "gpt-5-mini"}); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("Trigger error = %v, want final upsert failure", err)
	}
	if !launcher.spawned || !launcher.destroyed || launcher.destroyedHandle != "review-mer-2" {
		t.Fatalf("replacement reviewer should be cleaned up after upsert failure: %+v", launcher)
	}
	if store.review == nil || store.review.ReviewerHandleID != "review-mer-1" || store.review.AgentSessionID != "" {
		t.Fatalf("stored review row = %+v, want old handle retained with cleared stale native id", store.review)
	}
}

func TestTriggerConfigOverridePreflightFailurePreservesRunningReviewer(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunRunning, Verdict: domain.VerdictNone,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, preflightErr: errors.New("missing binary")}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha2"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "gpt-5-mini"}); err == nil || !strings.Contains(err.Error(), "reviewer preflight") {
		t.Fatalf("Trigger error = %v, want reviewer preflight failure", err)
	}
	if !launcher.preflighted || launcher.destroyed || launcher.notified || launcher.spawned {
		t.Fatalf("replacement should fail before touching the live reviewer: %+v", launcher)
	}
	if got := store.runs[0]; got.Status != domain.ReviewRunRunning {
		t.Fatalf("running review should remain active, got %+v", got)
	}
	if len(store.runs) != 2 || store.runs[1].Status != domain.ReviewRunFailed {
		t.Fatalf("runs = %+v, want old running run plus failed replacement run", store.runs)
	}
}

func TestTriggerConfigOverrideLaunchFailurePreservesRunningReviewer(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunRunning, Verdict: domain.VerdictNone,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, spawnErr: errors.New("create failed")}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha2"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "gpt-5-mini"}); err == nil || !strings.Contains(err.Error(), "launch reviewer") {
		t.Fatalf("Trigger error = %v, want launch failure", err)
	}
	if !launcher.preflighted || !launcher.spawned || launcher.destroyed || launcher.notified {
		t.Fatalf("replacement should fail without cancelling the live reviewer state first: %+v", launcher)
	}
	if got := store.runs[0]; got.Status != domain.ReviewRunRunning {
		t.Fatalf("running review should remain active, got %+v", got)
	}
	if len(store.runs) != 2 || store.runs[1].Status != domain.ReviewRunFailed {
		t.Fatalf("runs = %+v, want old running run plus failed replacement run", store.runs)
	}
}

func TestTriggerRejectsInvalidReviewerConfig(t *testing.T) {
	eng := newEngineForTest(&fakeStore{}, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{})
	if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{Mode: "turbo"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestTriggerConfigOverrideWhileSameCommitRunningRestartsReview(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Harness: domain.ReviewerClaudeCode,
			Status:  domain.ReviewRunRunning, Verdict: domain.VerdictNone,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerClaudeCode
	worker.ReviewerConfig = domain.AgentConfig{Model: "gpt-5"}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-2"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{Model: "gpt-5-mini"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || res.Run.ID != "run-1" {
		t.Fatalf("expected running same-commit config override to restart the existing run as a fresh pass: %+v", res)
	}
	if !launcher.spawned || launcher.notified {
		t.Fatalf("config override should relaunch, not reuse running pane: %+v", launcher)
	}
	if got := launcher.gotSpec.AgentConfig.Model; got != "gpt-5-mini" {
		t.Fatalf("spawn config model = %q, want gpt-5-mini", got)
	}
	if got := store.runs[0]; got.Status != domain.ReviewRunRunning {
		t.Fatalf("running run should stay active in storage while the pane restarts, got %+v", got)
	}
	if len(store.runs) != 1 {
		t.Fatalf("runs = %+v, want the existing running run to be reused", store.runs)
	}
}

func TestTriggerReusesRunningRowWithNoVerdict(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs:   []domain.ReviewRun{{ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning}},
	}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-2"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created || res.Run.ID != "run-1" {
		t.Fatalf("expected reuse of the running review for the same commit: %+v", res)
	}
	if launcher.spawned || launcher.notified {
		t.Fatalf("running same-commit review should not relaunch: %+v", launcher)
	}
	if got := store.runs[0]; got.Status != domain.ReviewRunRunning {
		t.Fatalf("running row should remain running, got %+v", got)
	}
}

func TestTriggerRetriesRunningRowWhenReviewerDead(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs:   []domain.ReviewRun{{ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning}},
	}
	launcher := &fakeLauncher{alive: false, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || res.Run.ID == "run-1" {
		t.Fatalf("expected stale running review to be retried with a new run: %+v", res)
	}
	if !launcher.spawned || launcher.notified {
		t.Fatalf("expected reviewer relaunch for stale running row: %+v", launcher)
	}
	if store.runs[0].Status != domain.ReviewRunCancelled {
		t.Fatalf("stale running row = %+v, want cancelled", store.runs[0])
	}
	if len(store.runs) != 2 || store.runs[1].Status != domain.ReviewRunRunning {
		t.Fatalf("runs = %+v, want cancelled old run and new running run", store.runs)
	}
}

func TestTriggerRetriesTerminalRowWithNoVerdict(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-empty-verdict", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Status: domain.ReviewRunComplete, Verdict: domain.VerdictNone,
		}},
	}
	launcher := &fakeLauncher{handle: "review-mer-2"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || res.Run.ID == "run-empty-verdict" {
		t.Fatalf("expected retry to create a new run, got %+v", res)
	}
	if len(store.runs) != 2 || !launcher.spawned || launcher.restored || launcher.notified {
		t.Fatalf("expected fresh launch/run after terminal empty-verdict row: launcher=%+v runs=%+v", launcher, store.runs)
	}
}

func TestTriggerReusesReviewerOnNewCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs:   []domain.ReviewRun{{ID: "run-0", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha0", Status: domain.ReviewRunComplete}},
	}
	launcher := &fakeLauncher{alive: true}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if launcher.spawned || !launcher.notified {
		t.Fatalf("expected live reviewer process to be notified: %+v", launcher)
	}
	if launcher.preflighted {
		t.Fatal("expected live reviewer process not to be preflighted")
	}
	if !res.Created || res.Run.TargetSHA != "sha1" || len(store.runs) != 2 {
		t.Fatalf("expected a new run for sha1: res=%+v runs=%+v", res, store.runs)
	}
}

func TestTriggerSupersedesOlderRunningRunOnNewCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs:   []domain.ReviewRun{{ID: "run-old", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha0", Status: domain.ReviewRunRunning}},
	}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || res.Run.TargetSHA != "sha1" {
		t.Fatalf("expected new run for new commit, got %+v", res)
	}
	if old := store.runs[0]; old.ID != "run-old" || old.Status != domain.ReviewRunFailed {
		t.Fatalf("expected older running run to be failed, got %+v", old)
	}
	if launcher.spawned || !launcher.notified {
		t.Fatalf("expected reviewer process to be notified for new commit: %+v", launcher)
	}
}

func TestTriggerReusesRunningReviewerBeforeAgentSessionIDRecorded(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1"},
		runs:   []domain.ReviewRun{{ID: "run-old", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha0", Status: domain.ReviewRunRunning}},
	}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !launcher.notified || launcher.spawned {
		t.Fatalf("expected running reviewer pane reused before native session id is recorded: %+v", launcher)
	}
}

func TestTriggerRestoresWhenRecordedReviewerDead(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs:   []domain.ReviewRun{{ID: "run-0", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha0", Status: domain.ReviewRunComplete}},
	}
	launcher := &fakeLauncher{alive: false, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !launcher.spawned || launcher.restored || launcher.notified {
		t.Fatalf("expected spawn when reviewer dead: %+v", launcher)
	}
}

func TestTriggerSpawnsFreshPassForNonReusableReviewer(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerAuggie, ReviewerHandleID: "review-mer-1"},
		runs:   []domain.ReviewRun{{ID: "run-0", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha0", Status: domain.ReviewRunComplete}},
	}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1", reusableSet: true, reusable: false}
	projects := fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{Harness: domain.ReviewerAuggie}}}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), projects, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || res.Run.TargetSHA != "sha1" {
		t.Fatalf("expected a fresh review run for sha1, got %+v", res)
	}
	if !launcher.spawned || launcher.restored || launcher.notified || launcher.aliveChecked {
		t.Fatalf("Auggie reviewer should spawn fresh without alive/restore/notify reuse: %+v", launcher)
	}
	if launcher.gotSpec.Harness != domain.ReviewerAuggie {
		t.Fatalf("spawn harness = %q, want auggie", launcher.gotSpec.Harness)
	}
}

func TestTriggerRespawnsLivePaneWithoutReviewerAgentSession(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1"},
		runs:   []domain.ReviewRun{{ID: "run-0", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha0", Status: domain.ReviewRunComplete}},
	}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !launcher.spawned || launcher.notified {
		t.Fatalf("expected spawn when live pane has no native reviewer session id: %+v", launcher)
	}
}

// A live reviewer pane launched under a previous harness must be respawned under
// the newly-resolved harness, not reused via Notify: the pane's sandbox/
// permissions/env are fixed at Spawn, so reusing a codex pane to serve a
// claude-code review (or vice versa) would run under the wrong profile.
func TestTriggerRespawnsWhenReviewerHarnessChanged(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs:   []domain.ReviewRun{{ID: "run-0", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha0", Status: domain.ReviewRunComplete}},
	}
	// Live pane exists (alive), but the worker/project now resolves to claude-code
	// while the pane was launched under codex.
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !launcher.spawned || launcher.restored || launcher.notified {
		t.Fatalf("expected respawn under the new harness, not reuse via notify: %+v", launcher)
	}
	if launcher.gotSpec.Harness != domain.ReviewerClaudeCode {
		t.Fatalf("respawn harness = %q, want claude-code", launcher.gotSpec.Harness)
	}
	if res.Run.Harness != domain.ReviewerClaudeCode {
		t.Fatalf("run harness = %q, want claude-code", res.Run.Harness)
	}
}

// A harness switch observed on a trigger that creates no run (the current
// commit is already reviewed) must NOT advance the recorded harness. The live
// pane keeps running under the previous harness, so recording the new harness
// on this no-created path would make the next trigger read it back as
// prevHarness, match the resolved harness, and reuse (Notify) the stale pane.
func TestTriggerSwitchKillsOldReviewerWhenNothingCreated(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	// Live codex pane; the worker/project now resolves to claude-code.
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created || launcher.spawned || launcher.notified {
		t.Fatalf("already-reviewed commit: expected no launch, got res=%+v launcher=%+v", res, launcher)
	}
	if !launcher.destroyed || launcher.destroyedHandle != "review-mer-1" {
		t.Fatalf("expected old reviewer pane to be destroyed on switch: %+v", launcher)
	}
	if store.review.Harness != domain.ReviewerClaudeCode || store.review.ReviewerHandleID != "" || store.review.AgentSessionID != "" {
		t.Fatalf("recorded active reviewer after switch = %+v, want claude-code with no active pane", store.review)
	}
}

// End-to-end of the blocker: a harness switch seen on a no-run trigger must not
// defeat respawn on the next commit. Before the fix, the eager upsert recorded
// the new harness on the no-created trigger, so the following commit saw
// prevHarness == harness and reused (Notify) the stale old-harness pane.
func TestTriggerRespawnsOnNextCommitAfterHarnessSwitchWithNoRun(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		}},
	}
	// Trigger 1: current commit sha1 is already reviewed → no run created, while
	// the worker now resolves to claude-code but the live pane is still codex.
	l1 := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng1 := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, l1)
	if _, err := eng1.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); err != nil {
		t.Fatalf("trigger 1: %v", err)
	}
	if l1.spawned || l1.notified {
		t.Fatalf("trigger 1 (nothing new) should not launch: %+v", l1)
	}

	// Trigger 2: a new commit arrives → a run is created. The reviewer must
	// respawn under claude-code, not Notify the stale codex pane.
	l2 := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng2 := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha2"), fakeProjects{}, l2)
	res, err := eng2.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("trigger 2: %v", err)
	}
	if !res.Created || !l2.spawned || l2.restored || l2.notified {
		t.Fatalf("trigger 2 must respawn under the new harness, not reuse the stale pane: res=%+v launcher=%+v", res, l2)
	}
	if l2.gotSpec.Harness != domain.ReviewerClaudeCode {
		t.Fatalf("respawn harness = %q, want claude-code", l2.gotSpec.Harness)
	}
}

func TestTriggerLaunchFailureRecordsFailedRun(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{spawnErr: fmt.Errorf("claude: %w", ports.ErrAgentBinaryNotFound)}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("err = %v, want ports.ErrAgentBinaryNotFound", err)
	}
	if store.review == nil || len(store.runs) != 1 {
		t.Fatalf("expected persisted failed review/run: review=%+v runs=%+v", store.review, store.runs)
	}
	run := store.runs[0]
	if run.Status != domain.ReviewRunFailed || run.Verdict != domain.VerdictNone {
		t.Fatalf("run = %+v, want failed with no verdict", run)
	}
	if !strings.Contains(run.Body, "claude") || !strings.Contains(run.Body, ports.ErrAgentBinaryNotFound.Error()) {
		t.Fatalf("run body = %q, want launch cause", run.Body)
	}
}

func TestTriggerRetriesAfterFailedRunForSameCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs:   []domain.ReviewRun{{ID: "run-failed", ReviewID: "rev-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunFailed}},
	}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || res.Run.ID == "run-failed" {
		t.Fatalf("expected retry to create a new run, got %+v", res)
	}
	if len(store.runs) != 2 || !launcher.spawned || launcher.restored || launcher.notified {
		t.Fatalf("expected fresh launch/run after failed pass: launcher=%+v runs=%+v", launcher, store.runs)
	}
}

func TestTriggerRetriesAfterCancelledRunForSameCommit(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs:   []domain.ReviewRun{{ID: "run-cancelled", ReviewID: "rev-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunCancelled}},
	}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || res.Run.ID == "run-cancelled" {
		t.Fatalf("expected retry to create a new run, got %+v", res)
	}
	if len(store.runs) != 2 || !launcher.spawned || launcher.restored || launcher.notified {
		t.Fatalf("expected fresh launch/run after cancelled pass: launcher=%+v runs=%+v", launcher, store.runs)
	}
}

func TestAutoTriggerWaitsForNewCommitAfterCancelledRun(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs:   []domain.ReviewRun{{ID: "run-cancelled", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunCancelled}},
	}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.TriggerWithSource(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{}, domain.ReviewTriggerAuto)
	if err != nil {
		t.Fatalf("TriggerWithSource: %v", err)
	}
	if res.Created || len(store.runs) != 1 || launcher.spawned || launcher.notified {
		t.Fatalf("cancelled auto-review reran unchanged head: result=%+v launcher=%+v runs=%+v", res, launcher, store.runs)
	}
}

func TestAutoTriggerStopsRetryingAfterThreeFailedRunsOnSameHead(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{
			{ID: "run-failed-1", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, TriggerSource: domain.ReviewTriggerAuto, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunFailed},
			{ID: "run-failed-2", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, TriggerSource: domain.ReviewTriggerAuto, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunFailed},
			{ID: "run-failed-3", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, TriggerSource: domain.ReviewTriggerAuto, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunFailed},
		},
	}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.TriggerWithSource(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{}, domain.ReviewTriggerAuto)
	if err != nil {
		t.Fatalf("TriggerWithSource: %v", err)
	}
	if res.Created || len(store.runs) != 3 || launcher.spawned || launcher.notified {
		t.Fatalf("auto-review retried past the per-head failure cap: result=%+v launcher=%+v runs=%+v", res, launcher, store.runs)
	}
}

func TestAutoReviewHeadBlockedCapsFailedAutoRetriesPerHeadAndHarness(t *testing.T) {
	prURL := "https://github.com/o/r/pull/1"
	harness := domain.ReviewerClaudeCode
	tests := []struct {
		name string
		runs []domain.ReviewRun
		want bool
	}{
		{
			name: "two failed auto runs do not block",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "sha1", Harness: harness, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: harness, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
			},
			want: false,
		},
		{
			name: "three failed auto runs block",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "sha1", Harness: harness, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: harness, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: harness, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
			},
			want: true,
		},
		{
			name: "other harness does not count toward the cap",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
			},
			want: false,
		},
		{
			name: "other head sha does not count toward the cap",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "old-sha", Harness: harness, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "old-sha", Harness: harness, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "old-sha", Harness: harness, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
			},
			want: false,
		},
		{
			name: "manual failures do not count toward the cap",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "sha1", Harness: harness, TriggerSource: domain.ReviewTriggerManual, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: harness, TriggerSource: domain.ReviewTriggerManual, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: harness, TriggerSource: domain.ReviewTriggerManual, Status: domain.ReviewRunFailed},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autoReviewHeadBlocked(tt.runs, prURL, "sha1", harness); got != tt.want {
				t.Fatalf("autoReviewHeadBlocked() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAutoTriggerSkipsCappedFailedHeadButRunsOtherEligiblePRs(t *testing.T) {
	pr1 := "https://github.com/o/r/pull/1"
	pr2 := "https://github.com/o/r/pull/2"
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode},
		runs: []domain.ReviewRun{
			{ID: "run-failed-1", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, TriggerSource: domain.ReviewTriggerAuto, PRURL: pr1, TargetSHA: "sha1", Status: domain.ReviewRunFailed},
			{ID: "run-failed-2", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, TriggerSource: domain.ReviewTriggerAuto, PRURL: pr1, TargetSHA: "sha1", Status: domain.ReviewRunFailed},
			{ID: "run-failed-3", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, TriggerSource: domain.ReviewTriggerAuto, PRURL: pr1, TargetSHA: "sha1", Status: domain.ReviewRunFailed},
		},
	}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	prs := fakePRs{prs: []domain.PullRequest{
		{URL: pr1, Number: 1, HeadSHA: "sha1"},
		{URL: pr2, Number: 2, HeadSHA: "sha2"},
	}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prs, fakeProjects{}, launcher)

	res, err := eng.TriggerWithSource(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{}, domain.ReviewTriggerAuto)
	if err != nil {
		t.Fatalf("TriggerWithSource: %v", err)
	}
	if !res.Created || len(res.CreatedRuns) != 1 || res.CreatedRuns[0].PRURL != pr2 {
		t.Fatalf("auto-review did not skip only the capped head: result=%+v", res)
	}
	if len(store.runs) != 4 {
		t.Fatalf("runs=%+v, want only one new run for the uncapped head", store.runs)
	}
}

func TestAutoTriggerDoesNotAppendPRToLiveRunningReviewer(t *testing.T) {
	pr1 := "https://github.com/o/r/pull/1"
	pr2 := "https://github.com/o/r/pull/2"
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode,
			PRURL: pr1, TargetSHA: "sha1", Status: domain.ReviewRunRunning,
		}},
	}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	prs := fakePRs{prs: []domain.PullRequest{
		{URL: pr1, Number: 1, HeadSHA: "sha1"},
		{URL: pr2, Number: 2, HeadSHA: "sha2"},
	}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prs, fakeProjects{}, launcher)

	res, err := eng.TriggerWithSource(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{}, domain.ReviewTriggerAuto)
	if err != nil {
		t.Fatalf("TriggerWithSource: %v", err)
	}
	if res.Created || len(store.runs) != 1 {
		t.Fatalf("live running reviewer accepted another PR: result=%+v runs=%+v", res, store.runs)
	}
	if !launcher.aliveChecked || launcher.spawned || launcher.notified {
		t.Fatalf("auto trigger should only check liveness: %+v", launcher)
	}
}

func TestAutoTriggerReconcilesDeadReviewerBeforeRunningGate(t *testing.T) {
	pr1 := "https://github.com/o/r/pull/1"
	pr2 := "https://github.com/o/r/pull/2"
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1"},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode,
			PRURL: pr1, TargetSHA: "sha1", Status: domain.ReviewRunRunning,
		}},
	}
	launcher := &fakeLauncher{alive: false, handle: "review-mer-1"}
	prs := fakePRs{prs: []domain.PullRequest{
		{URL: pr1, Number: 1, HeadSHA: "sha1"},
		{URL: pr2, Number: 2, HeadSHA: "sha2"},
	}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prs, fakeProjects{}, launcher)

	res, err := eng.TriggerWithSource(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{}, domain.ReviewTriggerAuto)
	if err != nil {
		t.Fatalf("TriggerWithSource: %v", err)
	}
	if !res.Created || res.Run.PRURL != pr2 {
		t.Fatalf("dead reviewer did not release pending PR: result=%+v", res)
	}
	if store.runs[0].Status != domain.ReviewRunCancelled || len(store.runs) != 2 {
		t.Fatalf("stale run was not reconciled before retry: %+v", store.runs)
	}
	if !launcher.aliveChecked || !launcher.spawned || launcher.notified {
		t.Fatalf("pending PR should launch a fresh reviewer: %+v", launcher)
	}
}

func TestAutoTriggerRevalidatesSessionPolicyUnderLock(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*domain.SessionRecord)
		wantReason string
	}{
		{name: "disabled", mutate: func(worker *domain.SessionRecord) { worker.AutoReviewEnabled = false }, wantReason: "disabled"},
		{name: "active", mutate: func(worker *domain.SessionRecord) { worker.Activity.State = domain.ActivityActive }, wantReason: "not_idle"},
		{name: "idle threshold reset", mutate: func(worker *domain.SessionRecord) { worker.Activity.LastActivityAt = time.Unix(-59, 0).UTC() }, wantReason: "idle_threshold_not_met"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := liveWorker()
			tt.mutate(&worker)
			store := &fakeStore{}
			launcher := &fakeLauncher{handle: "review-mer-1"}
			eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)

			result, err := eng.TriggerWithSource(context.Background(), "mer-1", domain.ReviewerClaudeCode, domain.AgentConfig{}, domain.ReviewTriggerAuto)
			if err != nil {
				t.Fatalf("TriggerWithSource: %v", err)
			}
			if result.Created || result.SkipReason != tt.wantReason || len(store.runs) != 0 || launcher.spawned {
				t.Fatalf("result=%+v runs=%+v launcher=%+v", result, store.runs, launcher)
			}
		})
	}
}

func TestTriggerCreatesRunsForMultipleEligiblePRsWithOneReviewer(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	prs := fakePRs{prs: []domain.PullRequest{
		{URL: "https://github.com/o/r/pull/1", Number: 1, HeadSHA: "sha1"},
		{URL: "https://github.com/o/r/pull/2", Number: 2, HeadSHA: "sha2"},
	}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prs, fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || len(res.CreatedRuns) != 2 || len(store.runs) != 2 {
		t.Fatalf("created batch = %+v runs=%+v", res, store.runs)
	}
	if res.CreatedRuns[0].BatchID == "" || res.CreatedRuns[0].BatchID != res.CreatedRuns[1].BatchID {
		t.Fatalf("created runs should share one batch id: %+v", res.CreatedRuns)
	}
	if launcher.spawnCount != 1 || len(launcher.handles) != 0 {
		t.Fatalf("expected one spawn and no extra notify, launcher=%+v", launcher)
	}
	if len(launcher.specs) != 1 {
		t.Fatalf("launch specs = %d, want 1: %+v", len(launcher.specs), launcher.specs)
	}
	spec := launcher.specs[0]
	if spec.BatchID != res.CreatedRuns[0].BatchID {
		t.Fatalf("launch spec batch id %q != created batch %q", spec.BatchID, res.CreatedRuns[0].BatchID)
	}
	if spec.ReviewIndex != 0 || len(spec.ReviewQueue) != 2 {
		t.Fatalf("spec queue context = index %d queue %+v", spec.ReviewIndex, spec.ReviewQueue)
	}
	if spec.ReviewQueue[0].PRURL != "https://github.com/o/r/pull/1" || spec.ReviewQueue[1].PRURL != "https://github.com/o/r/pull/2" {
		t.Fatalf("spec queue URLs = %+v", spec.ReviewQueue)
	}
	if store.review == nil || store.review.ReviewerHandleID != "review-mer-1" || store.review.PRURL != "" {
		t.Fatalf("review row = %+v, want shared handle and no behavioral pr_url", store.review)
	}
}

func TestTriggerAllowsTwoPRsWithSameHeadSHA(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	prs := fakePRs{prs: []domain.PullRequest{
		{URL: "https://github.com/o/r/pull/1", Number: 1, HeadSHA: "same"},
		{URL: "https://github.com/o/r/pull/2", Number: 2, HeadSHA: "same"},
	}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prs, fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if len(res.CreatedRuns) != 2 {
		t.Fatalf("created runs = %d, want 2: %+v", len(res.CreatedRuns), res.CreatedRuns)
	}
	if store.runs[0].PRURL == store.runs[1].PRURL || store.runs[0].TargetSHA != store.runs[1].TargetSHA {
		t.Fatalf("runs should differ by PR only: %+v", store.runs)
	}
}

func TestTriggerRerunsApprovedAndReusesRunningCurrentHead(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{
			{ID: "approved", ReviewID: "rev-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, CreatedAt: time.Unix(1, 0)},
			{ID: "running", ReviewID: "rev-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/2", TargetSHA: "sha2", Status: domain.ReviewRunRunning, CreatedAt: time.Unix(2, 0)},
		},
	}
	launcher := &fakeLauncher{alive: true}
	prs := fakePRs{prs: []domain.PullRequest{
		{URL: "https://github.com/o/r/pull/1", Number: 1, HeadSHA: "sha1"},
		{URL: "https://github.com/o/r/pull/2", Number: 2, HeadSHA: "sha2"},
	}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prs, fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Created || len(res.CreatedRuns) != 0 {
		t.Fatalf("expected no rerun for the approved PR: res=%+v", res)
	}
	if launcher.notified || launcher.spawned {
		t.Fatalf("reviewer should not receive the approved PR rerun: %+v", launcher)
	}
	if launcher.preflighted {
		t.Fatal("expected preflight not to run")
	}
	if len(res.Reviews) != 2 || res.Reviews[0].Status != ReviewStateUpToDate || res.Reviews[1].Status != ReviewStateRunning {
		t.Fatalf("review states = %+v", res.Reviews)
	}
}

func TestTriggerRerunsChangesRequestedCurrentHead(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1", AgentSessionID: "native-reviewer-1"},
		runs: []domain.ReviewRun{{
			ID: "changes", ReviewID: "rev-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1",
			Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested, CreatedAt: time.Unix(1, 0),
		}},
	}
	launcher := &fakeLauncher{alive: true, handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !res.Created || len(res.CreatedRuns) != 1 || !launcher.notified || launcher.spawned {
		t.Fatalf("expected changes_requested current head to rerun on same reviewer: res=%+v launcher=%+v", res, launcher)
	}
	if res.CreatedRuns[0].Harness != domain.ReviewerClaudeCode || res.CreatedRuns[0].TargetSHA != "sha1" {
		t.Fatalf("created run = %+v, want same-harness retry for sha1", res.CreatedRuns[0])
	}
}

func TestTriggerUsesConfiguredReviewerHarness(t *testing.T) {
	store := &fakeStore{}
	projects := fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{Harness: domain.ReviewerHarness("greptile")}}}}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), projects, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Run.Harness != domain.ReviewerHarness("greptile") || launcher.gotSpec.Harness != domain.ReviewerHarness("greptile") {
		t.Fatalf("harness not used: run=%+v spec=%+v", res.Run, launcher.gotSpec)
	}
}

func TestTriggerUsesSessionReviewerHarnessBeforeProjectDefault(t *testing.T) {
	store := &fakeStore{}
	projects := fakeProjects{cfg: domain.ProjectConfig{Reviewers: []domain.ReviewerConfig{{Harness: domain.ReviewerCodex}}}}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerOpenCode
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), projects, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if res.Run.Harness != domain.ReviewerOpenCode || launcher.gotSpec.Harness != domain.ReviewerOpenCode {
		t.Fatalf("session harness not used: run=%+v spec=%+v", res.Run, launcher.gotSpec)
	}
}

func TestTriggerRejectsBadWorkerState(t *testing.T) {
	t.Run("unknown worker", func(t *testing.T) {
		eng := newEngineForTest(&fakeStore{}, fakeSessions{ok: false}, prAt("sha1"), fakeProjects{}, &fakeLauncher{})
		if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("no pr", func(t *testing.T) {
		eng := newEngineForTest(&fakeStore{}, fakeSessions{rec: liveWorker(), ok: true}, fakePRs{}, fakeProjects{}, &fakeLauncher{})
		if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	})
}

func TestListReturnsHandleAndRuns(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode, ReviewerHandleID: "review-mer-1"},
		runs:   []domain.ReviewRun{{ID: "run-1", SessionID: "mer-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1"}},
	}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{})
	got, err := eng.List(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.ReviewerHandleID != "review-mer-1" || got.ReviewerHarness != domain.ReviewerClaudeCode || len(got.Runs) != 1 {
		t.Fatalf("list = %+v", got)
	}
}

func TestListReturnsActiveOneShotOverrideHandle(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "rev-1", SessionID: "mer-1", Harness: domain.ReviewerOpenCode, ReviewerHandleID: "opencode-pane"},
		runs: []domain.ReviewRun{{
			ID:        "run-1",
			SessionID: "mer-1",
			Harness:   domain.ReviewerOpenCode,
			PRURL:     "https://github.com/o/r/pull/1",
			TargetSHA: "sha1",
			Status:    domain.ReviewRunRunning,
		}},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{})

	got, err := eng.List(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.ReviewerHandleID != "opencode-pane" || got.ReviewerHarness != domain.ReviewerOpenCode {
		t.Fatalf("list = %+v, want active opencode override handle", got)
	}
}

func TestTriggerPreflightFailureRecordsFailedRun(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{preflightErr: fmt.Errorf("codex: %w", ports.ErrAgentBinaryNotFound)}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	_, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err == nil {
		t.Fatal("expected error from preflight, got nil")
	}
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("err = %v, want wrapped ErrAgentBinaryNotFound", err)
	}
	if !launcher.preflighted {
		t.Fatal("expected Preflight to be called")
	}
	if launcher.spawned {
		t.Fatal("expected no spawn attempt when preflight fails")
	}
	if len(store.runs) != 1 {
		t.Fatalf("expected 1 review run (failed), got %d", len(store.runs))
	}
	run := store.runs[0]
	if run.Status != domain.ReviewRunFailed || run.Verdict != domain.VerdictNone {
		t.Fatalf("run = %+v, want failed with no verdict", run)
	}
	if !strings.Contains(run.Body, "codex") || !strings.Contains(run.Body, ports.ErrAgentBinaryNotFound.Error()) {
		t.Fatalf("run body = %q, want preflight cause", run.Body)
	}
}

func TestTriggerProceedsNormallyAfterSuccessfulPreflight(t *testing.T) {
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1"}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	res, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !launcher.preflighted {
		t.Fatal("expected Preflight to be called")
	}
	if !res.Created || res.ReviewerHandleID != "review-mer-1" {
		t.Fatalf("result = %+v", res)
	}
	if !launcher.spawned {
		t.Fatal("expected spawn after successful preflight")
	}
	if len(store.runs) != 1 {
		t.Fatalf("expected 1 review run, got %d", len(store.runs))
	}
}
