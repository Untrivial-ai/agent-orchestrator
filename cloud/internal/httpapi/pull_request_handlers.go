package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

type pullRequestFailingCheckResponse struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

type mergePullRequestRequest struct {
	PRURL           string `json:"prUrl"`
	ExpectedHeadSHA string `json:"expectedHeadSha"`
}

func pullRequestSnapshotReadyForMerge(snapshot domain.PullRequestSnapshot, expectedHeadSHA string) bool {
	state := snapshot.Observation
	if state.State != contract.PRStateOpen || state.Draft || state.HeadSHA != expectedHeadSHA ||
		state.CIState != contract.CIPassing || state.Mergeability != contract.MergeMergeable ||
		(state.ReviewState != contract.ReviewApproved && state.ReviewState != contract.ReviewNone) || snapshot.ReviewsPartial {
		return false
	}
	for _, comment := range snapshot.Comments {
		if !comment.IsBot && !comment.Resolved && !comment.Outdated {
			return false
		}
	}
	return true
}

func (s *Server) mergeSessionPullRequest(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID := chi.URLParam(r, "orgId"), chi.URLParam(r, "sessionId")
	number, err := strconv.Atoi(chi.URLParam(r, "number"))
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil || number <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid organization, session and PR number are required.")
		return
	}
	var input mergePullRequestRequest
	if err := decodeJSON(w, r, &input); err != nil || strings.TrimSpace(input.ExpectedHeadSHA) == "" || strings.TrimSpace(input.PRURL) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A PR URL and expected head SHA are required.")
		return
	}
	pr, err := s.store.PullRequestForMerge(r.Context(), principalFrom(r), orgID, sessionID, number, input.PRURL)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if pr.Provider != "github" || pr.State != "open" || pr.Draft || pr.HeadSHA != input.ExpectedHeadSHA {
		writeError(w, r, http.StatusConflict, "pr_not_mergeable", "The pull request has changed. Refresh its status before merging.")
		return
	}
	snapshot, err := s.store.PullRequestSnapshot(r.Context(), orgID, pr.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if pr.CIState != "passing" || pr.Mergeability != "mergeable" ||
		(pr.ReviewState != "approved" && pr.ReviewState != "none") ||
		toPullRequestSummaryResponse(pr, snapshot).Review.HasUnresolvedHumanComments {
		writeError(w, r, http.StatusConflict, "pr_not_mergeable", "The pull request is not ready to merge.")
		return
	}
	owner, repo, ok := strings.Cut(pr.Repository, "/")
	if !ok || owner == "" || repo == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_repository", "The pull request repository is invalid.")
		return
	}
	// Credential precedence: prefer the GitHub App when the control plane has one
	// configured. The App is the installation-scoped credential that opened the PR
	// and is always current; a caller's stored PAT is a cached snapshot that can be
	// revoked while still recorded as validation_state=valid — preferring it let a
	// dead PAT shadow a healthy App and fail every merge with a 401 ("GitHub could
	// not refresh this pull request"). The PAT is used only when there is no App
	// (an App-less deployment) or as a fallback when the App path cannot serve the
	// merge (e.g. the App is not installed on that repository). This mirrors the
	// worker checkout/push/pull-request credential precedence. The webhook remains
	// responsible for recording the merged state.
	merged := false
	appTried := false
	if s.github != nil {
		appTried = true
		fresh, fetchErr := s.github.FetchPullRequestSnapshot(r.Context(), domain.PullRequestRef{ID: pr.ID, OrgID: orgID, Provider: pr.Provider, Repository: pr.Repository, Number: number})
		if fetchErr != nil {
			// Do not fail outright: fall through to a PAT fallback if one exists.
			s.logger.Warn("refresh pull request via GitHub App before merge; will try PAT fallback", "error", fetchErr, "request_id", requestID(r))
		} else {
			if !pullRequestSnapshotReadyForMerge(fresh, input.ExpectedHeadSHA) {
				writeError(w, r, http.StatusConflict, "pr_not_mergeable", "The pull request has changed. Refresh its status before merging.")
				return
			}
			err = s.github.MergePullRequest(r.Context(), orgID, pr.Repository, number, input.ExpectedHeadSHA)
			merged = true
		}
	}
	if !merged && s.patWrites != nil && s.secretCipher != nil {
		if credentialStore, ok := s.store.(userProviderConnectionStore); ok {
			principal := principalFrom(r)
			encrypted, nonce, credentialErr := credentialStore.UserProviderConnectionSecret(r.Context(), principal, githubPATProvider, defaultAgentConnectionLabel)
			if credentialErr == nil {
				secret, decryptErr := s.secretCipher.Decrypt(encrypted, nonce, providerSecretAssociatedData("user:"+principal.UserID, githubPATProvider))
				if decryptErr != nil {
					writeError(w, r, http.StatusInternalServerError, "internal_error", "The GitHub token could not be read.")
					return
				}
				defer clear(secret)
				fresh, fetchErr := s.patWrites.FetchPullRequestSnapshot(r.Context(), string(secret), owner, repo, number)
				if fetchErr != nil {
					s.logger.Error("refresh pull request before merge", "error", fetchErr, "request_id", requestID(r))
					writeError(w, r, http.StatusBadGateway, "github_unavailable", "GitHub could not refresh this pull request.")
					return
				}
				if !pullRequestSnapshotReadyForMerge(fresh, input.ExpectedHeadSHA) {
					writeError(w, r, http.StatusConflict, "pr_not_mergeable", "The pull request has changed. Refresh its status before merging.")
					return
				}
				err = s.patWrites.MergePullRequest(r.Context(), string(secret), owner, repo, number, input.ExpectedHeadSHA)
				merged = true
			} else if !errors.Is(credentialErr, postgres.ErrNotFound) {
				s.writeStoreError(w, r, credentialErr)
				return
			}
		}
	}
	if !merged {
		if appTried {
			// An App was configured but its refresh failed and no PAT could serve
			// the merge as a fallback — surface the refresh failure.
			writeError(w, r, http.StatusBadGateway, "github_unavailable", "GitHub could not refresh this pull request.")
			return
		}
		writeError(w, r, http.StatusServiceUnavailable, "github_unavailable", "GitHub merge is not configured for this account.")
		return
	}
	if err != nil {
		var githubErr *githubapp.HTTPError
		if errors.As(err, &githubErr) && (githubErr.StatusCode == http.StatusConflict || githubErr.StatusCode == http.StatusMethodNotAllowed || githubErr.StatusCode == http.StatusUnprocessableEntity) {
			writeError(w, r, http.StatusConflict, "pr_not_mergeable", "GitHub could not merge this pull request. Refresh its status and try again.")
			return
		}
		s.logger.Error("merge GitHub pull request", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "github_unavailable", "GitHub could not merge this pull request.")
		return
	}
	// Reflect the merge immediately so the client flips to the merged card without
	// waiting for GitHub's webhook round-trip (the webhook stays the backstop and the
	// authority for terminate-on-merge). Best effort via the App, which can read the
	// just-merged PR when it is installed on the repository; if the merge went through
	// a PAT because the App is absent, this fetch simply fails and the webhook/fallback
	// still converges. The refresh source is Webhook, not Fallback: this handler holds
	// no fallback lease, and the Fallback branch's lease-scoped update would roll back
	// the whole snapshot apply (dropping the merged flip + notification) whenever the
	// scanner happens to hold the row's lease. Webhook's unconditional upsert is both
	// correct and race-free here.
	if s.github != nil {
		if _, refreshErr := s.github.RefreshPullRequestStatus(r.Context(), domain.PullRequestRef{
			ID: pr.ID, OrgID: orgID, Provider: pr.Provider, Repository: pr.Repository, Number: number,
		}, domain.PullRequestRefreshContext{Source: domain.PullRequestRefreshWebhook}); refreshErr != nil {
			s.logger.Warn("refresh after merge", "error", refreshErr, "request_id", requestID(r))
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "merge_accepted"})
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
