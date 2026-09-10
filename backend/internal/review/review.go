// Package review holds the core code-review logic: triggering a reviewer over a
// worker's worktree, recording review runs, and accepting submitted results.
//
// It is independent of any transport. The daemon's HTTP service
// (internal/service/review) is a thin boundary over this engine today, and the
// same engine can back an in-process CLI trigger later without going through the
// API. Transport-specific concerns (DTOs, error→status mapping) stay in the
// service/controller layers; the orchestration and run-id generation live here.
package review

import (
	stdctx "context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ErrInvalid and ErrNotFound let the transport layer map failures to 422/404.
var (
	ErrInvalid  = errors.New("review: invalid input")
	ErrNotFound = errors.New("review: not found")
)

// Store is the persistence surface the engine needs. *sqlite.Store satisfies it
// in production; tests use a fake.
type Store interface {
	UpsertReview(ctx stdctx.Context, r domain.Review) error
	SetSessionReviewerConfig(ctx stdctx.Context, id domain.SessionID, harness domain.ReviewerHarness, config domain.AgentConfig, updatedAt time.Time) (bool, error)
	GetReviewBySession(ctx stdctx.Context, id domain.SessionID) (domain.Review, bool, error)
	ClearReviewerHandle(ctx stdctx.Context, id domain.SessionID) error
	GetReviewBySessionAndHarness(ctx stdctx.Context, id domain.SessionID, harness domain.ReviewerHarness) (domain.Review, bool, error)
	ListReviewsBySession(ctx stdctx.Context, id domain.SessionID) ([]domain.Review, error)
	ClearReviewerHandleByHarness(ctx stdctx.Context, id domain.SessionID, harness domain.ReviewerHarness) error
	InsertReviewRun(ctx stdctx.Context, r domain.ReviewRun) error
	UpdateReviewRunResult(ctx stdctx.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, githubReviewID string, autoInjectReview bool) (bool, error)
	UpdateReviewAgentSessionID(ctx stdctx.Context, id, agentSessionID string) (bool, error)
	SupersedeStaleRunningReviewRuns(ctx stdctx.Context, sessionID domain.SessionID, prURL, targetSHA, body string) (int64, error)
	CancelRunningReviewRunsBySession(ctx stdctx.Context, sessionID domain.SessionID, body string) (int64, error)
	CancelRunningReviewRunsBySessionAndHarness(ctx stdctx.Context, sessionID domain.SessionID, harness domain.ReviewerHarness, body string) (int64, error)
	GetReviewRun(ctx stdctx.Context, id string) (domain.ReviewRun, bool, error)
	GetReviewRunBySessionPRAndSHA(ctx stdctx.Context, id domain.SessionID, prURL, targetSHA string) (domain.ReviewRun, bool, error)
	GetReviewRunBySessionPRSHAAndHarness(ctx stdctx.Context, id domain.SessionID, prURL, targetSHA string, harness domain.ReviewerHarness) (domain.ReviewRun, bool, error)
	ListReviewRunsBySession(ctx stdctx.Context, id domain.SessionID) ([]domain.ReviewRun, error)
	ListRunningReviewRunsBySession(ctx stdctx.Context, id domain.SessionID) ([]domain.ReviewRun, error)
}

// Sessions resolves the worker session under review.
type Sessions interface {
	GetSession(ctx stdctx.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
}

// PRs resolves the PR a worker owns.
type PRs interface {
	ListPRsBySession(ctx stdctx.Context, id domain.SessionID) ([]domain.PullRequest, error)
}

// Projects resolves the per-project reviewer config.
type Projects interface {
	GetProject(ctx stdctx.Context, id string) (domain.ProjectRecord, bool, error)
}

// Deps wires the engine.
type Deps struct {
	Store    Store
	Sessions Sessions
	PRs      PRs
	Projects Projects
	Launcher Launcher

	// Clock and NewID are injectable for deterministic tests.
	Clock func() time.Time
	NewID func() string
}

// Engine is the core code-review engine.
type Engine struct {
	store    Store
	sessions Sessions
	prs      PRs
	projects Projects
	launcher Launcher
	clock    func() time.Time
	newID    func() string

	// triggerMu guards triggerLocks; triggerLocks holds one mutex per worker
	// session so concurrent Trigger calls for the same worker serialise (see
	// lockWorker). Distinct workers never contend.
	triggerMu    sync.Mutex
	triggerLocks map[domain.SessionID]*sync.Mutex
}

const autoReviewFailedRetryLimit = 3

// New wires an Engine from its dependencies, defaulting the clock and id source.
func New(d Deps) *Engine {
	clock := d.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	newID := d.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	return &Engine{
		store:        d.Store,
		sessions:     d.Sessions,
		prs:          d.PRs,
		projects:     d.Projects,
		launcher:     d.Launcher,
		clock:        clock,
		newID:        newID,
		triggerLocks: make(map[domain.SessionID]*sync.Mutex),
	}
}

// lockWorker serialises Trigger calls for a single worker session and returns
// the unlock func. Without it, two concurrent triggers for the same worker can
// both pass the per-commit idempotency check and each spawn a reviewer against
// the same deterministic handle, leaving two running runs for one commit (#242).
//
// The per-worker mutex is created on first use and kept for the lifetime of the
// engine; the entry is a single pointer, so the unbounded-by-session-count map
// is a negligible, bounded-in-practice cost.
func (e *Engine) lockWorker(id domain.SessionID) func() {
	e.triggerMu.Lock()
	mu, ok := e.triggerLocks[id]
	if !ok {
		mu = &sync.Mutex{}
		e.triggerLocks[id] = mu
	}
	e.triggerMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// TriggerResult is the outcome of a trigger: the (new or existing) run, the live
// reviewer pane's handle so the UI can attach its terminal, and whether a new
// pass was started (false when an existing run for the same commit was reused).
type TriggerResult struct {
	Run              domain.ReviewRun
	ReviewerHandleID string
	Created          bool
	Reviews          []PRReviewState
	Runs             []domain.ReviewRun
	CreatedRuns      []domain.ReviewRun
	// SkipReason is set only for a normal automatic-trigger policy race, such
	// as the worker becoming active after the coordinator's initial read.
	SkipReason string
}

// SessionReviews is a worker's review state: the live reviewer handle plus its
// recorded passes, newest first.
type SessionReviews struct {
	ReviewerHandleID string
	ReviewerHarness  domain.ReviewerHarness
	Runs             []domain.ReviewRun
	Reviews          []PRReviewState
}

// CancelResult is the review state after a reviewer pane cancellation.
type CancelResult struct {
	ReviewerHandleID string
	Reviews          []PRReviewState
	CancelledRuns    []domain.ReviewRun
}

// TerminateResult reports the reviewer pane hard-teardown performed because
// the owning worker session is leaving its live lifecycle.
type TerminateResult struct {
	ReviewerHandleID string
	CancelledRuns    []domain.ReviewRun
}

// RestoreReviewerResult reports an idle reviewer pane restored alongside its
// worker session.
type RestoreReviewerResult struct {
	ReviewerHandleID string
	Restored         bool
}

// Trigger starts reviews for every PR on the worker session that needs review.
// It reuses running/up-to-date runs, retries failed/current changes-requested
// heads, and uses one reviewer pane for every new run in the batch. Automatic
// retries against the same PR head stop after three failed auto-review runs.
//
// An empty override keeps the project's configured reviewer. A known one runs
// this pass under it without editing project config, so picking a reviewer for
// one session cannot change what any other session in the project runs. The
// harness-change path below already handles the swap by respawning the pane.
func (e *Engine) Trigger(ctx stdctx.Context, workerID domain.SessionID, override domain.ReviewerHarness, config domain.AgentConfig) (TriggerResult, error) {
	return e.TriggerWithSource(ctx, workerID, override, config, domain.ReviewTriggerManual)
}

// TriggerWithSource starts a review and records who initiated the pass.
func (e *Engine) TriggerWithSource(ctx stdctx.Context, workerID domain.SessionID, override domain.ReviewerHarness, overrideConfig domain.AgentConfig, source domain.ReviewTriggerSource) (TriggerResult, error) {
	if workerID == "" {
		return TriggerResult{}, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	if override != "" && !override.IsKnown() {
		return TriggerResult{}, fmt.Errorf("%w: unknown reviewer harness %q", ErrInvalid, override)
	}
	if source != domain.ReviewTriggerManual && source != domain.ReviewTriggerAuto {
		return TriggerResult{}, fmt.Errorf("%w: unknown review trigger source %q", ErrInvalid, source)
	}

	// Serialise concurrent triggers for this worker so the idempotency check
	// below (and the reviewer spawn that follows it) can't be raced into a
	// double-spawn. Held across the spawn deliberately: the loser then re-reads
	// the freshly-recorded run and short-circuits to Created:false.
	unlock := e.lockWorker(workerID)
	defer unlock()

	worker, ok, err := e.sessions.GetSession(ctx, workerID)
	if err != nil {
		return TriggerResult{}, err
	}
	if !ok {
		return TriggerResult{}, fmt.Errorf("%w: worker session %q", ErrNotFound, workerID)
	}
	if source == domain.ReviewTriggerAuto {
		if reason := autoReviewSessionReason(worker, e.clock()); reason != "" {
			return TriggerResult{SkipReason: reason}, nil
		}
	}
	if worker.IsTerminated {
		return TriggerResult{}, fmt.Errorf("%w: worker session %q is terminated", ErrInvalid, workerID)
	}
	if worker.Metadata.WorkspacePath == "" {
		return TriggerResult{}, fmt.Errorf("%w: worker session %q has no workspace to review", ErrInvalid, workerID)
	}

	prs, err := e.prs.ListPRsBySession(ctx, workerID)
	if err != nil {
		return TriggerResult{}, err
	}
	if len(prs) == 0 {
		return TriggerResult{}, fmt.Errorf("%w: worker %q has no PR to review", ErrInvalid, workerID)
	}
	runs, err := e.store.ListReviewRunsBySession(ctx, workerID)
	if err != nil {
		return TriggerResult{}, err
	}

	if err := overrideConfig.Validate(); err != nil {
		return TriggerResult{}, fmt.Errorf("%w: reviewer config: %w", ErrInvalid, err)
	}

	harness, config, err := e.reviewerSelection(ctx, worker)
	if err != nil {
		return TriggerResult{}, err
	}
	resolvedHarness := harness
	resolvedConfig := config
	hasHarnessOverride := override != ""
	hasConfigOverride := false
	if hasHarnessOverride {
		harness = override
		if override == resolvedHarness {
			config = mergeReviewerAgentConfig(resolvedConfig, overrideConfig)
			hasConfigOverride = config != resolvedConfig
		} else if !overrideConfig.IsZero() {
			config = mergeReviewerAgentConfig(domain.AgentConfig{}, overrideConfig)
		} else {
			config = domain.AgentConfig{}
		}
	} else if !overrideConfig.IsZero() {
		config = mergeReviewerAgentConfig(config, overrideConfig)
		hasConfigOverride = config != resolvedConfig
	}
	reviewRows, err := e.store.ListReviewsBySession(ctx, workerID)
	if err != nil {
		return TriggerResult{}, err
	}
	if err := e.destroyOtherReviewerHandles(ctx, workerID, harness, reviewRows); err != nil {
		return TriggerResult{}, err
	}
	reviewRow, hasReview, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, harness)
	if err != nil {
		return TriggerResult{}, err
	}
	if stale, err := e.cancelStaleRunningRuns(ctx, workerID, reviewRow, hasReview, runs); err != nil {
		return TriggerResult{}, err
	} else if stale {
		runs, err = e.store.ListReviewRunsBySession(ctx, workerID)
		if err != nil {
			return TriggerResult{}, err
		}
	}
	hadRunningReviewer := reviewRunsContainRunningForHarness(runs, harness)
	reviews := Plan(prs, runs)
	if source == domain.ReviewTriggerAuto {
		reviews = Plan(prs, reviewRunsForHarness(runs, harness))
		// Automatic sweeps may discover another eligible PR while this harness is
		// already reviewing one. Reconciliation above has proved the reviewer
		// terminal is still alive, so do not append work to its active batch. A
		// later sweep can start the remaining PR after the current run terminates.
		if hadRunningReviewer {
			return TriggerResult{
				Run:              firstReusableRun(reviews),
				ReviewerHandleID: reviewRow.ReviewerHandleID,
				Created:          false,
				Reviews:          reviews,
				Runs:             runs,
			}, nil
		}
	}

	now := e.clock()
	reviewRow, err = e.upsertReview(ctx, worker, harness, reviewRow.ReviewerHandleID, reviewRow.AgentSessionID, now)
	if err != nil {
		return TriggerResult{}, err
	}

	var created []domain.ReviewRun
	type staleSupersede struct {
		prURL     string
		targetSHA string
	}
	var pendingSupersedes []staleSupersede
	var restarted []domain.ReviewRun
	batchID := ""
	for _, reviewState := range reviews {
		// A PR that is already up to date has nothing due — unless the caller asked
		// for a different reviewer than the one that produced that verdict. Picking
		// another agent is precisely a request for a second opinion on this commit,
		// so refusing it makes the reviewer choice inert exactly when it is most
		// useful. Ineligible PRs stay excluded: nothing can review those.
		eligible := reviewState.Status == ReviewStateNeedsReview || (source == domain.ReviewTriggerManual && reviewState.Status == ReviewStateChangesRequested)
		if source == domain.ReviewTriggerAuto && autoReviewHeadBlocked(runs, reviewState.PRURL, reviewState.TargetSHA, harness) {
			eligible = false
		}
		if !eligible && !secondOpinionWanted(reviewState, hasHarnessOverride, hasConfigOverride, harness) {
			continue
		}
		if hasConfigOverride {
			pendingSupersedes = append(pendingSupersedes, staleSupersede{prURL: reviewState.PRURL, targetSHA: reviewState.TargetSHA})
			if reviewState.LatestRun != nil && reviewState.LatestRun.Status == domain.ReviewRunRunning && (reviewState.LatestRun.Harness == harness || reviewState.LatestRun.Harness == "") {
				restarted = append(restarted, *reviewState.LatestRun)
				continue
			}
		} else if _, err := e.store.SupersedeStaleRunningReviewRuns(ctx, workerID, reviewState.PRURL, reviewState.TargetSHA, "superseded by a review trigger for a newer commit"); err != nil {
			return TriggerResult{}, err
		}
		if batchID == "" {
			batchID = e.newID()
		}
		run := domain.ReviewRun{
			ID:            e.newID(),
			ReviewID:      reviewRow.ID,
			SessionID:     workerID,
			BatchID:       batchID,
			Harness:       harness,
			TriggerSource: source,
			PRURL:         reviewState.PRURL,
			TargetSHA:     reviewState.TargetSHA,
			Status:        domain.ReviewRunRunning,
			Verdict:       domain.VerdictNone,
			CreatedAt:     now,
			// Completion refreshes this snapshot before delivery. Keeping the
			// trigger-time value also makes a running pass truthful in the API.
			AutoInjectReview: worker.AutoInjectReview,
		}
		if err := e.store.InsertReviewRun(ctx, run); err != nil {
			if errors.Is(err, domain.ErrDuplicateReviewRun) {
				if existing, ok, getErr := e.store.GetReviewRunBySessionPRSHAAndHarness(ctx, workerID, reviewState.PRURL, reviewState.TargetSHA, harness); getErr != nil {
					return TriggerResult{}, getErr
				} else if ok {
					reviews = replaceReviewLatestRun(reviews, reviewState.PRURL, reviewState.TargetSHA, existing)
					continue
				}
			}
			return TriggerResult{}, err
		}
		created = append(created, run)
		reviews = replaceReviewLatestRun(reviews, reviewState.PRURL, reviewState.TargetSHA, run)
	}
	if len(created) == 0 && len(restarted) == 0 {
		return TriggerResult{Run: firstReusableRun(reviews), ReviewerHandleID: reviewRow.ReviewerHandleID, Created: false, Reviews: reviews, Runs: runs}, nil
	}

	failRuns := func(start int, err error) error {
		for _, run := range created[start:] {
			if _, updateErr := e.store.UpdateReviewRunResult(ctx, run.ID, domain.ReviewRunFailed, domain.VerdictNone, err.Error(), "", run.AutoInjectReview); updateErr != nil {
				return updateErr
			}
		}
		return err
	}

	queueRuns := append([]domain.ReviewRun{}, restarted...)
	queueRuns = append(queueRuns, created...)
	queue := reviewQueue(queueRuns)
	launchRun := queueRuns[0]
	previousHandleID := reviewRow.ReviewerHandleID
	previousAgentSessionID := reviewRow.AgentSessionID
	launchAgentSessionID := reviewRow.AgentSessionID
	if hasConfigOverride {
		launchAgentSessionID = ""
	}
	persistedAgentSessionID := launchAgentSessionID
	handleID := ""
	if !hasConfigOverride && reviewRow.ReviewerHandleID != "" && reviewerPaneReusable(reviewRow, hadRunningReviewer) {
		alive, err := e.launcher.Alive(ctx, reviewRow.ReviewerHandleID)
		if err != nil {
			return TriggerResult{}, failRuns(0, err)
		}
		if alive {
			handleID = reviewRow.ReviewerHandleID
		}
	}
	if handleID == "" {
		// Each pass gets a fresh reviewer process on the same stable terminal
		// handle when there is no resumable live agent session to notify.
		if err := e.launcher.Preflight(ctx, harness, worker.Metadata.WorkspacePath); err != nil {
			return TriggerResult{}, failRuns(0, fmt.Errorf("reviewer preflight: %w", err))
		}
		launch, err := e.launcher.Spawn(ctx, reviewLaunchSpec(worker, harness, config, launchRun, queue, 0, launchAgentSessionID))
		if err != nil {
			return TriggerResult{}, failRuns(0, fmt.Errorf("launch reviewer: %w", err))
		}
		handleID = launch.HandleID
		if launch.AgentSessionID != "" {
			persistedAgentSessionID = launch.AgentSessionID
		}
	} else {
		if err := e.launcher.Notify(ctx, handleID, reviewLaunchSpec(worker, harness, config, launchRun, queue, 0, reviewRow.AgentSessionID)); err != nil {
			return TriggerResult{}, failRuns(0, fmt.Errorf("notify reviewer: %w", err))
		}
	}
	for _, stale := range pendingSupersedes {
		if _, err := e.store.SupersedeStaleRunningReviewRuns(ctx, workerID, stale.prURL, stale.targetSHA, "superseded by a review trigger for a newer commit"); err != nil {
			if handleID != "" {
				_ = e.launcher.Destroy(ctx, handleID)
			}
			return TriggerResult{}, failRuns(0, err)
		}
	}
	if hasConfigOverride && persistedAgentSessionID == "" && reviewRow.ID != "" {
		if _, err := e.store.UpdateReviewAgentSessionID(ctx, reviewRow.ID, ""); err != nil {
			if handleID != "" {
				_ = e.launcher.Destroy(ctx, handleID)
			}
			return TriggerResult{}, failRuns(0, err)
		}
	}
	reviewRow, err = e.upsertReview(ctx, worker, harness, handleID, persistedAgentSessionID, now)
	if err != nil {
		if handleID != "" {
			_ = e.launcher.Destroy(ctx, handleID)
		}
		return TriggerResult{}, err
	}
	if hasConfigOverride && previousHandleID != "" && previousHandleID != handleID {
		if err := e.launcher.Destroy(ctx, previousHandleID); err != nil {
			if _, rollbackErr := e.upsertReview(ctx, worker, harness, previousHandleID, previousAgentSessionID, now); rollbackErr != nil {
				return TriggerResult{}, failRuns(0, fmt.Errorf("destroy previous reviewer: %w; rollback review row: %w", err, rollbackErr))
			}
			if handleID != "" {
				if destroyNewErr := e.launcher.Destroy(ctx, handleID); destroyNewErr != nil {
					return TriggerResult{}, failRuns(0, fmt.Errorf("destroy previous reviewer: %w; cleanup replacement reviewer: %w", err, destroyNewErr))
				}
			}
			return TriggerResult{}, failRuns(0, fmt.Errorf("destroy previous reviewer: %w", err))
		}
	}
	for i := range created {
		created[i].ReviewID = reviewRow.ID
	}
	triggerRuns := append([]domain.ReviewRun{}, created...)
	triggerRuns = append(triggerRuns, runs...)
	resultRun := launchRun
	createdFlag := len(created) > 0 || len(restarted) > 0
	return TriggerResult{Run: resultRun, ReviewerHandleID: handleID, Created: createdFlag, Reviews: reviews, Runs: triggerRuns, CreatedRuns: created}, nil
}

func autoReviewSessionReason(worker domain.SessionRecord, now time.Time) string {
	switch {
	case !worker.AutoReviewEnabled:
		return "disabled"
	case worker.Kind != domain.KindWorker:
		return "not_worker"
	case worker.IsTerminated:
		return "terminated"
	case worker.Activity.State != domain.ActivityIdle:
		return "not_idle"
	case worker.Activity.LastActivityAt.IsZero() || now.Sub(worker.Activity.LastActivityAt) < time.Minute:
		return "idle_threshold_not_met"
	default:
		return ""
	}
}

// SwitchReviewer serializes reviewer preference changes with trigger/restore
// and returns the authoritative post-switch review state.
func (e *Engine) SwitchReviewer(
	ctx stdctx.Context,
	workerID domain.SessionID,
	harness domain.ReviewerHarness,
	config domain.AgentConfig,
) (SessionReviews, error) {
	if workerID == "" {
		return SessionReviews{}, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	if harness != "" && !harness.IsKnown() {
		return SessionReviews{}, fmt.Errorf("%w: unknown reviewer harness %q", ErrInvalid, harness)
	}
	if err := config.Validate(); err != nil {
		return SessionReviews{}, fmt.Errorf("%w: reviewer config: %w", ErrInvalid, err)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()

	worker, ok, err := e.sessions.GetSession(ctx, workerID)
	if err != nil {
		return SessionReviews{}, err
	}
	if !ok {
		return SessionReviews{}, fmt.Errorf("%w: worker session %q", ErrNotFound, workerID)
	}
	previousSelected, previousConfig, err := e.reviewerSelection(ctx, worker)
	if err != nil {
		return SessionReviews{}, err
	}
	if ok, err := e.store.SetSessionReviewerConfig(ctx, workerID, harness, config, e.clock()); err != nil {
		return SessionReviews{}, err
	} else if !ok {
		return SessionReviews{}, fmt.Errorf("%w: worker session %q", ErrNotFound, workerID)
	}
	worker.ReviewerHarness = harness
	worker.ReviewerConfig = config
	selected, selectedConfig, err := e.reviewerSelection(ctx, worker)
	if err != nil {
		return SessionReviews{}, err
	}
	reviewRows, err := e.store.ListReviewsBySession(ctx, workerID)
	if err != nil {
		return SessionReviews{}, err
	}
	if err := e.destroyOtherReviewerHandles(ctx, workerID, selected, reviewRows); err != nil {
		return SessionReviews{}, err
	}
	if previousSelected == selected && previousConfig != selectedConfig {
		if err := e.resetReviewerRuntimeLocked(ctx, workerID, selected); err != nil {
			return SessionReviews{}, err
		}
	}
	if _, err := e.restoreReviewerLocked(ctx, workerID, worker, selected, selectedConfig); err != nil {
		return SessionReviews{}, err
	}
	return e.listLocked(ctx, workerID, selected)
}

func reviewerPaneReusable(reviewRow domain.Review, hadRunningReviewer bool) bool {
	if strings.TrimSpace(reviewRow.AgentSessionID) != "" {
		return true
	}
	return hadRunningReviewer
}

func reviewRunsContainRunningForHarness(runs []domain.ReviewRun, harness domain.ReviewerHarness) bool {
	for _, run := range runs {
		if (run.Harness == harness || run.Harness == "") && run.Status == domain.ReviewRunRunning {
			return true
		}
	}
	return false
}

func (e *Engine) destroyOtherReviewerHandles(ctx stdctx.Context, workerID domain.SessionID, selected domain.ReviewerHarness, reviews []domain.Review) error {
	for _, review := range reviews {
		if review.Harness == selected || review.ReviewerHandleID == "" {
			continue
		}
		if err := e.launcher.Destroy(ctx, review.ReviewerHandleID); err != nil {
			return err
		}
		if err := e.store.ClearReviewerHandleByHarness(ctx, workerID, review.Harness); err != nil {
			return err
		}
		if _, err := e.store.CancelRunningReviewRunsBySessionAndHarness(ctx, workerID, review.Harness, "cancelled because reviewer agent was switched"); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) resetReviewerRuntimeLocked(ctx stdctx.Context, workerID domain.SessionID, harness domain.ReviewerHarness) error {
	review, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, harness)
	if err != nil || !ok {
		return err
	}
	if review.ReviewerHandleID != "" {
		if err := e.launcher.Destroy(ctx, review.ReviewerHandleID); err != nil {
			return err
		}
		if err := e.store.ClearReviewerHandleByHarness(ctx, workerID, harness); err != nil {
			return err
		}
	}
	if _, err := e.store.UpdateReviewAgentSessionID(ctx, review.ID, ""); err != nil {
		return err
	}
	return nil
}

// RestoreReviewer relaunches the reviewer terminal for a restored worker when
// that worker already has review history. It does not create review_run rows or
// start a review; explicit trigger remains the only path that starts review
// work.
func (e *Engine) RestoreReviewer(ctx stdctx.Context, workerID domain.SessionID) (RestoreReviewerResult, error) {
	if workerID == "" {
		return RestoreReviewerResult{}, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	worker, ok, err := e.sessions.GetSession(ctx, workerID)
	if err != nil {
		return RestoreReviewerResult{}, err
	}
	if !ok {
		return RestoreReviewerResult{}, fmt.Errorf("%w: worker session %q", ErrNotFound, workerID)
	}
	if worker.IsTerminated || worker.Metadata.WorkspacePath == "" {
		return RestoreReviewerResult{}, nil
	}
	harness, config, err := e.reviewerSelection(ctx, worker)
	if err != nil {
		return RestoreReviewerResult{}, err
	}
	return e.restoreReviewerLocked(ctx, workerID, worker, harness, config)
}

// CodexReviewerRunning reports whether this worker owns a live Codex reviewer
// controller. It performs no launch or mutation and never infers liveness from
// a persisted handle alone.
func (e *Engine) CodexReviewerRunning(ctx stdctx.Context, workerID domain.SessionID) (bool, error) {
	snapshot, err := e.SnapshotCodexReviewer(ctx, workerID)
	return snapshot.Running, err
}

// SnapshotCodexReviewer reads liveness, handle, and native identity under one
// worker lock so account switching can fence exactly the controller observed.
func (e *Engine) SnapshotCodexReviewer(ctx stdctx.Context, workerID domain.SessionID) (ports.CodexReviewerControllerSnapshot, error) {
	if workerID == "" {
		return ports.CodexReviewerControllerSnapshot{}, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	reviewRow, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, domain.ReviewerCodex)
	if err != nil || !ok {
		return ports.CodexReviewerControllerSnapshot{}, err
	}
	snapshot := ports.CodexReviewerControllerSnapshot{
		HandleID: strings.TrimSpace(reviewRow.ReviewerHandleID), NativeSessionID: strings.TrimSpace(reviewRow.AgentSessionID),
	}
	if snapshot.HandleID == "" {
		return snapshot, nil
	}
	snapshot.Running, err = e.launcher.Alive(ctx, snapshot.HandleID)
	return snapshot, err
}

// CodexReviewerBusy reports whether a Codex review turn is still durable as
// running. Account switching waits for that turn instead of interrupting it.
func (e *Engine) CodexReviewerBusy(ctx stdctx.Context, workerID domain.SessionID) (bool, error) {
	runs, err := e.store.ListRunningReviewRunsBySession(ctx, workerID)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.Harness == domain.ReviewerCodex || run.Harness == "" {
			return true, nil
		}
	}
	return false, nil
}

// CodexReviewerNativeSession returns only the durable provider identity already
// attached to this worker's Codex reviewer. It never discovers or scans native
// history; Session Manager uses the exact ID to migrate the one attributed
// rollout before global credential switching.
func (e *Engine) CodexReviewerNativeSession(ctx stdctx.Context, workerID domain.SessionID) (string, bool, error) {
	if workerID == "" {
		return "", false, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	reviewRow, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, domain.ReviewerCodex)
	if err != nil || !ok {
		return "", false, err
	}
	id := strings.TrimSpace(reviewRow.AgentSessionID)
	return id, id != "", nil
}

// SuspendCodexReviewer stops only the Codex reviewer pane while retaining its
// native identity and review rows for an exact resume after account activation.
func (e *Engine) SuspendCodexReviewer(ctx stdctx.Context, workerID domain.SessionID) (bool, error) {
	snapshot, err := e.SnapshotCodexReviewer(ctx, workerID)
	if err != nil || !snapshot.Running {
		return false, err
	}
	return e.SuspendCodexReviewerExact(ctx, workerID, snapshot.HandleID, snapshot.NativeSessionID)
}

// SuspendCodexReviewerExact destroys only the snapshotted reviewer identity.
func (e *Engine) SuspendCodexReviewerExact(ctx stdctx.Context, workerID domain.SessionID, expectedHandleID, expectedNativeSessionID string) (bool, error) {
	if workerID == "" {
		return false, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	reviewRow, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, domain.ReviewerCodex)
	if err != nil || !ok || reviewRow.ReviewerHandleID == "" {
		return false, err
	}
	if strings.TrimSpace(reviewRow.ReviewerHandleID) != strings.TrimSpace(expectedHandleID) ||
		strings.TrimSpace(reviewRow.AgentSessionID) != strings.TrimSpace(expectedNativeSessionID) {
		return false, errors.New("codex reviewer identity changed")
	}
	alive, err := e.launcher.Alive(ctx, reviewRow.ReviewerHandleID)
	if err != nil || !alive {
		return false, err
	}
	if err := e.launcher.Destroy(ctx, reviewRow.ReviewerHandleID); err != nil {
		return false, err
	}
	if err := e.store.ClearReviewerHandleByHarness(ctx, workerID, domain.ReviewerCodex); err != nil {
		return false, err
	}
	return true, nil
}

// RestoreCodexReviewer relaunches only the retained Codex reviewer identity.
func (e *Engine) RestoreCodexReviewer(ctx stdctx.Context, workerID domain.SessionID) error {
	nativeID, found, err := e.CodexReviewerNativeSession(ctx, workerID)
	if err != nil || !found {
		return err
	}
	return e.RestoreCodexReviewerExact(ctx, workerID, nativeID)
}

// RestoreCodexReviewerExact relaunches only when the durable native history
// still matches the switch snapshot.
func (e *Engine) RestoreCodexReviewerExact(ctx stdctx.Context, workerID domain.SessionID, expectedNativeSessionID string) error {
	if workerID == "" {
		return fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	worker, ok, err := e.sessions.GetSession(ctx, workerID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: worker session %q", ErrNotFound, workerID)
	}
	if worker.IsTerminated || worker.Metadata.WorkspacePath == "" {
		return nil
	}
	reviewRow, found, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, domain.ReviewerCodex)
	if err != nil {
		return err
	}
	if !found || strings.TrimSpace(reviewRow.AgentSessionID) != strings.TrimSpace(expectedNativeSessionID) {
		return errors.New("codex reviewer native identity changed")
	}
	selected, config, err := e.reviewerSelection(ctx, worker)
	if err != nil {
		return err
	}
	if selected != domain.ReviewerCodex {
		config = domain.AgentConfig{}
	}
	_, err = e.restoreReviewerLocked(ctx, workerID, worker, domain.ReviewerCodex, config)
	return err
}

func (e *Engine) restoreReviewerLocked(
	ctx stdctx.Context,
	workerID domain.SessionID,
	worker domain.SessionRecord,
	harness domain.ReviewerHarness,
	config domain.AgentConfig,
) (RestoreReviewerResult, error) {
	reviewRows, err := e.store.ListReviewsBySession(ctx, workerID)
	if err != nil {
		return RestoreReviewerResult{}, err
	}
	if err := e.destroyOtherReviewerHandles(ctx, workerID, harness, reviewRows); err != nil {
		return RestoreReviewerResult{}, err
	}
	reviewRow, hasReview, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, harness)
	if err != nil {
		return RestoreReviewerResult{}, err
	}
	runs, err := e.store.ListReviewRunsBySession(ctx, workerID)
	if err != nil {
		return RestoreReviewerResult{}, err
	}
	previousRuns := reviewRunsForHarness(runs, harness)
	if !hasReview && len(previousRuns) == 0 {
		return RestoreReviewerResult{}, nil
	}
	if hasReview && reviewRow.ReviewerHandleID != "" {
		alive, err := e.launcher.Alive(ctx, reviewRow.ReviewerHandleID)
		if err != nil {
			return RestoreReviewerResult{}, err
		}
		if alive {
			return RestoreReviewerResult{ReviewerHandleID: reviewRow.ReviewerHandleID, Restored: false}, nil
		}
	}
	agentSessionID := ""
	if hasReview {
		agentSessionID = reviewRow.AgentSessionID
	} else {
		reviewRow, err = e.upsertReview(ctx, worker, harness, "", "", e.clock())
		if err != nil {
			return RestoreReviewerResult{}, err
		}
	}
	launch, err := e.launcher.RestoreTerminal(ctx, LaunchSpec{
		ReviewSessionID:      reviewRow.ID,
		WorkerID:             worker.ID,
		ProjectID:            worker.ProjectID,
		Harness:              harness,
		AgentConfig:          config,
		WorkspacePath:        worker.Metadata.WorkspacePath,
		AgentSessionID:       agentSessionID,
		RequireNativeHistory: harness == domain.ReviewerCodex,
		PreviousRuns:         previousRuns,
	})
	if err != nil {
		return RestoreReviewerResult{}, fmt.Errorf("restore reviewer: %w", err)
	}
	if launch.AgentSessionID != "" {
		agentSessionID = launch.AgentSessionID
	}
	if _, err := e.upsertReview(ctx, worker, harness, launch.HandleID, agentSessionID, e.clock()); err != nil {
		_ = e.launcher.Destroy(ctx, launch.HandleID)
		return RestoreReviewerResult{}, err
	}
	return RestoreReviewerResult{ReviewerHandleID: launch.HandleID, Restored: true}, nil
}

// TeardownReviewerTerminal destroys reviewer panes before shutdown removes the
// worker worktree, but preserves review rows and native agent session ids so
// worker restore can recreate the reviewer terminal later.
func (e *Engine) TeardownReviewerTerminal(ctx stdctx.Context, workerID domain.SessionID) error {
	if workerID == "" {
		return fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	reviews, err := e.store.ListReviewsBySession(ctx, workerID)
	if err != nil {
		return err
	}
	for _, review := range reviews {
		if review.ReviewerHandleID == "" {
			continue
		}
		if err := e.launcher.Destroy(ctx, review.ReviewerHandleID); err != nil {
			return err
		}
	}
	if len(reviews) > 0 {
		if err := e.store.ClearReviewerHandle(ctx, workerID); err != nil {
			return err
		}
	}
	return nil
}

func autoReviewHeadBlocked(runs []domain.ReviewRun, prURL, targetSHA string, harness domain.ReviewerHarness) bool {
	failedAutoRuns := 0
	for _, run := range runs {
		if run.PRURL != prURL || run.TargetSHA != targetSHA || (run.Harness != harness && run.Harness != "") {
			continue
		}
		if run.Status == domain.ReviewRunRunning || run.Status == domain.ReviewRunCancelled || run.Verdict == domain.VerdictApproved || run.Verdict == domain.VerdictChangesRequested {
			return true
		}
		if run.Status == domain.ReviewRunFailed && run.TriggerSource == domain.ReviewTriggerAuto {
			failedAutoRuns++
			if failedAutoRuns >= autoReviewFailedRetryLimit {
				return true
			}
		}
	}
	return false
}

func reviewRunsForHarness(runs []domain.ReviewRun, harness domain.ReviewerHarness) []domain.ReviewRun {
	out := make([]domain.ReviewRun, 0, len(runs))
	for _, run := range runs {
		if run.Harness == harness {
			out = append(out, run)
		}
	}
	return out
}

func (e *Engine) cancelStaleRunningRuns(ctx stdctx.Context, workerID domain.SessionID, reviewRow domain.Review, hasReview bool, runs []domain.ReviewRun) (bool, error) {
	hasRunning := false
	for _, run := range runs {
		if run.SessionID == workerID && run.Status == domain.ReviewRunRunning && run.Verdict == domain.VerdictNone {
			hasRunning = true
			break
		}
	}
	if !hasRunning {
		return false, nil
	}
	if !hasReview || reviewRow.ReviewerHandleID == "" {
		if _, err := e.store.CancelRunningReviewRunsBySession(ctx, workerID, "cancelled because reviewer terminal is unavailable"); err != nil {
			return false, err
		}
		return true, nil
	}
	alive, err := e.launcher.Alive(ctx, reviewRow.ReviewerHandleID)
	if err != nil {
		return false, err
	}
	if alive {
		return false, nil
	}
	if _, err := e.store.CancelRunningReviewRunsBySession(ctx, workerID, "cancelled because reviewer terminal is unavailable"); err != nil {
		return false, err
	}
	return true, nil
}

func reviewLaunchSpec(
	worker domain.SessionRecord,
	harness domain.ReviewerHarness,
	config domain.AgentConfig,
	run domain.ReviewRun,
	queue []ports.ReviewTask,
	index int,
	agentSessionID string,
) LaunchSpec {
	return LaunchSpec{
		RunID:           run.ID,
		BatchID:         run.BatchID,
		ReviewSessionID: run.ReviewID,
		WorkerID:        worker.ID,
		ProjectID:       worker.ProjectID,
		Harness:         harness,
		AgentConfig:     config,
		WorkspacePath:   worker.Metadata.WorkspacePath,
		AgentSessionID:  agentSessionID,
		PreviousRuns:    nil,
		PRURL:           run.PRURL,
		TargetSHA:       run.TargetSHA,
		ReviewQueue:     queue,
		ReviewIndex:     index,
	}
}

func reviewQueue(runs []domain.ReviewRun) []ports.ReviewTask {
	queue := make([]ports.ReviewTask, 0, len(runs))
	for _, run := range runs {
		queue = append(queue, ports.ReviewTask{
			RunID:     run.ID,
			PRURL:     run.PRURL,
			TargetSHA: run.TargetSHA,
		})
	}
	return queue
}

// secondOpinionWanted reports whether an explicit manual override makes an
// otherwise up-to-date PR worth running again. A config-only override (for
// example, picking a model under the same harness) always requests another
// pass. A harness-only override only does so when it actually changes the
// reviewer whose pass is currently authoritative; re-picking the same harness
// must still reuse. That remains true even while another harness is already
// running: a different reviewer is a second opinion, not a restart. Legacy
// runs may have an empty harness, which means "the same resolved harness as
// today" for this comparison.
func secondOpinionWanted(state PRReviewState, hasHarnessOverride, hasConfigOverride bool, harness domain.ReviewerHarness) bool {
	if state.Status == ReviewStateIneligible {
		return false
	}
	if state.LatestRun == nil {
		return false
	}
	if hasConfigOverride {
		return true
	}
	if !hasHarnessOverride {
		return false
	}
	latestHarness := state.LatestRun.Harness
	if latestHarness == "" {
		latestHarness = harness
	}
	return latestHarness != harness
}

func replaceReviewLatestRun(reviews []PRReviewState, prURL, targetSHA string, run domain.ReviewRun) []PRReviewState {
	for i := range reviews {
		if reviews[i].PRURL == prURL && reviews[i].TargetSHA == targetSHA {
			reviews[i].LatestRun = &run
			if run.Status == domain.ReviewRunRunning {
				reviews[i].Status = ReviewStateRunning
			}
			break
		}
	}
	return reviews
}

func firstReusableRun(reviews []PRReviewState) domain.ReviewRun {
	// Legacy compatibility only: in the multi-PR model the authoritative state
	// is Reviews. When no run is created, this field is just a best-effort
	// non-empty run for older clients.
	for _, review := range reviews {
		if review.LatestRun != nil {
			return *review.LatestRun
		}
	}
	return domain.ReviewRun{}
}

// List returns a worker's review state: the live reviewer handle and its passes.
func (e *Engine) List(ctx stdctx.Context, workerID domain.SessionID) (SessionReviews, error) {
	if workerID == "" {
		return SessionReviews{}, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	worker, ok, err := e.sessions.GetSession(ctx, workerID)
	if err != nil {
		return SessionReviews{}, err
	}
	if !ok {
		return SessionReviews{}, fmt.Errorf("%w: worker session %q", ErrNotFound, workerID)
	}
	selectedHarness, _, err := e.reviewerSelection(ctx, worker)
	if err != nil {
		return SessionReviews{}, err
	}
	return e.listLocked(ctx, workerID, selectedHarness)
}

func (e *Engine) listLocked(ctx stdctx.Context, workerID domain.SessionID, selectedHarness domain.ReviewerHarness) (SessionReviews, error) {
	runs, err := e.store.ListReviewRunsBySession(ctx, workerID)
	if err != nil {
		return SessionReviews{}, err
	}
	var handle string
	reviewerHarness := selectedHarness
	if review, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, selectedHarness); err != nil {
		return SessionReviews{}, err
	} else if ok && review.ReviewerHandleID != "" {
		handle = review.ReviewerHandleID
		reviewerHarness = review.Harness
	} else if review, ok, err := e.store.GetReviewBySession(ctx, workerID); err != nil {
		return SessionReviews{}, err
	} else if ok && review.ReviewerHandleID != "" {
		handle = review.ReviewerHandleID
		reviewerHarness = review.Harness
	}
	prs, err := e.prs.ListPRsBySession(ctx, workerID)
	if err != nil {
		return SessionReviews{}, err
	}
	return SessionReviews{ReviewerHandleID: handle, ReviewerHarness: reviewerHarness, Runs: runs, Reviews: Plan(prs, runs)}, nil
}

// Cancel interrupts the live reviewer pane for a worker and marks running
// review runs as cancelled so they no longer block a fresh trigger.
func (e *Engine) Cancel(ctx stdctx.Context, workerID domain.SessionID) (CancelResult, error) {
	if workerID == "" {
		return CancelResult{}, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	worker, ok, err := e.sessions.GetSession(ctx, workerID)
	if err != nil {
		return CancelResult{}, err
	}
	if !ok {
		return CancelResult{}, fmt.Errorf("%w: worker session %q", ErrNotFound, workerID)
	}
	harness, _, err := e.reviewerSelection(ctx, worker)
	if err != nil {
		return CancelResult{}, err
	}
	running, err := e.store.ListRunningReviewRunsBySession(ctx, workerID)
	if err != nil {
		return CancelResult{}, err
	}
	if len(running) == 0 {
		review, ok, err := e.currentReviewForSession(ctx, workerID, harness)
		if err != nil {
			return CancelResult{}, err
		}
		handle := ""
		if ok {
			handle = review.ReviewerHandleID
		}
		prs, err := e.prs.ListPRsBySession(ctx, workerID)
		if err != nil {
			return CancelResult{}, err
		}
		runs, err := e.store.ListReviewRunsBySession(ctx, workerID)
		if err != nil {
			return CancelResult{}, err
		}
		return CancelResult{ReviewerHandleID: handle, Reviews: Plan(prs, runs)}, nil
	}
	review, ok, err := e.currentReviewForCancel(ctx, workerID, harness, running)
	if err != nil {
		return CancelResult{}, err
	}
	if !ok || review.ReviewerHandleID == "" {
		return CancelResult{}, fmt.Errorf("%w: reviewer for worker session %q", ErrNotFound, workerID)
	}
	if err := e.launcher.Cancel(ctx, review.ReviewerHandleID, review.Harness); err != nil {
		alive, aliveErr := e.launcher.Alive(ctx, review.ReviewerHandleID)
		if aliveErr != nil {
			return CancelResult{}, err
		}
		if alive {
			return CancelResult{}, err
		}
	}
	if _, err := e.store.CancelRunningReviewRunsBySessionAndHarness(ctx, workerID, review.Harness, "cancelled by user"); err != nil {
		return CancelResult{}, err
	}
	cancelled := make([]domain.ReviewRun, 0, len(running))
	for _, run := range running {
		if run.Harness != review.Harness && run.Harness != "" {
			continue
		}
		run.Status = domain.ReviewRunCancelled
		run.Verdict = domain.VerdictNone
		run.Body = "cancelled by user"
		run.GithubReviewID = ""
		cancelled = append(cancelled, run)
	}
	prs, err := e.prs.ListPRsBySession(ctx, workerID)
	if err != nil {
		return CancelResult{}, err
	}
	runs, err := e.store.ListReviewRunsBySession(ctx, workerID)
	if err != nil {
		return CancelResult{}, err
	}
	return CancelResult{ReviewerHandleID: review.ReviewerHandleID, Reviews: Plan(prs, runs), CancelledRuns: cancelled}, nil
}

func (e *Engine) currentReviewForCancel(ctx stdctx.Context, workerID domain.SessionID, selected domain.ReviewerHarness, running []domain.ReviewRun) (domain.Review, bool, error) {
	if len(running) > 0 {
		if selected != "" {
			for _, run := range running {
				if run.Harness != selected && run.Harness != "" {
					continue
				}
				review, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, selected)
				if err != nil || (ok && review.ReviewerHandleID != "") {
					return review, ok, err
				}
				break
			}
		}
		for _, run := range running {
			if run.Harness == "" || run.Harness == selected {
				continue
			}
			review, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, run.Harness)
			if err != nil || (ok && review.ReviewerHandleID != "") {
				return review, ok, err
			}
		}
	}
	return e.currentReviewForSession(ctx, workerID, selected)
}

func (e *Engine) currentReviewForSession(ctx stdctx.Context, workerID domain.SessionID, selected domain.ReviewerHarness) (domain.Review, bool, error) {
	if selected != "" {
		review, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, selected)
		if err != nil || ok {
			return review, ok, err
		}
	}
	return e.store.GetReviewBySession(ctx, workerID)
}

// TerminateReviewer destroys the live reviewer pane for a worker and cancels
// any running review runs. Unlike Cancel, this does not ask the reviewer
// adapter for a graceful interrupt sequence: worker termination/restore must
// remove the terminal pane itself.
func (e *Engine) TerminateReviewer(ctx stdctx.Context, workerID domain.SessionID, body string) (TerminateResult, error) {
	if workerID == "" {
		return TerminateResult{}, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	reviews, err := e.store.ListReviewsBySession(ctx, workerID)
	if err != nil {
		return TerminateResult{}, err
	}
	running, err := e.store.ListRunningReviewRunsBySession(ctx, workerID)
	if err != nil {
		return TerminateResult{}, err
	}
	destroyedHandle := ""
	for _, review := range reviews {
		if review.ReviewerHandleID == "" {
			continue
		}
		if err := e.launcher.Destroy(ctx, review.ReviewerHandleID); err != nil {
			return TerminateResult{}, err
		}
		if destroyedHandle == "" {
			destroyedHandle = review.ReviewerHandleID
		}
	}
	if len(reviews) > 0 {
		if err := e.store.ClearReviewerHandle(ctx, workerID); err != nil {
			return TerminateResult{}, err
		}
	}
	if body == "" {
		body = "cancelled by worker session lifecycle"
	}
	if _, err := e.store.CancelRunningReviewRunsBySession(ctx, workerID, body); err != nil {
		return TerminateResult{}, err
	}
	cancelled := make([]domain.ReviewRun, 0, len(running))
	for _, run := range running {
		run.Status = domain.ReviewRunCancelled
		run.Verdict = domain.VerdictNone
		run.Body = body
		run.GithubReviewID = ""
		cancelled = append(cancelled, run)
	}
	return TerminateResult{ReviewerHandleID: destroyedHandle, CancelledRuns: cancelled}, nil
}

// reviewerHarness resolves which harness reviews the worker's PR: a persisted
// session preference wins, then project configuration, then the worker's own
// harness when supported, otherwise claude-code.
func (e *Engine) reviewerSelection(
	ctx stdctx.Context,
	worker domain.SessionRecord,
) (domain.ReviewerHarness, domain.AgentConfig, error) {
	if worker.ReviewerHarness != "" || !worker.ReviewerConfig.IsZero() {
		harness := worker.ReviewerHarness
		projectHarness, projectConfig, err := e.projectReviewerSelection(ctx, worker)
		if err != nil {
			return "", domain.AgentConfig{}, err
		}
		if harness == "" {
			harness = projectHarness
		}
		baseConfig := domain.AgentConfig{}
		if harness == projectHarness {
			baseConfig = projectConfig
		}
		return harness, mergeReviewerAgentConfig(baseConfig, worker.ReviewerConfig), nil
	}
	return e.projectReviewerSelection(ctx, worker)
}

func mergeReviewerAgentConfig(base, override domain.AgentConfig) domain.AgentConfig {
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Mode != "" {
		base.Mode = override.Mode
	}
	if override.Permissions != "" {
		base.Permissions = override.Permissions
	}
	return base
}

func (e *Engine) projectReviewerSelection(
	ctx stdctx.Context,
	worker domain.SessionRecord,
) (domain.ReviewerHarness, domain.AgentConfig, error) {
	var cfg domain.ProjectConfig
	if e.projects != nil {
		if proj, ok, err := e.projects.GetProject(ctx, string(worker.ProjectID)); err != nil {
			return "", domain.AgentConfig{}, err
		} else if ok {
			cfg = proj.Config
		}
	}
	if len(cfg.Reviewers) > 0 {
		return cfg.Reviewers[0].Harness, cfg.Reviewers[0].AgentConfig, nil
	}
	return cfg.ResolveReviewerHarness(worker.Harness), domain.AgentConfig{}, nil
}

func (e *Engine) upsertReview(ctx stdctx.Context, worker domain.SessionRecord, harness domain.ReviewerHarness, handleID, agentSessionID string, now time.Time) (domain.Review, error) {
	existing, ok, err := e.store.GetReviewBySessionAndHarness(ctx, worker.ID, harness)
	if err != nil {
		return domain.Review{}, err
	}
	agentSessionID = strings.TrimSpace(agentSessionID)
	review := domain.Review{
		ID:               e.newID(),
		SessionID:        worker.ID,
		ProjectID:        worker.ProjectID,
		Harness:          harness,
		PRURL:            "",
		ReviewerHandleID: handleID,
		AgentSessionID:   agentSessionID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if ok {
		// Reuse the existing row's identity and creation time; UpsertReview
		// refreshes harness/pr_url/reviewer_handle_id/updated_at.
		review.ID = existing.ID
		review.CreatedAt = existing.CreatedAt
	}
	if err := e.store.UpsertReview(ctx, review); err != nil {
		return domain.Review{}, err
	}
	return review, nil
}
