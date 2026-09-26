import { describe, expect, it } from "vitest";
import type { DashboardPR, DashboardSession } from "./api";
import {
	collectPRs,
	comparePRs,
	mergeReasonLabel,
	prBlockerLine,
	prListSections,
	prLifecycle,
	prStatusAtoms,
	prSummaryLine,
	prTitle,
} from "./prView";

const pr = (over: Partial<DashboardPR> = {}): DashboardPR => ({
	number: 184,
	url: "https://github.com/o/r/pull/184",
	state: "open",
	...over,
});

const session = (projectId: string, prs: DashboardPR[], id = `${projectId}-1`): DashboardSession =>
	({ id, projectId, prs }) as DashboardSession;

describe("collectPRs", () => {
	// Regression: the key was `${owner}/${repo}#${number}`, but the board endpoint
	// never sends owner or repo — so it collapsed to "/#12" and two projects with
	// a PR #12 dropped one of each other.
	it("keeps the same PR number in two different projects", () => {
		const got = collectPRs([session("alpha", [pr({ number: 12 })]), session("beta", [pr({ number: 12 })])]);
		expect(got.map((x) => x.session.projectId)).toEqual(["alpha", "beta"]);
	});

	// Deduping still has a job: several sessions in one project can point at the
	// same PR, and it should appear once.
	it("collapses the same PR seen from two sessions in one project", () => {
		const got = collectPRs([session("alpha", [pr({ number: 12 })], "alpha-1"), session("alpha", [pr({ number: 12 })], "alpha-2")]);
		expect(got).toHaveLength(1);
	});

	it("skips placeholder PRs with no real number", () => {
		expect(collectPRs([session("alpha", [pr({ number: 0 })])])).toHaveLength(0);
	});

	it("falls back to the single `pr` field when `prs` is empty", () => {
		const s = { id: "a-1", projectId: "alpha", prs: [], pr: pr({ number: 7 }) } as unknown as DashboardSession;
		expect(collectPRs([s]).map((x) => x.pr.number)).toEqual([7]);
	});
});

describe("prTitle", () => {
	it("prefers the PR's own title", () => {
		expect(prTitle(pr({ title: "Fix auth timeouts" }), "session title")).toBe("Fix auth timeouts");
	});

	// The regression this exists for: the list endpoint never sends PR titles, so
	// every card in the tab read "Pull request #N" and looked identical.
	it("falls back to the session title when the daemon sent none", () => {
		expect(prTitle(pr(), "Fix auth timeouts on refresh")).toBe("Fix auth timeouts on refresh");
	});

	it("falls back to the PR number only when there is nothing else", () => {
		expect(prTitle(pr())).toBe("Pull request #184");
		expect(prTitle(pr(), "")).toBe("Pull request #184");
		expect(prTitle(pr(), null)).toBe("Pull request #184");
	});

	it("ignores a blank title rather than rendering an empty card", () => {
		expect(prTitle(pr({ title: "   " }), "from session")).toBe("from session");
	});
});

describe("prLifecycle", () => {
	// mapPR folds the wire's "draft" into state:"open" and keeps isDraft, so the
	// flag is the only surviving signal — and nothing rendered it before.
	it("reports a draft even though its state is open", () => {
		expect(prLifecycle(pr({ state: "open", isDraft: true }))).toBe("draft");
	});

	it("reports merged and closed over draft", () => {
		expect(prLifecycle(pr({ state: "merged", isDraft: true }))).toBe("merged");
		expect(prLifecycle(pr({ state: "closed", isDraft: true }))).toBe("closed");
	});

	it("defaults to open", () => {
		expect(prLifecycle(pr())).toBe("open");
	});
});

describe("mergeReasonLabel", () => {
	it("humanises the reasons desktop knows", () => {
		expect(mergeReasonLabel("behind_base")).toBe("branch behind base");
		expect(mergeReasonLabel("ci_failing")).toBe("CI failing");
		expect(mergeReasonLabel("blocked_by_provider")).toBe("provider blocked");
	});

	it("degrades an unknown reason into readable words", () => {
		expect(mergeReasonLabel("some_new_reason")).toBe("some new reason");
	});
});

describe("prSummaryLine", () => {
	it("says only the outcome for a decided PR", () => {
		// "merged · CI failing" would be noise about a PR nobody can act on.
		expect(prSummaryLine(pr({ state: "merged", ciStatus: "failing" })).text).toBe("Merged");
		expect(prSummaryLine(pr({ state: "closed" })).text).toBe("Closed without merging");
	});

	it("leads with the worst problem", () => {
		const line = prSummaryLine(pr({ ciStatus: "failing", reviewDecision: "changes_requested" }));
		expect(line.text.startsWith("CI failing")).toBe(true);
		expect(line.tone).toBe("error");
	});

	it("never shows more than two problems", () => {
		const line = prSummaryLine(pr({ ciStatus: "failing", reviewDecision: "changes_requested", unresolvedThreads: 1 }));
		expect(line.text.split(" · ")).toHaveLength(2);
	});

	it("calls out a PR that is ready to go", () => {
		const line = prSummaryLine(pr({ reviewDecision: "approved", mergeability: { mergeable: true } }));
		expect(line).toMatchObject({ text: "Ready to merge", tone: "success" });
	});

	it("reports a draft as a draft", () => {
		expect(prSummaryLine(pr({ isDraft: true })).text).toBe("Draft");
	});

	it("falls back through CI and review before plain open", () => {
		expect(prSummaryLine(pr({ ciStatus: "pending" })).text).toBe("CI running");
		expect(prSummaryLine(pr({ reviewDecision: "pending" })).text).toBe("Awaiting review");
		expect(prSummaryLine(pr()).text).toBe("Open");
	});
});

describe("prStatusAtoms", () => {
	it("collapses a decided PR to one atom", () => {
		// "Merged · CI passing" is noise about a PR nobody can act on.
		// Purple, matching the lifecycle badge above the title. The green in this
		// slot is for "CI passing · Mergeable" — states you can still act on — and
		// merged borrowed it, so the same word wore two colours on one card.
		expect(prStatusAtoms({ state: "merged", ci: { state: "passing" } })).toEqual([
			{ text: "Merged", tone: "merged" },
		]);
		expect(prStatusAtoms({ state: "closed" })).toEqual([{ text: "Closed", tone: "passive" }]);
	});

	it("reports CI, merge and review in that order", () => {
		const atoms = prStatusAtoms({
			state: "open",
			ci: { state: "passing" },
			mergeability: { state: "mergeable" },
			review: { decision: "approved" },
		});
		expect(atoms.map((a) => a.text)).toEqual(["CI passing", "Mergeable", "Approved"]);
	});

	// The daemon reports "unknown" while it is still looking; a card that said
	// "CI checking · Merge checking" on every fresh PR would be pure noise.
	it("omits anything the daemon has not determined", () => {
		expect(prStatusAtoms({ state: "open", ci: { state: "unknown" }, mergeability: { state: "unknown" } })).toEqual([]);
	});

	it("colours failures and blockers correctly", () => {
		const atoms = prStatusAtoms({
			state: "open",
			ci: { state: "failing" },
			mergeability: { state: "conflicting" },
			review: { decision: "changes_requested" },
		});
		expect(atoms.map((a) => a.tone)).toEqual(["error", "error", "warning"]);
	});

	it("surfaces unresolved comments when there is no formal decision", () => {
		const atoms = prStatusAtoms({ state: "open", review: { decision: "none", hasUnresolvedHumanComments: true } });
		expect(atoms).toEqual([{ text: "Unresolved comments", tone: "warning" }]);
	});
});

describe("prBlockerLine", () => {
	it("names failing checks and merge reasons together", () => {
		const line = prBlockerLine({
			ci: { failingChecks: [{ name: "go test" }] },
			mergeability: { reasons: ["behind_base"] },
		});
		expect(line).toBe("go test · branch behind base");
	});

	// A PR can fail a dozen checks; the card has one line for them.
	it("caps the list and counts the remainder rather than truncating silently", () => {
		const line = prBlockerLine({
			ci: { failingChecks: [{ name: "a" }, { name: "b" }, { name: "c" }, { name: "d" }] },
		});
		expect(line).toBe("a · b +2 more");
	});

	it("returns nothing when there is nothing blocking", () => {
		expect(prBlockerLine({})).toBeNull();
		expect(prBlockerLine({ ci: { failingChecks: [] }, mergeability: { reasons: [] } })).toBeNull();
	});
});

describe("comparePRs", () => {
	const sorted = (list: DashboardPR[]) => [...list].sort(comparePRs).map((p) => p.number);

	it("orders open before draft before merged before closed", () => {
		const closed = pr({ number: 1, state: "closed" });
		const merged = pr({ number: 2, state: "merged" });
		const draft = pr({ number: 3, isDraft: true });
		const open = pr({ number: 4 });
		expect(sorted([closed, merged, draft, open])).toEqual([4, 3, 2, 1]);
	});

	it("floats a ready-to-merge PR to the top of the open bucket", () => {
		const plain = pr({ number: 10 });
		const ready = pr({ number: 2, reviewDecision: "approved", mergeability: { mergeable: true } });
		expect(sorted([plain, ready])[0]).toBe(2);
	});

	it("puts PRs needing attention above quiet ones", () => {
		const quiet = pr({ number: 9 });
		const failing = pr({ number: 3, ciStatus: "failing" });
		const changes = pr({ number: 5, reviewDecision: "changes_requested" });
		expect(sorted([quiet, changes, failing])).toEqual([3, 5, 9]);
	});

	it("breaks ties with the newest PR first", () => {
		expect(sorted([pr({ number: 4 }), pr({ number: 12 }), pr({ number: 7 })])).toEqual([12, 7, 4]);
	});
});

describe("prListSections", () => {
	const item = (over: Partial<DashboardPR>) => ({
		pr: pr(over),
		session: session("alpha", []),
	});

	it("groups open pull requests by the next useful action", () => {
		const sections = prListSections([
			item({ number: 1, reviewDecision: "pending" }),
			item({ number: 2, ciStatus: "failing" }),
			item({ number: 3, reviewDecision: "approved", mergeability: { mergeable: true } }),
			item({ number: 4, isDraft: true }),
		], "open");

		expect(sections.map((section) => [section.key, section.data.map(({ pr }) => pr.number)])).toEqual([
			["ready", [3]],
			["attention", [2]],
			["review", [1]],
			["draft", [4]],
		]);
	});

	it("keeps merged and closed history out of the open board", () => {
		const items = [item({ number: 5, state: "merged" }), item({ number: 6, state: "closed" })];
		expect(prListSections(items, "open")).toEqual([]);
		expect(prListSections(items, "merged").map((section) => section.key)).toEqual(["merged"]);
		expect(prListSections(items, "all").map((section) => section.key)).toEqual(["merged", "closed"]);
	});

	it("orders newer pull requests first within a section", () => {
		const sections = prListSections([item({ number: 7 }), item({ number: 12 }), item({ number: 4 })], "open");
		expect(sections[0]?.data.map(({ pr }) => pr.number)).toEqual([12, 7, 4]);
	});
});
