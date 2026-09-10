// Package review is the daemon's HTTP-facing code-review service boundary. The
// core orchestration lives in internal/review; this layer is the thin contract
// the API controller depends on and delegates to the engine, so the same engine
// can also back a future in-process CLI trigger.
package review

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/reqid"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
	"github.com/aoagents/agent-orchestrator/backend/internal/telemetrymeta"
)

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
	Trigger(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness, config domain.AgentConfig) (reviewcore.TriggerResult, error)
	RequestRereview(ctx context.Context, workerID domain.SessionID, prURL, reviewer string) error
	ResolveReviewComment(ctx context.Context, workerID domain.SessionID, prURL, commentURL string) error
	TriggerAuto(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness) (reviewcore.TriggerResult, error)
	Cancel(ctx context.Context, workerID domain.SessionID) (reviewcore.CancelResult, error)
	TerminateReviewer(ctx context.Context, workerID domain.SessionID, body string) error
	TeardownReviewerTerminal(ctx context.Context, workerID domain.SessionID) error
	RestoreReviewer(ctx context.Context, workerID domain.SessionID) error
	SwitchReviewer(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness, config domain.AgentConfig) (reviewcore.SessionReviews, error)
	ApplyReviewActivitySignal(ctx context.Context, reviewSessionID string, signal ActivitySignal) error
	Submit(ctx context.Context, workerID domain.SessionID, runID string, verdict domain.ReviewVerdict, body, githubReviewID string) (domain.ReviewRun, error)
	SubmitMany(ctx context.Context, workerID domain.SessionID, reviews []SubmittedReview) ([]domain.ReviewRun, error)
	List(ctx context.Context, workerID domain.SessionID) (reviewcore.SessionReviews, error)
}

// Service is the API-facing review service. It delegates to the core engine.
type Service struct {
	engine             *reviewcore.Engine
	store              Store
	requester          ports.SCMReviewRequester
	resolver           ports.SCMReviewResolver
	lifecycle          Reducer
	clock              func() time.Time
	telemetry          ports.EventSink
	codexOperationGate ports.CodexOperationGate
	// engineTrigger indirects the engine's source-tagged trigger so the
	// instrumented path can be exercised without standing up a full engine and
	// its eighteen-method store. Defaulted in New; only tests replace it.
	engineTrigger func(context.Context, domain.SessionID, domain.ReviewerHarness, domain.AgentConfig, domain.ReviewTriggerSource) (reviewcore.TriggerResult, error)
}

var _ Manager = (*Service)(nil)

// Store is the review_run persistence surface owned by the service submit path.
type Store interface {
	GetReviewByID(ctx context.Context, id string) (domain.Review, bool, error)
	UpdateReviewAgentSessionID(ctx context.Context, id, agentSessionID string) (bool, error)
	GetReviewRun(ctx context.Context, id string) (domain.ReviewRun, bool, error)
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
	UpdateReviewRunResult(ctx context.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, githubReviewID string, autoInjectReview bool) (bool, error)
	MarkReviewRunDelivered(ctx context.Context, id string, deliveredAt time.Time) (bool, error)
	ListPRsBySession(ctx context.Context, id domain.SessionID) ([]domain.PullRequest, error)
	ListPRReviews(ctx context.Context, prURL string) ([]domain.PullRequestReview, error)
	ListPRComments(ctx context.Context, prURL string) ([]domain.PullRequestComment, error)
	MarkPRCommentResolved(ctx context.Context, prURL, commentID string) (bool, error)
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

// WithReviewRequester wires provider-backed re-review requests.
func WithReviewRequester(requester ports.SCMReviewRequester) Option {
	return func(s *Service) { s.requester = requester }
}

// WithReviewResolver wires provider-backed review-thread resolution.
func WithReviewResolver(resolver ports.SCMReviewResolver) Option {
	return func(s *Service) { s.resolver = resolver }
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

// WithCodexAccountOperationGate prevents new Codex reviewer controllers from
// entering while the device-global Codex credential is changing.
func WithCodexAccountOperationGate(gate ports.CodexOperationGate) Option {
	return func(s *Service) { s.codexOperationGate = gate }
}

// emit reports an event when a sink is wired.
//
// Only enum-like fields are ever passed in. Never the review body, the PR URL,
// or the target SHA: the body is reviewer prose about someone's code, and the URL
// and SHA identify the repository. The daemon's remote allowlist would drop
// unknown keys anyway, but the intent belongs at the call site.
func (s *Service) emit(ctx context.Context, name string, sessionID domain.SessionID, payload map[string]any) {
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
		RequestID:  reqid.FromContext(ctx),
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
	if s.engineTrigger == nil {
		s.engineTrigger = func(
			ctx context.Context,
			workerID domain.SessionID,
			harness domain.ReviewerHarness,
			config domain.AgentConfig,
			source domain.ReviewTriggerSource,
		) (reviewcore.TriggerResult, error) {
			return s.engine.TriggerWithSource(ctx, workerID, harness, config, source)
		}
	}
	return s
}

// RequestRereview asks the SCM provider to request another review from reviewer
// on one of the worker session's tracked PRs.
func (s *Service) RequestRereview(ctx context.Context, workerID domain.SessionID, prURL, reviewer string) error {
	reviewer = strings.TrimSpace(strings.TrimPrefix(reviewer, "@"))
	if workerID == "" {
		return fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	if reviewer == "" {
		return fmt.Errorf("%w: reviewer is required", ErrInvalid)
	}
	if s.requester == nil {
		return fmt.Errorf("%w: review request provider is unavailable", ErrInvalid)
	}
	prs, err := s.store.ListPRsBySession(ctx, workerID)
	if err != nil {
		return err
	}
	if len(prs) == 0 {
		return fmt.Errorf("%w: worker %q has no PR", ErrInvalid, workerID)
	}
	pr, ok := selectRereviewPR(prs, prURL)
	if !ok {
		return fmt.Errorf("%w: pull request is not tracked for worker %q", ErrNotFound, workerID)
	}
	if pr.Closed || pr.Merged {
		return fmt.Errorf("%w: pull request is not open", ErrInvalid)
	}
	if !reviewerReviewedPR(ctx, s.store, pr.URL, reviewer) {
		return fmt.Errorf("%w: reviewer %q has not reviewed this PR", ErrInvalid, reviewer)
	}
	ref, err := reviewRequestRef(pr)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := s.requester.RequestReview(ctx, ports.SCMReviewRequest{PR: ref, Reviewer: reviewer}); err != nil {
		if errors.Is(err, ports.ErrSCMNotFound) {
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		if errors.Is(err, ports.ErrSCMUnsupported) {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return err
	}
	s.emit(ctx, "ao.review.rereview_requested", workerID, map[string]any{
		"provider": pr.Provider,
	})
	return nil
}

func selectRereviewPR(prs []domain.PullRequest, prURL string) (domain.PullRequest, bool) {
	want := strings.TrimSpace(prURL)
	if want == "" && len(prs) == 1 {
		return prs[0], true
	}
	for _, pr := range prs {
		if pr.URL == want || pr.HTMLURL == want {
			return pr, true
		}
	}
	return domain.PullRequest{}, false
}

func reviewerReviewedPR(ctx context.Context, store Store, prURL, reviewer string) bool {
	reviews, err := store.ListPRReviews(ctx, prURL)
	if err != nil {
		return false
	}
	for _, review := range reviews {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(review.Author), "@"), reviewer) {
			return true
		}
	}
	comments, err := store.ListPRComments(ctx, prURL)
	if err != nil {
		return false
	}
	for _, comment := range comments {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(comment.Author), "@"), reviewer) {
			return true
		}
	}
	return false
}

func reviewRequestRef(pr domain.PullRequest) (ports.SCMPRRef, error) {
	repo := ports.SCMRepo{Provider: pr.Provider, Host: pr.Host, Repo: pr.Repo}
	if repo.Provider == "" {
		repo.Provider = providerFromPRURL(pr.URL)
	}
	if repo.Host == "" {
		repo.Host = hostFromPRURL(pr.URL)
	}
	if repo.Repo != "" {
		parts := strings.SplitN(repo.Repo, "/", 2)
		if len(parts) == 2 {
			repo.Owner = parts[0]
			repo.Name = parts[1]
		}
	}
	if repo.Provider == "github" && (repo.Owner == "" || repo.Name == "") {
		owner, name := githubOwnerRepoFromPRURL(pr.URL)
		repo.Owner, repo.Name = owner, name
		if repo.Repo == "" && owner != "" && name != "" {
			repo.Repo = owner + "/" + name
		}
	}
	if pr.Number <= 0 || repo.Provider == "" || repo.Owner == "" || repo.Name == "" {
		return ports.SCMPRRef{}, fmt.Errorf("invalid pull request reference")
	}
	return ports.SCMPRRef{Repo: repo, Number: pr.Number, URL: pr.URL}, nil
}

func providerFromPRURL(raw string) string {
	if strings.Contains(raw, "/-/merge_requests/") {
		return "gitlab"
	}
	if strings.Contains(raw, "/pull/") {
		return "github"
	}
	return ""
}

func hostFromPRURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func githubOwnerRepoFromPRURL(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && parts[2] == "pull" {
		return parts[0], parts[1]
	}
	return "", ""
}

// ResolveReviewComment resolves the provider review thread that owns the given
// unresolved review comment.
func (s *Service) ResolveReviewComment(ctx context.Context, workerID domain.SessionID, prURL, commentURL string) error {
	commentURL = strings.TrimSpace(commentURL)
	if workerID == "" {
		return fmt.Errorf("%w: worker session id is required", ErrInvalid)
	}
	if commentURL == "" {
		return fmt.Errorf("%w: comment URL is required", ErrInvalid)
	}
	if s.resolver == nil {
		return fmt.Errorf("%w: review resolver provider is unavailable", ErrInvalid)
	}
	prs, err := s.store.ListPRsBySession(ctx, workerID)
	if err != nil {
		return err
	}
	pr, ok := selectRereviewPR(prs, prURL)
	if !ok {
		return fmt.Errorf("%w: pull request is not tracked for worker %q", ErrNotFound, workerID)
	}
	comments, err := s.store.ListPRComments(ctx, pr.URL)
	if err != nil {
		return err
	}
	var target domain.PullRequestComment
	for _, comment := range comments {
		if comment.URL == commentURL {
			target = comment
			break
		}
	}
	if target.ThreadID == "" {
		return fmt.Errorf("%w: review comment is not tracked for this PR", ErrNotFound)
	}
	if target.Resolved {
		return nil
	}
	ref, err := reviewRequestRef(pr)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := s.resolver.ResolveReviewThread(ctx, ports.SCMReviewResolveRequest{PR: ref, ThreadID: target.ThreadID}); err != nil {
		if errors.Is(err, ports.ErrSCMNotFound) {
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		if errors.Is(err, ports.ErrSCMUnsupported) {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return err
	}
	if updated, err := s.store.MarkPRCommentResolved(ctx, pr.URL, target.ID); err != nil {
		return err
	} else if !updated {
		return fmt.Errorf("%w: review comment is not tracked for this PR", ErrNotFound)
	}
	s.emit(ctx, "ao.review.comment_resolved", workerID, map[string]any{"provider": pr.Provider})
	return nil
}

// Trigger starts (or reuses) a review pass for a worker's PR. An empty harness
// runs under the project's configured reviewer; a non-empty one overrides it for
// this pass only, so choosing a reviewer for one session leaves every other
// session in the project untouched.
func (s *Service) Trigger(
	ctx context.Context,
	workerID domain.SessionID,
	harness domain.ReviewerHarness,
	config domain.AgentConfig,
) (reviewcore.TriggerResult, error) {
	return s.triggerWithSource(ctx, workerID, harness, config, domain.ReviewTriggerManual)
}

// TriggerAuto starts a daemon-initiated review pass.
func (s *Service) TriggerAuto(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness) (reviewcore.TriggerResult, error) {
	return s.triggerWithSource(ctx, workerID, harness, domain.AgentConfig{}, domain.ReviewTriggerAuto)
}

// triggerWithSource is the single instrumented trigger path. Both entry points
// route through it so an automatic pass is never invisible: before this, only
// the manual Trigger emitted, which made auto-review indistinguishable from
// manual review in every downstream funnel even though the two answer
// completely different product questions.
func (s *Service) triggerWithSource(
	ctx context.Context,
	workerID domain.SessionID,
	harness domain.ReviewerHarness,
	config domain.AgentConfig,
	source domain.ReviewTriggerSource,
) (reviewcore.TriggerResult, error) {
	triggeredPayload := map[string]any{"trigger": string(source)}
	if err := config.Validate(); err != nil {
		err = fmt.Errorf("%w: reviewer config: %w", ErrInvalid, err)
		s.emit(ctx, "ao.review.trigger_failed", workerID, map[string]any{
			"error_kind": reviewErrorKind(err),
			"trigger":    string(source),
		})
		s.emit(ctx, "ao.review.triggered", workerID, triggeredPayload)
		return reviewcore.TriggerResult{}, err
	}
	usesCodex := s.codexReviewUsesCodex(ctx, workerID, harness)
	var release func()
	if usesCodex && s.codexOperationGate != nil {
		var err error
		release, err = s.codexOperationGate.AcquireShared(ctx)
		if err != nil {
			return reviewcore.TriggerResult{}, err
		}
		defer release()
	}
	result, err := s.engineTrigger(ctx, workerID, harness, config, source)
	if err != nil {
		s.emit(ctx, "ao.review.trigger_failed", workerID, map[string]any{
			"error_kind": reviewErrorKind(err),
			"trigger":    string(source),
		})
		s.emit(ctx, "ao.review.triggered", workerID, triggeredPayload)
		return result, err
	}
	if result.Run.Harness != "" {
		triggeredPayload["harness"] = string(result.Run.Harness)
	}
	// ao.review.triggered counts every attempt. created_runs still counts only
	// brand-new rows, while Created also covers restart flows that relaunch a
	// pass against an existing row after a reviewer config change. reused must
	// stay false for those restarts even though created_runs is zero.
	createdOrRestarted := result.Created || len(result.CreatedRuns) > 0
	triggeredPayload["created_runs"] = len(result.CreatedRuns)
	triggeredPayload["reused"] = !createdOrRestarted
	s.emit(ctx, "ao.review.triggered", workerID, triggeredPayload)
	return result, nil
}

// Cancel stops the live reviewer pane and marks running review passes as failed.
func (s *Service) Cancel(ctx context.Context, workerID domain.SessionID) (reviewcore.CancelResult, error) {
	result, err := s.engine.Cancel(ctx, workerID)
	if err != nil {
		return result, err
	}
	s.emit(ctx, "ao.review.cancelled", workerID, map[string]any{
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
	release, err := s.acquireReviewerCodexAdmission(ctx, workerID, "")
	if err != nil {
		return err
	}
	defer release()
	_, err = s.engine.RestoreReviewer(ctx, workerID)
	return err
}

// CodexReviewerRunning reports whether the worker has a live Codex reviewer.
func (s *Service) CodexReviewerRunning(ctx context.Context, workerID domain.SessionID) (bool, error) {
	return s.engine.CodexReviewerRunning(ctx, workerID)
}

// CodexReviewerBusy reports whether the worker's Codex reviewer is active.
func (s *Service) CodexReviewerBusy(ctx context.Context, workerID domain.SessionID) (bool, error) {
	return s.engine.CodexReviewerBusy(ctx, workerID)
}

// CodexReviewerNativeSession returns the reviewer's exact native history identity.
func (s *Service) CodexReviewerNativeSession(ctx context.Context, workerID domain.SessionID) (string, bool, error) {
	return s.engine.CodexReviewerNativeSession(ctx, workerID)
}

// SnapshotCodexReviewer captures the live reviewer identity for an account switch.
func (s *Service) SnapshotCodexReviewer(ctx context.Context, workerID domain.SessionID) (ports.CodexReviewerControllerSnapshot, error) {
	return s.engine.SnapshotCodexReviewer(ctx, workerID)
}

// SuspendCodexReviewer stops the exact reviewer generation for account switching.
func (s *Service) SuspendCodexReviewer(ctx context.Context, workerID domain.SessionID) (bool, error) {
	return s.engine.SuspendCodexReviewer(ctx, workerID)
}

// SuspendCodexReviewerExact stops only the recorded reviewer identity.
func (s *Service) SuspendCodexReviewerExact(ctx context.Context, workerID domain.SessionID, expectedHandleID, expectedNativeSessionID string) (bool, error) {
	return s.engine.SuspendCodexReviewerExact(ctx, workerID, expectedHandleID, expectedNativeSessionID)
}

// RestoreCodexReviewer resumes the recorded reviewer native history.
func (s *Service) RestoreCodexReviewer(ctx context.Context, workerID domain.SessionID) error {
	return s.engine.RestoreCodexReviewer(ctx, workerID)
}

// RestoreCodexReviewerExact resumes only the recorded reviewer native history.
func (s *Service) RestoreCodexReviewerExact(ctx context.Context, workerID domain.SessionID, expectedNativeSessionID string) error {
	return s.engine.RestoreCodexReviewerExact(ctx, workerID, expectedNativeSessionID)
}

// SwitchReviewer atomically persists a worker's reviewer preference and returns
// the authoritative post-switch review state.
func (s *Service) SwitchReviewer(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness, config domain.AgentConfig) (reviewcore.SessionReviews, error) {
	release, err := s.acquireReviewerCodexAdmission(ctx, workerID, harness)
	if err != nil {
		return reviewcore.SessionReviews{}, err
	}
	defer release()
	return s.engine.SwitchReviewer(ctx, workerID, harness, config)
}

func (s *Service) codexReviewUsesCodex(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness) bool {
	if harness == domain.ReviewerCodex {
		return true
	}
	if harness != "" {
		return false
	}
	rec, ok, err := s.store.GetSession(ctx, workerID)
	return err == nil && ok && (rec.Harness == domain.HarnessCodex || rec.ReviewerHarness == domain.ReviewerCodex)
}

func (s *Service) acquireReviewerCodexAdmission(ctx context.Context, workerID domain.SessionID, harness domain.ReviewerHarness) (func(), error) {
	if s.codexOperationGate == nil || !s.codexReviewUsesCodex(ctx, workerID, harness) {
		return func() {}, nil
	}
	return s.codexOperationGate.AcquireShared(ctx)
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
			return nil, err
		}
		runs = append(runs, run)
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
		session, found, err := s.store.GetSession(ctx, workerID)
		if err != nil {
			return domain.ReviewRun{}, err
		}
		if !found {
			return domain.ReviewRun{}, fmt.Errorf("%w: worker session %q", ErrNotFound, workerID)
		}
		updated, err := s.store.UpdateReviewRunResult(ctx, run.ID, domain.ReviewRunComplete, verdict, body, githubReviewID, session.AutoInjectReview)
		if err != nil {
			return domain.ReviewRun{}, err
		}
		if !updated {
			return domain.ReviewRun{}, fmt.Errorf("%w: review run %q is not running", ErrInvalid, runID)
		}
		run.Status = domain.ReviewRunComplete
		run.Verdict = verdict
		run.Body = body
		run.GithubReviewID = githubReviewID
		run.AutoInjectReview = session.AutoInjectReview
		// Only on the real running -> complete transition. Re-submitting an
		// already-complete run returns early below, so telemetry stays idempotent
		// the same way the store does.
		s.emit(ctx, "ao.review.submitted", workerID, map[string]any{
			"harness":            string(run.Harness),
			"verdict":            string(verdict),
			"duration_ms":        s.clock().Sub(run.CreatedAt).Milliseconds(),
			"posted_to_provider": githubReviewID != "",
			// Which pass produced this verdict. A manual and an automatic review
			// mean different things about how the feature is being used, and the
			// verdict split between them is the whole question.
			"trigger": string(run.TriggerSource),
			// A size, never the text. Review depth is otherwise unobservable: a
			// changes-requested verdict with a two-line body and one with a full
			// findings list are the same event without it.
			"body_bytes": len(body),
			// Whether the session policy will let this result reach the worker at
			// all, recorded at the moment it is snapshotted onto the run.
			"auto_inject": session.AutoInjectReview,
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
		return domain.ReviewRun{}, fmt.Errorf("%w: review run %q is not running", ErrInvalid, runID)
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
		if run.Status != domain.ReviewRunComplete || run.Verdict != domain.VerdictChangesRequested || run.DeliveredAt != nil || !run.AutoInjectReview {
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
