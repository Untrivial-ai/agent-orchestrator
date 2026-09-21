// Board vocabulary for the Agents tab. Pure — no React Native or Expo imports —
// so the zoning, archive rule and copy are unit-testable, the same split as
// prView.ts / orchestratorView.ts.
//
// Mirrors the desktop board (frontend/src/renderer/components/SessionsBoard.tsx
// and lib/session-presentation.ts) so the two speak the same language: same
// zone names, same archive rule, same "PR #12, #13 open" phrasing.
import type { DashboardPR, DashboardSession, KanbanColumn } from "./api";
import { relativeTime } from "./notificationView";
import { prLifecycle, type Tone } from "./prView";
import { attentionOf, sessionTitle } from "./sessionStatus";
import { statusVisual, type Theme } from "./theme";

/**
 * The board's sections: desktop's delivery lanes, plus one mobile-only section
 * above them.
 *
 * Four of these are the daemon's own Kanban columns, so the two apps place a
 * session identically. `needs_you` is deliberately mobile's: a worker blocked
 * on a person has no PR yet, so desktop files it under Building alongside every
 * other agent that happens to be running. That is right for a pipeline view and
 * wrong for a phone, which is opened to find what is stuck.
 */
export type BoardZone = "needs_you" | "needs_review" | "ready" | "building" | "validating";

/**
 * Mobile's order: the three sections a person owns, then the two a machine does.
 *
 * Desktop orders its lanes by delivery progress (building → validating →
 * needs_review → ready) because a board is read left to right as a pipeline. A
 * phone is read top down as a queue, so the order is by who is blocked.
 */
export const BOARD_ZONES: BoardZone[] = ["needs_you", "needs_review", "ready", "building", "validating"];

/**
 * Statuses where the agent itself is waiting on a person.
 *
 * Deliberately agent-level only. `ci_failed` and `changes_requested` are PR
 * facts, and the daemon already decides whether AO or a person owns their next
 * turn — lifting them here would second-guess that and split one PR's lifecycle
 * across two sections.
 */
const AGENT_BLOCKED = new Set(["needs_input", "stuck", "errored", "exited"]);

export function agentBlocked(session: Pick<DashboardSession, "status" | "displayStatus">): boolean {
	return AGENT_BLOCKED.has(session.status ?? "") || session.displayStatus === "Blocked";
}

/**
 * The daemon's column, falling back to deriving one.
 *
 * Mirrors desktop's `toKanbanColumn`: trust the server's placement when it sends
 * one, because it is derived from durable delivery facts that the client cannot
 * see — whether AO's review pass is mid-run, whether auto-inject is configured.
 * The fallback only covers a daemon too old to send the field.
 */
export function kanbanColumnOf(session: DashboardSession): KanbanColumn {
	if (session.kanbanColumn) return session.kanbanColumn;
	switch (attentionOf(session)) {
		case "merge":
			return "ready";
		case "pending":
			return "validating";
		case "respond":
		case "action":
		case "review":
			return "needs_review";
		case "done":
			return "archive";
		default:
			return "building";
	}
}

/**
 * Which section a session belongs in.
 *
 * Agent-level blockage outranks delivery placement: a worker waiting on your
 * reply is the reason the app was opened, whether or not it has produced a PR.
 */
export function boardZoneOf(session: DashboardSession): BoardZone {
	if (agentBlocked(session)) return "needs_you";
	const column = kanbanColumnOf(session);
	// `archive` never reaches a section — isArchived routes terminated runtimes
	// to the archive strip before grouping.
	return column === "archive" ? "building" : column;
}

/**
 * Section labels, taken from desktop's own strings so the two apps name the
 * same thing identically (product-ui session-presentation.ts, `column.*`).
 */
/**
 * A shape for each status, so state does not rest on colour alone.
 *
 * The row already tints its trailing label by status, which is invisible to a
 * colour-blind reader and weak in bright sun. A glyph adds a second channel
 * carrying the same fact.
 *
 * Feather names rather than an icon component, so this stays a pure mapping the
 * row can render however it likes — and so it is testable without React Native.
 */
export type WorkerStatusGlyph = "alert-circle" | "message-square" | "x-octagon" | "check-circle" | "git-pull-request" | "loader" | "moon";

export function workerStatusGlyph(status?: string | null): WorkerStatusGlyph | null {
	switch (status) {
		case "needs_input":
			return "message-square";
		case "changes_requested":
			return "message-square";
		case "stuck":
		case "errored":
		case "exited":
			return "alert-circle";
		case "ci_failed":
			return "x-octagon";
		case "mergeable":
		case "approved":
			return "check-circle";
		case "merged":
		case "pr_open":
		case "draft":
		case "review_pending":
			return "git-pull-request";
		case "working":
		case "detecting":
		case "spawning":
			return "loader";
		case "idle":
			return "moon";
		default:
			// No glyph beats a meaningless one: an unknown status has nothing
			// specific to say, and a generic dot would only add noise.
			return null;
	}
}

export function zoneMeta(t: Theme, zone: BoardZone): { label: string; color: string } {
	switch (zone) {
		case "needs_you":
			return { label: "Needs you", color: t.amber };
		case "needs_review":
			return { label: "In review", color: t.purple };
		case "ready":
			return { label: "Ready", color: t.green };
		case "validating":
			return { label: "Validating", color: t.textTertiary };
		default:
			return { label: "Building", color: t.orange };
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

export type WorkerRowPresentation = {
	title: string;
	project: string;
	branch: string | null;
	trailing: string;
	trailingKind: "status" | "time";
};

function compactProjectLabel(value: string, max = 20): string {
	if (value.length <= max) return value;
	const keep = max - 1;
	const head = Math.ceil(keep / 2);
	const tail = Math.floor(keep / 2);
	return `${value.slice(0, head)}…${value.slice(value.length - tail)}`;
}

/**
 * The compact identity and state shown by the Workers list.
 *
 * Active states earn a semantic label. Quiet states use the last-activity age
 * instead, because repeating "Idle" down an entire section adds less context
 * than showing which worker changed most recently.
 */
export function workerRowPresentation(
	t: Theme,
	session: DashboardSession,
	projectName?: string,
	now: number = Date.now(),
): WorkerRowPresentation {
	const title = sessionTitle(session);
	const visual = statusVisual(t, session.status);
	const elapsedStatuses = new Set(["idle", "no_signal", "unknown", "done", "killed", "terminated"]);
	const elapsed = relativeTime(session.lastActivityAt, now);
	const useElapsed = elapsedStatuses.has(session.status ?? "") && Boolean(elapsed);

	return {
		title,
		// A standalone agent session has no project at all, so there is nothing to
		// abbreviate — say what it is rather than showing an empty slot.
		project: projectName?.trim() || (session.projectId ? compactProjectLabel(session.projectId) : "Standalone"),
		branch: showBranch(session.branch, title) ? session.branch : null,
		trailing: useElapsed ? elapsed : visual.label,
		trailingKind: useElapsed ? "time" : "status",
	};
}

function comparePinned(a: DashboardSession, b: DashboardSession): number {
	return Number(Boolean(b.isPinned)) - Number(Boolean(a.isPinned));
}

function compareActivity(a: DashboardSession, b: DashboardSession, newestFirst: boolean): number {
	const left = a.lastActivityAt ?? "";
	const right = b.lastActivityAt ?? "";
	return newestFirst ? right.localeCompare(left) : left.localeCompare(right);
}

function compareInZone(zone: BoardZone, a: DashboardSession, b: DashboardSession): number {
	// Sections a machine is turning read newest-first, because the interesting
	// one is whatever just moved. Sections a person owns read oldest-first, so
	// what has been waiting longest is at the top.
	return compareActivity(a, b, zone === "building" || zone === "validating");
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
): { pinned: DashboardSession[]; sections: BoardSection[]; archived: DashboardSession[] } {
	const pinned: DashboardSession[] = [];
	const live: DashboardSession[] = [];
	const archived: DashboardSession[] = [];
	for (const s of sessions) {
		if (isArchived(s)) archived.push(s);
		else if (s.isPinned) pinned.push(s);
		else live.push(s);
	}
	// Pinning is a deliberate bookmark, so the most recently pinned worker gets
	// the first slot. Activity is the fallback for older daemon versions.
	pinned.sort((a, b) => (b.pinnedAt ?? b.lastActivityAt ?? "").localeCompare(a.pinnedAt ?? a.lastActivityAt ?? ""));

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
	return { pinned, sections, archived };
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
