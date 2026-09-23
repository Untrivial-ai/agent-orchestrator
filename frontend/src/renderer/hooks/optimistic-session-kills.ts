import { toKanbanColumn, type WorkspaceSession, type WorkspaceSummary } from "../types/workspace";

/** Session IDs with an in-flight optimistic kill — survives workspace refetch/CDC. */
const optimisticKillIds = new Set<string>();

function markTerminated(sessionId: string) {
	return (session: WorkspaceSession): WorkspaceSession =>
		session.id === sessionId
			? {
				...session,
				isTerminated: true,
				status: "terminated",
				kanbanColumn: toKanbanColumn(undefined, "terminated"),
			}
			: session;
}

export function trackOptimisticSessionKill(sessionId: string): void {
	optimisticKillIds.add(sessionId);
}

export function clearOptimisticSessionKill(sessionId: string): void {
	optimisticKillIds.delete(sessionId);
}

export function applyTerminatedSession(
	workspaces: WorkspaceSummary[] | undefined,
	sessionId: string,
): WorkspaceSummary[] | undefined {
	return workspaces?.map((workspace) => ({
		...workspace,
		sessions: workspace.sessions.map(markTerminated(sessionId)),
	}));
}

/** Re-apply pending kills so a mid-flight workspace refetch cannot resurrect rows. */
export function applyOptimisticSessionKills(
	workspaces: WorkspaceSummary[] | undefined,
): WorkspaceSummary[] | undefined {
	if (!workspaces || optimisticKillIds.size === 0) return workspaces;
	let changed = false;
	const next = workspaces.map((workspace) => {
		let sessionsChanged = false;
		const sessions = workspace.sessions.map((session) => {
			if (!optimisticKillIds.has(session.id) || session.isTerminated === true) return session;
			sessionsChanged = true;
			return markTerminated(session.id)(session);
		});
		if (!sessionsChanged) return workspace;
		changed = true;
		return { ...workspace, sessions };
	});
	return changed ? next : workspaces;
}
