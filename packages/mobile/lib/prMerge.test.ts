import { describe, expect, it } from "vitest";
import type { SessionPRSummary } from "./api";
import { mergeReadiness } from "./prMerge";

const ready = (): Parameters<typeof mergeReadiness>[0] => ({
	state: "open",
	url: "https://github.com/acme/repo/pull/12",
	headSha: "a".repeat(40),
	ci: { state: "passing", failingChecks: [] },
	mergeability: { state: "mergeable", reasons: [] },
	review: { decision: "approved", hasUnresolvedHumanComments: false, unresolvedBy: [] } as SessionPRSummary["review"],
});

describe("merge readiness", () => {
	it("allows an open, green, mergeable, approved PR like desktop", () => {
		expect(mergeReadiness(ready()).canMerge).toBe(true);
		expect(mergeReadiness({ ...ready(), review: { ...ready().review, decision: "none" } }).canMerge).toBe(true);
	});

	it("blocks with desktop's reason", () => {
		expect(mergeReadiness({ ...ready(), review: { ...ready().review, hasUnresolvedHumanComments: true } })).toMatchObject({ canMerge: false, reason: expect.stringContaining("Unresolved review comments") });
		expect(mergeReadiness({ ...ready(), review: { ...ready().review, decision: "changes_requested" } })).toMatchObject({ canMerge: false, reason: expect.stringContaining("required review") });
		expect(mergeReadiness({ ...ready(), ci: { state: "failing", failingChecks: [] } })).toMatchObject({ canMerge: false, reason: expect.stringContaining("Failing checks") });
		expect(mergeReadiness({ ...ready(), mergeability: { state: "conflicting", reasons: [] } })).toMatchObject({ canMerge: false, label: "Conflict" });
		expect(mergeReadiness({ ...ready(), ci: { state: "pending", failingChecks: [] } })).toMatchObject({ canMerge: false, label: "Checking" });
	});

	it("colors states by meaning, like the rest of the app", () => {
		expect(mergeReadiness(ready()).tone).toBe("green");
		expect(mergeReadiness({ ...ready(), mergeability: { state: "conflicting", reasons: [] } }).tone).toBe("red");
		expect(mergeReadiness({ ...ready(), review: { ...ready().review, hasUnresolvedHumanComments: true } }).tone).toBe("amber");
	});

	it("never merges without the head commit the merge is fenced to", () => {
		expect(mergeReadiness({ ...ready(), headSha: undefined }).canMerge).toBe(false);
	});

	it("reports merged and closed PRs without a merge action", () => {
		expect(mergeReadiness({ ...ready(), state: "merged", targetBranch: "main" })).toEqual({ canMerge: false, tone: "purple", icon: "git-merge", label: "Merged", reason: "This pull request was merged into main." });
		expect(mergeReadiness({ ...ready(), state: "closed" })).toMatchObject({ canMerge: false, tone: "muted", label: "Closed" });
	});
});
