export const PULL_REQUEST_STATES = ["draft", "open", "merged", "closed"] as const;
export type PullRequestState = (typeof PULL_REQUEST_STATES)[number];

export const CI_STATES = ["unknown", "pending", "passing", "failing"] as const;
export type CIState = (typeof CI_STATES)[number];

export const PULL_REQUEST_CHECK_STATUSES = [
	"unknown",
	"queued",
	"in_progress",
	"passed",
	"failed",
	"skipped",
	"cancelled",
] as const;
export type PullRequestCheckStatus = (typeof PULL_REQUEST_CHECK_STATUSES)[number];

export const REVIEW_DECISIONS = ["none", "approved", "changes_requested", "review_required"] as const;
export type ReviewDecision = (typeof REVIEW_DECISIONS)[number];

export const MERGEABILITY_STATES = ["unknown", "mergeable", "conflicting", "blocked", "unstable"] as const;
export type MergeabilityState = (typeof MERGEABILITY_STATES)[number];

export const AO_REVIEW_RUN_STATUSES = ["running", "complete", "delivered", "failed", "cancelled"] as const;
export type AOReviewRunStatus = (typeof AO_REVIEW_RUN_STATUSES)[number];

// The empty value is AO's existing stored "no verdict yet" vocabulary.
export const AO_REVIEW_VERDICTS = ["", "approved", "changes_requested"] as const;
export type AOReviewVerdict = (typeof AO_REVIEW_VERDICTS)[number];

export const AO_REVIEW_STATES = [
	"needs_review",
	"running",
	"up_to_date",
	"changes_requested",
	"ineligible",
] as const;
export type AOReviewState = (typeof AO_REVIEW_STATES)[number];

export type PullRequestFailingCheck = {
	name: string;
	status: PullRequestCheckStatus;
	conclusion: string;
	url?: string;
};

export type PullRequestCISummary = {
	state: CIState;
	failingChecks: PullRequestFailingCheck[];
};

export type PullRequestReviewCommentLink = {
	url?: string;
	file?: string;
	line?: number;
	body?: string;
	autoInjectReview: boolean;
};

export type PullRequestUnresolvedReviewer = {
	reviewerId: string;
	count: number;
	links: PullRequestReviewCommentLink[];
	reviewUrl?: string;
	isBot?: boolean;
};

export type PullRequestSubmittedReview = {
	reviewerId: string;
	verdict: ReviewDecision;
	body?: string;
	reviewUrl?: string;
	submittedAt: string;
	isBot?: boolean;
	autoInjectReview: boolean;
};

export type PullRequestReviewSummary = {
	decision: ReviewDecision;
	hasUnresolvedHumanComments: boolean;
	unresolvedBy: PullRequestUnresolvedReviewer[];
	resolvedBy?: PullRequestUnresolvedReviewer[];
	reviews: PullRequestSubmittedReview[];
};

export type PullRequestConflictFile = {
	path: string;
	url?: string;
};

export type PullRequestMergeabilitySummary = {
	state: MergeabilityState;
	reasons: string[];
	pullRequestUrl: string;
	conflictFiles: PullRequestConflictFile[];
};

export type PullRequestSummary = {
	url: string;
	htmlUrl?: string;
	number: number;
	title: string;
	state: PullRequestState;
	provider: string;
	repository: string;
	author: string;
	authorAvatarUrl?: string;
	sourceBranch: string;
	targetBranch: string;
	headSha: string;
	additions: number;
	deletions: number;
	changedFiles: number;
	ci: PullRequestCISummary;
	review: PullRequestReviewSummary;
	mergeability: PullRequestMergeabilitySummary;
	stateChangedAt?: string;
	createdAt?: string;
	updatedAt: string;
	observedAt: string;
	ciObservedAt: string;
	reviewObservedAt: string;
};

export type AOReviewRun = {
	id: string;
	reviewId: string;
	sessionId: string;
	batchId: string;
	harness: string;
	pullRequestUrl: string;
	targetSha: string;
	status: AOReviewRunStatus;
	verdict: AOReviewVerdict;
	body: string;
	providerReviewId: string;
	createdAt: string;
	deliveredAt?: string;
	autoInjectReview: boolean;
};

export type AOPullRequestReviewState = {
	pullRequestUrl: string;
	pullRequestNumber: number;
	title: string;
	targetSha: string;
	status: AOReviewState;
	/** The head SHA of the newest completed run that does not match targetSha. */
	staleTargetSha?: string;
	latestRun?: AOReviewRun;
	previousRun?: AOReviewRun;
};

export type SessionPullRequests = {
	sessionId: string;
	pullRequests: PullRequestSummary[];
};

export type SessionReviewState = {
	sessionId: string;
	reviewerHandleId?: string;
	reviewerHarness?: string;
	reviews: AOPullRequestReviewState[];
	runs: AOReviewRun[];
};
