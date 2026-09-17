package review

import (
	"sort"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

// StateStatus is the per-PR review planning state.
type StateStatus = contract.AOReviewState

const (
	// ReviewStateNeedsReview means an eligible PR has no current AO approval or running pass.
	ReviewStateNeedsReview = contract.AOReviewNeedsReview
	// ReviewStateRunning means a review run is already active for the PR's current head.
	ReviewStateRunning = contract.AOReviewRunning
	// ReviewStateUpToDate means AO approved the PR's current head.
	ReviewStateUpToDate = contract.AOReviewUpToDate
	// ReviewStateChangesRequested means AO requested changes on the PR's current head.
	ReviewStateChangesRequested = contract.AOReviewChangesRequested
	// ReviewStateIneligible means the PR is closed, merged, or missing required facts.
	ReviewStateIneligible = contract.AOReviewIneligible
)

// PRReviewState is one PR-scoped review decision for a worker session.
type PRReviewState struct {
	PRURL       string            `json:"prUrl"`
	PRNumber    int               `json:"prNumber"`
	Title       string            `json:"title"`
	TargetSHA   string            `json:"targetSha"`
	Status      StateStatus       `json:"status" enum:"needs_review,running,up_to_date,changes_requested,ineligible"`
	LatestRun   *domain.ReviewRun `json:"latestRun,omitempty"`
	PreviousRun *domain.ReviewRun `json:"previousRun,omitempty"`
}

// Plan computes per-PR review work from the currently observed PRs and existing
// review runs. It is pure so the trigger path and API list path share exactly
// the same eligibility/status rules.
func Plan(prs []domain.PullRequest, runs []domain.ReviewRun) []PRReviewState {
	latest := latestRunsByPRAndSHA(runs)
	reviews := make([]PRReviewState, 0, len(prs))
	for _, pr := range prs {
		review := PRReviewState{
			PRURL:     pr.URL,
			PRNumber:  pr.Number,
			Title:     pr.Title,
			TargetSHA: pr.HeadSHA,
			Status:    ReviewStateNeedsReview,
		}
		if run, ok := latestCompletedRunForOtherSHA(runs, review.PRURL, review.TargetSHA); ok {
			review.PreviousRun = &run
		}
		if pr.URL == "" || pr.HeadSHA == "" || pr.Merged || pr.Closed {
			review.Status = ReviewStateIneligible
			if run, ok := latest[review.PRURL+"\x00"+review.TargetSHA]; ok {
				review.LatestRun = &run
			}
			reviews = append(reviews, review)
			continue
		}
		if run, ok := latest[review.PRURL+"\x00"+review.TargetSHA]; ok {
			review.LatestRun = &run
			switch {
			case run.Status == domain.ReviewRunRunning:
				review.Status = ReviewStateRunning
			case run.Verdict == domain.VerdictApproved:
				review.Status = ReviewStateUpToDate
			case run.Verdict == domain.VerdictChangesRequested:
				review.Status = ReviewStateChangesRequested
			case run.Status == domain.ReviewRunFailed || run.Status == domain.ReviewRunCancelled:
				review.Status = ReviewStateNeedsReview
			default:
				review.Status = ReviewStateNeedsReview
			}
		}
		reviews = append(reviews, review)
	}
	sort.SliceStable(reviews, func(i, j int) bool {
		if reviews[i].PRNumber != reviews[j].PRNumber {
			return reviews[i].PRNumber < reviews[j].PRNumber
		}
		return reviews[i].PRURL < reviews[j].PRURL
	})
	return reviews
}

func latestCompletedRunForOtherSHA(runs []domain.ReviewRun, prURL, targetSHA string) (domain.ReviewRun, bool) {
	if prURL == "" || targetSHA == "" {
		return domain.ReviewRun{}, false
	}
	var latest domain.ReviewRun
	found := false
	for _, run := range runs {
		if run.PRURL != prURL || run.TargetSHA == "" || run.TargetSHA == targetSHA {
			continue
		}
		if run.Status != domain.ReviewRunComplete && run.Status != domain.ReviewRunDelivered {
			continue
		}
		if run.Verdict != domain.VerdictApproved && run.Verdict != domain.VerdictChangesRequested {
			continue
		}
		if !found || run.CreatedAt.After(latest.CreatedAt) {
			latest = run
			found = true
		}
	}
	return latest, found
}

func latestRunsByPRAndSHA(runs []domain.ReviewRun) map[string]domain.ReviewRun {
	latest := make(map[string]domain.ReviewRun)
	for _, run := range runs {
		if run.PRURL == "" || run.TargetSHA == "" {
			continue
		}
		key := run.PRURL + "\x00" + run.TargetSHA
		if existing, ok := latest[key]; !ok || run.CreatedAt.After(existing.CreatedAt) {
			latest[key] = run
		}
	}
	return latest
}

// PlanAggregate computes per-PR review state aggregating results from all configured reviewers.
// A PR is considered fully reviewed only when ALL configured reviewers have approved the current head.
// Any changes_requested verdict keeps the PR unresolved. Reviewer failure/timeout is surfaced as incomplete.
func PlanAggregate(prs []domain.PullRequest, runs []domain.ReviewRun) []PRReviewState {
	// Group runs by PR+SHA+harness to get the latest run for each reviewer
	runsByPRAndSHAAndHarness := make(map[string]domain.ReviewRun)
	for _, run := range runs {
		if run.PRURL == "" || run.TargetSHA == "" || run.Harness == "" {
			continue
		}
		key := run.PRURL + "\x00" + run.TargetSHA + "\x00" + string(run.Harness)
		if existing, ok := runsByPRAndSHAAndHarness[key]; !ok || run.CreatedAt.After(existing.CreatedAt) {
			runsByPRAndSHAAndHarness[key] = run
		}
	}

	// Group runs by PR+SHA to get all reviewer verdicts for each commit
	runsByPRAndSHA := make(map[string][]domain.ReviewRun)
	for _, run := range runsByPRAndSHAAndHarness {
		key := run.PRURL + "\x00" + run.TargetSHA
		runsByPRAndSHA[key] = append(runsByPRAndSHA[key], run)
	}

	reviews := make([]PRReviewState, 0, len(prs))
	for _, pr := range prs {
		review := PRReviewState{
			PRURL:     pr.URL,
			PRNumber:  pr.Number,
			Title:     pr.Title,
			TargetSHA: pr.HeadSHA,
			Status:    ReviewStateNeedsReview,
		}
		if run, ok := latestCompletedRunForOtherSHA(runs, review.PRURL, review.TargetSHA); ok {
			review.PreviousRun = &run
		}
		if pr.URL == "" || pr.HeadSHA == "" || pr.Merged || pr.Closed {
			review.Status = ReviewStateIneligible
			// Still show the latest run if available
			if runs, ok := runsByPRAndSHA[review.PRURL+"\x00"+review.TargetSHA]; ok && len(runs) > 0 {
				// Pick the most recent run across all reviewers
				latest := runs[0]
				for _, run := range runs {
					if run.CreatedAt.After(latest.CreatedAt) {
						latest = run
					}
				}
				review.LatestRun = &latest
			}
			reviews = append(reviews, review)
			continue
		}

		// Get all reviewer runs for this PR head
		runsForHead, ok := runsByPRAndSHA[review.PRURL+"\x00"+review.TargetSHA]
		if !ok || len(runsForHead) == 0 {
			review.Status = ReviewStateNeedsReview
			reviews = append(reviews, review)
			continue
		}

		// Aggregate verdicts across all reviewers
		aggregateStatus := aggregateReviewStatus(runsForHead)
		review.Status = aggregateStatus

		// Set LatestRun to the most recent run across all reviewers
		latest := runsForHead[0]
		for _, run := range runsForHead {
			if run.CreatedAt.After(latest.CreatedAt) {
				latest = run
			}
		}
		review.LatestRun = &latest

		reviews = append(reviews, review)
	}

	sort.SliceStable(reviews, func(i, j int) bool {
		if reviews[i].PRNumber != reviews[j].PRNumber {
			return reviews[i].PRNumber < reviews[j].PRNumber
		}
		return reviews[i].PRURL < reviews[j].PRURL
	})
	return reviews
}

// aggregateReviewStatus computes the aggregate status across multiple reviewer runs for the same PR head.
// Rules:
// - Any running reviewer -> Running
// - Any changes_requested -> ChangesRequested
// - Any failed/cancelled reviewer -> NeedsReview (incomplete)
// - All reviewers approved -> UpToDate
// - Mixed approved/no verdict -> NeedsReview (incomplete)
func aggregateReviewStatus(runs []domain.ReviewRun) StateStatus {
	if len(runs) == 0 {
		return ReviewStateNeedsReview
	}

	// Check for any running reviewer
	for _, run := range runs {
		if run.Status == domain.ReviewRunRunning {
			return ReviewStateRunning
		}
	}

	// Check for any changes_requested
	for _, run := range runs {
		if run.Verdict == domain.VerdictChangesRequested {
			return ReviewStateChangesRequested
		}
	}

	// Check for any failed/cancelled reviewers (incomplete)
	for _, run := range runs {
		if run.Status == domain.ReviewRunFailed || run.Status == domain.ReviewRunCancelled {
			return ReviewStateNeedsReview
		}
	}

	// All reviewers must have approved verdict
	for _, run := range runs {
		if run.Verdict != domain.VerdictApproved {
			return ReviewStateNeedsReview
		}
	}

	return ReviewStateUpToDate
}
