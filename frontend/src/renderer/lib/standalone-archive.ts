import {
	STANDALONE_WORKSPACE_ID,
	workerSessions,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "../types/workspace";

function isArchivedSession(session: WorkspaceSession): boolean {
	return (
		session.kanbanColumn === "archive" ||
		session.isTerminated === true ||
		session.status === "terminated"
	);
}

export function archivedStandaloneSessions(
	workspaces: readonly WorkspaceSummary[],
): WorkspaceSession[] {
	const standalone = workspaces.find((workspace) => workspace.id === STANDALONE_WORKSPACE_ID);
	return workerSessions(standalone?.sessions ?? [])
		.filter(isArchivedSession)
		.sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
}
