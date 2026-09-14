import type { DashboardSession, ProjectInfo } from "./api";
import { isArchived } from "./agentsView";
import { collectPRs, prLifecycle } from "./prView";
import { attentionOf } from "./sessionStatus";

export type ProjectSummary = {
	project: ProjectInfo;
	activeWorkers: number;
	needsAttention: number;
	openPRs: number;
	failingPRs: number;
	lastActivityAt: string | null;
};

export function projectWorkers(projectId: string, sessions: readonly DashboardSession[]): DashboardSession[] {
	return sessions
		.filter((session) => session.projectId === projectId && !isArchived(session))
		.sort((a, b) => {
			const pinned = Number(Boolean(b.isPinned)) - Number(Boolean(a.isPinned));
			return pinned || b.lastActivityAt.localeCompare(a.lastActivityAt);
		});
}

export function projectSummaries(
	projects: readonly ProjectInfo[],
	sessions: readonly DashboardSession[],
): ProjectSummary[] {
	return projects
		.map((project, index) => {
			const all = sessions.filter((session) => session.projectId === project.id);
			const active = projectWorkers(project.id, all);
			const prs = collectPRs(all);
			const open = prs.filter(({ pr }) => {
				const lifecycle = prLifecycle(pr);
				return lifecycle === "open" || lifecycle === "draft";
			});
			return {
				project,
				activeWorkers: active.length,
				needsAttention: active.filter((session) =>
					["respond", "action", "review"].includes(attentionOf(session)),
				).length,
				openPRs: open.length,
				failingPRs: open.filter(({ pr }) => pr.ciStatus === "failing").length,
				lastActivityAt: all.reduce<string | null>(
					(latest, session) => (!latest || session.lastActivityAt > latest ? session.lastActivityAt : latest),
					null,
				),
				index,
			};
		})
		.sort((a, b) => {
			const active = Number(b.activeWorkers > 0) - Number(a.activeWorkers > 0);
			if (active) return active;
			const activity = (b.lastActivityAt ?? "").localeCompare(a.lastActivityAt ?? "");
			return activity || a.index - b.index;
		})
		.map(({ index: _index, ...summary }) => summary);
}
