package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

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

func toAOReviewRunResponse(run domain.ReviewRunPullRequest) aoReviewRunResponse {
	return aoReviewRunResponse{
		ID:               run.ID,
		ReviewID:         run.ID,
		SessionID:        run.ReviewSessionID,
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

func (s *Server) getSessionReviewState(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	runs, err := s.store.ListReviewRunsBySession(r.Context(), principalFrom(r), orgID, sessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	allRuns := make([]aoReviewRunResponse, 0, len(runs))
	var reviews []aoPullRequestReviewStateResponse
	var currentPullRequestID string
	for _, run := range runs {
		allRuns = append(allRuns, toAOReviewRunResponse(run))
		response := toAOReviewRunResponse(run)
		if run.PullRequestID == currentPullRequestID && len(reviews) > 0 {
			if reviews[len(reviews)-1].PreviousRun == nil {
				reviews[len(reviews)-1].PreviousRun = &response
			}
			continue
		}
		currentPullRequestID = run.PullRequestID
		reviews = append(reviews, aoPullRequestReviewStateResponse{
			PullRequestURL:    run.PullRequestURL,
			PullRequestNumber: run.PullRequestNumber,
			Title:             run.PullRequestTitle,
			TargetSHA:         run.TargetSHA,
			Status:            string(run.PullRequestAOReviewState),
			LatestRun:         &response,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionId": sessionID,
		"reviews":   nonNilReviews(reviews),
		"runs":      allRuns,
	})
}

func nonNilReviews(reviews []aoPullRequestReviewStateResponse) []aoPullRequestReviewStateResponse {
	if reviews == nil {
		return []aoPullRequestReviewStateResponse{}
	}
	return reviews
}
