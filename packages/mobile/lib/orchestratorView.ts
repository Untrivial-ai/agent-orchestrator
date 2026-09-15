// Presentation rules for the orchestrator tab. Pure — no React Native or Expo
// imports — so the lifecycle mapping is unit-testable, the same split as
// prView.ts / pushStatus.ts.
import type { DashboardSession, OrchestratorLink } from "./api";
import { attentionOf } from "./sessionStatus";
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
	// Running: defer to the shared status vocabulary so the orchestrator speaks
	// the same language as a session card.
	const v = link?.status ? statusVisual(t, link.status) : null;
	return v ? { label: v.label, color: v.color, breathing: !!v.breathing } : { label: "Online", color: t.blue, breathing: false };
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
