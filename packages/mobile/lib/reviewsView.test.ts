import { describe, expect, it } from "vitest";
import {
	commentLocation,
	prReviewEntries,
	reviewIsRunning,
	reviewRunActionLabel,
	reviewRunDisabled,
	reviewStateFor,
	reviewStateLabel,
	reviewVerdictLabel,
	reviewsSummaryLine,
	unresolvedCommentCount,
	type ReviewedPR,
} from "./reviewsView";

// Shapes match SessionPRReviewSummary / SessionPRReviewEntry /
// SessionPRUnresolvedReviewer in backend/internal/httpd/controllers/dto.go —
// the response GET /sessions/{id}/pr actually returns.
const pr = (review: ReviewedPR["review"], author = "octocat"): ReviewedPR => ({ author, review });

describe("prReviewEntries", () => {
	it("returns nothing for a PR nobody has reviewed", () => {
		expect(prReviewEntries(undefined)).toEqual([]);
		expect(prReviewEntries({})).toEqual([]);
		expect(prReviewEntries(pr({ decision: "none", hasUnresolvedHumanComments: false }))).toEqual([]);
	});

	it("pairs a reviewer's summary with their open and resolved comments", () => {
		const [entry] = prReviewEntries(
			pr({
				decision: "changes_requested",
				hasUnresolvedHumanComments: true,
				reviews: [
					{
						reviewerId: "prateek",
						verdict: "changes_requested",
						body: "The toolbar density change needs a second look.",
						reviewUrl: "https://github.com/o/r/pull/318#pullrequestreview-3101",
						submittedAt: "2026-09-19T10:00:00Z",
					},
				],
				unresolvedBy: [
					{
						reviewerId: "prateek",
						count: 2,
						links: [
							{ file: "lib/TerminalPane.tsx", line: 84, body: "Keep the label on one line." },
							{ file: "lib/styles.css", line: 219, body: "This token is too large." },
						],
					},
				],
				resolvedBy: [
					{ reviewerId: "prateek", count: 1, links: [{ file: "lib/TerminalPane.tsx", line: 62, body: "Fixed." }] },
				],
			}),
		);
		expect(entry.reviewerId).toBe("prateek");
		expect(entry.verdict).toBe("changes_requested");
		expect(entry.body).toBe("The toolbar density change needs a second look.");
		expect(entry.comments.map(commentLocation)).toEqual(["lib/TerminalPane.tsx:84", "lib/styles.css:219"]);
		expect(entry.resolvedComments.map((c) => c.resolved)).toEqual([true]);
	});

	// GitHub records an author's replies on their own PR as reviews. Desktop
	// drops them too (externalReviewActorMatchesPRAuthor).
	it("drops the PR author's own review regardless of case or a leading @", () => {
		const entries = prReviewEntries(
			pr(
				{
					reviews: [
						{ reviewerId: "@OctoCat", verdict: "none", body: "bumping this", submittedAt: "2026-09-19T10:00:00Z" },
						{ reviewerId: "prateek", verdict: "approved", body: "lgtm", submittedAt: "2026-09-19T09:00:00Z" },
					],
					unresolvedBy: [{ reviewerId: "octocat", count: 1, links: [{ file: "a.ts", line: 1, body: "note to self" }] }],
				},
				"octocat",
			),
		);
		expect(entries.map((e) => e.reviewerId)).toEqual(["prateek"]);
	});

	// Without this the "N unresolved comments" the PR card promises has nothing
	// behind it on this screen.
	it("keeps a reviewer who left comments but never submitted a review", () => {
		const entries = prReviewEntries(
			pr({
				unresolvedBy: [{ reviewerId: "aditi", count: 1, links: [{ file: "a.ts", line: 4, body: "spacing" }] }],
			}),
		);
		expect(entries).toHaveLength(1);
		expect(entries[0].verdict).toBe("none");
		expect(entries[0].submittedAt).toBe("");
		expect(entries[0].comments).toHaveLength(1);
	});

	it("does not invent a reviewer from an empty comment list", () => {
		expect(prReviewEntries(pr({ unresolvedBy: [{ reviewerId: "aditi", count: 0, links: [] }] }))).toEqual([]);
	});

	it("orders newest first and puts comment-only reviewers last", () => {
		const entries = prReviewEntries(
			pr({
				reviews: [
					{ reviewerId: "old", verdict: "approved", submittedAt: "2026-09-19T08:00:00Z" },
					{ reviewerId: "new", verdict: "changes_requested", submittedAt: "2026-09-19T12:00:00Z" },
				],
				unresolvedBy: [{ reviewerId: "commenter", count: 1, links: [{ file: "a.ts", line: 1, body: "x" }] }],
			}),
		);
		expect(entries.map((e) => e.reviewerId)).toEqual(["new", "old", "commenter"]);
	});

	it("gives two reviewers' comments distinct ids when the daemon sends no urls", () => {
		const entries = prReviewEntries(
			pr({
				unresolvedBy: [
					{ reviewerId: "a", count: 1, links: [{ file: "x.ts", line: 1, body: "one" }] },
					{ reviewerId: "b", count: 1, links: [{ file: "x.ts", line: 1, body: "two" }] },
				],
			}),
		);
		const ids = entries.flatMap((e) => e.comments.map((c) => c.id));
		expect(new Set(ids).size).toBe(ids.length);
	});

	it("prefers the comment url as its identity when there is one", () => {
		const [entry] = prReviewEntries(
			pr({
				unresolvedBy: [
					{ reviewerId: "a", count: 1, links: [{ url: "https://github.com/o/r/pull/1#discussion_r1", body: "one" }] },
				],
			}),
		);
		expect(entry.comments[0].id).toBe("https://github.com/o/r/pull/1#discussion_r1");
	});

	it("treats a bot reviewer as a bot and a missing flag as human", () => {
		const entries = prReviewEntries(
			pr({
				reviews: [
					{ reviewerId: "codex", verdict: "approved", isBot: true, submittedAt: "2026-09-19T10:00:00Z" },
					{ reviewerId: "prateek", verdict: "approved", submittedAt: "2026-09-19T09:00:00Z" },
				],
			}),
		);
		expect(entries.map((e) => e.isBot)).toEqual([true, false]);
	});

	it("drops a line number the daemon could not determine", () => {
		const [entry] = prReviewEntries(
			pr({ unresolvedBy: [{ reviewerId: "a", count: 1, links: [{ file: "x.ts", line: 0, body: "file-level note" }] }] }),
		);
		expect(entry.comments[0].line).toBeUndefined();
		expect(commentLocation(entry.comments[0])).toBe("x.ts");
	});
});

describe("reviewVerdictLabel", () => {
	// The words are desktop's githubVerdict; a verdict with no decision is
	// "Commented", which is what GitHub means by an empty verdict.
	it("says what desktop says", () => {
		expect(reviewVerdictLabel("approved")).toEqual({ text: "Approved", tone: "success" });
		expect(reviewVerdictLabel("changes_requested")).toEqual({ text: "Changes requested", tone: "error" });
		expect(reviewVerdictLabel("review_required")).toEqual({ text: "Not run", tone: "neutral" });
		expect(reviewVerdictLabel("none")).toEqual({ text: "Commented", tone: "neutral" });
	});
});

describe("reviewsSummaryLine", () => {
	it("stays silent when nobody has reviewed", () => {
		expect(reviewsSummaryLine([])).toBeNull();
	});

	it("counts reviewers and open comments, singular and plural", () => {
		const one = prReviewEntries(
			pr({ unresolvedBy: [{ reviewerId: "a", count: 1, links: [{ file: "x.ts", line: 1, body: "one" }] }] }),
		);
		expect(reviewsSummaryLine(one)).toBe("1 reviewer  ·  1 unresolved comment");
		const many = prReviewEntries(
			pr({
				reviews: [{ reviewerId: "b", verdict: "approved", submittedAt: "2026-09-19T10:00:00Z" }],
				unresolvedBy: [
					{ reviewerId: "a", count: 2, links: [{ file: "x.ts", line: 1, body: "one" }, { file: "y.ts", line: 2, body: "two" }] },
				],
			}),
		);
		expect(reviewsSummaryLine(many)).toBe("2 reviewers  ·  2 unresolved comments");
		expect(unresolvedCommentCount(many)).toBe(2);
	});

	it("omits the comment clause when every thread is resolved", () => {
		const entries = prReviewEntries(
			pr({
				reviews: [{ reviewerId: "a", verdict: "approved", submittedAt: "2026-09-19T10:00:00Z" }],
				resolvedBy: [{ reviewerId: "a", count: 1, links: [{ file: "x.ts", line: 1, body: "done" }] }],
			}),
		);
		expect(reviewsSummaryLine(entries)).toBe("1 reviewer");
	});
});

// Shapes mirror PRReviewState in backend/internal/review/planner.go.
const URL_A = "https://github.com/o/r/pull/2";

describe("reviewStateFor", () => {
	it("picks this PR's state out of the session's", () => {
		const states = [
			{ prUrl: "https://github.com/o/r/pull/1", status: "running" },
			{ prUrl: URL_A, status: "needs_review" },
		];
		expect(reviewStateFor(states, URL_A)?.status).toBe("needs_review");
	});

	// A PR whose url we do not know yet must not borrow another PR's state and
	// offer "Stop review" for a pass running somewhere else.
	it("returns nothing when the PR url is unknown", () => {
		expect(reviewStateFor([{ prUrl: URL_A, status: "running" }], undefined)).toBeUndefined();
		expect(reviewStateFor([{ prUrl: URL_A, status: "running" }], "https://github.com/o/r/pull/9")).toBeUndefined();
	});
});

describe("review run control", () => {
	it("is dead for a PR the daemon says cannot be reviewed", () => {
		expect(reviewRunDisabled({ prUrl: URL_A, status: "ineligible" }, false)).toBe(true);
	});

	it("is dead while a tap is in flight, and when the daemon has said nothing", () => {
		expect(reviewRunDisabled({ prUrl: URL_A, status: "needs_review" }, true)).toBe(true);
		expect(reviewRunDisabled(undefined, false)).toBe(true);
	});

	it("is live for a reviewable PR", () => {
		expect(reviewRunDisabled({ prUrl: URL_A, status: "needs_review" }, false)).toBe(false);
		expect(reviewRunDisabled({ prUrl: URL_A, status: "up_to_date" }, false)).toBe(false);
	});

	// The wordings are desktop's reviewSessionRunAction.
	it("says what the next pass would do", () => {
		expect(reviewRunActionLabel(undefined, false)).toBe("Run review");
		expect(reviewRunActionLabel({ status: "needs_review" }, false)).toBe("Re-review PR");
		expect(reviewRunActionLabel({ status: "changes_requested" }, false)).toBe("Re-run review");
		expect(reviewRunActionLabel({ status: "up_to_date", latestRun: { id: "r1" } }, false)).toBe("Re-run review");
		expect(reviewRunActionLabel({ status: "running" }, false)).toBe("Reviewing…");
		expect(reviewRunActionLabel({ status: "needs_review" }, true)).toBe("Reviewing…");
	});

	it("knows when a pass is in flight", () => {
		expect(reviewIsRunning({ status: "running" })).toBe(true);
		expect(reviewIsRunning({ status: "needs_review" })).toBe(false);
		expect(reviewIsRunning(undefined)).toBe(false);
	});
});

describe("reviewStateLabel", () => {
	it("stays silent when the daemon has no state for this PR", () => {
		expect(reviewStateLabel(undefined)).toBeNull();
		expect(reviewStateLabel({ status: "something-new" })).toBeNull();
	});

	it("names each state the planner can report", () => {
		expect(reviewStateLabel({ status: "running" })).toEqual({ text: "Review in progress", tone: "neutral" });
		expect(reviewStateLabel({ status: "needs_review" })?.tone).toBe("warning");
		expect(reviewStateLabel({ status: "changes_requested" })?.tone).toBe("error");
		expect(reviewStateLabel({ status: "up_to_date" })?.tone).toBe("success");
		expect(reviewStateLabel({ status: "ineligible" })?.tone).toBe("passive");
	});
});
