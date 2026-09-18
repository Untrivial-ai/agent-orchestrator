// Presentation rules for pull requests. Pure — no React Native or Expo imports
// — so the wording, severity and ordering are unit-testable, the same split as
// pushStatus.ts / push.ts.
//
// The vocabulary deliberately mirrors the desktop app's
// frontend/src/renderer/lib/pr-display.ts, so the same PR state is described
// with the same word and the same severity colour on both surfaces. Where the
// two could drift, prefer whatever desktop already says.
// `import type` only: pulling a value out of ./api would chain through
// ./config into AsyncStorage and react-native, and this module must stay
// importable by a plain unit test.
import type { DashboardPR, DashboardSession } from "./api";
import type { Theme } from "./theme";

/**
 * Every PR across the given sessions, de-duplicated.
 *
 * Lives here rather than in api.ts so it can be unit-tested — api.ts chains
 * through config.ts into AsyncStorage and react-native.
 *
 * The key is the PROJECT plus the number, not owner/repo. The board endpoint
 * never populates `owner` or `repo` (see `mapPR`), so the old
 * `${owner}/${repo}#${number}` key collapsed to "/#12" for every PR, and two
 * projects that each had a PR #12 silently dropped one of them. Deduping still
 * matters: several sessions in one project can point at the same PR.
 */
export function collectPRs(sessions: DashboardSession[]): { pr: DashboardPR; session: DashboardSession }[] {
	const seen = new Set<string>();
	const out: { pr: DashboardPR; session: DashboardSession }[] = [];
	for (const s of sessions) {
		const list = s.prs && s.prs.length ? s.prs : s.pr ? [s.pr] : [];
		for (const pr of list) {
			// Real GitHub/GitLab PR numbers are >= 1; 0/missing signals a placeholder.
			if (!pr || !pr.number || pr.number <= 0) continue;
			const key = `${pr.owner ?? ""}/${pr.repo ?? ""}@${s.projectId}#${pr.number}`;
			if (seen.has(key)) continue;
			seen.add(key);
			out.push({ pr, session: s });
		}
	}
	return out;
}

export type Tone = "neutral" | "passive" | "success" | "warning" | "error";

export function toneColor(t: Theme, tone: Tone): string {
	switch (tone) {
		case "success":
			return t.green;
		case "warning":
			return t.amber;
		case "error":
			return t.red;
		case "passive":
			return t.textTertiary;
		default:
			return t.textSecondary;
	}
}

/**
 * The PR's own title when the daemon supplied one, else the caller's fallback
 * (in practice `sessionTitle(session)`).
 *
 * The list endpoint (`GET /sessions`) does not carry PR titles, so without this
 * every card in the tab reads "Pull request #N" and they are indistinguishable.
 * Desktop solves it the same way — see `sessionPRFactToSummary`, which backfills
 * `title` from the session.
 */
export function prTitle(pr: DashboardPR, fallback?: string | null): string {
	const own = pr.title?.trim();
	if (own) return own;
	const backfill = fallback?.trim();
	if (backfill) return backfill;
	return `Pull request #${pr.number}`;
}

export type PRLifecycle = "draft" | "open" | "merged" | "closed";
export type PRListFilter = "open" | "merged" | "all";
export type PRListItem = { pr: DashboardPR; session: DashboardSession };
export type PRListSectionKey = "ready" | "attention" | "review" | "draft" | "merged" | "closed";
export type PRListSection = { key: PRListSectionKey; label: string; data: PRListItem[] };

/** Lifecycle state, reading `isDraft` — which the card has never rendered. */
export function prLifecycle(pr: DashboardPR): PRLifecycle {
	if (pr.state === "merged") return "merged";
	if (pr.state === "closed") return "closed";
	// `mapPR` folds the wire's "draft" into state:"open" and records it here, so
	// the flag is the only place draft survives.
	return pr.isDraft ? "draft" : "open";
}

function prListSectionKey(pr: DashboardPR): PRListSectionKey {
	const life = prLifecycle(pr);
	if (life === "merged" || life === "closed" || life === "draft") return life;
	if (pr.mergeability?.mergeable && pr.reviewDecision === "approved") return "ready";
	if (pr.ciStatus === "failing" || pr.reviewDecision === "changes_requested" || !!pr.unresolvedThreads) {
		return "attention";
	}
	return "review";
}

const PR_LIST_SECTIONS: readonly { key: PRListSectionKey; label: string }[] = [
	{ key: "ready", label: "Ready to merge" },
	{ key: "attention", label: "Needs attention" },
	{ key: "review", label: "In review" },
	{ key: "draft", label: "Drafts" },
	{ key: "merged", label: "Merged" },
	{ key: "closed", label: "Closed" },
];

export function prListSections(items: PRListItem[], filter: PRListFilter): PRListSection[] {
	const allowed = (key: PRListSectionKey) => {
		if (filter === "open") return key !== "merged" && key !== "closed";
		if (filter === "merged") return key === "merged";
		return true;
	};

	return PR_LIST_SECTIONS.flatMap(({ key, label }) => {
		if (!allowed(key)) return [];
		const data = items
			.filter(({ pr }) => prListSectionKey(pr) === key)
			.sort((a, b) => b.pr.number - a.pr.number);
		return data.length ? [{ key, label, data }] : [];
	});
}

export function prStateVisual(t: Theme, pr: DashboardPR): { label: PRLifecycle; color: string; tint: string } {
	return stateVisualOf(t, prLifecycle(pr));
}

/**
 * Colours for a lifecycle already known. The rich summary reports `draft`
 * directly, so it doesn't need the `isDraft` reconstruction `prLifecycle` does.
 */
export function stateVisualOf(t: Theme, life: PRLifecycle): { label: PRLifecycle; color: string; tint: string } {
	switch (life) {
		case "merged":
			return { label: "merged", color: t.textSecondary, tint: t.bgSubtle };
		case "closed":
			return { label: "closed", color: t.red, tint: t.tintRed };
		case "draft":
			return { label: "draft", color: t.textTertiary, tint: t.bgSubtle };
		default:
			return { label: "open", color: t.green, tint: t.tintGreen };
	}
}

/** Humanised merge blocker, matching desktop's mergeReasonLabel. */
export function mergeReasonLabel(reason: string): string {
	switch (reason) {
		case "behind_base":
			return "branch behind base";
		case "ci_failing":
			return "CI failing";
		case "changes_requested":
			return "changes requested";
		case "review_required":
			return "review required";
		case "blocked_by_provider":
			return "provider blocked";
		default:
			return reason.replaceAll("_", " ");
	}
}

/**
 * The card's single status line: the worst one or two things true of this PR.
 *
 * A card has one line to say why you should care, so this is a severity ladder,
 * not a dump of every attribute. Merged and closed PRs are terminal and say only
 * that — "merged · CI failing" would be noise about a decided PR.
 */
export function prSummaryLine(pr: DashboardPR): { text: string; tone: Tone } {
	const life = prLifecycle(pr);
	if (life === "merged") return { text: "Merged", tone: "success" };
	if (life === "closed") return { text: "Closed without merging", tone: "passive" };

	const atoms: { text: string; tone: Tone }[] = [];
	if (pr.ciStatus === "failing") atoms.push({ text: "CI failing", tone: "error" });
	if (pr.reviewDecision === "changes_requested") atoms.push({ text: "Changes requested", tone: "warning" });
	else if (pr.unresolvedThreads) atoms.push({ text: "Unresolved comments", tone: "warning" });

	if (atoms.length === 0) {
		if (life === "draft") return { text: "Draft", tone: "passive" };
		if (pr.mergeability?.mergeable && pr.reviewDecision === "approved") {
			return { text: "Ready to merge", tone: "success" };
		}
		if (pr.ciStatus === "pending") return { text: "CI running", tone: "neutral" };
		if (pr.reviewDecision === "approved") return { text: "Approved", tone: "success" };
		if (pr.reviewDecision === "pending") return { text: "Awaiting review", tone: "neutral" };
		return { text: "Open", tone: "passive" };
	}

	// Worst tone wins for the line's colour; at most two atoms so it never wraps.
	const shown = atoms.slice(0, 2);
	const tone: Tone = shown.some((a) => a.tone === "error") ? "error" : "warning";
	return { text: shown.map((a) => a.text).join(" · "), tone };
}

/** Just the parts of the rich summary the card reads, kept structural so this module stays pure. */
export type RichPR = {
	state?: string;
	ci?: { state?: string; failingChecks?: { name: string }[] };
	review?: { decision?: string; hasUnresolvedHumanComments?: boolean };
	mergeability?: { state?: string; reasons?: string[] };
};

/**
 * The card's status line, built from the rich summary: one short atom each for
 * CI, mergeability and review, worst-coloured, omitting whatever the daemon
 * hasn't determined yet.
 *
 * Merged and closed PRs collapse to a single atom — "Merged · CI passing" is
 * noise about a PR nobody can act on.
 */
export function prStatusAtoms(rich: RichPR): { text: string; tone: Tone }[] {
	// Green, not the badge's purple: this line is the status summary, the same
	// slot that says "CI passing · Mergeable". Purple is reserved for the
	// lifecycle badge on the identity line above.
	if (rich.state === "merged") return [{ text: "Merged", tone: "success" }];
	if (rich.state === "closed") return [{ text: "Closed", tone: "passive" }];

	const atoms: { text: string; tone: Tone }[] = [];

	if (rich.ci?.state === "passing") atoms.push({ text: "CI passing", tone: "success" });
	else if (rich.ci?.state === "failing") atoms.push({ text: "CI failing", tone: "error" });
	else if (rich.ci?.state === "pending") atoms.push({ text: "CI running", tone: "neutral" });

	const merge = rich.mergeability?.state;
	if (merge === "mergeable") atoms.push({ text: "Mergeable", tone: "success" });
	else if (merge === "conflicting") atoms.push({ text: "Conflict", tone: "error" });
	else if (merge === "blocked") atoms.push({ text: "Blocked", tone: "warning" });
	else if (merge === "unstable") atoms.push({ text: "Unstable", tone: "warning" });

	const review = rich.review?.decision;
	if (review === "approved") atoms.push({ text: "Approved", tone: "success" });
	else if (review === "changes_requested") atoms.push({ text: "Changes requested", tone: "warning" });
	else if (review === "review_required") atoms.push({ text: "Review pending", tone: "neutral" });
	else if (rich.review?.hasUnresolvedHumanComments) atoms.push({ text: "Unresolved comments", tone: "warning" });

	return atoms;
}

/**
 * The names of what is actually failing, for the card's blocker line. Capped —
 * a PR can fail a dozen checks and the card has one line — with the remainder
 * reported as a count rather than truncated silently.
 */
export function prBlockerLine(rich: RichPR, max = 2): string | null {
	const names = [
		...(rich.ci?.failingChecks ?? []).map((c) => c.name).filter(Boolean),
		...(rich.mergeability?.reasons ?? []).map(mergeReasonLabel),
	];
	if (names.length === 0) return null;
	const shown = names.slice(0, max);
	const extra = names.length - shown.length;
	return extra > 0 ? `${shown.join(" · ")} +${extra} more` : shown.join(" · ");
}

// Lifecycle ordering, matching desktop's comparePRDisplaySummaries. Drafts sit
// below open PRs because they are not asking anything of you yet.
const LIFECYCLE_ORDER: Record<PRLifecycle, number> = { open: 0, draft: 1, merged: 2, closed: 3 };

/** Within the open bucket: the ones needing you first, ready-to-merge at the top. */
function openRank(pr: DashboardPR): number {
	if (pr.mergeability?.mergeable && pr.reviewDecision === "approved") return 0;
	if (pr.ciStatus === "failing") return 1;
	if (pr.reviewDecision === "changes_requested" || pr.unresolvedThreads) return 2;
	return 3;
}

/**
 * Sort order for the list. There is none today — PRs appear in whatever order
 * the daemon returned them, so the one you can act on could be anywhere.
 */
export function comparePRs(a: DashboardPR, b: DashboardPR): number {
	const life = LIFECYCLE_ORDER[prLifecycle(a)] - LIFECYCLE_ORDER[prLifecycle(b)];
	if (life !== 0) return life;
	const rank = openRank(a) - openRank(b);
	if (rank !== 0) return rank;
	// Newest first within a bucket.
	return b.number - a.number;
}
