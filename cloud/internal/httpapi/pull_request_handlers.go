package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
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
	ReviewID         string `json:"reviewId,omitempty"`
	File             string `json:"file,omitempty"`
	Line             int    `json:"line,omitempty"`
	Body             string `json:"body,omitempty"`
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
	ResolvedBy                 []pullRequestUnresolvedReviewerResponse `json:"resolvedBy"`
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
	AuthorAvatarURL  string                                 `json:"authorAvatarUrl,omitempty"`
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

func toPullRequestSummaryResponse(pr domain.PullRequest, snapshot domain.PullRequestSnapshot) pullRequestSummaryResponse {
	createdAt := pr.CreatedAt
	review := pullRequestReviewResponse(pr, snapshot)
	reasons := []string{}
	if review.HasUnresolvedHumanComments {
		reasons = append(reasons, "unresolved_comments")
	}
	return pullRequestSummaryResponse{
		URL:             pr.URL,
		HTMLURL:         pr.URL,
		Number:          pr.Number,
		Title:           pr.Title,
		State:           string(pr.State),
		Provider:        pr.Provider,
		Repository:      pr.Repository,
		Author:          pr.Author,
		AuthorAvatarURL: pr.AuthorAvatarURL,
		SourceBranch:    pr.SourceBranch,
		TargetBranch:    pr.TargetBranch,
		HeadSHA:         pr.HeadSHA,
		Additions:       pr.Additions,
		Deletions:       pr.Deletions,
		ChangedFiles:    pr.ChangedFiles,
		CI: pullRequestCISummaryResponse{
			State:         string(pr.CIState),
			FailingChecks: pullRequestFailingChecks(pr.Checks),
		},
		Review: review,
		Mergeability: pullRequestMergeabilitySummaryResponse{
			State:          string(pr.Mergeability),
			Reasons:        reasons,
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

func pullRequestReviewResponse(pr domain.PullRequest, snapshot domain.PullRequestSnapshot) pullRequestReviewSummaryResponse {
	out := pullRequestReviewSummaryResponse{Decision: string(pr.ReviewState), UnresolvedBy: []pullRequestUnresolvedReviewerResponse{}, ResolvedBy: []pullRequestUnresolvedReviewerResponse{}, Reviews: []pullRequestSubmittedReviewResponse{}}
	type group struct {
		count int
		links []pullRequestReviewCommentLinkResponse
		bot   bool
	}
	unresolved := map[string]*group{}
	resolved := map[string]*group{}
	for _, comment := range snapshot.Comments {
		if comment.IsBot {
			continue
		}
		reviewer := comment.Author
		if reviewer == "" {
			reviewer = "unknown"
		}
		target := unresolved
		if comment.Resolved || comment.Outdated {
			target = resolved
		}
		entry := target[reviewer]
		if entry == nil {
			entry = &group{}
			target[reviewer] = entry
		}
		entry.count++
		entry.links = append(entry.links, pullRequestReviewCommentLinkResponse{URL: comment.URL, ReviewID: comment.ReviewProviderID, File: comment.Path, Line: comment.Line, Body: comment.Body, AutoInjectReview: comment.AutoInjectReview})
	}
	appendGroups := func(source map[string]*group) []pullRequestUnresolvedReviewerResponse {
		keys := make([]string, 0, len(source))
		for key := range source {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make([]pullRequestUnresolvedReviewerResponse, 0, len(keys))
		for _, key := range keys {
			value := source[key]
			result = append(result, pullRequestUnresolvedReviewerResponse{ReviewerID: key, Count: value.count, Links: value.links, IsBot: value.bot})
		}
		return result
	}
	out.UnresolvedBy = appendGroups(unresolved)
	out.ResolvedBy = appendGroups(resolved)
	out.HasUnresolvedHumanComments = len(out.UnresolvedBy) > 0
	latest := map[string]domain.PullRequestReview{}
	for _, review := range snapshot.Reviews {
		reviewer := review.Author
		if reviewer == "" {
			reviewer = "unknown"
		}
		current, ok := latest[reviewer]
		if !ok || reviewTimeAfter(review, current) {
			latest[reviewer] = review
		}
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		review := latest[key]
		out.Reviews = append(out.Reviews, pullRequestSubmittedReviewResponse{ReviewerID: key, Verdict: string(review.State), Body: review.Body, ReviewURL: review.URL, SubmittedAt: review.SubmittedAt, IsBot: review.IsBot, AutoInjectReview: review.AutoInjectReview})
	}
	return out
}

func reviewTimeAfter(left, right domain.PullRequestReview) bool {
	if left.SubmittedAt == nil {
		return false
	}
	if right.SubmittedAt == nil {
		return true
	}
	return left.SubmittedAt.After(*right.SubmittedAt)
}

func pullRequestFailingChecks(snapshot json.RawMessage) []pullRequestFailingCheckResponse {
	var checks []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
		URL        string `json:"url"`
	}
	if len(snapshot) == 0 || json.Unmarshal(snapshot, &checks) != nil {
		return []pullRequestFailingCheckResponse{}
	}
	result := make([]pullRequestFailingCheckResponse, 0, len(checks))
	for _, check := range checks {
		switch check.Conclusion {
		case "failure", "timed_out", "action_required", "startup_failure", "cancelled":
			status := "failed"
			if check.Conclusion == "cancelled" {
				status = "cancelled"
			}
			result = append(result, pullRequestFailingCheckResponse{
				Name: check.Name, Status: status, Conclusion: check.Conclusion, URL: firstNonEmptyString(check.HTMLURL, check.URL),
			})
		}
	}
	return result
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
		snapshot, err := s.store.PullRequestSnapshot(r.Context(), orgID, pr.ID)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		items = append(items, toPullRequestSummaryResponse(pr, snapshot))
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
