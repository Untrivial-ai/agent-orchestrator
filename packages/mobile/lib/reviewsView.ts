// Presentation rules for the reviews left on a pull request. Pure — no React
// Native or Expo imports — so the grouping, wording and ordering are
// unit-testable, the same split as prView.ts / pushStatus.ts.
//
// Everything here is derived from GET /sessions/{id}/pr, which the PRs tab
// already fetches (see usePRSummaries). The daemon has always sent the review
// bodies and the inline comments on that response; the client type simply did
// not model them, so they were parsed away on arrival and the phone could say
// only that a PR *had* unresolved comments.
//
// Desktop splits the same data into "Agent reviews" and "External reviews"
// (SessionInspector.tsx MergedReviewsSection). That split needs
// GET /sessions/{id}/reviews to know which GitHub review id AO's own reviewer
// produced, and mobile does not call it. AO's reviewer submits through GitHub
// like any other reviewer, so its pass is already in `review.reviews[]` — one
// list keyed by reviewer loses the label, not the content.
import type { Tone } from "./prView";

export type ReviewVerdict = "approved" | "changes_requested" | "review_required" | "none";

/** One inline comment on a line of the diff. */
export type ReviewComment = {
	/** Stable across renders; the daemon does not always send a url. */
	id: string;
	file?: string;
	line?: number;
	body: string;
	url?: string;
	resolved: boolean;
};

/** Everything one reviewer left on this PR: their verdict, their summary, their comments. */
export type ReviewEntry = {
	id: string;
	reviewerId: string;
	isBot: boolean;
	verdict: ReviewVerdict;
	/** The summary body submitted with the verdict. Empty when they only left inline comments. */
	body: string;
	/** ISO timestamp, or "" for a reviewer who left inline comments but never submitted a review. */
	submittedAt: string;
	reviewUrl?: string;
	comments: ReviewComment[];
	resolvedComments: ReviewComment[];
};

/** Just the parts of the rich summary this module reads, kept structural so it stays pure. */
export type ReviewedPR = {
	author?: string;
	review?: {
		decision?: string;
		hasUnresolvedHumanComments?: boolean;
		reviews?: {
			reviewerId?: string;
			verdict?: string;
			body?: string;
			reviewUrl?: string;
			submittedAt?: string;
			isBot?: boolean;
		}[];
		unresolvedBy?: ReviewerComments[];
		resolvedBy?: ReviewerComments[];
	};
};

type ReviewerComments = {
	reviewerId?: string;
	count?: number;
	reviewUrl?: string;
	isBot?: boolean;
	links?: { url?: string; file?: string; line?: number; body?: string }[];
};

/**
 * GitHub records a review actor with whatever casing and "@" the caller used.
 * Desktop normalises the same way (`normalizeReviewerId`) before comparing an
 * actor with the PR author.
 */
function normalizeReviewerId(value: string | undefined): string {
	return value?.trim().replace(/^@+/, "").toLowerCase() ?? "";
}

function isPRAuthor(reviewerId: string | undefined, author: string | undefined): boolean {
	const actor = normalizeReviewerId(reviewerId);
	return actor !== "" && actor === normalizeReviewerId(author);
}

function verdictOf(value: string | undefined): ReviewVerdict {
	switch (value) {
		case "approved":
		case "changes_requested":
		case "review_required":
			return value;
		default:
			return "none";
	}
}

/**
 * What the badge beside a reviewer's name says, and how loud it is.
 *
 * The words are desktop's (`githubVerdict` in SessionInspector.tsx) so the same
 * verdict reads the same on both surfaces — including "Commented" for a review
 * carrying no decision, which is what GitHub means by an empty verdict.
 */
export function reviewVerdictLabel(verdict: ReviewVerdict): { text: string; tone: Tone } {
	switch (verdict) {
		case "approved":
			return { text: "Approved", tone: "success" };
		case "changes_requested":
			return { text: "Changes requested", tone: "error" };
		case "review_required":
			return { text: "Not run", tone: "neutral" };
		default:
			return { text: "Commented", tone: "neutral" };
	}
}

function commentsFrom(entry: ReviewerComments, resolved: boolean): ReviewComment[] {
	return (entry.links ?? []).map((link, index) => ({
		// The url is the only stable identity the daemon offers and it is
		// optional, so fall back to the reviewer and position rather than to the
		// array index alone — two reviewers' comments share a list on screen.
		id: link.url || `${normalizeReviewerId(entry.reviewerId)}:${resolved ? "r" : "u"}:${index}`,
		file: link.file || undefined,
		line: link.line && link.line > 0 ? link.line : undefined,
		body: (link.body ?? "").trim(),
		url: link.url || entry.reviewUrl || undefined,
		resolved,
	}));
}

function collectBy(list: ReviewerComments[] | undefined, author: string | undefined, resolved: boolean) {
	const byReviewer = new Map<string, ReviewComment[]>();
	for (const entry of list ?? []) {
		if (isPRAuthor(entry.reviewerId, author)) continue;
		const key = normalizeReviewerId(entry.reviewerId);
		if (key === "") continue;
		const comments = commentsFrom(entry, resolved);
		if (comments.length === 0) continue;
		byReviewer.set(key, [...(byReviewer.get(key) ?? []), ...comments]);
	}
	return byReviewer;
}

/**
 * Every reviewer's pass on this PR, newest first.
 *
 * A reviewer who left inline comments but never submitted a summary review
 * still gets an entry, because otherwise the "N unresolved comments" the card
 * promises has nothing behind it on this screen. Desktop does the same.
 *
 * Reviews by the PR's own author are dropped: GitHub records an author's
 * replies on their own PR as reviews, and reading your own words back under a
 * "Reviews" heading is noise.
 */
export function prReviewEntries(pr: ReviewedPR | undefined): ReviewEntry[] {
	if (!pr?.review) return [];
	const author = pr.author;
	const unresolved = collectBy(pr.review.unresolvedBy, author, false);
	const resolved = collectBy(pr.review.resolvedBy, author, true);
	const seen = new Set<string>();
	const entries: ReviewEntry[] = [];

	for (const review of pr.review.reviews ?? []) {
		if (isPRAuthor(review.reviewerId, author)) continue;
		const key = normalizeReviewerId(review.reviewerId);
		if (key === "") continue;
		seen.add(key);
		entries.push({
			id: review.reviewUrl || `${key}:${review.submittedAt ?? ""}`,
			reviewerId: (review.reviewerId ?? "").trim().replace(/^@+/, ""),
			isBot: review.isBot === true,
			verdict: verdictOf(review.verdict),
			body: (review.body ?? "").trim(),
			submittedAt: review.submittedAt ?? "",
			reviewUrl: review.reviewUrl || undefined,
			comments: unresolved.get(key) ?? [],
			resolvedComments: resolved.get(key) ?? [],
		});
	}

	// Comment-only reviewers, in the order the daemon listed them. They carry no
	// timestamp, so they cannot be sorted with the rest and sit at the end.
	for (const key of [...unresolved.keys(), ...resolved.keys()]) {
		if (seen.has(key)) continue;
		seen.add(key);
		const source =
			(pr.review.unresolvedBy ?? []).find((entry) => normalizeReviewerId(entry.reviewerId) === key) ??
			(pr.review.resolvedBy ?? []).find((entry) => normalizeReviewerId(entry.reviewerId) === key);
		entries.push({
			id: `comments:${key}`,
			reviewerId: (source?.reviewerId ?? key).trim().replace(/^@+/, ""),
			isBot: source?.isBot === true,
			verdict: "none",
			body: "",
			submittedAt: "",
			reviewUrl: source?.reviewUrl || undefined,
			comments: unresolved.get(key) ?? [],
			resolvedComments: resolved.get(key) ?? [],
		});
	}

	// Newest first; a missing timestamp sorts last rather than to the epoch,
	// which would bury a reviewer who only left comments under nothing at all.
	return entries.sort((a, b) => {
		if (a.submittedAt === b.submittedAt) return 0;
		if (!a.submittedAt) return 1;
		if (!b.submittedAt) return -1;
		return b.submittedAt.localeCompare(a.submittedAt);
	});
}

/** How many unresolved inline comments are waiting, across every reviewer. */
export function unresolvedCommentCount(entries: ReviewEntry[]): number {
	return entries.reduce((total, entry) => total + entry.comments.length, 0);
}

/**
 * The one line under the screen title. It names what is on the screen so the
 * header is not just a repeat of the PR number, and stays silent rather than
 * saying "0 reviewers" on a PR nobody has looked at.
 */
export function reviewsSummaryLine(entries: ReviewEntry[]): string | null {
	if (entries.length === 0) return null;
	const reviewers = `${entries.length} ${entries.length === 1 ? "reviewer" : "reviewers"}`;
	const open = unresolvedCommentCount(entries);
	if (open === 0) return reviewers;
	return `${reviewers}  ·  ${open} unresolved ${open === 1 ? "comment" : "comments"}`;
}

/** `file:line`, `file`, or nothing — the label above one inline comment. */
export function commentLocation(comment: ReviewComment): string | null {
	if (!comment.file) return null;
	return comment.line ? `${comment.file}:${comment.line}` : comment.file;
}

// ---- AO's own reviewer: what the controls may do ----------------------------
//
// These mirror frontend/src/renderer/lib/session-reviews.ts so a PR in the same
// state offers the same action, worded the same way, on both surfaces.

/** Just the parts of a review state these rules read. */
export type ReviewState = {
	prUrl?: string;
	status?: string;
	latestRun?: { id?: string; status?: string } | null;
};

/**
 * The review states for PRs that can still be reviewed.
 *
 * Desktop scopes the controls to the session's open and draft PRs, because a
 * merged PR's state is history and must not make "Run review" look available.
 * Here the screen is already one PR, so the scope is that PR's own state.
 */
export function reviewStateFor(states: ReviewState[], prUrl: string | undefined): ReviewState | undefined {
	if (!prUrl) return undefined;
	return states.find((state) => state.prUrl === prUrl);
}

export function reviewIsRunning(state: ReviewState | undefined): boolean {
	return state?.status === "running";
}

/**
 * Whether the run control is dead.
 *
 * `ineligible` is the daemon saying this PR cannot be reviewed at all — merged,
 * closed, or draft-without-a-head — so the button must not be offered. Desktop
 * disables rather than hides it, which also explains *why* nothing happens.
 */
export function reviewRunDisabled(state: ReviewState | undefined, busy: boolean): boolean {
	return busy || state === undefined || state.status === "ineligible";
}

/**
 * The run control's label. The three wordings are desktop's: a PR nobody has
 * reviewed says "Run review", a reviewed PR with a newer commit says
 * "Re-review PR", and one already reviewed at this commit says "Re-run review".
 */
export function reviewRunActionLabel(state: ReviewState | undefined, busy: boolean): string {
	if (busy || state?.status === "running") return "Reviewing…";
	if (state?.status === "needs_review") return "Re-review PR";
	if (state?.status === "changes_requested" || state?.latestRun) return "Re-run review";
	return "Run review";
}

/** One line under the controls saying where AO's own reviewer has got to. */
export function reviewStateLabel(state: ReviewState | undefined): { text: string; tone: Tone } | null {
	switch (state?.status) {
		case "running":
			return { text: "Review in progress", tone: "neutral" };
		case "needs_review":
			return { text: "Latest commit has not been reviewed", tone: "warning" };
		case "changes_requested":
			return { text: "AO review requested changes", tone: "error" };
		case "up_to_date":
			return { text: "Reviewed at the latest commit", tone: "success" };
		case "ineligible":
			return { text: "This pull request cannot be reviewed", tone: "passive" };
		default:
			return null;
	}
}
