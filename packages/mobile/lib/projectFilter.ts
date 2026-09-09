import type { DashboardSession, ProjectInfo } from "./api";
import { isArchived } from "./agentsView";

/** The board filter that means "every project". */
export const ALL_PROJECTS = "all";

/**
 * What the app knows about one machine's projects.
 *
 * `known` is the whole point: `projects` is empty both before the first answer
 * and when the answer never came, and the board's filter has to treat those
 * differently from a daemon that really lists nothing.
 *
 * Built only by retainProjects and only from this starting value, which is what
 * keeps `known: false` and a non-empty list from ever pairing up.
 */
export type KnownProjects = {
	/** machineIdentity of the daemon this came from; "" before any answer. */
	machine: string;
	projects: ProjectInfo[];
	known: boolean;
};

export const NO_PROJECTS_KNOWN: KnownProjects = { machine: "", projects: [], known: false };

/**
 * Fold one tick's answer into what is known.
 *
 * getSessions reports a failed /projects as null rather than [], so a blip on
 * that one route is no longer indistinguishable from "this daemon has no
 * projects". Without the distinction the saved filter flickered: a failed tick
 * resolved it to "all", the next success resolved it back to the saved id, and
 * the next failure to "all" again (#5058 review).
 *
 * A list is kept per machine because another daemon's projects are not evidence
 * about this one's — re-pairing must not judge the new machine's filter against
 * the old machine's list. Returns `prev` unchanged when there is nothing to
 * change, so a failing tick costs no re-render.
 *
 * `activeMachine` is the machine the app is on NOW, and an answer from any other
 * one is dropped whole. fetchAll has no staleness guard, so a request already in
 * flight when the user re-pairs still lands: recording it as its own machine's
 * is not enough, because it DISPLACES the list the current machine had just
 * given us, and the current machine's next failed /projects then finds nothing
 * retained for itself and goes unknown — resurrecting the very filter this
 * fixes. Dropping it keeps the guarantee to "the last list THIS machine gave
 * us, until this machine gives us another" (#5058 review, round 2).
 */
export function retainProjects(
	prev: KnownProjects,
	answer: { machine: string; projects: ProjectInfo[] | null },
	activeMachine: string,
): KnownProjects {
	if (answer.machine !== activeMachine) return prev;
	// Explicitly against null, not truthiness: a successful [] takes this branch
	// and becomes known, which is the distinction the whole type exists for.
	if (answer.projects !== null) return { machine: answer.machine, projects: answer.projects, known: true };
	if (prev.machine === answer.machine && prev.known) return prev;
	return { machine: answer.machine, projects: [], known: false };
}

/**
 * What is known about the machine the app is on, and nothing else.
 *
 * The write side above drops foreign answers, but a re-pair still leaves the
 * previous machine's list in state until the new machine answers for the first
 * time. Reading it in that window would judge the new machine's filter against
 * the old machine's projects, and the spawn sheet would re-check its seed
 * against them too — invalidating a legitimate pick and clearing the model
 * chosen under it. Unknown is the honest answer until this machine has spoken.
 *
 * Identity-stable in both branches, so it adds no re-render.
 */
export function projectsForMachine(state: KnownProjects, activeMachine: string): KnownProjects {
	return state.machine === activeMachine ? state : NO_PROJECTS_KNOWN;
}

/**
 * The board's project filter, checked against what the daemon actually has.
 *
 * `ao.activeProject` is restored from storage and outlives the project it
 * names: `ao project rm` on the desktop, or pairing the phone with a machine
 * whose projects differ, leaves the filter pointing at an id no session will
 * ever carry. The board then filters everything out, the spawn sheet sends the
 * dead id to the daemon and shows its 404, and nothing says the board is
 * filtered: the switcher is hidden below two projects and Settings labelled the
 * stale id "All projects" because its lookup missed (#4843).
 *
 * Returns the filter to apply: the id when the daemon lists it, "all" when it
 * does not. Pure so the store can derive from it and the sheet can re-check
 * its seed with the same rule.
 *
 * `projectsKnown` is whether `projects` is a list the daemon actually answered
 * with, as opposed to the empty array that stands in before the first one lands.
 * Without it an empty list means two opposite things and the filter flickers:
 * a /projects failure resolved to "all", then the next success resolved back to
 * the saved id, then the next failure to "all" again (#5058 review). The store
 * keeps the last list a machine gave it, so a blip leaves this answer alone.
 */
export function resolveActiveProject(
	activeProjectId: string,
	projects: readonly { id: string }[],
	projectsKnown: boolean,
): string {
	if (activeProjectId === ALL_PROJECTS) return ALL_PROJECTS;
	// Nothing to judge against yet. Keeping the filter is the conservative half
	// of the trade: a cold start applies it for the tick before the list lands,
	// where clearing it would silently discard a choice the daemon may well
	// still honour. A list that names nothing IS evidence, though — sessions
	// outlive their project (`ao project rm` kills the live ones and archives
	// the project; the records stay listed), so a daemon with no projects can
	// still have sessions this filter would hide.
	if (!projectsKnown) return activeProjectId;
	return projects.some((p) => p.id === activeProjectId) ? activeProjectId : ALL_PROJECTS;
}

/**
 * What to call the filter wherever a name is shown. Goes through
 * resolveActiveProject first, so the label cannot disagree with the filter the
 * board applies whichever id a caller hands it. The raw id stands in only while
 * no list is known: it is what the user typed to create the project, and it is
 * honest — the "All projects" that used to fill that gap is what hid the stale
 * filter from the reporter.
 */
export function activeProjectLabel(
	activeProjectId: string,
	projects: readonly { id: string; name: string }[],
	projectsKnown: boolean,
): string {
	const applied = resolveActiveProject(activeProjectId, projects, projectsKnown);
	if (applied === ALL_PROJECTS) return "All projects";
	return projects.find((p) => p.id === applied)?.name ?? applied;
}

/**
 * The board's empty state when the filter, not the fleet, is why nothing is
 * listed: the daemon returned sessions and every one is in another project.
 * Null otherwise — the filter is All, a session is visible, or nothing is
 * hidden — so the caller falls through to the ordinary empty state without
 * repeating the test.
 *
 * Names the filter and counts what "Show all projects" would put on the
 * board. Live and archived are counted apart, by the board's own isArchived,
 * because the archive is collapsed there: "0 agents are in other projects"
 * beside that button would argue against it, so archived-only gets its own
 * sentence.
 */
export function filteredEmptyCopy(
	activeProjectId: string,
	projects: readonly { id: string; name: string }[],
	projectsKnown: boolean,
	sessions: readonly DashboardSession[],
): { title: string; message: string } | null {
	const applied = resolveActiveProject(activeProjectId, projects, projectsKnown);
	if (applied === ALL_PROJECTS) return null;
	let live = 0;
	let archived = 0;
	for (const s of sessions) {
		if (s.projectId === applied) return null;
		if (isArchived(s)) archived += 1;
		else live += 1;
	}
	if (live + archived === 0) return null;
	const rest =
		live > 0
			? `${live === 1 ? "1 agent is" : `${live} agents are`} in other projects.`
			: "Other projects have only archived sessions.";
	return { title: "No agents in this project", message: `Filtered to ${activeProjectLabel(applied, projects, projectsKnown)}. ${rest}` };
}
