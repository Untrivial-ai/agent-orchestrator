package session

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// PRSummary is the user-facing SCM read model for one PR owned by a session.
type PRSummary struct {
	URL          string
	HTMLURL      string
	Number       int
	Title        string
	State        domain.PRState
	Provider     string
	Repo         string
	Author       string
	SourceBranch string
	TargetBranch string
	HeadSHA      string
	Additions    int
	Deletions    int
	ChangedFiles int
	CI           PRCISummary
	Review       PRReviewSummary
	Mergeability PRMergeabilitySummary
	// StateChangedAt is when the current draft/open/merged/closed state became
	// active. It is backend-selected from durable PR/provider facts.
	StateChangedAt   time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ObservedAt       time.Time
	CIObservedAt     time.Time
	ReviewObservedAt time.Time
}

// PRCISummary describes the latest CI status and failing checks for a PR.
type PRCISummary struct {
	State         domain.CIState
	FailingChecks []PRFailingCheck
}

// PRFailingCheck is one failed or cancelled CI check for a PR.
type PRFailingCheck struct {
	Name       string
	Status     domain.PRCheckStatus
	Conclusion string
	URL        string
}

// PRReviewSummary describes the latest review decision and unresolved comments.
type PRReviewSummary struct {
	Decision                   domain.ReviewDecision
	HasUnresolvedHumanComments bool
	UnresolvedBy               []PRUnresolvedReviewer
	// Reviews is the latest decisive submitted review per reviewer, carrying
	// the reviewer's summary body so the UI can show a verdict with context.
	// Inline review comment bodies are deliberately not included here; they
	// stay folded into UnresolvedBy counts and links.
	Reviews []PRReviewEntry
}

// PRReviewEntry is one submitted provider review summary: a reviewer's decisive
// verdict and the body they submitted with it.
type PRReviewEntry struct {
	Reviewer         string
	Verdict          domain.ReviewDecision
	Body             string
	URL              string
	SubmittedAt      time.Time
	IsBot            bool
	AutoInjectReview bool
}

// PRUnresolvedReviewer groups unresolved human comments by reviewer.
type PRUnresolvedReviewer struct {
	ReviewerID string
	Count      int
	Links      []PRReviewCommentLink
	ReviewURL  string
	IsBot      bool
}

// PRReviewCommentLink points to one unresolved review comment.
type PRReviewCommentLink struct {
	URL              string
	File             string
	Line             int
	AutoInjectReview bool
}

// PRMergeabilitySummary describes whether a PR can be merged and why.
type PRMergeabilitySummary struct {
	State         domain.Mergeability
	Reasons       []string
	PRURL         string
	ConflictFiles []PRConflictFile
}

// PRConflictFile is one file involved in a PR merge conflict.
type PRConflictFile struct {
	Path string
	URL  string
}

// ListPRSummaries returns all PRs owned by a session with concise SCM details
// assembled from persisted PR/check/review facts.
func (s *Service) ListPRSummaries(ctx context.Context, id domain.SessionID) ([]PRSummary, error) {
	if _, ok, err := s.store.GetSession(ctx, id); err != nil {
		return nil, fmt.Errorf("get %s: %w", id, err)
	} else if !ok {
		return nil, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	prs, err := s.store.ListPRsBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	groups := groupPullRequestAliases(prs)
	out := make([]PRSummary, 0, len(groups))
	for _, group := range groups {
		var checks []domain.PullRequestCheck
		var threads []domain.PullRequestReviewThread
		var reviews []domain.PullRequestReview
		var comments []domain.PullRequestComment
		for _, pr := range group.aliases {
			prChecks, err := s.store.ListChecks(ctx, pr.URL)
			if err != nil {
				return nil, err
			}
			checks = append(checks, prChecks...)
			prThreads, err := s.store.ListPRReviewThreads(ctx, pr.URL)
			if err != nil {
				return nil, err
			}
			threads = append(threads, prThreads...)
			prReviews, err := s.store.ListPRReviews(ctx, pr.URL)
			if err != nil {
				return nil, err
			}
			reviews = append(reviews, prReviews...)
			prComments, err := s.store.ListPRComments(ctx, pr.URL)
			if err != nil {
				return nil, err
			}
			comments = append(comments, prComments...)
		}
		out = append(out, summarizePR(group.primary, checks, reviews, threads, comments))
	}
	sortPRSummaries(out)
	return out, nil
}

func summarizePR(pr domain.PullRequest, checks []domain.PullRequestCheck, reviews []domain.PullRequestReview, threads []domain.PullRequestReviewThread, comments []domain.PullRequestComment) PRSummary {
	return PRSummary{
		URL:              pr.URL,
		HTMLURL:          firstNonEmpty(pr.HTMLURL, pr.URL),
		Number:           pr.Number,
		Title:            pr.Title,
		State:            pullRequestState(pr),
		Provider:         firstNonEmpty(pr.Provider, "github"),
		Repo:             pr.Repo,
		Author:           pr.Author,
		SourceBranch:     pr.SourceBranch,
		TargetBranch:     pr.TargetBranch,
		HeadSHA:          pr.HeadSHA,
		Additions:        pr.Additions,
		Deletions:        pr.Deletions,
		ChangedFiles:     pr.ChangedFiles,
		CI:               summarizeCI(pr, checks),
		Review:           summarizeReview(pr, comments, reviews),
		Mergeability:     summarizeMergeability(pr, threads),
		StateChangedAt:   summarizePRStateChangedAt(pr),
		CreatedAt:        pr.CreatedAtProvider,
		UpdatedAt:        pr.UpdatedAt,
		ObservedAt:       pr.ObservedAt,
		CIObservedAt:     pr.CIObservedAt,
		ReviewObservedAt: pr.ReviewObservedAt,
	}
}

func summarizePRStateChangedAt(pr domain.PullRequest) time.Time {
	if !pr.StateChangedAt.IsZero() {
		return pr.StateChangedAt
	}
	switch pullRequestState(pr) {
	case domain.PRStateMerged:
		return pr.MergedAtProvider
	case domain.PRStateClosed:
		return pr.ClosedAtProvider
	case domain.PRStateDraft, domain.PRStateOpen:
		return pr.CreatedAtProvider
	default:
		return time.Time{}
	}
}

func summarizeCI(pr domain.PullRequest, checks []domain.PullRequestCheck) PRCISummary {
	state := ciOrUnknown(pr.CI)
	out := PRCISummary{State: state}
	if state != domain.CIFailing || pr.Merged || pr.Closed {
		return out
	}
	for _, ch := range checks {
		if ch.Status != domain.PRCheckFailed && ch.Status != domain.PRCheckCancelled {
			continue
		}
		if pr.HeadSHA != "" && ch.CommitHash != "" && !strings.EqualFold(ch.CommitHash, pr.HeadSHA) {
			continue
		}
		out.FailingChecks = append(out.FailingChecks, PRFailingCheck{
			Name:       ch.Name,
			Status:     ch.Status,
			Conclusion: ch.Conclusion,
			URL:        ch.URL,
		})
	}
	return out
}

func summarizeReview(pr domain.PullRequest, comments []domain.PullRequestComment, reviews []domain.PullRequestReview) PRReviewSummary {
	out := PRReviewSummary{Decision: reviewOrNone(pr.Review)}
	if pr.Merged || pr.Closed {
		return out
	}
	byReviewer := map[string]int{}
	order := []string{}
	links := map[string][]PRReviewCommentLink{}
	isBot := map[string]bool{}
	for _, c := range comments {
		if c.Resolved || c.IsBot {
			continue
		}
		reviewer := strings.TrimSpace(c.Author)
		if reviewer == "" {
			reviewer = "unknown"
		}
		if _, ok := byReviewer[reviewer]; !ok {
			order = append(order, reviewer)
		}
		byReviewer[reviewer]++
		isBot[reviewer] = c.IsBot
		links[reviewer] = append(links[reviewer], PRReviewCommentLink{
			URL:              c.URL,
			File:             c.File,
			Line:             c.Line,
			AutoInjectReview: c.AutoInjectReview,
		})
	}
	latestReviews := latestChangesRequestedReviews(reviews)
	reviewURLByAuthor := map[string]string{}
	for reviewer, review := range latestReviews {
		if _, ok := byReviewer[reviewer]; !ok {
			order = append(order, reviewer)
		}
		reviewURLByAuthor[reviewer] = review.URL
		isBot[reviewer] = review.IsBot
	}
	// Reviews carries every reviewer's latest decisive verdict (approved and
	// changes_requested alike), not just the changes-requested subset used for
	// the unresolved-comment grouping above, so an approved review's summary
	// body is surfaced too.
	for reviewer, review := range latestDecisiveReviews(reviews) {
		out.Reviews = append(out.Reviews, PRReviewEntry{
			Reviewer:         reviewer,
			Verdict:          reviewOrNone(review.State),
			Body:             review.Body,
			URL:              review.URL,
			SubmittedAt:      review.SubmittedAt,
			IsBot:            review.IsBot,
			AutoInjectReview: review.AutoInjectReview,
		})
	}
	sort.Slice(out.Reviews, func(i, j int) bool { return out.Reviews[i].Reviewer < out.Reviews[j].Reviewer })
	sort.Strings(order)
	for _, reviewer := range order {
		out.UnresolvedBy = append(out.UnresolvedBy, PRUnresolvedReviewer{
			ReviewerID: reviewer,
			Count:      byReviewer[reviewer],
			Links:      links[reviewer],
			ReviewURL:  reviewURLByAuthor[reviewer],
			IsBot:      isBot[reviewer],
		})
	}
	for _, reviewer := range out.UnresolvedBy {
		if reviewer.Count > 0 && !reviewer.IsBot {
			out.HasUnresolvedHumanComments = true
			break
		}
	}
	return out
}

// latestDecisiveReviews returns each reviewer's most recent decisive review —
// the latest one whose verdict is approved or changes_requested.
func latestDecisiveReviews(reviews []domain.PullRequestReview) map[string]domain.PullRequestReview {
	latestByReviewer := map[string]domain.PullRequestReview{}
	for _, review := range reviews {
		if review.State != domain.ReviewChangesRequest && review.State != domain.ReviewApproved {
			continue
		}
		reviewer := strings.TrimSpace(review.Author)
		if reviewer == "" {
			reviewer = "unknown"
		}
		current, ok := latestByReviewer[reviewer]
		if !ok || reviewAfter(review, current) {
			latestByReviewer[reviewer] = review
		}
	}
	return latestByReviewer
}

// latestChangesRequestedReviews narrows latestDecisiveReviews to reviewers whose
// latest decisive verdict is changes_requested — used to attach a review link to
// the unresolved-comment grouping.
func latestChangesRequestedReviews(reviews []domain.PullRequestReview) map[string]domain.PullRequestReview {
	out := map[string]domain.PullRequestReview{}
	for reviewer, review := range latestDecisiveReviews(reviews) {
		if review.State == domain.ReviewChangesRequest {
			out[reviewer] = review
		}
	}
	return out
}

func reviewAfter(a, b domain.PullRequestReview) bool {
	if a.SubmittedAt.IsZero() || b.SubmittedAt.IsZero() {
		return a.SubmittedAt.IsZero() == b.SubmittedAt.IsZero() && a.ID > b.ID
	}
	if a.SubmittedAt.Equal(b.SubmittedAt) {
		return a.ID > b.ID
	}
	return a.SubmittedAt.After(b.SubmittedAt)
}

func summarizeMergeability(pr domain.PullRequest, _ []domain.PullRequestReviewThread) PRMergeabilitySummary {
	return PRMergeabilitySummary{
		State:   mergeabilityOrUnknown(pr.Mergeability),
		Reasons: mergeabilityReasons(pr),
		PRURL:   firstNonEmpty(pr.HTMLURL, pr.URL),
	}
}

func mergeabilityReasons(pr domain.PullRequest) []string {
	if pr.Merged || pr.Closed {
		return nil
	}
	if pr.Mergeability != domain.MergeConflicting && pr.Mergeability != domain.MergeBlocked && pr.Mergeability != domain.MergeUnstable {
		return nil
	}
	reasons := map[string]bool{}
	add := func(reason string) {
		if reason != "" {
			reasons[reason] = true
		}
	}
	if pr.Mergeability == domain.MergeConflicting || containsAny(pr.ProviderMergeable, "conflict", "dirty") || containsAny(pr.ProviderMergeStateStatus, "conflict", "dirty") {
		add("conflicts")
	}
	if containsAny(pr.ProviderMergeStateStatus, "behind") {
		add("behind_base")
	}
	if pr.Draft {
		add("draft")
	}
	if pr.CI == domain.CIFailing {
		add("ci_failing")
	}
	if pr.Review == domain.ReviewChangesRequest {
		add("changes_requested")
	}
	if pr.Review == domain.ReviewRequired {
		add("review_required")
	}
	if pr.Mergeability == domain.MergeBlocked && len(reasons) == 0 {
		add("blocked_by_provider")
	}
	if pr.Mergeability == domain.MergeUnstable && len(reasons) == 0 {
		add("blocked_by_provider")
	}
	out := make([]string, 0, len(reasons))
	for reason := range reasons {
		out = append(out, reason)
	}
	sort.Strings(out)
	return out
}

func containsAny(s string, needles ...string) bool {
	s = strings.ToLower(s)
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func sortPRSummaries(prs []PRSummary) {
	sort.SliceStable(prs, func(i, j int) bool {
		ia, ja := prSummaryActive(prs[i]), prSummaryActive(prs[j])
		if ia != ja {
			return ia
		}
		return prs[i].UpdatedAt.After(prs[j].UpdatedAt)
	})
}

func prSummaryActive(pr PRSummary) bool {
	return pr.State != domain.PRStateMerged && pr.State != domain.PRStateClosed
}

func pullRequestState(pr domain.PullRequest) domain.PRState {
	switch {
	case pr.Merged:
		return domain.PRStateMerged
	case pr.Closed:
		return domain.PRStateClosed
	case pr.Draft:
		return domain.PRStateDraft
	default:
		return domain.PRStateOpen
	}
}

func ciOrUnknown(state domain.CIState) domain.CIState {
	if state == "" {
		return domain.CIUnknown
	}
	return state
}

func reviewOrNone(decision domain.ReviewDecision) domain.ReviewDecision {
	if decision == "" {
		return domain.ReviewNone
	}
	return decision
}

func mergeabilityOrUnknown(state domain.Mergeability) domain.Mergeability {
	if state == "" {
		return domain.MergeUnknown
	}
	return state
}
