import type { DashboardSession } from "./api";
import { sessionTitle } from "./sessionStatus";

export function workerSearchClearState(query: string) {
	const visible = query.length > 0;
	return {
		disabled: !visible,
		opacity: visible ? 1 : 0,
		scale: visible ? 1 : 0.82,
	};
}

export function filterWorkerSessions(
	sessions: readonly DashboardSession[],
	query: string,
	projectNameFor: (projectId: string) => string,
	statusLabelFor: (status: string | null) => string,
): DashboardSession[] {
	const needle = query.trim().toLocaleLowerCase();
	if (!needle) return [...sessions];

	return sessions.filter((session) => {
		const fields = [
			sessionTitle(session),
			projectNameFor(session.projectId),
			session.branch,
			statusLabelFor(session.status),
		];
		return fields.some((field) => field?.toLocaleLowerCase().includes(needle));
	});
}
