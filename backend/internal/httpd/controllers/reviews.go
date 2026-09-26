package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
	reviewsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/review"
)

// ListReviewsResponse is the body of GET /api/v1/sessions/{sessionId}/reviews.
// reviewerHandleId is the live reviewer pane's runtime handle, for the UI to
// attach its terminal over /mux (empty when no reviewer has run).
type ListReviewsResponse struct {
	ReviewerHandleID      string                     `json:"reviewerHandleId"`
	ReviewerHarness       domain.ReviewerHarness     `json:"reviewerHarness,omitempty"`
	ReviewerActivityState string                     `json:"reviewerActivityState,omitempty" enum:"active,idle,waiting_input,blocked,exited"`
	Reviews               []reviewcore.PRReviewState `json:"reviews"`
	// Runs is every recorded pass for this session, newest first. Reviews only
	// carries the current and previous run per PR, which cannot answer "what did
	// the other reviewer say" once a third pass has run — so the client cannot
	// show one summary across reviewers without this.
	Runs            []domain.ReviewRun      `json:"runs"`
	ReviewerSurface *domain.ReviewerSurface `json:"reviewerSurface,omitempty"`
}

// ReviewRunResponse is the body of submit (200). It carries the run plus the
// reviewer pane handle so the UI can attach a terminal.
type ReviewRunResponse struct {
	Review           domain.ReviewRun   `json:"review"`
	Reviews          []domain.ReviewRun `json:"reviews"`
	ReviewerHandleID string             `json:"reviewerHandleId"`
}

// TriggerReviewResponse is the body of trigger (200/201). reviews carries the
// PR-scoped review state after the trigger.
type TriggerReviewResponse struct {
	ReviewerHandleID string                     `json:"reviewerHandleId"`
	Reviews          []reviewcore.PRReviewState `json:"reviews"`
	Runs             []domain.ReviewRun         `json:"runs"`
	ReviewerSurface  *domain.ReviewerSurface    `json:"reviewerSurface,omitempty"`
	// Created is true when a new review pass was started (HTTP 201) and false
	// when an existing run for the same commit was reused (HTTP 200).
	Created bool `json:"created" description:"True when a new review pass was started; false when an existing run for the same commit was reused."`
}

// CancelReviewResponse is the body of cancel (200). reviews carries the
// PR-scoped review state after running passes have been stopped.
type CancelReviewResponse struct {
	ReviewerHandleID string                     `json:"reviewerHandleId"`
	Reviews          []reviewcore.PRReviewState `json:"reviews"`
}

// RestoreReviewResponse is the body of reviewer session restore (200).
type RestoreReviewResponse struct {
	ReviewerHandleID string                     `json:"reviewerHandleId"`
	ReviewerHarness  domain.ReviewerHarness     `json:"reviewerHarness,omitempty"`
	Reviews          []reviewcore.PRReviewState `json:"reviews"`
	Runs             []domain.ReviewRun         `json:"runs"`
}

// KillReviewResponse is the body of reviewer session kill (200).
type KillReviewResponse struct {
	ReviewerHandleID string                     `json:"reviewerHandleId"`
	ReviewerHarness  domain.ReviewerHarness     `json:"reviewerHarness,omitempty"`
	Reviews          []reviewcore.PRReviewState `json:"reviews"`
	Runs             []domain.ReviewRun         `json:"runs"`
}

// SubmitReviewComment is one inline finding of a submit request.
type SubmitReviewComment struct {
	Path string `json:"path" description:"Repository path the finding anchors to."`
	Line int    `json:"line" description:"Line in the file's diff the finding anchors to."`
	Body string `json:"body" description:"Single-line finding body. Multi-line prose belongs in the review body."`
}

// SubmitReviewInput is the body of POST /api/v1/sessions/{sessionId}/reviews/submit.
// AO publishes the provider review itself, so GitHub review ids are outputs of
// this call, never inputs.
type SubmitReviewInput struct {
	RunID    string                `json:"runId,omitempty" description:"Review run id being completed."`
	Verdict  string                `json:"verdict,omitempty" description:"Review verdict: approved or changes_requested."`
	Body     string                `json:"body,omitempty" description:"Review body recorded by AO and published to the provider. Required for changes_requested."`
	Comments []SubmitReviewComment `json:"comments,omitempty" description:"Inline findings published as the review's anchored comments."`
}

// ReviewsController owns the session-scoped /reviews routes. A nil Svc returns 501.
type ReviewsController struct {
	Svc reviewsvc.Manager
}

// Register mounts the review routes on the supplied router.
func (c *ReviewsController) Register(r chi.Router) {
	r.Post("/reviews/{reviewSessionID}/activity", c.activity)
	r.Get("/sessions/{sessionId}/reviews", c.list)
	r.Post("/sessions/{sessionId}/reviews/trigger", c.trigger)
	r.Post("/sessions/{sessionId}/reviews/rerequest", c.rerequest)
	r.Post("/sessions/{sessionId}/reviews/comments/resolve", c.resolveComment)
	r.Post("/sessions/{sessionId}/reviews/cancel", c.cancel)
	r.Post("/sessions/{sessionId}/reviews/kill", c.kill)
	r.Post("/sessions/{sessionId}/reviews/restore", c.restore)
	r.Post("/sessions/{sessionId}/reviews/switch", c.switchReviewer)
	r.Post("/sessions/{sessionId}/reviews/submit", c.submit)
}

func (c *ReviewsController) activity(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/reviews/{reviewSessionID}/activity")
		return
	}
	reviewSessionID := strings.TrimSpace(chi.URLParam(r, "reviewSessionID"))
	if reviewSessionID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "REVIEW_ACTIVITY_INVALID", "Review session id is required", nil)
		return
	}
	var in SetReviewActivityRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	state := domain.ActivityState(strings.TrimSpace(in.State))
	if state != "" {
		switch state {
		case domain.ActivityActive, domain.ActivityIdle, domain.ActivityWaitingInput, domain.ActivityBlocked, domain.ActivityExited:
		default:
			// Reviewer hooks are best-effort. If a reviewer CLI stops emitting one
			// of AO's known activity states, degrade to a no-op instead of turning
			// review-run polling into a surfaced hook failure.
			state = ""
		}
	}
	agentSessionID := capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.AgentSessionID)))
	if state == "" && agentSessionID == "" {
		envelope.WriteJSON(w, http.StatusOK, SetReviewActivityResponse{OK: true, ReviewSessionID: reviewSessionID})
		return
	}
	if err := c.Svc.ApplyReviewActivitySignal(r.Context(), reviewSessionID, reviewsvc.ActivitySignal{
		Event:          capActivityMeta(domain.SanitizeControlChars(in.Event)),
		State:          state,
		AgentSessionID: agentSessionID,
		LaunchID:       capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.LaunchID))),
	}); err != nil {
		if errors.Is(err, reviewsvc.ErrNotFound) {
			envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "REVIEW_NOT_FOUND", "Unknown review session", nil)
			return
		}
		writeReviewError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SetReviewActivityResponse{OK: true, ReviewSessionID: reviewSessionID})
}

func (c *ReviewsController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/reviews")
		return
	}
	res, err := c.Svc.List(r.Context(), sessionID(r))
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	reviews := res.Reviews
	if reviews == nil {
		reviews = []reviewcore.PRReviewState{}
	}
	runs := res.Runs
	if runs == nil {
		runs = []domain.ReviewRun{}
	}
	envelope.WriteJSON(w, http.StatusOK, reviewsResponse(res, reviews, runs))
}

func (c *ReviewsController) trigger(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/reviews/trigger")
		return
	}
	// Body is optional: omitting it runs under the project's configured reviewer.
	var in TriggerReviewRequest
	if err := decodeJSON(r, &in); err != nil && !isEmptyBody(err) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	res, err := c.Svc.Trigger(r.Context(), sessionID(r), in.Harness, in.AgentConfig)
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	// 201 when a new pass was started; 200 when an existing run for the same
	// commit was reused.
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	reviews := res.Reviews
	if reviews == nil {
		reviews = []reviewcore.PRReviewState{}
	}
	runs := res.Runs
	if runs == nil {
		runs = []domain.ReviewRun{}
	}
	envelope.WriteJSON(w, status, TriggerReviewResponse{
		ReviewerHandleID: res.ReviewerHandleID,
		Reviews:          reviews,
		Runs:             runs,
		Created:          res.Created,
		ReviewerSurface:  reviewerSurfacePayload(res.ReviewerSurface),
	})
}

func (c *ReviewsController) resolveComment(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/reviews/comments/resolve")
		return
	}
	var in ResolveReviewCommentRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if err := c.Svc.ResolveReviewComment(r.Context(), sessionID(r), in.PullRequestURL, in.CommentURL); err != nil {
		writeReviewError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ResolveReviewCommentResponse{OK: true})
}

func (c *ReviewsController) rerequest(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/reviews/rerequest")
		return
	}
	var in RequestRereviewRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if err := c.Svc.RequestRereview(r.Context(), sessionID(r), in.PullRequestURL, in.ReviewerID); err != nil {
		writeReviewError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, RequestRereviewResponse{OK: true})
}

func (c *ReviewsController) cancel(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/reviews/cancel")
		return
	}
	res, err := c.Svc.Cancel(r.Context(), sessionID(r))
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	reviews := res.Reviews
	if reviews == nil {
		reviews = []reviewcore.PRReviewState{}
	}
	envelope.WriteJSON(w, http.StatusOK, CancelReviewResponse{
		ReviewerHandleID: res.ReviewerHandleID,
		Reviews:          reviews,
	})
}

func (c *ReviewsController) kill(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/reviews/kill")
		return
	}
	workerID := sessionID(r)
	if err := c.Svc.TerminateReviewer(r.Context(), workerID, "cancelled because reviewer session was killed"); err != nil {
		writeReviewError(w, r, err)
		return
	}
	res, err := c.Svc.List(r.Context(), workerID)
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	reviews := res.Reviews
	if reviews == nil {
		reviews = []reviewcore.PRReviewState{}
	}
	runs := res.Runs
	if runs == nil {
		runs = []domain.ReviewRun{}
	}
	envelope.WriteJSON(w, http.StatusOK, KillReviewResponse{ReviewerHandleID: res.ReviewerHandleID, ReviewerHarness: res.ReviewerHarness, Reviews: reviews, Runs: runs})
}

func (c *ReviewsController) restore(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/reviews/restore")
		return
	}
	workerID := sessionID(r)
	if err := c.Svc.RestoreReviewer(r.Context(), workerID); err != nil {
		writeReviewError(w, r, err)
		return
	}
	res, err := c.Svc.List(r.Context(), workerID)
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	reviews := res.Reviews
	if reviews == nil {
		reviews = []reviewcore.PRReviewState{}
	}
	runs := res.Runs
	if runs == nil {
		runs = []domain.ReviewRun{}
	}
	envelope.WriteJSON(w, http.StatusOK, RestoreReviewResponse{ReviewerHandleID: res.ReviewerHandleID, ReviewerHarness: res.ReviewerHarness, Reviews: reviews, Runs: runs})
}

func (c *ReviewsController) switchReviewer(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/reviews/switch")
		return
	}
	var in SetSessionReviewerRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	res, err := c.Svc.SwitchReviewer(r.Context(), sessionID(r), in.Harness, in.AgentConfig)
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, reviewsResponse(res, nil, nil))
}

func reviewsResponse(res reviewcore.SessionReviews, reviews []reviewcore.PRReviewState, runs []domain.ReviewRun) ListReviewsResponse {
	if reviews == nil {
		reviews = res.Reviews
	}
	if reviews == nil {
		reviews = []reviewcore.PRReviewState{}
	}
	if runs == nil {
		runs = res.Runs
	}
	if runs == nil {
		runs = []domain.ReviewRun{}
	}
	return ListReviewsResponse{
		ReviewerHandleID:      res.ReviewerHandleID,
		ReviewerHarness:       res.ReviewerHarness,
		ReviewerActivityState: string(res.ReviewerActivityState),
		Reviews:               reviews,
		Runs:                  runs,
		ReviewerSurface:       reviewerSurfacePayload(res.ReviewerSurface),
	}
}

func reviewerSurfacePayload(surface domain.ReviewerSurface) *domain.ReviewerSurface {
	if surface.ReviewID == "" {
		return nil
	}
	return &surface
}

func (c *ReviewsController) submit(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/reviews/submit")
		return
	}
	// The old contract let callers pass a batched "reviews" array and a
	// caller-posted "githubReviewId". Both are obsolete: one submission
	// carries one run, and GitHub review ids are daemon-produced outputs.
	// Reject them with a clear message instead of silently ignoring.
	var raw struct {
		SubmitReviewInput
		GithubReviewID string                `json:"githubReviewId"`
		Reviews        []SubmitReviewComment `json:"reviews"`
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_BODY", "Invalid request body", nil)
		return
	}
	if raw.GithubReviewID != "" {
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "REVIEW_INPUT_OBSOLETE", "githubReviewId is no longer accepted: AO publishes the review and records its id", nil)
		return
	}
	if len(raw.Reviews) > 0 {
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "REVIEW_INPUT_OBSOLETE", "batched reviews are no longer accepted: submit one review per run with runId, verdict, body, and comments", nil)
		return
	}
	in := raw.SubmitReviewInput
	runs, err := c.Svc.SubmitMany(r.Context(), sessionID(r), []reviewsvc.SubmittedReview{{
		RunID:    in.RunID,
		Verdict:  domain.ReviewVerdict(in.Verdict),
		Body:     in.Body,
		Findings: findingsFromInput(in.Comments),
	}})
	if err != nil {
		writeReviewError(w, r, err)
		return
	}
	first := domain.ReviewRun{}
	if len(runs) > 0 {
		first = runs[0]
	}
	envelope.WriteJSON(w, http.StatusOK, ReviewRunResponse{Review: first, Reviews: runs})
}

func findingsFromInput(comments []SubmitReviewComment) []domain.ReviewFinding {
	if len(comments) == 0 {
		return nil
	}
	findings := make([]domain.ReviewFinding, 0, len(comments))
	for _, comment := range comments {
		findings = append(findings, domain.ReviewFinding{Path: comment.Path, Line: comment.Line, Body: comment.Body})
	}
	return findings
}

func writeReviewError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, reviewsvc.ErrInvalid):
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "REVIEW_INVALID", err.Error(), nil)
	case errors.Is(err, reviewsvc.ErrNotFound):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "REVIEW_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, reviewsvc.ErrAgentBinaryNotFound):
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "REVIEWER_BINARY_NOT_FOUND", err.Error(), nil)
	default:
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "REVIEW_OPERATION_FAILED", "Review operation failed", nil)
	}
}
