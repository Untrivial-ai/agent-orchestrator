// Presentation rules for the orchestrator tab. Pure — no React Native or Expo
// imports — so the lifecycle mapping is unit-testable, the same split as
// prView.ts / pushStatus.ts.
import { agentBlocked, boardZoneOf, isArchived } from "./agentsView";
import type { DashboardSession, OrchestratorLink, ProjectInfo } from "./api";
import { relativeTime } from "./notificationView";
import { collectPRs, prLifecycle } from "./prView";
import { attentionOf, sessionTitle } from "./sessionStatus";
import { statusVisual, type Theme } from "./theme";

export type OrchestratorState = "missing" | "stopped" | "running";

/**
 * Where this project's orchestrator is in its lifecycle.
 *
 * Replaces three booleans the screen computed inline. The daemon does not send
 * a single state field, and `hasRuntime`/`isTerminal` are both derived from one
 * `isTerminated` flag — so treat a session as stopped only when explicitly
 * flagged, never on a missing field, or a build that omits them reads every
 * live orchestrator as dead.
 */
export function orchestratorState(link: OrchestratorLink | null | undefined): OrchestratorState {
	if (!link?.id) return "missing";
	if (link.hasRuntime === false || link.isTerminal === true) return "stopped";
	return "running";
}

export function orchestratorStatus(
	t: Theme,
	link: OrchestratorLink | null | undefined,
): { label: string; color: string; breathing: boolean } {
	const state = orchestratorState(link);
	if (state === "missing") return { label: "Not started", color: t.textFaint, breathing: false };
	if (state === "stopped") return { label: "Stopped", color: t.textTertiary, breathing: false };
	// `no_signal` describes missing activity telemetry, not a dead runtime. The
	// project row already knows this orchestrator is live from its runtime facts,
	// so presenting it as anything other than online is misleading.
	if (link?.status === "no_signal") return { label: "Online", color: t.green, breathing: false };
	// Running: defer to the shared status vocabulary so the orchestrator speaks
	// the same language as a session card.
	const v = link?.status ? statusVisual(t, link.status) : null;
	return v ? { label: v.label, color: v.color, breathing: !!v.breathing } : { label: "Online", color: t.green, breathing: false };
}

export type LaunchIntent = { clean: boolean; label: string; confirm: boolean };

/**
 * What the launch button should do, say, and whether to confirm first.
 *
 * The `clean` flag is the whole subtlety, and the screen had it backwards.
 * `SpawnOrchestrator` treats `clean: false` as an idempotent *ensure*: if an
 * active orchestrator already exists it returns that one and spawns nothing. So
 * "Restart" on a running orchestrator was sending `clean: false` and silently
 * doing nothing at all.
 *
 * Only `clean: true` restarts — and it is destructive: every live orchestrator
 * for the project is sent a retire notice ("AO is replacing this project
 * orchestrator. Stop coordinating new work now") and replaced. That is worth a
 * confirmation, which it never had.
 *
 * The card does not currently expose the running case: a healthy orchestrator
 * offers Open and nothing else, because a restart button is only meaningful
 * once there is something to restart. The branch stays because it is the
 * correct answer to "what would launching do right now", and because the
 * destructive path must keep its confirmation if the card ever offers it again
 * (a long-press, say) — not because the screen calls it today.
 */
export function launchIntent(state: OrchestratorState): LaunchIntent {
	if (state === "running") return { clean: true, label: "Restart orchestrator", confirm: true };
	// Nothing live to retire, so the cheap ensure is also the correct call. The
	// two remaining states are not the same action to a reader, though: an
	// orchestrator that exited is being *restarted*, one that never existed is
	// being *started*. Saying "Start" over a session that has clearly already
	// run reads as though the app lost track of it.
	if (state === "stopped") return { clean: false, label: "Restart orchestrator", confirm: false };
	return { clean: false, label: "Start orchestrator", confirm: false };
}

/** Worker sessions bucketed by attention zone, for the card's pills. */
export function zoneCounts(sessions: DashboardSession[]): Record<string, number> {
	const out: Record<string, number> = {};
	for (const s of sessions) {
		const a = attentionOf(s);
		out[a] = (out[a] ?? 0) + 1;
	}
	return out;
}

/**
 * The project's worker sessions.
 *
 * "Workers of this orchestrator" is not something the data can express: the
 * daemon has no parentId, no spawnedBy, no orchestrator column. Sessions are
 * related to an orchestrator only by sharing a project, so that is what this
 * computes — and why the card says "workers", not "its workers".
 */
export function workersOf(
	sessions: DashboardSession[],
	projectId: string,
	link: OrchestratorLink | null | undefined,
): DashboardSession[] {
	return sessions.filter((s) => s.projectId === projectId && s.id !== link?.id);
}

export type OrchestratorProjectAction = "open" | "start" | "resume";
export type OrchestratorProjectSectionKey = "attention" | "coordinating" | "not-running";

export type OrchestratorProjectRow = {
	project: ProjectInfo;
	link: OrchestratorLink | null;
	workers: DashboardSession[];
	section: OrchestratorProjectSectionKey;
	action: OrchestratorProjectAction;
	detail: string;
	activityAt: string | null;
	urgency: number;
};

export type OrchestratorProjectSection = {
	key: OrchestratorProjectSectionKey;
	title: "Needs Attention" | "Coordinating" | "Not Running";
	data: OrchestratorProjectRow[];
};

export type OrchestratorWorkerPreview = {
	id: string;
	name: string;
	status: string;
};

/** The small, recency-first worker snapshot shown below a project orchestrator. */
export function orchestratorWorkerPreviews(
	workers: readonly DashboardSession[],
	limit = 3,
): OrchestratorWorkerPreview[] {
	return workers
		.map((worker, index) => ({ worker, index }))
		.filter(({ worker }) => !isArchived(worker))
		.sort((a, b) => {
			const activity = (b.worker.lastActivityAt ?? "").localeCompare(a.worker.lastActivityAt ?? "");
			return activity || a.index - b.index;
		})
		.slice(0, limit)
		.map(({ worker }) => ({
			id: worker.id,
			name: sessionTitle(worker),
			status: worker.status === "starting" ? "spawning" : worker.status ?? "idle",
		}));
}

export function orchestratorWorkerAccessibilityLabel(
	worker: OrchestratorWorkerPreview,
	statusLabel: string,
): string {
	return `Open worker ${worker.name}, ${statusLabel}`;
}

export function orchestratorRowAccessibilityLabel(
	projectName: string,
	statusLabel: string,
	action: OrchestratorProjectAction,
): string {
	if (action === "open") return `Open orchestrator for ${projectName}, ${statusLabel}`;
	return `${action === "resume" ? "Resume" : "Start"} orchestrator for ${projectName}`;
}

const ACTIONABLE_ORCHESTRATOR_STATUS = new Set(["needs_input", "changes_requested", "stuck", "errored", "ci_failed"]);

function latestTimestamp(values: Array<string | null | undefined>): string | null {
	return values.reduce<string | null>((latest, value) => (!value || (latest && value <= latest) ? latest : value), null);
}

function earliestTimestamp(values: Array<string | null | undefined>): string | null {
	return values.reduce<string | null>((earliest, value) => (!value || (earliest && value >= earliest) ? earliest : value), null);
}

function countPhrase(count: number, singular: string, plural: string): string {
	return `${count} ${count === 1 ? singular : plural}`;
}

function readyPullRequests(workers: DashboardSession[]): number {
	return collectPRs(workers).filter(({ pr }) => {
		const lifecycle = prLifecycle(pr);
		return (
			(lifecycle === "open" || lifecycle === "draft") &&
			(pr.mergeability?.mergeable === true || pr.reviewDecision === "approved")
		);
	}).length;
}

function detailFor(state: OrchestratorState, workers: DashboardSession[], zones: Record<string, number>): string {
	if (state === "missing") return "Start one to coordinate work for this project";
	if (state === "stopped") return "Resume it to coordinate work for this project";

	const parts: string[] = [];
	const needsInput = (zones.respond ?? 0) + (zones.action ?? 0);
	const needsReview = zones.review ?? 0;
	const ready = readyPullRequests(workers);
	if (needsInput) parts.push(countPhrase(needsInput, "worker needs input", "workers need input"));
	if (needsReview) parts.push(countPhrase(needsReview, "worker needs review", "workers need review"));
	if (ready) parts.push(countPhrase(ready, "pull request is ready", "pull requests are ready"));
	if (parts.length) return parts.join(" · ");
	if (workers.length) return countPhrase(workers.length, "active worker", "active workers");
	return "Ready for coordinated work";
}

/**
 * The counts a project row shows as chips, in the order they earn attention.
 *
 * `detailFor` already computes these numbers, but spends them on prose in the
 * quietest style on the row — so the figures you actually scan for are the least
 * visible thing there. Same numbers, promoted.
 *
 * Returns tones rather than colours so this stays theme-free and testable; the
 * component resolves them through `attentionMetaFor`. That is the same split
 * agentsView.ts already uses for `boardZoneOf` (pure) vs `zoneMeta(t, zone)`.
 */
export type ProjectChipTone = "attention" | "review" | "merge" | "working";
export type ProjectRowChip = { id: string; label: string; tone: ProjectChipTone };

export function projectRowChips(row: OrchestratorProjectRow): ProjectRowChip[] {
	if (orchestratorState(row.link) !== "running") return [];

	const zones = zoneCounts(row.workers);
	const needsYou = (zones.respond ?? 0) + (zones.action ?? 0);
	const failing = zones.review ?? 0;
	const ready = readyPullRequests(row.workers);
	const working = zones.working ?? 0;

	const chips: ProjectRowChip[] = [];
	// Zero counts are dropped rather than rendered as "0": a row should carry only
	// the facts that are true of it.
	if (needsYou) chips.push({ id: "needs-you", label: `${needsYou} need${needsYou === 1 ? "s" : ""} you`, tone: "attention" });
	if (failing) chips.push({ id: "failing", label: `${failing} failing`, tone: "review" });
	if (ready) chips.push({ id: "ready", label: `${ready} ready`, tone: "merge" });
	if (working) chips.push({ id: "working", label: `${working} working`, tone: "working" });

	// Three is the most a phone row can hold without the line wrapping; the order
	// above means what gets dropped is always the least urgent.
	return chips.slice(0, 3);
}

/**
 * The specific thing blocking a project, for rows in Needs Attention.
 *
 * This is the line the row was missing. `showProjectDetail` suppressed the
 * detail on exactly the rows in the attention section, so the most urgent rows
 * said the least: "Project needs attention · Needs input" and nothing about
 * which worker, waiting on what, or for how long.
 *
 * Returned as parts, not a joined string, so the component owns the separators
 * and the typography — and so the test can assert on the worker rather than on
 * punctuation.
 */
export type ProjectBlocker = { worker: string; reason: string; age: string };

const BLOCKER_REASON: Record<string, string> = {
	respond: "waiting on your reply",
	action: "waiting on your approval",
	review: "checks failing",
	merge: "ready to merge",
};

export function projectBlockerLine(
	row: OrchestratorProjectRow,
	now: number = Date.now(),
): ProjectBlocker | null {
	if (row.section !== "attention") return null;

	// Oldest first: the thing that has been blocked longest is the thing to name.
	const blocked = row.workers
		.filter((worker) => BLOCKER_REASON[attentionOf(worker)])
		.sort((a, b) => (a.lastActivityAt ?? "").localeCompare(b.lastActivityAt ?? ""));

	const worker = blocked[0];
	if (!worker) return null;

	return {
		worker: sessionTitle(worker),
		reason: BLOCKER_REASON[attentionOf(worker)] ?? "needs attention",
		age: worker.lastActivityAt ? relativeTime(worker.lastActivityAt, now) : "",
	};
}

/**
 * The colour of the row's leading status rail.
 *
 * Worst state across the orchestrator and its workers, so one glance down the
 * rail tells you where to look. Replaces the per-row mascot, which was identical
 * on every row and so could not tell any two of them apart.
 */
export type ProjectRailTone = "attention" | "review" | "working" | "idle" | "stopped";

export function projectRailTone(row: OrchestratorProjectRow): ProjectRailTone {
	const state = orchestratorState(row.link);
	if (state !== "running") return "stopped";

	if (orchestratorUrgency(row.link?.status) === 0) return "review";
	if (orchestratorUrgency(row.link?.status) === 1) return "attention";

	const zones = zoneCounts(row.workers);
	if ((zones.respond ?? 0) + (zones.action ?? 0) > 0) return "attention";
	if ((zones.review ?? 0) > 0) return "review";
	if ((zones.working ?? 0) > 0) return "working";
	return "idle";
}

function orchestratorUrgency(status?: string | null): number | null {
	if (status === "stuck" || status === "errored" || status === "ci_failed") return 0;
	if (status === "needs_input" || status === "changes_requested") return 1;
	return null;
}

function workerUrgency(zones: Record<string, number>): number | null {
	if ((zones.respond ?? 0) + (zones.action ?? 0) > 0) return 2;
	if ((zones.review ?? 0) > 0) return 3;
	if ((zones.merge ?? 0) > 0) return 4;
	return null;
}

function attentionTimestamp(link: OrchestratorLink | null, workers: DashboardSession[]): string | null {
	const workerTimes = workers
		.filter((worker) => ["respond", "action", "review", "merge"].includes(attentionOf(worker)))
		.map((worker) => worker.lastActivityAt);
	const linkTime = ACTIONABLE_ORCHESTRATOR_STATUS.has(link?.status ?? "") ? link?.updatedAt : null;
	return earliestTimestamp([linkTime, ...workerTimes]);
}

export function orchestratorProjectSections(
	projects: readonly ProjectInfo[],
	sessions: readonly DashboardSession[],
	orchestrators: readonly OrchestratorLink[],
): OrchestratorProjectSection[] {
	const links = new Map(orchestrators.map((orchestrator) => [orchestrator.projectId, orchestrator]));
	const rows = projects.map((project, index) => {
		const link = links.get(project.id) ?? null;
		const state = orchestratorState(link);
		const workers = workersOf([...sessions], project.id, link).filter((worker) => !isArchived(worker));
		const zones = zoneCounts(workers);
		const linkUrgency = orchestratorUrgency(link?.status);
		const sessionUrgency = workerUrgency(zones);
		const urgency = Math.min(linkUrgency ?? Number.POSITIVE_INFINITY, sessionUrgency ?? Number.POSITIVE_INFINITY);
		const hasAttention = Number.isFinite(urgency);
		const section: OrchestratorProjectSectionKey =
			state !== "running" ? "not-running" : hasAttention ? "attention" : "coordinating";
		const action: OrchestratorProjectAction = state === "running" ? "open" : state === "stopped" ? "resume" : "start";
		const activityAt = latestTimestamp([link?.updatedAt, ...workers.map((worker) => worker.lastActivityAt)]);

		return {
			project,
			link,
			workers,
			section,
			action,
			detail: detailFor(state, workers, zones),
			activityAt,
			urgency,
			attentionAt: attentionTimestamp(link, workers),
			index,
		};
	});

	const definitions: Array<Pick<OrchestratorProjectSection, "key" | "title">> = [
		{ key: "attention", title: "Needs Attention" },
		{ key: "coordinating", title: "Coordinating" },
		{ key: "not-running", title: "Not Running" },
	];

	return definitions.flatMap((definition) => {
		const data = rows
			.filter((row) => row.section === definition.key)
			.sort((a, b) => {
				if (definition.key === "attention") {
					const urgency = a.urgency - b.urgency;
					if (urgency) return urgency;
					const unresolved = (a.attentionAt ?? "").localeCompare(b.attentionAt ?? "");
					if (unresolved) return unresolved;
				} else {
					const activity = (b.activityAt ?? "").localeCompare(a.activityAt ?? "");
					if (activity) return activity;
				}
				return a.index - b.index;
			})
			.map(({ attentionAt: _attentionAt, index: _index, ...row }) => row);
		return data.length ? [{ ...definition, data }] : [];
	});
}

/**
 * The one line of counts a project card carries under its name.
 *
 * Deliberately a sentence in the quiet text style rather than a row of coloured
 * chips: the orchestrator button is what the card is for, and chips competing
 * with it in colour and weight is exactly what the redesign removed. The one
 * count worth colour — work waiting on a person — is reported separately so the
 * card can tint it on its own.
 */
export type ProjectCardSummary = {
	/** Live workers in the project, e.g. "3 workers". */
	workers: string;
	/** Workers waiting on a person; zero when none. */
	needsYou: number;
};

export function projectCardSummary(row: OrchestratorProjectRow): ProjectCardSummary {
	const count = row.workers.length;
	const zones = zoneCounts(row.workers);
	return {
		workers: count === 0 ? "No workers" : `${count} worker${count === 1 ? "" : "s"}`,
		needsYou: (zones.respond ?? 0) + (zones.action ?? 0),
	};
}

/** What the card's orchestrator button says and does. */
export type OrchestratorButtonCopy = {
	label: string;
	/** The same action in one word, for the compact pill on a project row. */
	short: string;
	/** Running orchestrators show their state beside the label. */
	running: boolean;
};

export function orchestratorButtonCopy(row: OrchestratorProjectRow, busy: boolean): OrchestratorButtonCopy {
	if (busy) {
		const label = row.action === "resume" ? "Resuming…" : "Starting…";
		return { label, short: label, running: false };
	}
	switch (row.action) {
		case "open": return { label: "Open orchestrator", short: "Orchestrator", running: true };
		case "resume": return { label: "Resume orchestrator", short: "Resume", running: false };
		case "start": return { label: "Start orchestrator", short: "Start", running: false };
	}
}

/**
 * Every session a project detail page lists: its workers, archived ones
 * included. The Workers tab filters by project too, but it is scoped to live
 * work; a project's own page is where its history belongs, so nothing is dropped
 * for being finished. The store already keeps orchestrators in a list of their
 * own, so these are workers only; the page shows the orchestrator above them.
 */
export function projectDetailSessions(projectId: string, sessions: readonly DashboardSession[]): DashboardSession[] {
	return sessions.filter((session) => session.projectId === projectId);
}

export type ProjectPageStats = { workers: number; needsYou: number; ready: number; archived: number };

/**
 * The counts across the top of a project page. Zones come from the board's own
 * classifier, so "Needs you" and "Ready" agree with the sections listed below.
 * "Needs you" also counts the orchestrator when it is the one waiting — it is
 * something to answer, even though it is not in the worker list.
 */
export function projectPageStats(
	sessions: readonly DashboardSession[],
	orchestrator?: OrchestratorLink | null,
): ProjectPageStats {
	const orchestratorWaiting = orchestrator && orchestrator.isTerminal !== true && agentBlocked({ status: orchestrator.status ?? null });
	const stats: ProjectPageStats = { workers: 0, needsYou: orchestratorWaiting ? 1 : 0, ready: 0, archived: 0 };
	for (const session of sessions) {
		if (isArchived(session)) {
			stats.archived += 1;
			continue;
		}
		stats.workers += 1;
		const zone = boardZoneOf(session);
		if (zone === "needs_you") stats.needsYou += 1;
		if (zone === "ready") stats.ready += 1;
	}
	return stats;
}
