// Package autoreview decides when the daemon may automatically review a
// worker's current pull-request heads.
package autoreview

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

const (
	// DefaultIdleThreshold is how long a worker must remain idle before an
	// automatic review may start.
	DefaultIdleThreshold = time.Minute
	// DefaultSweepInterval is the cadence for reevaluating live sessions.
	DefaultSweepInterval = time.Minute
)

// Store provides the durable session, project, PR, and review facts used by
// the coordinator.
type Store interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	ListAllSessions(context.Context) ([]domain.SessionRecord, error)
	GetProject(context.Context, string) (domain.ProjectRecord, bool, error)
	ListPRsBySession(context.Context, domain.SessionID) ([]domain.PullRequest, error)
	ListReviewRunsBySession(context.Context, domain.SessionID) ([]domain.ReviewRun, error)
}

// Trigger starts a source-tagged automatic review through the normal review
// engine.
type Trigger interface {
	TriggerAuto(context.Context, domain.SessionID, domain.ReviewerHarness) (reviewcore.TriggerResult, error)
}

// Result describes whether an evaluation started a review and why it skipped
// or triggered.
type Result struct {
	Triggered bool
	Reason    string
}

// Coordinator evaluates auto-review policy and owns the periodic sweep.
type Coordinator struct {
	store         Store
	reviews       Trigger
	clock         func() time.Time
	idleThreshold time.Duration
	sweepInterval time.Duration
	logger        *slog.Logger
}

// Config customizes coordinator timing and logging.
type Config struct {
	Clock         func() time.Time
	IdleThreshold time.Duration
	SweepInterval time.Duration
	Logger        *slog.Logger
}

// New constructs an auto-review coordinator.
func New(store Store, reviews Trigger, cfg Config) *Coordinator {
	c := &Coordinator{store: store, reviews: reviews, clock: cfg.Clock, idleThreshold: cfg.IdleThreshold, sweepInterval: cfg.SweepInterval, logger: cfg.Logger}
	if c.clock == nil {
		c.clock = time.Now
	}
	if c.idleThreshold <= 0 {
		c.idleThreshold = DefaultIdleThreshold
	}
	if c.sweepInterval <= 0 {
		c.sweepInterval = DefaultSweepInterval
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	return c
}

// EvaluateSession evaluates one worker against the current project, activity,
// PR, and review-run facts.
func (c *Coordinator) EvaluateSession(ctx context.Context, id domain.SessionID) (Result, error) {
	session, ok, err := c.store.GetSession(ctx, id)
	if err != nil || !ok {
		if err == nil {
			return Result{Reason: "session_not_found"}, nil
		}
		return Result{}, err
	}
	if reason := sessionGate(session, c.clock(), c.idleThreshold); reason != "" {
		return Result{Reason: reason}, nil
	}
	project, ok, err := c.store.GetProject(ctx, string(session.ProjectID))
	if err != nil || !ok {
		if err == nil {
			return Result{Reason: "missing_reviewer_harness"}, nil
		}
		return Result{}, err
	}
	harness := effectiveReviewerHarness(session, project.Config)
	if harness == "" {
		return Result{Reason: "missing_reviewer_harness"}, nil
	}
	prs, err := c.store.ListPRsBySession(ctx, id)
	if err != nil {
		return Result{}, err
	}
	runs, err := c.store.ListReviewRunsBySession(ctx, id)
	if err != nil {
		return Result{}, err
	}
	harnessRuns := make([]domain.ReviewRun, 0, len(runs))
	for _, run := range runs {
		if run.Harness == harness || run.Harness == "" {
			harnessRuns = append(harnessRuns, run)
		}
	}
	states := reviewcore.Plan(prs, harnessRuns)
	if len(states) == 0 {
		return Result{Reason: "no_pr"}, nil
	}
	eligible := false
	hasRunning := false
	reason := "planner_ineligible"
	for _, state := range states {
		switch state.Status {
		case reviewcore.ReviewStateRunning:
			hasRunning = true
			reason = "review_running"
		case reviewcore.ReviewStateUpToDate:
			reason = "already_approved"
		case reviewcore.ReviewStateChangesRequested:
			reason = "changes_requested_same_sha"
		case reviewcore.ReviewStateIneligible:
			reason = ineligibleReason(prs, state.PRURL)
		case reviewcore.ReviewStateNeedsReview:
			if blocked := existingHeadReason(harnessRuns, state.PRURL, state.TargetSHA); blocked != "" {
				reason = blocked
				continue
			}
			eligible = true
		}
	}
	if !eligible && !hasRunning {
		return Result{Reason: reason}, nil
	}
	result, err := c.reviews.TriggerAuto(ctx, id, harness)
	if err != nil {
		return Result{}, fmt.Errorf("trigger auto review for %s: %w", id, err)
	}
	if result.SkipReason != "" {
		return Result{Reason: result.SkipReason}, nil
	}
	if !result.Created {
		return Result{Reason: triggerResultReason(prs, result, harness)}, nil
	}
	return Result{Triggered: true, Reason: "triggered"}, nil
}

func triggerResultReason(prs []domain.PullRequest, result reviewcore.TriggerResult, harness domain.ReviewerHarness) string {
	for _, state := range result.Reviews {
		if state.Status == reviewcore.ReviewStateRunning {
			return "review_running"
		}
	}
	runs := make([]domain.ReviewRun, 0, len(result.Runs))
	for _, run := range result.Runs {
		if run.Harness == harness || run.Harness == "" {
			runs = append(runs, run)
		}
	}
	for _, pr := range prs {
		if blocked := existingHeadReason(runs, pr.URL, pr.HeadSHA); blocked != "" {
			return blocked
		}
	}
	return "planner_up_to_date"
}

func existingHeadReason(runs []domain.ReviewRun, prURL, targetSHA string) string {
	for _, run := range runs {
		if run.PRURL != prURL || run.TargetSHA != targetSHA {
			continue
		}
		if run.Status == domain.ReviewRunRunning {
			return "review_running"
		}
		if run.Status == domain.ReviewRunCancelled {
			return "cancelled_same_sha"
		}
		if run.Verdict == domain.VerdictApproved {
			return "already_approved"
		}
		if run.Verdict == domain.VerdictChangesRequested {
			return "changes_requested_same_sha"
		}
	}
	return ""
}

func effectiveReviewerHarness(session domain.SessionRecord, config domain.ProjectConfig) domain.ReviewerHarness {
	if session.ReviewerHarness != "" {
		return session.ReviewerHarness
	}
	return config.ResolveReviewerHarness(session.Harness)
}

func ineligibleReason(prs []domain.PullRequest, url string) string {
	for _, pr := range prs {
		if pr.URL != url {
			continue
		}
		switch {
		case pr.Draft:
			return "draft_pr"
		case pr.Merged:
			return "merged_pr"
		case pr.Closed:
			return "closed_pr"
		case pr.HeadSHA == "":
			return "missing_head_sha"
		default:
			return "planner_ineligible"
		}
	}
	return "planner_ineligible"
}

func sessionGate(session domain.SessionRecord, now time.Time, threshold time.Duration) string {
	if !session.AutoReviewEnabled {
		return "disabled"
	}
	if session.Kind != domain.KindWorker {
		return "not_worker"
	}
	if session.IsTerminated {
		return "terminated"
	}
	if session.Activity.State != domain.ActivityIdle {
		return "not_idle"
	}
	if session.Activity.LastActivityAt.IsZero() || now.Sub(session.Activity.LastActivityAt) < threshold {
		return "idle_threshold_not_met"
	}
	return ""
}

// Sweep evaluates every known session, isolating per-session failures.
func (c *Coordinator) Sweep(ctx context.Context) error {
	sessions, err := c.store.ListAllSessions(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if reason := sessionGate(session, c.clock(), c.idleThreshold); reason != "" {
			continue
		}
		if _, err := c.EvaluateSession(ctx, session.ID); err != nil {
			c.logger.Error("auto-review: evaluate session failed", "session_id", session.ID, "err", err)
		}
	}
	return nil
}

// Start evaluates persisted session and PR facts on the configured cadence
// until ctx is cancelled.
func (c *Coordinator) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(c.sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.Sweep(ctx); err != nil {
					c.logger.Error("auto-review: sweep failed", "err", err)
				}
			}
		}
	}()
	return done
}
