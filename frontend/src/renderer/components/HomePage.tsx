import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Folder, Star } from "lucide-react";
import { aoBridge } from "../lib/bridge";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import type { WorkspaceSummary } from "../types/workspace";
import { BoardWelcome } from "./BoardEmptyStates";
import { TopbarButton } from "./TopbarButton";

const GITHUB_REPOSITORY_URL = "https://github.com/Untrivial-ai/agent-orchestrator";

function latestProjectTimestamp(project: WorkspaceSummary): string {
	return project.sessions.reduce((latest, session) => (session.updatedAt > latest ? session.updatedAt : latest), "");
}

function ProjectRow({ project, onClick }: { project: WorkspaceSummary; onClick: () => void }) {
	return (
		<button
			className="group flex w-full items-center gap-3 rounded-md p-3 text-left text-foreground/75 hover:bg-interactive-hover hover:text-foreground"
			onClick={onClick}
			type="button"
		>
			<span className="grid size-6 shrink-0 place-items-center text-foreground/65 group-hover:text-foreground" aria-hidden="true">
				<Folder className="size-5" strokeWidth={1.8} />
			</span>
			<span className="min-w-0 text-[16px] leading-tight tracking-[-0.01em]">
				<span className="block truncate text-foreground">{project.name}</span>
				<span className="mt-1 block truncate text-sm text-muted-foreground">{project.path}</span>
			</span>
			<span className="ml-auto shrink-0 self-center text-sm text-muted-foreground">
				{project.sessions.length} {project.sessions.length === 1 ? "session" : "sessions"}
			</span>
		</button>
	);
}

export function HomePage() {
	const navigate = useNavigate();
	const { t } = useTranslation();
	const workspaceQuery = useWorkspaceQuery();
	const projects = (workspaceQuery.data ?? [])
		.slice()
		.sort((left, right) => latestProjectTimestamp(right).localeCompare(latestProjectTimestamp(left)))
		.slice(0, 3);

	if (workspaceQuery.isSuccess && projects.length === 0) return <BoardWelcome />;

	return (
		<div className="flex min-h-full items-center justify-center px-6 py-16">
			<div className="w-full max-w-[530px] -translate-y-3">
				<div className="space-y-3">
					<div className="flex items-center justify-between gap-4 px-3">
						<h1 className="text-[17px] font-medium tracking-[-0.01em] text-foreground/80">{t("home.jumpBack")}</h1>
						<TopbarButton
							className="shrink-0 font-mono text-[15px] tracking-[0.03em] transition-[transform,filter,background,color,border-color] duration-150 ease-out active:scale-[0.96] motion-reduce:transform-none"
							onClick={() => void aoBridge.app.openExternal(GITHUB_REPOSITORY_URL)}
							variant="accent"
						>
							<Star className="size-4" strokeWidth={1.8} aria-hidden="true" />
							{t("home.starUs")}
						</TopbarButton>
					</div>
					<div className="space-y-1">
						{projects.map((project) => (
							<ProjectRow
								key={project.id}
								project={project}
								onClick={() => void navigate({ to: "/projects/$projectId", params: { projectId: project.id } })}
							/>
						))}
					</div>
				</div>
			</div>
		</div>
	);
}
