package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

// Worker events are namespaced so a compromised sandbox cannot forge a
// control-plane or billing event onto its own session stream.
var workerEventTypes = map[string]struct{}{
	"agent.activity":       {},
	"worker.ready":         {},
	"chat.assistant_delta": {},
}

const (
	maxWorkerEventType   = 100
	maxWorkerControlBody = 8 << 10
	maxWorkerOutput      = 16 << 10
	maxWorkerError       = 4 << 10
)

// workerBootstrap redeems a one-time ticket for a live worker credential. It is
// the only unauthenticated worker route: the ticket itself is the proof, and it
// is consumed atomically so a replayed token buys nothing.
func (s *Server) workerBootstrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.workerTokens == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "Worker bootstrap is not enabled.")
		return
	}
	var input worker.BootstrapRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.BootstrapToken) == "" {
		writeError(w, r, http.StatusUnauthorized, "INVALID_BOOTSTRAP", "A bootstrap token is required.")
		return
	}

	ticket, err := s.store.RedeemWorkerBootstrapTicket(r.Context(), input.BootstrapToken)
	if errors.Is(err, postgres.ErrInvalidTicket) {
		writeError(w, r, http.StatusUnauthorized, "INVALID_BOOTSTRAP", "The bootstrap token is invalid, expired, or already used.")
		return
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if ticket.WorkerEpoch <= 0 {
		s.logger.Error("worker bootstrap produced no epoch", "session_id", ticket.SessionID, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "BOOTSTRAP_FAILED", "Worker bootstrap identity was not assigned.")
		return
	}

	launch, err := s.store.WorkerLaunchSpec(r.Context(), ticket.OrgID, ticket.SessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	workerID := worker.NextWorkerID(ticket.SessionID, ticket.WorkerEpoch)
	if err := s.store.RegisterWorkerBootstrap(
		r.Context(),
		ticket.OrgID,
		ticket.SessionID,
		workerID,
		input.Version,
		ticket.WorkerEpoch,
		input.Capabilities,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	scopes := ticket.Scopes
	if launch.Kind != "orchestrator" {
		scopes = slices.DeleteFunc(slices.Clone(scopes), func(scope string) bool {
			return scope == "worker:orchestrate"
		})
	}
	token, err := s.workerTokens.Issue(worker.Claims{
		OrgID:     ticket.OrgID,
		SessionID: ticket.SessionID,
		WorkerID:  workerID,
		Epoch:     ticket.WorkerEpoch,
		Scopes:    scopes,
	}, s.workerTokenTTL())
	if err != nil {
		s.logger.Error("issue worker token", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The worker credential could not be issued.")
		return
	}

	payload, _ := json.Marshal(map[string]any{"workerId": workerID, "epoch": ticket.WorkerEpoch})
	if _, err := s.store.AppendSessionEvent(
		r.Context(), ticket.OrgID, ticket.SessionID, "worker.connected", payload,
	); err != nil {
		s.logger.Warn("append worker.connected event", "error", err, "request_id", requestID(r))
	}

	writeJSON(w, http.StatusOK, worker.BootstrapResponse{
		WorkerToken: token,
		WorkerID:    workerID,
		Epoch:       ticket.WorkerEpoch,
		ExpiresIn:   int(s.workerTokenTTL().Seconds()),
		SessionID:   ticket.SessionID,
		Launch: worker.LaunchContext{
			SessionID:      launch.SessionID,
			ProjectID:      launch.ProjectID,
			Kind:           launch.Kind,
			Harness:        launch.Harness,
			DisplayName:    launch.DisplayName,
			Branch:         launch.Branch,
			Prompt:         launch.Prompt,
			AgentSessionID: launch.AgentSessionID,
			Mode:           launch.Mode,
			DeniedCommands: launch.DeniedCommands,
			RepositoryURL:  launch.RepositoryURL,
			DefaultBranch:  launch.DefaultBranch,
		},
	})
}

type workerContextKey struct{}

// workerAuth authenticates a live worker. A valid signature is not enough: the
// claimed epoch must still be the session's current one, so a worker that a
// recreate replaced is rejected even while its token is unexpired.
func (s *Server) workerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.workerTokens == nil {
			writeError(w, r, http.StatusNotFound, "not_found", "Worker routes are not enabled.")
			return
		}
		scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
		if !ok || !strings.EqualFold(scheme, "Worker") {
			writeError(w, r, http.StatusUnauthorized, "WORKER_AUTH_REQUIRED", "A worker credential is required.")
			return
		}
		claims, err := s.workerTokens.Verify(strings.TrimSpace(token))
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "INVALID_WORKER_TOKEN", "The worker credential is invalid or expired.")
			return
		}
		current, err := s.store.WorkerConnectionCurrent(
			r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID, claims.Epoch,
		)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		if !current {
			writeError(w, r, http.StatusUnauthorized, "STALE_WORKER_TOKEN", "The worker credential has been replaced.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), workerContextKey{}, claims)))
	})
}

func workerFrom(r *http.Request) worker.Claims {
	claims, _ := r.Context().Value(workerContextKey{}).(worker.Claims)
	return claims
}

// workerHeartbeat records liveness and renews the worker's short-lived token.
// This is the only path that promotes a sandbox to running.
func (s *Server) workerHeartbeat(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:connect") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:connect scope is required.")
		return
	}
	var input worker.HeartbeatRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.store.MarkWorkerSeen(
		r.Context(),
		claims.OrgID,
		claims.SessionID,
		claims.WorkerID,
		input.Version,
		claims.Epoch,
		input.Capabilities,
	); err != nil {
		if errors.Is(err, postgres.ErrStaleWorker) {
			writeError(w, r, http.StatusUnauthorized, "STALE_WORKER_TOKEN", "The worker credential has been replaced.")
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	renewed, err := s.workerTokens.Issue(claims, s.workerTokenTTL())
	if err != nil {
		s.logger.Error("renew worker token", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The worker credential could not be renewed.")
		return
	}
	writeJSON(w, http.StatusOK, worker.HeartbeatResponse{
		OK:          true,
		WorkerToken: renewed,
		ExpiresIn:   int(s.workerTokenTTL().Seconds()),
	})
}

// workerCheckoutGrant brokers a fresh repository-scoped installation token.
// The worker identity supplies the org and session; no repository or
// installation identifier is accepted from the sandbox.
func (s *Server) workerCheckoutGrant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:git scope is required.")
		return
	}
	if s.checkoutBroker == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SCM_BROKER_UNAVAILABLE", "Repository checkout is not available.")
		return
	}
	grant, err := s.checkoutBroker.IssueCheckoutGrant(r.Context(), claims.OrgID, claims.SessionID)
	if errors.Is(err, postgres.ErrForbidden) || errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "CHECKOUT_NOT_AUTHORIZED", "This session does not have an active repository grant.")
		return
	}
	if err != nil {
		s.logger.Error("issue worker checkout grant", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "SCM_BROKER_FAILED", "A repository checkout grant could not be issued.")
		return
	}
	if grant.Token == "" || grant.CloneURL == "" || !grant.ExpiresAt.After(time.Now()) {
		s.logger.Error("worker checkout broker returned an invalid grant", "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "SCM_BROKER_FAILED", "A repository checkout grant could not be issued.")
		return
	}
	writeJSON(w, http.StatusOK, worker.CheckoutGrantResponse{
		CloneURL: grant.CloneURL, Token: grant.Token, ExpiresAt: grant.ExpiresAt,
	})
}

func (s *Server) workerPushGrant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:git scope is required.")
		return
	}
	if s.checkoutBroker == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SCM_BROKER_UNAVAILABLE", "Repository push is not available.")
		return
	}
	grant, err := s.checkoutBroker.IssuePushGrant(r.Context(), claims.OrgID, claims.SessionID)
	if errors.Is(err, postgres.ErrForbidden) || errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "PUSH_NOT_AUTHORIZED", "This session does not have an active repository grant.")
		return
	}
	if err != nil {
		s.logger.Error("issue worker push grant", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "SCM_BROKER_FAILED", "A repository push grant could not be issued.")
		return
	}
	if grant.Token == "" || grant.CloneURL == "" || !grant.ExpiresAt.After(time.Now()) {
		s.logger.Error("worker push broker returned an invalid grant", "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "SCM_BROKER_FAILED", "A repository push grant could not be issued.")
		return
	}
	writeJSON(w, http.StatusOK, worker.CheckoutGrantResponse{
		CloneURL: grant.CloneURL, Token: grant.Token, ExpiresAt: grant.ExpiresAt,
	})
}

// workerGitHubToken gives worker-local Git tooling the same short-lived,
// repository-scoped write grant used by the explicit push bridge. The worker
// must request it on demand; no GitHub credential is persisted in the sandbox.
func (s *Server) workerGitHubToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:git scope is required.")
		return
	}
	if s.checkoutBroker == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SCM_BROKER_UNAVAILABLE", "GitHub credentials are not available.")
		return
	}
	grant, err := s.checkoutBroker.IssuePushGrant(r.Context(), claims.OrgID, claims.SessionID)
	if errors.Is(err, postgres.ErrForbidden) || errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "PUSH_NOT_AUTHORIZED", "This session does not have an active repository grant.")
		return
	}
	if err != nil {
		s.logger.Error("issue worker GitHub token", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "SCM_BROKER_FAILED", "A GitHub credential could not be issued.")
		return
	}
	if grant.Token == "" || !grant.ExpiresAt.After(time.Now()) {
		s.logger.Error("worker GitHub broker returned an invalid grant", "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "SCM_BROKER_FAILED", "A GitHub credential could not be issued.")
		return
	}
	writeJSON(w, http.StatusOK, worker.GitHubTokenResponse{
		Token: grant.Token, ExpiresAt: grant.ExpiresAt,
	})
}

func (s *Server) workerRaisePullRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:git scope is required.")
		return
	}
	if s.checkoutBroker == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SCM_BROKER_UNAVAILABLE", "Raising a pull request is not available.")
		return
	}
	var input worker.RaisePullRequestRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	input.HeadBranch = strings.TrimSpace(input.HeadBranch)
	if input.Title == "" || len(input.Title) > 256 {
		writeError(w, r, http.StatusBadRequest, "INVALID_TITLE", "The pull request title must be 1-256 characters.")
		return
	}
	if input.HeadBranch == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_HEAD_BRANCH", "The pushed branch name is required.")
		return
	}
	pr, err := s.checkoutBroker.RaisePullRequest(r.Context(), claims.OrgID, claims.SessionID, domain.RaisePullRequest{
		Title:      input.Title,
		Body:       input.Body,
		HeadBranch: input.HeadBranch,
		BaseBranch: input.BaseBranch,
	})
	if errors.Is(err, postgres.ErrForbidden) || errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "PULL_REQUEST_NOT_AUTHORIZED", "This session does not have an active repository grant.")
		return
	}
	if errors.Is(err, postgres.ErrInvalid) {
		writeError(w, r, http.StatusBadRequest, "INVALID_PULL_REQUEST", "The pull request could not be opened with the given branches.")
		return
	}
	if err != nil {
		s.logger.Error("raise worker pull request", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "PULL_REQUEST_FAILED", "The pull request could not be opened.")
		return
	}
	s.appendSessionProjectionEvent(r.Context(), claims.OrgID, claims.SessionID, "pull_request.created", pr)
	writeJSON(w, http.StatusCreated, worker.RaisePullRequestResponse{
		ID:         pr.ID,
		Number:     pr.Number,
		HTMLURL:    pr.URL,
		HeadBranch: pr.SourceBranch,
		BaseBranch: pr.TargetBranch,
	})
}

// workerClaimPullRequest records a pull request opened by worker-side tooling
// such as the GitHub CLI. GitHub has already created the PR by this point; this
// route makes the control plane the authoritative place that associates it
// with the worker/session and wakes the inspector projection.
func (s *Server) workerClaimPullRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:git scope is required.")
		return
	}
	if s.checkoutBroker == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SCM_BROKER_UNAVAILABLE", "Pull request tracking is not available.")
		return
	}
	var input worker.ClaimPullRequestRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Reference = strings.TrimSpace(input.Reference)
	if input.Reference == "" || len(input.Reference) > 2048 {
		writeError(w, r, http.StatusBadRequest, "INVALID_PULL_REQUEST", "A pull request number or URL is required.")
		return
	}
	pr, err := s.checkoutBroker.ClaimPullRequest(r.Context(), claims.OrgID, claims.SessionID, input.Reference)
	if errors.Is(err, postgres.ErrForbidden) || errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "PULL_REQUEST_NOT_AUTHORIZED", "This session does not have an active repository grant.")
		return
	}
	if errors.Is(err, postgres.ErrInvalid) {
		writeError(w, r, http.StatusBadRequest, "INVALID_PULL_REQUEST", "The pull request reference is invalid for this repository.")
		return
	}
	if err != nil {
		s.logger.Error("claim worker pull request", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "PULL_REQUEST_FAILED", "The pull request could not be tracked.")
		return
	}
	s.appendSessionProjectionEvent(r.Context(), claims.OrgID, claims.SessionID, "pull_request.claimed", pr)
	writeJSON(w, http.StatusOK, worker.ClaimPullRequestResponse{
		ID: pr.ID, Number: pr.Number, HTMLURL: pr.URL,
	})
}

func (s *Server) workerSubmitReview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:git") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:git scope is required.")
		return
	}
	if s.checkoutBroker == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SCM_BROKER_UNAVAILABLE", "Submitting a review is not available.")
		return
	}
	reviewRunID := chi.URLParam(r, "reviewRunId")
	if requireUUID(reviewRunID, "reviewRunId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reviewRunId must be a UUID.")
		return
	}
	var input worker.SubmitReviewRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	run, err := s.checkoutBroker.SubmitReview(r.Context(), claims.OrgID, claims.SessionID, reviewRunID, domain.SubmitReviewResult{
		Verdict: contract.AOReviewVerdict(strings.TrimSpace(input.Verdict)),
		Body:    input.Body,
	})
	if errors.Is(err, postgres.ErrForbidden) || errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "REVIEW_NOT_AUTHORIZED", "This session may not submit a verdict for this review.")
		return
	}
	if errors.Is(err, postgres.ErrInvalid) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REVIEW", "The review verdict could not be recorded.")
		return
	}
	if err != nil {
		s.logger.Error("submit worker review", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "REVIEW_FAILED", "The review could not be delivered.")
		return
	}
	s.appendSessionProjectionEvent(r.Context(), claims.OrgID, claims.SessionID, "review.submitted", run)
	writeJSON(w, http.StatusOK, worker.SubmitReviewResponse{ID: run.ID, Status: string(run.Status)})
}

// appendSessionProjectionEvent makes a completed worker-side state change
// observable to connected browser projections. The source of truth remains the
// normal database record; event persistence is deliberately best-effort here
// so an event-stream outage cannot turn a successful GitHub operation into a
// failed worker command.
func (s *Server) appendSessionProjectionEvent(
	ctx context.Context, orgID, sessionID, eventType string, payload any,
) {
	if s.store == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("marshal session projection event", "event_type", eventType, "error", err)
		return
	}
	if _, err := s.store.AppendSessionEvent(ctx, orgID, sessionID, eventType, raw); err != nil {
		s.logger.Error("append session projection event", "event_type", eventType, "error", err)
	}
}

// workerEvent publishes one worker-originated event onto the session stream.
func (s *Server) workerEvent(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:event") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:event scope is required.")
		return
	}
	var input struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := decodeJSONLimit(w, r, &input, maxWorkerOutput+maxWorkerControlBody); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Type = strings.TrimSpace(input.Type)
	if !allowedWorkerEventType(input.Type) {
		writeError(w, r, http.StatusBadRequest, "INVALID_EVENT_TYPE", "The worker event type is not allowed.")
		return
	}
	// ao_events.payload is constrained to a JSON object. Unmarshalling into a
	// map is not enough of a check on its own: JSON null unmarshals into a nil
	// map without error, so it would pass here and then fail the constraint as
	// a 500 rather than being refused as the bad request it is.
	if len(input.Payload) > 0 {
		var object map[string]any
		if err := json.Unmarshal(input.Payload, &object); err != nil || object == nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_EVENT_PAYLOAD", "The worker event payload must be a JSON object.")
			return
		}
	}
	switch input.Type {
	case "agent.activity":
		var activity worker.ActivityEvent
		if err := json.Unmarshal(input.Payload, &activity); err != nil ||
			!worker.ValidActivityEvent(activity) {
			writeError(w, r, http.StatusBadRequest, "INVALID_EVENT_PAYLOAD", "The agent activity payload is invalid.")
			return
		}
		launch, err := s.store.WorkerLaunchSpec(
			r.Context(), claims.OrgID, claims.SessionID,
		)
		if err != nil {
			s.writeWorkerStoreError(w, r, err)
			return
		}
		if activity.Harness != launch.Harness {
			writeError(w, r, http.StatusBadRequest, "INVALID_EVENT_PAYLOAD", "The activity harness does not match this session.")
			return
		}
		err = s.store.SetWorkerActivity(
			r.Context(),
			claims.OrgID,
			claims.SessionID,
			claims.WorkerID,
			claims.Epoch,
			activity,
		)
		if err != nil {
			s.writeWorkerStoreError(w, r, err)
			return
		}
		s.appendSessionProjectionEvent(
			r.Context(), claims.OrgID, claims.SessionID, input.Type, activity,
		)
	case "worker.ready":
		var ready worker.ReadyEvent
		if err := json.Unmarshal(input.Payload, &ready); err != nil ||
			ready.WorkerID != claims.WorkerID ||
			ready.Epoch != claims.Epoch ||
			strings.TrimSpace(ready.Version) == "" ||
			len(ready.Version) > 100 ||
			len(ready.Capabilities) > 64 {
			writeError(w, r, http.StatusBadRequest, "INVALID_EVENT_PAYLOAD", "The worker.ready payload is invalid.")
			return
		}
		if _, err := s.store.AppendSessionEvent(
			r.Context(), claims.OrgID, claims.SessionID, input.Type, input.Payload,
		); err != nil {
			s.writeWorkerStoreError(w, r, err)
			return
		}
	case "chat.assistant_delta":
		var output worker.OutputEvent
		if err := json.Unmarshal(input.Payload, &output); err != nil ||
			requireUUID(output.TurnID, "turnId") != nil ||
			output.Attempt <= 0 ||
			(output.Stream != "stdout" && output.Stream != "stderr") ||
			output.Text == "" ||
			len(output.Text) > maxWorkerOutput {
			writeError(w, r, http.StatusBadRequest, "INVALID_EVENT_PAYLOAD", "The assistant output payload is invalid.")
			return
		}
		if err := s.store.AppendWorkerTurnOutput(
			r.Context(),
			claims.OrgID,
			claims.SessionID,
			claims.WorkerID,
			output.TurnID,
			claims.Epoch,
			output.Attempt,
			output.Stream,
			output.Text,
		); err != nil {
			s.writeWorkerStoreError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func allowedWorkerEventType(eventType string) bool {
	if eventType == "" || len(eventType) > maxWorkerEventType {
		return false
	}
	_, allowed := workerEventTypes[eventType]
	return allowed
}

func (s *Server) workerClaimTurn(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:turn:claim") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:turn:claim scope is required.")
		return
	}
	var input worker.ClaimTurnRequest
	if err := decodeJSONLimit(w, r, &input, maxWorkerControlBody); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	turn, ok, err := s.store.ClaimWorkerTurn(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID, claims.Epoch,
	)
	if err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	response := worker.ClaimTurnResponse{}
	if ok {
		response.Turn = &worker.Turn{
			ID:              turn.ID,
			Prompt:          turn.Prompt,
			Mode:            turn.Mode,
			DeniedCommands:  turn.DeniedCommands,
			Harness:         turn.Harness,
			Attempt:         turn.Attempt,
			CancelRequested: turn.CancelRequested,
			AgentSessionID:  turn.AgentSessionID,
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) workerTurnCancellation(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:turn:poll") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:turn:poll scope is required.")
		return
	}
	turnID := chi.URLParam(r, "turnId")
	attempt, err := strconv.Atoi(r.URL.Query().Get("attempt"))
	if requireUUID(turnID, "turnId") != nil || err != nil || attempt <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid turnId and attempt are required.")
		return
	}
	requested, err := s.store.WorkerTurnCancellationRequested(
		r.Context(),
		claims.OrgID,
		claims.SessionID,
		claims.WorkerID,
		turnID,
		claims.Epoch,
		attempt,
	)
	if err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, worker.CancellationResponse{Requested: requested})
}

func (s *Server) workerCompleteTurn(w http.ResponseWriter, r *http.Request) {
	s.workerFinishTurn(w, r, "completed")
}

func (s *Server) workerFailTurn(w http.ResponseWriter, r *http.Request) {
	s.workerFinishTurn(w, r, "failed")
}

func (s *Server) workerFinishTurn(w http.ResponseWriter, r *http.Request, outcome string) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:turn:complete") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:turn:complete scope is required.")
		return
	}
	turnID := chi.URLParam(r, "turnId")
	if requireUUID(turnID, "turnId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "turnId must be a UUID.")
		return
	}
	attempt := 0
	errorMessage := ""
	if outcome == "failed" {
		var input worker.FailTurnRequest
		if err := decodeJSONLimit(w, r, &input, maxWorkerError+maxWorkerControlBody); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
			return
		}
		attempt = input.Attempt
		errorMessage = strings.TrimSpace(input.Error)
		if errorMessage == "" || len(errorMessage) > maxWorkerError {
			writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The failure message is invalid.")
			return
		}
	} else {
		var input worker.FinishTurnRequest
		if err := decodeJSONLimit(w, r, &input, maxWorkerControlBody); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
			return
		}
		attempt = input.Attempt
		if input.Cancelled {
			outcome = "cancelled"
		}
	}
	if attempt <= 0 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "attempt must be positive.")
		return
	}
	alreadyFinished, err := s.store.FinishWorkerTurn(
		r.Context(),
		claims.OrgID,
		claims.SessionID,
		claims.WorkerID,
		turnID,
		claims.Epoch,
		attempt,
		outcome,
		errorMessage,
	)
	if err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, worker.FinishTurnResponse{
		OK:              true,
		AlreadyFinished: alreadyFinished,
	})
}

func (s *Server) workerCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:credential:read") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:credential:read scope is required.")
		return
	}
	if s.secretCipher == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CREDENTIALS_UNAVAILABLE", "Coding-agent credentials are unavailable.")
		return
	}
	credential, err := s.store.WorkerAgentCredential(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID, claims.Epoch,
	)
	if err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	if !validAgentProvider(credential.Provider) ||
		!validAgentCredentialType(credential.Provider, credential.CredentialType) {
		writeError(w, r, http.StatusUnprocessableEntity, "INVALID_CREDENTIAL", "The selected coding-agent credential is invalid.")
		return
	}
	secretOwner := claims.OrgID
	if credential.OwnerUserID != "" {
		secretOwner = "user:" + credential.OwnerUserID
	}
	plaintext, err := s.secretCipher.Decrypt(
		credential.EncryptedSecret,
		credential.Nonce,
		providerSecretAssociatedData(secretOwner, credential.Provider),
	)
	if err != nil {
		s.logger.Error("decrypt worker credential", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_DECRYPTION_FAILED", "The coding-agent credential could not be decrypted.")
		return
	}
	defer clear(plaintext)
	writeJSON(w, http.StatusOK, worker.CredentialResponse{
		Provider:       credential.Provider,
		CredentialType: credential.CredentialType,
		Secret:         string(plaintext),
	})
}

func (s *Server) writeWorkerStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, postgres.ErrStaleWorker) {
		writeError(w, r, http.StatusUnauthorized, "STALE_WORKER_TOKEN", "The worker credential has been replaced.")
		return
	}
	s.writeStoreError(w, r, err)
}

// workerTokenTTL is how long an issued worker credential stays valid. An
// operator who shortens it gets a shorter blast radius on a leaked token at the
// cost of more renewals; an unset value falls back to the protocol default
// rather than to zero, which Issue would treat as "no lifetime at all".
func (s *Server) workerTokenTTL() time.Duration {
	if s.workerTokenLifetime > 0 {
		return s.workerTokenLifetime
	}
	return worker.DefaultTokenTTL
}
