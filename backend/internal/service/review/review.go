// Package review is the daemon's HTTP-facing code-review service boundary. The
// core orchestration lives in internal/review; this layer is the thin contract
// the API controller depends on and delegates to the engine, so the same engine
// can also back a future in-process CLI trigger.
package review

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
	"github.com/aoagents/agent-orchestrator/backend/internal/telemetrymeta"
)

// errRunSuperseded marks a run that is no longer in a submittable state — it was
// superseded or terminated (e.g. a new commit fired SupersedeStaleRunningReviewRuns
// and moved it to failed) before its result arrived. It is an internal sentinel:
// SubmitMany skips such a run so it cannot strand the valid sibling results in the
// same submission, per SubmitMany's documented delivery-scoping guarantee.
var errRunSuperseded = errors.New("review: run is no longer running")

// ErrInvalid and ErrNotFound re-export the engine sentinels so the HTTP
// controller maps service failures to 422/404 without importing the core.
var (
	ErrInvalid             = reviewcore.ErrInvalid
	ErrNotFound            = reviewcore.ErrNotFound
	ErrAgentBinaryNotFound = ports.ErrAgentBinaryNotFound
)

// reviewErrorKind reduces a trigger failure to a safe category. Raw error text
// can carry repository paths and agent binary locations, so only the kind and a
// stable code ever leave the process.
func reviewErrorKind(err error) string {
	// The engine returns its own sentinels (reviewcore.ErrInvalid / ErrNotFound)
	// and ports.ErrAgentBinaryNotFound, wrapped with %w. Those are mapped to API
	// error kinds only at the HTTP controller boundary, after Trigger has already
	// returned here, so telemetrymeta.ErrorKindAndCode (which only classifies
	// *apierr.Error) would collapse every trigger failure to "internal" and the
	// field could never say why a trigger failed. Classify the sentinels first.
	switch {
	case errors.Is(err, reviewcore.ErrInvalid):
		return "invalid"
	case errors.Is(err, reviewcore.ErrNotFound):
		return "not_found"
	case errors.Is(err, ports.ErrAgentBinaryNotFound):
		return "agent_unavailable"
	}
	kind, _ := telemetrymeta.ErrorKindAndCode(err)
	return kind
}

// Manager is the reviews surface the HTTP controller depends on.
type Manager interface {
	Trigger(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness) (reviewcore.TriggerResult, error)
	Cancel(ctx context.Context, workerID domain.SessionID) (reviewcore.CancelResult, error)
	TerminateReviewer(ctx context.Context, workerID domain.SessionID, body string) error
	TeardownReviewerTerminal(ctx context.Context, workerID domain.SessionID) error
	RestoreReviewer(ctx context.Context, workerID domain.SessionID) error
	SwitchReviewer(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness) (reviewcore.SessionReviews, error)
	ApplyReviewActivitySignal(ctx context.Context, reviewSessionID string, signal ActivitySignal) error
	Submit(ctx context.Context, workerID domain.SessionID, runID string, verdict domain.ReviewVerdict, body, githubReviewID string) (domain.ReviewRun, error)
	SubmitMany(ctx context.Context, workerID domain.SessionID, reviews []SubmittedReview) ([]domain.ReviewRun, error)
	List(ctx context.Context, workerID domain.SessionID) (reviewcore.SessionReviews, error)
}

// Service is the API-facing review service. It delegates to the core engine.
type Service struct {
	engine    *reviewcore.Engine
	store     Store
	lifecycle Reducer
	clock     func() time.Time
	telemetry ports.EventSink
}

var _ Manager = (*Service)(nil)

// Store is the review_run persistence surface owned by the service submit path.
type Store interface {
	GetReviewByID(ctx context.Context, id string) (domain.Review, bool, error)
	UpdateReviewAgentSessionID(ctx context.Context, id, agentSessionID string) (bool, error)
	GetReviewRun(ctx context.Context, id string) (domain.ReviewRun, bool, error)
	UpdateReviewRunResult(ctx context.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, githubReviewID string) (bool, error)
	MarkReviewRunDelivered(ctx context.Context, id string, deliveredAt time.Time) (bool, error)
	ListPRsBySession(ctx context.Context, id domain.SessionID) ([]domain.PullRequest, error)
}

// Reducer is the lifecycle reaction boundary used after a review result has
// been persisted.
type Reducer interface {
	ApplyReviewBatch(ctx context.Context, workerID domain.SessionID, batchID string, results []lifecycle.ReviewResult) (lifecycle.ReviewDeliveryOutcome, error)
}

// Option customizes the review service.
type Option func(*Service)

// WithLifecycleReducer wires post-submit review delivery through lifecycle.
func WithLifecycleReducer(r Reducer) Option {
	return func(s *Service) { s.lifecycle = r }
}

// WithClock overrides the service clock for tests.
func WithClock(clock func() time.Time) Option {
	return func(s *Service) { s.clock = clock }
}

// WithTelemetry records review outcomes.
//
// Code review is a headline feature with no telemetry at all, so there is no way
// to answer whether reviewers are used, whether they approve or request changes,
// or how long a pass takes. Optional so the service still works unwired, which
// is how every existing test constructs it.
func WithTelemetry(sink ports.EventSink) Option {
	return func(s *Service) { s.telemetry = sink }
}

// emit reports an event when a sink is wired.
//
// Only enum-like fields are ever passed in. Never the review body, the PR URL,
// or the target SHA: the body is reviewer prose about someone's code, and the URL
// and SHA identify the repository. The daemon's remote allowlist would drop
// unknown keys anyway, but the intent belongs at the call site.
func (s *Service) emit(name string, sessionID domain.SessionID, payload map[string]any) {
	if s.telemetry == nil {
		return
	}
	session := sessionID
	s.telemetry.Emit(context.Background(), ports.TelemetryEvent{
		Name:       name,
		Source:     "review_service",
		OccurredAt: s.clock(),
		Level:      ports.TelemetryLevelInfo,
		SessionID:  &session,
		Payload:    payload,
	})
}

// New wraps a core review engine as the API-facing service.
func New(engine *reviewcore.Engine, store Store, opts ...Option) *Service {
	s := &Service{
		engine: engine,
		store:  store,
		clock:  func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Trigger starts (or reuses) a review pass for a worker's PR. An empty harness
// runs under the project's configured reviewer; a non-empty one overrides it for
// this pass only, so choosing a reviewer for one session leaves every other
// session in the project untouched.
func (s *Service) Trigger(
	ctx context.Context,
	workerID domain.SessionID,
	harness domain.ReviewerHarness,
) (reviewcore.TriggerResult, error) {
	result, err := s.engine.Trigger(ctx, workerID, harness)
	if err != nil {
		s.emit("ao.review.trigger_failed", workerID, map[string]any{
			"error_kind": reviewErrorKind(err),
		})
		return result, err
	}
	// created_runs distinguishes a genuinely new pass from a reuse of a running
	// or up-to-date one, which the engine also reports as success. harness is the
	// one actually used, resolved by the engine, not the caller's override, which
	// may be empty.
	s.emit("ao.review.triggered", workerID, map[string]any{
		"harness":      string(result.Run.Harness),
		"created_runs": len(result.CreatedRuns),
		"reused":       len(result.CreatedRuns) == 0,
	})
	return result, nil
}

// Cancel stops the live reviewer pane and marks running review passes as failed.
func (s *Service) Cancel(ctx context.Context, workerID domain.SessionID) (reviewcore.CancelResult, error) {
	result, err := s.engine.Cancel(ctx, workerID)
	if err != nil {
		return result, err
	}
	s.emit("ao.review.cancelled", workerID, map[string]any{
		"cancelled_runs": len(result.CancelledRuns),
	})
	return result, nil
}

// TerminateReviewer hard-destroys the reviewer pane for worker lifecycle
// teardown and marks any running review runs as cancelled.
func (s *Service) TerminateReviewer(ctx context.Context, workerID domain.SessionID, body string) error {
	_, err := s.engine.TerminateReviewer(ctx, workerID, body)
	return err
}

// TeardownReviewerTerminal removes reviewer panes during recovery-oriented
// worker shutdown while preserving review rows and native reviewer session ids.
func (s *Service) TeardownReviewerTerminal(ctx context.Context, workerID domain.SessionID) error {
	return s.engine.TeardownReviewerTerminal(ctx, workerID)
}

// RestoreReviewer relaunches an idle reviewer pane after its worker has been restored.
func (s *Service) RestoreReviewer(ctx context.Context, workerID domain.SessionID) error {
	_, err := s.engine.RestoreReviewer(ctx, workerID)
	return err
}

// SwitchReviewer atomically persists a worker's reviewer preference and returns
// the authoritative post-switch review state.
func (s *Service) SwitchReviewer(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness) (reviewcore.SessionReviews, error) {
	return s.engine.SwitchReviewer(ctx, workerID, harness)
}

// ActivitySignal is reviewer-owned hook metadata. It deliberately does not
// model activity state for session/Kanban display; for now hooks only keep the
// reviewer native conversation id up to date for restore.
type ActivitySignal struct {
	Event          string
	AgentSessionID string
}

// ApplyReviewActivitySignal records reviewer-owned hook facts without touching
// the worker session lifecycle row.
func (s *Service) ApplyReviewActivitySignal(ctx context.Context, reviewSessionID string, signal ActivitySignal) error {
	if reviewSessionID == "" {
		return fmt.Errorf("%w: review session id is required", ErrInvalid)
	}
	if _, ok, err := s.store.GetReviewByID(ctx, reviewSessionID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: review session %q", ErrNotFound, reviewSessionID)
	}
	if signal.AgentSessionID == "" {
		return nil
	}
	updated, err := s.store.UpdateReviewAgentSessionID(ctx, reviewSessionID, signal.AgentSessionID)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("%w: review session %q", ErrNotFound, reviewSessionID)
	}
	return nil
}

// SubmittedReview is one review result supplied by the reviewer CLI.
type SubmittedReview struct {
	RunID          string
	Verdict        domain.ReviewVerdict
	Body           string
	GithubReviewID string
}

// Submit records a reviewer's result for a specific worker review pass.
func (s *Service) Submit(ctx context.Context, workerID domain.SessionID, runID string, verdict domain.ReviewVerdict, body, githubReviewID string) (domain.ReviewRun, error) {
	runs, err := s.SubmitMany(ctx, workerID, []SubmittedReview{{
		RunID:          runID,
		Verdict:        verdict,
		Body:           body,
		GithubReviewID: githubReviewID,
	}})
	if err != nil {
		return domain.ReviewRun{}, err
	}
	if len(runs) == 0 {
		return domain.ReviewRun{}, fmt.Errorf("%w: no review result submitted", ErrInvalid)
	}
	return runs[0], nil
}

// SubmitMany records one reviewer CLI submission containing results for one or
// more PR-scoped runs. Delivery is scoped to the runs in this submission, so a
// missing/stuck result for another PR in the same trigger cannot block feedback.
func (s *Service) SubmitMany(ctx context.Context, workerID domain.SessionID, reviews []SubmittedReview) ([]domain.ReviewRun, error) {
	if workerID == "" {
		return nil, fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	if len(reviews) == 0 {
		return nil, fmt.Errorf("%w: at least one review result is required", ErrInvalid)
	}
	if s.store == nil {
		return nil, fmt.Errorf("review service store is not configured")
	}
	runs := make([]domain.ReviewRun, 0, len(reviews))
	for _, review := range reviews {
		run, err := s.submitOne(ctx, workerID, review)
		if err != nil {
			// A run superseded/terminated by a newer trigger is not submittable, but
			// it must not strand the valid sibling results in this submission — skip
			// it and keep delivering the rest. Genuine input errors still abort.
			if errors.Is(err, errRunSuperseded) {
				continue
			}
			return nil, err
		}
		runs = append(runs, run)
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%w: no submittable review runs in submission", ErrInvalid)
	}
	if s.lifecycle == nil {
		return runs, nil
	}
	delivered, err := s.deliverSubmitted(ctx, workerID, runs)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]domain.ReviewRun, len(delivered))
	for _, run := range delivered {
		byID[run.ID] = run
	}
	for i, run := range runs {
		if deliveredRun, ok := byID[run.ID]; ok {
			runs[i] = deliveredRun
		}
	}
	return runs, nil
}

func (s *Service) submitOne(ctx context.Context, workerID domain.SessionID, review SubmittedReview) (domain.ReviewRun, error) {
	runID := review.RunID
	verdict := review.Verdict
	body := review.Body
	githubReviewID := review.GithubReviewID
	if runID == "" {
		return domain.ReviewRun{}, fmt.Errorf("%w: review run id is required", ErrInvalid)
	}
	if !verdict.Valid() {
		return domain.ReviewRun{}, fmt.Errorf("%w: verdict must be %q or %q", ErrInvalid, domain.VerdictApproved, domain.VerdictChangesRequested)
	}
	if verdict == domain.VerdictChangesRequested && body == "" {
		return domain.ReviewRun{}, fmt.Errorf("%w: a changes_requested review requires a body", ErrInvalid)
	}
	run, ok, err := s.store.GetReviewRun(ctx, runID)
	if err != nil {
		return domain.ReviewRun{}, err
	}
	if !ok {
		return domain.ReviewRun{}, fmt.Errorf("%w: review run %q", ErrNotFound, runID)
	}
	if run.SessionID != workerID {
		return domain.ReviewRun{}, fmt.Errorf("%w: review run %q does not belong to worker %q", ErrInvalid, runID, workerID)
	}

	switch run.Status {
	case domain.ReviewRunRunning:
		updated, err := s.store.UpdateReviewRunResult(ctx, run.ID, domain.ReviewRunComplete, verdict, body, githubReviewID)
		if err != nil {
			return domain.ReviewRun{}, err
		}
		if !updated {
			return domain.ReviewRun{}, fmt.Errorf("%w: review run %q is not running", errRunSuperseded, runID)
		}
		run.Status = domain.ReviewRunComplete
		run.Verdict = verdict
		run.Body = body
		run.GithubReviewID = githubReviewID
		// Only on the real running -> complete transition. Re-submitting an
		// already-complete run returns early below, so telemetry stays idempotent
		// the same way the store does.
		s.emit("ao.review.submitted", workerID, map[string]any{
			"harness":            string(run.Harness),
			"verdict":            string(verdict),
			"duration_ms":        s.clock().Sub(run.CreatedAt).Milliseconds(),
			"posted_to_provider": githubReviewID != "",
		})
	case domain.ReviewRunComplete:
		if run.Verdict != verdict {
			return domain.ReviewRun{}, fmt.Errorf("%w: review run %q already recorded verdict %q", ErrInvalid, runID, run.Verdict)
		}
		if body != "" && body != run.Body {
			return domain.ReviewRun{}, fmt.Errorf("%w: review run %q already recorded a different body", ErrInvalid, runID)
		}
		if githubReviewID != "" && githubReviewID != run.GithubReviewID {
			return domain.ReviewRun{}, fmt.Errorf("%w: review run %q already recorded GitHub review id %q", ErrInvalid, runID, run.GithubReviewID)
		}
	case domain.ReviewRunDelivered:
		return run, nil
	default:
		return domain.ReviewRun{}, fmt.Errorf("%w: review run %q is not running", errRunSuperseded, runID)
	}
	return run, nil
}

func (s *Service) deliverSubmitted(ctx context.Context, workerID domain.SessionID, runs []domain.ReviewRun) ([]domain.ReviewRun, error) {
	deliverable, err := s.deliverableRuns(ctx, workerID, runs)
	if err != nil {
		return nil, err
	}
	if len(deliverable) == 0 {
		return nil, nil
	}
	results := reviewResults(workerID, deliverable)
	outcome, err := s.lifecycle.ApplyReviewBatch(ctx, workerID, results[0].BatchID, results)
	if err != nil {
		return nil, err
	}
	if outcome != lifecycle.ReviewDeliverySent {
		return nil, nil
	}
	deliveredAt := s.clock()
	delivered := make([]domain.ReviewRun, 0, len(deliverable))
	for _, run := range deliverable {
		updated, err := s.store.MarkReviewRunDelivered(ctx, run.ID, deliveredAt)
		if err != nil {
			return nil, err
		}
		if updated {
			run.Status = domain.ReviewRunDelivered
			run.DeliveredAt = &deliveredAt
			delivered = append(delivered, run)
		}
	}
	return delivered, nil
}

func (s *Service) deliverableRuns(ctx context.Context, workerID domain.SessionID, runs []domain.ReviewRun) ([]domain.ReviewRun, error) {
	currentHeads, err := s.currentHeadsByPR(ctx, workerID)
	if err != nil {
		return nil, err
	}
	deliverable := make([]domain.ReviewRun, 0, len(runs))
	for _, run := range runs {
		if run.Status != domain.ReviewRunComplete || run.Verdict != domain.VerdictChangesRequested || run.DeliveredAt != nil {
			continue
		}
		if currentHeads[run.PRURL] != run.TargetSHA {
			continue
		}
		deliverable = append(deliverable, run)
	}
	return deliverable, nil
}

func reviewResults(workerID domain.SessionID, runs []domain.ReviewRun) []lifecycle.ReviewResult {
	results := make([]lifecycle.ReviewResult, 0, len(runs))
	for _, run := range runs {
		results = append(results, lifecycle.ReviewResult{
			RunID:          run.ID,
			BatchID:        run.BatchID,
			WorkerID:       workerID,
			PRURL:          run.PRURL,
			TargetSHA:      run.TargetSHA,
			Verdict:        run.Verdict,
			Body:           run.Body,
			GithubReviewID: run.GithubReviewID,
			DeliveredAt:    run.DeliveredAt,
		})
	}
	return results
}

func (s *Service) currentHeadsByPR(ctx context.Context, workerID domain.SessionID) (map[string]string, error) {
	prs, err := s.store.ListPRsBySession(ctx, workerID)
	if err != nil {
		return nil, err
	}
	current := make(map[string]string, len(prs))
	for _, pr := range prs {
		current[pr.URL] = pr.HeadSHA
	}
	return current, nil
}

// List returns a worker's review state.
func (s *Service) List(ctx context.Context, workerID domain.SessionID) (reviewcore.SessionReviews, error) {
	return s.engine.List(ctx, workerID)
}
