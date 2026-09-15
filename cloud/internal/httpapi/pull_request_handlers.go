package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type pullRequestFailingCheckResponse struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

type pullRequestCISummaryResponse struct {
	State         string                            `json:"state"`
	FailingChecks []pullRequestFailingCheckResponse `json:"failingChecks"`
}

type pullRequestReviewCommentLinkResponse struct {
	URL              string `json:"url"`
	File             string `json:"file,omitempty"`
	Line             int    `json:"line,omitempty"`
	AutoInjectReview bool   `json:"autoInjectReview"`
}

type pullRequestUnresolvedReviewerResponse struct {
	ReviewerID string                                 `json:"reviewerId"`
	Count      int                                    `json:"count"`
	Links      []pullRequestReviewCommentLinkResponse `json:"links"`
	ReviewURL  string                                 `json:"reviewUrl,omitempty"`
	IsBot      bool                                   `json:"isBot,omitempty"`
}

type pullRequestSubmittedReviewResponse struct {
	ReviewerID       string     `json:"reviewerId"`
	Verdict          string     `json:"verdict"`
	Body             string     `json:"body,omitempty"`
	ReviewURL        string     `json:"reviewUrl,omitempty"`
	SubmittedAt      *time.Time `json:"submittedAt,omitempty"`
	IsBot            bool       `json:"isBot,omitempty"`
	AutoInjectReview bool       `json:"autoInjectReview"`
}

type pullRequestReviewSummaryResponse struct {
	Decision                   string                                  `json:"decision"`
	HasUnresolvedHumanComments bool                                    `json:"hasUnresolvedHumanComments"`
	UnresolvedBy               []pullRequestUnresolvedReviewerResponse `json:"unresolvedBy"`
	Reviews                    []pullRequestSubmittedReviewResponse    `json:"reviews"`
}

type pullRequestConflictFileResponse struct {
	Path string `json:"path"`
	URL  string `json:"url,omitempty"`
}

type pullRequestMergeabilitySummaryResponse struct {
	State          string                            `json:"state"`
	Reasons        []string                          `json:"reasons"`
	PullRequestURL string                            `json:"pullRequestUrl"`
	ConflictFiles  []pullRequestConflictFileResponse `json:"conflictFiles"`
}

type pullRequestSummaryResponse struct {
	URL              string                                 `json:"url"`
	HTMLURL          string                                 `json:"htmlUrl,omitempty"`
	Number           int                                    `json:"number"`
	Title            string                                 `json:"title"`
	State            string                                 `json:"state"`
	Provider         string                                 `json:"provider"`
	Repository       string                                 `json:"repository"`
	Author           string                                 `json:"author"`
	SourceBranch     string                                 `json:"sourceBranch"`
	TargetBranch     string                                 `json:"targetBranch"`
	HeadSHA          string                                 `json:"headSha"`
	Additions        int                                    `json:"additions"`
	Deletions        int                                    `json:"deletions"`
	ChangedFiles     int                                    `json:"changedFiles"`
	CI               pullRequestCISummaryResponse           `json:"ci"`
	Review           pullRequestReviewSummaryResponse       `json:"review"`
	Mergeability     pullRequestMergeabilitySummaryResponse `json:"mergeability"`
	StateChangedAt   *time.Time                             `json:"stateChangedAt,omitempty"`
	CreatedAt        *time.Time                             `json:"createdAt,omitempty"`
	UpdatedAt        time.Time                              `json:"updatedAt"`
	ObservedAt       time.Time                              `json:"observedAt"`
	CIObservedAt     time.Time                              `json:"ciObservedAt"`
	ReviewObservedAt time.Time                              `json:"reviewObservedAt"`
}

func toPullRequestSummaryResponse(pr domain.PullRequest) pullRequestSummaryResponse {
	createdAt := pr.CreatedAt
	return pullRequestSummaryResponse{
		URL:          pr.URL,
		HTMLURL:      pr.URL,
		Number:       pr.Number,
		Title:        pr.Title,
		State:        string(pr.State),
		Provider:     pr.Provider,
		Repository:   pr.Repository,
		Author:       pr.Author,
		SourceBranch: pr.SourceBranch,
		TargetBranch: pr.TargetBranch,
		HeadSHA:      pr.HeadSHA,
		Additions:    pr.Additions,
		Deletions:    pr.Deletions,
		ChangedFiles: pr.ChangedFiles,
		CI: pullRequestCISummaryResponse{
			State:         string(pr.CIState),
			FailingChecks: []pullRequestFailingCheckResponse{},
		},
		Review: pullRequestReviewSummaryResponse{
			Decision:     string(pr.ReviewState),
			UnresolvedBy: []pullRequestUnresolvedReviewerResponse{},
			Reviews:      []pullRequestSubmittedReviewResponse{},
		},
		Mergeability: pullRequestMergeabilitySummaryResponse{
			State:          string(pr.Mergeability),
			Reasons:        []string{},
			PullRequestURL: pr.URL,
			ConflictFiles:  []pullRequestConflictFileResponse{},
		},
		CreatedAt:        &createdAt,
		UpdatedAt:        pr.UpdatedAt,
		ObservedAt:       pr.ObservedAt,
		CIObservedAt:     pr.ObservedAt,
		ReviewObservedAt: pr.ObservedAt,
	}
}

func (s *Server) listSessionPullRequests(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	pullRequests, err := s.store.ListPullRequestsBySession(r.Context(), principalFrom(r), orgID, sessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]pullRequestSummaryResponse, 0, len(pullRequests))
	for _, pr := range pullRequests {
		items = append(items, toPullRequestSummaryResponse(pr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": sessionID, "pullRequests": items})
}

type aoReviewRunResponse struct {
	ID               string     `json:"id"`
	ReviewID         string     `json:"reviewId"`
	SessionID        string     `json:"sessionId"`
	BatchID          string     `json:"batchId"`
	Harness          string     `json:"harness"`
	PullRequestURL   string     `json:"pullRequestUrl"`
	TargetSHA        string     `json:"targetSha"`
	Status           string     `json:"status"`
	Verdict          string     `json:"verdict"`
	Body             string     `json:"body"`
	ProviderReviewID string     `json:"providerReviewId"`
	CreatedAt        time.Time  `json:"createdAt"`
	DeliveredAt      *time.Time `json:"deliveredAt,omitempty"`
	AutoInjectReview bool       `json:"autoInjectReview"`
}

func toAOReviewRunResponse(run domain.ReviewRunPullRequest, harness string) aoReviewRunResponse {
	return aoReviewRunResponse{
		ID:        run.ID,
		ReviewID:  run.ID,
		SessionID: run.ReviewSessionID,
		// Cloud runs are one-pass batches. Keep the stable run ID here rather
		// than inventing a second grouping record just to satisfy the shared
		// inspector's history model.
		BatchID:          run.ID,
		Harness:          harness,
		PullRequestURL:   run.PullRequestURL,
		TargetSHA:        run.TargetSHA,
		Status:           string(run.Status),
		Verdict:          string(run.Verdict),
		Body:             run.Body,
		ProviderReviewID: run.ProviderReviewID,
		CreatedAt:        run.CreatedAt,
		DeliveredAt:      run.DeliveredAt,
	}
}

type aoPullRequestReviewStateResponse struct {
	PullRequestURL    string               `json:"pullRequestUrl"`
	PullRequestNumber int                  `json:"pullRequestNumber"`
	Title             string               `json:"title"`
	TargetSHA         string               `json:"targetSha"`
	Status            string               `json:"status"`
	LatestRun         *aoReviewRunResponse `json:"latestRun,omitempty"`
	PreviousRun       *aoReviewRunResponse `json:"previousRun,omitempty"`
}

func (s *Server) sessionReviewPayload(r *http.Request, orgID, sessionID string) (map[string]any, error) {
	session, err := s.store.GetSession(r.Context(), principalFrom(r), orgID, sessionID)
	if err != nil {
		return nil, err
	}
	prs, err := s.store.ListPullRequestsBySession(r.Context(), principalFrom(r), orgID, sessionID)
	if err != nil {
		return nil, err
	}
	runs, err := s.store.ListReviewRunsBySession(r.Context(), principalFrom(r), orgID, sessionID)
	if err != nil {
		return nil, err
	}
	allRuns := make([]aoReviewRunResponse, 0, len(runs))
	runsByPR := make(map[string][]domain.ReviewRunPullRequest, len(prs))
	for _, run := range runs {
		allRuns = append(allRuns, toAOReviewRunResponse(run, session.Harness))
		runsByPR[run.PullRequestID] = append(runsByPR[run.PullRequestID], run)
	}
	reviews := make([]aoPullRequestReviewStateResponse, 0, len(prs))
	reviewerHandleID := ""
	for _, pr := range prs {
		state := reviewStateForPullRequest(pr, runsByPR[pr.ID])
		response := aoPullRequestReviewStateResponse{
			PullRequestURL: pr.URL, PullRequestNumber: pr.Number, Title: pr.Title,
			TargetSHA: pr.HeadSHA, Status: state,
		}
		if current := runsByPR[pr.ID]; len(current) > 0 {
			latest := toAOReviewRunResponse(current[0], session.Harness)
			response.LatestRun = &latest
			if latest.Status == "running" && current[0].ReviewTerminalID != "" {
				reviewerHandleID = current[0].ReviewTerminalID
			}
			if len(current) > 1 {
				previous := toAOReviewRunResponse(current[1], session.Harness)
				response.PreviousRun = &previous
			}
		}
		reviews = append(reviews, response)
	}
	return map[string]any{
		"sessionId":        sessionID,
		"reviewerHandleId": reviewerHandleID,
		"reviewerHarness":  session.Harness,
		"reviews":          nonNilReviews(reviews),
		"runs":             allRuns,
	}, nil
}

func reviewStateForPullRequest(pr domain.PullRequest, runs []domain.ReviewRunPullRequest) string {
	if pr.Draft || pr.State != contract.PRStateOpen || pr.HeadSHA == "" {
		return "ineligible"
	}
	if len(runs) == 0 || runs[0].TargetSHA != pr.HeadSHA {
		return "needs_review"
	}
	latest := runs[0]
	switch latest.Status {
	case contract.AOReviewRunRunning:
		return "running"
	case contract.AOReviewRunDelivered:
		if latest.Verdict == contract.AOReviewVerdictApproved {
			return "up_to_date"
		}
		if latest.Verdict == contract.AOReviewVerdictChangesRequested {
			return "changes_requested"
		}
	}
	return "needs_review"
}

func (s *Server) getSessionReviewState(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	payload, err := s.sessionReviewPayload(r, orgID, sessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// triggerSessionReviews starts one reviewer terminal for every open PR in the
// worker session that has not already been reviewed at its current head.
func (s *Server) triggerSessionReviews(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	if s.github == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SCM_BROKER_UNAVAILABLE", "Starting a review is not available.")
		return
	}
	prs, err := s.store.ListPullRequestsBySession(r.Context(), principalFrom(r), orgID, sessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	created := false
	for _, pr := range prs {
		if pr.Draft || pr.State != contract.PRStateOpen || pr.HeadSHA == "" {
			continue
		}
		_, didCreate, err := s.github.TriggerReview(r.Context(), orgID, sessionID, pr)
		if err != nil {
			s.logger.Error("trigger cloud review", "error", err, "request_id", requestID(r), "pull_request_id", pr.ID)
			writeError(w, r, http.StatusBadGateway, "REVIEW_FAILED", "The review could not be started.")
			return
		}
		created = created || didCreate
	}
	payload, err := s.sessionReviewPayload(r, orgID, sessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, payload)
}

func (s *Server) cancelSessionReviews(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	if s.github == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SCM_BROKER_UNAVAILABLE", "Cancelling a review is not available.")
		return
	}
	if _, err := s.github.CancelReviews(r.Context(), orgID, sessionID); err != nil {
		s.logger.Error("cancel cloud review", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "REVIEW_FAILED", "The review could not be cancelled.")
		return
	}
	payload, err := s.sessionReviewPayload(r, orgID, sessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func nonNilReviews(reviews []aoPullRequestReviewStateResponse) []aoPullRequestReviewStateResponse {
	if reviews == nil {
		return []aoPullRequestReviewStateResponse{}
	}
	return reviews
}
