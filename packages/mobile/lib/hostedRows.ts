import type { DashboardSession, OrchestratorLink, ProjectInfo } from "./api";
import { orchestratorProjectSections, type OrchestratorProjectRow, type OrchestratorProjectSection } from "./orchestratorView";

export type HostedSession = DashboardSession & { hostId: string; hostName: string };
export type HostedProject = ProjectInfo & { hostId: string; hostName: string };
export type HostedOrchestrator = OrchestratorLink & { hostId: string; hostName: string };
export type HostedProjectRow = Omit<OrchestratorProjectRow, "project"> & { project: HostedProject };

export function hostedRowKey(hostId: string, id: string): string {
	return JSON.stringify([hostId, id]);
}

export function hostedProjectKey(project: { id: string; hostId?: string }): string {
	return project.hostId ? hostedRowKey(project.hostId, project.id) : project.id;
}

export function sessionHostId(session: DashboardSession): string | undefined {
	return "hostId" in session && typeof session.hostId === "string" ? session.hostId : undefined;
}

export function hostedSessionKey(session: DashboardSession): string {
	const hostId = sessionHostId(session);
	return hostId ? hostedRowKey(hostId, session.id) : `${session.projectId}:${session.id}`;
}

/** Build each host's project rows separately so matching project IDs never share workers or an orchestrator. */
export function hostedProjectSections(hosts: readonly {
	hostId: string;
	name: string;
	connection?: "closed" | "connecting" | "open";
	projects: readonly ProjectInfo[];
	sessions: readonly DashboardSession[];
	orchestrators: readonly OrchestratorLink[];
}[]): Array<Omit<OrchestratorProjectSection, "data"> & { data: HostedProjectRow[] }> {
	const sections = new Map<OrchestratorProjectSection["key"], Omit<OrchestratorProjectSection, "data"> & { data: HostedProjectRow[] }>();
	for (const host of hosts) {
		for (const section of orchestratorProjectSections(host.projects, host.sessions, host.orchestrators)) {
			const current = sections.get(section.key) ?? { key: section.key, title: section.title, data: [] };
			current.data.push(...section.data.map((row) => ({
				...row,
				project: {
					...row.project,
					hostId: host.hostId,
					hostName: `${host.name}${host.connection === "closed" ? " (offline)" : ""}`,
				},
			})));
			sections.set(section.key, current);
		}
	}
	return ["attention", "coordinating", "not-running"].flatMap((key) => {
		const section = sections.get(key as OrchestratorProjectSection["key"]);
		return section ? [section] : [];
	});
}
