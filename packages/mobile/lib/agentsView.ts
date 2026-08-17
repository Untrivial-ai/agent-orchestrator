// Board vocabulary for the Agents tab. Pure — no React Native or Expo imports —
// so the zoning, archive rule and copy are unit-testable, the same split as
// prView.ts / orchestratorView.ts.
//
// Mirrors the desktop board (frontend/src/renderer/components/SessionsBoard.tsx
// and lib/session-presentation.ts) so the two speak the same language: same
// zone names, same archive rule, same "PR #12, #13 open" phrasing.
import type { DashboardPR, DashboardSession } from "./api";
import { prLifecycle, type Tone } from "./prView";
import { attentionOf } from "./sessionStatus";
import type { Theme } from "./theme";

/** The four board columns, as desktop names them. */
export type BoardZone = "working" | "action" | "pending" | "merge";

/** Mobile's action-first section order. */
export const BOARD_ZONES: BoardZone[] = ["action", "merge", "working", "pending"];

/**
 * Which column a session belongs in.
 *
 * A presentation mapping over `attentionOf`, not a replacement for it — the
 * orchestrator tab's zone pills use its finer six-bucket taxonomy, and changing
 * that would change them too.
 *
 * `respond` and `review` both fold into `action`: desktop files `ci_failed` and
 * `changes_requested` under "Needs you" rather than giving review its own
 * column, and a phone has even less room to split them.
 */
export function boardZoneOf(session: DashboardSession): BoardZone {
	switch (attentionOf(session)) {
		case "merge":
			return "merge";
		case "pending":
			return "pending";
		case "respond":
		case "action":
		case "review":
			return "action";
		default:
			// `working` and `done`. A session only reaches here as `done` when it is
			// finished but its runtime is still alive — a dead one is archived
			// before zoning, see isArchived.
			return "working";
	}
}

export function zoneMeta(t: Theme, zone: BoardZone): { label: string; color: string } {
	switch (zone) {
		case "merge":
			return { label: "Ready to merge", color: t.green };
		case "action":
			return { label: "Needs you", color: t.amber };
		case "pending":
			return { label: "In review", color: t.textTertiary };
		default:
			return { label: "Working", color: t.orange };
	}
}

/**
 * Whether a session belongs in the archive rather than on the board.
 *
 * Desktop's exact rule (`isArchivedSession`): a dead *runtime*, not a finished
 * outcome. Deliberately not `attentionOf(s) === "done"` — a session that has
 * merged but whose agent is still running belongs in Ready to merge, and only
 * a terminated runtime is archive.
 */
export function isArchived(session: DashboardSession): boolean {
	return session.isTerminated === true || session.status === "terminated";
}

export type BoardSection = { zone: BoardZone; label: string; color: string; data: DashboardSession[] };

function comparePinned(a: DashboardSession, b: DashboardSession): number {
	return Number(Boolean(b.isPinned)) - Number(Boolean(a.isPinned));
}

function compareActivity(a: DashboardSession, b: DashboardSession, newestFirst: boolean): number {
	const left = a.lastActivityAt ?? "";
	const right = b.lastActivityAt ?? "";
	return newestFirst ? right.localeCompare(left) : left.localeCompare(right);
}

function compareInZone(zone: BoardZone, a: DashboardSession, b: DashboardSession): number {
	return comparePinned(a, b) || compareActivity(a, b, zone === "working");
}

/**
 * The board, split into its four sections plus the archive.
 *
 * Empty zones are dropped rather than rendered as empty headers — on a phone a
 * run of empty section titles is most of the screen.
 */
export function groupSessions(
	t: Theme,
	sessions: DashboardSession[],
): { sections: BoardSection[]; archived: DashboardSession[] } {
	const live: DashboardSession[] = [];
	const archived: DashboardSession[] = [];
	for (const s of sessions) (isArchived(s) ? archived : live).push(s);

	const byZone = new Map<BoardZone, DashboardSession[]>();
	for (const s of live) {
		const zone = boardZoneOf(s);
		const bucket = byZone.get(zone);
		if (bucket) bucket.push(s);
		else byZone.set(zone, [s]);
	}

	const sections = BOARD_ZONES.filter((z) => byZone.get(z)?.length).map((zone) => {
		const data = byZone.get(zone) ?? [];
		data.sort((a, b) => compareInZone(zone, a, b));
		return { zone, ...zoneMeta(t, zone), data };
	});

	// Pin history deliberately kept close, then show the newest remaining history.
	archived.sort((a, b) => comparePinned(a, b) || compareActivity(a, b, true));
	return { sections, archived };
}

/**
 * Whether the branch line says anything the title didn't.
 *
 * Desktop's `sameLabel`, unchanged — it strips only conventional git prefixes.
 *
 * Two earlier versions of this were stricter and both hid too much. Comparing
 * against the SESSION ID stopped making sense once titles came from `issueId`,
 * because the id then appeared nowhere on the card. And normalising away AO's
 * own `ao/<id>/root` scaffolding hid the branch on every unnamed session — but
 * that string is the worktree, it is the only place the card names it, and
 * desktop shows it. Only a branch that genuinely restates the title is dropped.
 */
export function showBranch(branch: string | null | undefined, title: string): boolean {
	const b = branch?.trim();
	if (!b) return false;
	const normalize = (v: string) =>
		v
			.toLowerCase()
			.replace(/^(feat|fix|chore|refactor|session)\//, "")
			.replace(/[^a-z0-9]+/g, "");
	return normalize(b) !== normalize(title);
}

// Tracker providers whose ids the intake daemon stamps sessions with, in
// "<provider>:<native>" form. Ported from desktop's TRACKER_PROVIDER_PREFIXES;
// adding Linear or Jira later is one more prefix here.
const TRACKER_PROVIDER_PREFIXES = ["github:"];

/**
 * The issue id when it came from tracker intake, or null for a manually created
 * session.
 *
 * `issueId` is free text — the daemon stores whatever the spawn caller passed
 * (`seedRecord`: `IssueID: cfg.IssueID`, no validation), so a session created by
 * hand carries the task name typed at spawn rather than a tracker reference.
 * Desktop shows the chip only for real tracker ids and hides the rest, and the
 * chip is a poor home for a sentence anyway.
 */
export function trackerIssueId(issueId?: string | null): string | null {
	const id = issueId?.trim();
	if (!id) return null;
	return TRACKER_PROVIDER_PREFIXES.some((prefix) => id.startsWith(prefix)) ? id : null;
}

/**
 * The card's PR line, grouped by lifecycle the way desktop's board card does:
 * `PR #12, #13 open`. Returns null when the session has no PR, so the card
 * renders nothing rather than an empty row.
 */
export function prLine(session: DashboardSession): { text: string; tone: Tone } | null {
	const list: DashboardPR[] = session.prs?.length ? session.prs : session.pr ? [session.pr] : [];
	const real = list.filter((pr) => pr?.number > 0);
	if (real.length === 0) return null;

	// Group in first-seen order, matching desktop's groupPRsByLifecycle.
	const groups = new Map<string, number[]>();
	for (const pr of real) {
		const life = prLifecycle(pr);
		const nums = groups.get(life);
		if (nums) nums.push(pr.number);
		else groups.set(life, [pr.number]);
	}

	const parts = [...groups.entries()].map(([life, nums]) => `${nums.map((n) => `#${n}`).join(", ")} ${life}`);
	// One tone for the whole line: the worst lifecycle present.
	const lifecycles = [...groups.keys()];
	const tone: Tone = lifecycles.includes("closed")
		? "error"
		: lifecycles.includes("open")
			? "success"
			: lifecycles.includes("merged")
				? "neutral"
				: "passive";
	return { text: `PR ${parts.join(" · ")}`, tone };
}
