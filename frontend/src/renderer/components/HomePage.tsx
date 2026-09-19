import type { ProjectSource } from "@aoagents/product-ui";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Bot, Folder, Folders, FolderOpen, GitFork, Star } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useSystemRequirementsGate } from "../hooks/useSystemRequirementsGate";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { aoBridge } from "../lib/bridge";
import { getProjectLastOpenedAt } from "../lib/project-history";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { useShell } from "../lib/shell-context";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store";
import {
	STANDALONE_PROJECT_KIND,
	STANDALONE_WORKSPACE_ID,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "../types/workspace";
import { BoardWelcome } from "./BoardEmptyStates";
import { CreateProjectFlow } from "./CreateProjectFlow";
import { DaemonStartupLoader } from "./DaemonStartupLoader";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "./NavRowHighlight";
import { Badge } from "./ui/badge";

/**
 * Home landing layout contracts (do not regress without explicit design sign-off):
 * - One centered column (`max-w-[640px]`); no upward translate hack.
 * - "Star us" is a quiet text link with dashed underline on hover — NOT a
 *   TopbarButton / accent pill / bordered card.
 * - Primary actions are a 2×2 grid; standalone agent lives IN the grid (not a
 *   full-width accent CTA above). Connect Mobile is settings-only — not here.
 * - Recent rows use shared {@link NavRowHighlight} (same as sidebar), not a
 *   flat `hover:bg-interactive-hover` wash.
 * - Section titles share {@link HOME_SECTION_TITLE_CLASS}; keep Jump back /
 *   Recent projects visually paired.
 */
const GITHUB_REPOSITORY_URL = "https://github.com/Untrivial-ai/agent-orchestrator";
const RECENT_PROJECT_LIMIT = 3;
const HOME_BUTTON_CLASS =
	"flex w-full items-center gap-3 rounded-lg bg-[var(--color-bg-import-card)] px-4 py-3 text-left transition-[scale] duration-fast ease-out hover:bg-interactive-hover hover:text-foreground active:scale-[0.96] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 motion-reduce:transform-none";
const HOME_ICON_SLOT_CLASS =
	"grid size-8 shrink-0 place-items-center text-muted-foreground [&_svg]:size-4";
const HOME_PROJECT_ICON_CLASS =
	"grid size-8 shrink-0 place-items-center rounded-lg text-muted-foreground [&_svg]:size-4";
const HOME_SECTION_TITLE_CLASS =
	"text-base font-medium tracking-tight text-foreground";

function latestProjectTimestamp(project: WorkspaceSummary): string {
	return [
		getProjectLastOpenedAt(project.id),
		...project.sessions.flatMap((session) => [session.lastUserMessageAt, session.createdAt]),
	]
		.filter((timestamp): timestamp is string => Boolean(timestamp))
		.sort()
		.at(-1) ?? "";
}

function relativeProjectTime(timestamp: string | undefined, emptyLabel: string, justNowLabel: string): string {
	if (!timestamp) return emptyLabel;
	const elapsedMinutes = Math.floor((Date.now() - new Date(timestamp).getTime()) / 60_000);
	if (!Number.isFinite(elapsedMinutes) || elapsedMinutes < 1) return justNowLabel;
	const unit: Intl.RelativeTimeFormatUnit = elapsedMinutes < 60 ? "minute" : elapsedMinutes < 1_440 ? "hour" : "day";
	const amount = unit === "minute" ? elapsedMinutes : unit === "hour" ? Math.floor(elapsedMinutes / 60) : Math.floor(elapsedMinutes / 1_440);
	return new Intl.RelativeTimeFormat(undefined, { numeric: "always" }).format(-amount, unit);
}

function sortProjectsByActivity(projects: WorkspaceSummary[]): WorkspaceSummary[] {
	return projects
		.slice()
		.sort((left, right) => latestProjectTimestamp(right).localeCompare(latestProjectTimestamp(left)));
}

function standaloneSessionTimestamp(session: WorkspaceSession): number {
	for (const value of [session.lastUserMessageAt, session.updatedAt, session.createdAt]) {
		const parsed = value ? Date.parse(value) : Number.NaN;
		if (!Number.isNaN(parsed)) return parsed;
	}
	return 0;
}

function mostRecentStandaloneSession(sessions: WorkspaceSession[]): WorkspaceSession | undefined {
	const candidates = sessions.filter((session) => session.isTerminated !== true && session.status !== "terminated");
	return (candidates.length > 0 ? candidates : sessions).reduce<WorkspaceSession | undefined>((latest, session) => {
		if (!latest) return session;
		const sessionTime = standaloneSessionTimestamp(session);
		const latestTime = standaloneSessionTimestamp(latest);
		if (sessionTime !== latestTime) return sessionTime > latestTime ? session : latest;
		return session.id > latest.id ? session : latest;
	}, undefined);
}

function ProjectRow({ project, onClick, emptyTimeLabel, justNowLabel }: { project: WorkspaceSummary; onClick: () => void; emptyTimeLabel: string; justNowLabel: string }) {
	const { t } = useTranslation();
	const lastOpenedAt = getProjectLastOpenedAt(project.id);
	const latestProjectFact = latestProjectTimestamp(project) || lastOpenedAt;

	return (
		<button
			// Host must use NAV_ROW_HIGHLIGHT_HOST_CLASS — pill owns the fill.
			className={cn(
				"flex w-full items-center gap-3 rounded-lg px-4 py-3 text-left text-muted-foreground",
				"focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
				NAV_ROW_HIGHLIGHT_HOST_CLASS,
			)}
			onClick={onClick}
			type="button"
		>
			<NavRowHighlight />
			<span className={cn(HOME_PROJECT_ICON_CLASS, "relative z-[1]")} aria-hidden="true">
				{project.folderMissing ? <AlertTriangle strokeWidth={1.8} className="text-warning" /> : <Folder strokeWidth={1.8} />}
			</span>
			<span className="relative z-[1] min-w-0 text-sm leading-5">
				<span className="flex items-center gap-1.5">
					<span className="block truncate font-medium text-foreground">{project.name}</span>
					{project.folderMissing ? (
						<Badge variant="warning" className="h-4 shrink-0 px-1.5 text-2xs">{t("home.folderMissing")}</Badge>
					) : null}
				</span>
				<span className="block truncate text-caption text-muted-foreground">{project.path}</span>
			</span>
			<span className="relative z-[1] ml-auto shrink-0 text-right text-caption tabular-nums text-muted-foreground">
				{relativeProjectTime(latestProjectFact, emptyTimeLabel, justNowLabel)}
			</span>
		</button>
	);
}

function HomeActionCard({
	disabled,
	icon,
	label,
	onClick,
}: {
	disabled?: boolean;
	icon: ReactNode;
	label: string;
	onClick?: () => void;
}) {
	return (
		<button
			className={`${HOME_BUTTON_CLASS} disabled:pointer-events-none disabled:opacity-50`}
			disabled={disabled}
			onClick={onClick}
			type="button"
		>
			<span className={HOME_ICON_SLOT_CLASS} aria-hidden="true">
				{icon}
			</span>
			<span className="min-w-0 text-sm font-medium leading-5 text-foreground">{label}</span>
		</button>
	);
}

export function HomePage() {
	const navigate = useNavigate();
	const { t } = useTranslation();
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const { cloneProject, createProject, daemonStatus, initializeProjectRepository, workspaceStartupState } =
		useShell();
	const { blocked: requirementsBlocked } = useSystemRequirementsGate();
	const workspaceQuery = useWorkspaceQuery();
	const [sourceSignal, setSourceSignal] = useState<{ source: ProjectSource; nonce: number } | null>(null);
	const projects = workspaceQuery.data ?? [];
	const recentProjects = useMemo(() => sortProjectsByActivity(projects).slice(0, RECENT_PROJECT_LIMIT), [projects]);

	const isDaemonReady = usesPreviewWorkspaceData || daemonStatus.state === "ready";
	const daemonHasFailed = Boolean(daemonStatus.code);
	const showStartup =
		!daemonHasFailed &&
		(!isDaemonReady ||
			workspaceStartupState === "loading" ||
			(!workspaceQuery.isSuccess && !workspaceQuery.isError) ||
			requirementsBlocked);

	if (showStartup) return <DaemonStartupLoader />;

	const requestSource = (source: ProjectSource) => {
		setSourceSignal({ source, nonce: Date.now() });
	};

	const openProject = (projectId: string) => {
		void navigate({ to: "/projects/$projectId", params: { projectId } });
	};
	const openExistingProject = (path: string) => {
		const project = projects.find((candidate) => candidate.path === path);
		if (project) void navigate({ to: "/projects/$projectId", params: { projectId: project.id } });
	};

	if (workspaceStartupState === "error" || workspaceQuery.isError) {
		return (
			<div className="flex min-h-full items-center justify-center px-6 py-16">
				<p className="text-center text-xs text-passive">{t("shell.couldNotLoadProjects")}</p>
			</div>
		);
	}

	if (projects.length === 0) return <BoardWelcome />;

	return (
		<div className="flex min-h-full items-center justify-center px-6 py-16">
			<div className="w-full max-w-[640px]">
				<div className="space-y-6">
					<section className="space-y-3 px-3">
						<div className="flex items-baseline justify-between gap-4">
							<h1 className={HOME_SECTION_TITLE_CLASS}>{t("home.jumpBack")}</h1>
							{/* Quiet text link — not TopbarButton / accent. Dashed underline only on hover. */}
							<button
								className="inline-flex shrink-0 items-center gap-1.5 border-b border-dashed border-transparent pb-px text-sm text-muted-foreground hover:border-current hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
								onClick={() => void aoBridge.app.openExternal(GITHUB_REPOSITORY_URL)}
								type="button"
							>
								<Star className="size-3.5" strokeWidth={1.8} aria-hidden="true" />
								{t("home.starUs")}
							</button>
						</div>

						{/* 2×2 action grid; standalone agent is a cell here, not a hero CTA above. */}
						<div className="grid grid-cols-2 gap-3">
							<HomeActionCard
								icon={<GitFork strokeWidth={1.8} />}
								label={t("createProject.cloneFromGit")}
								onClick={() => requestSource("clone")}
							/>
							<HomeActionCard
								icon={<FolderOpen strokeWidth={1.8} />}
								label={t("createProject.openLocal")}
								onClick={() => requestSource("local")}
							/>
							<HomeActionCard
								icon={<Folders strokeWidth={1.8} />}
								label={t("createProject.addWorkspace")}
								onClick={() => requestSource("workspace")}
							/>
							<HomeActionCard
								icon={<Bot strokeWidth={1.8} />}
								label={t("home.newStandaloneAgent")}
								onClick={() => requestNewTask(STANDALONE_WORKSPACE_ID)}
							/>
						</div>
					</section>

					<section className="space-y-3 px-3">
						<h2 className={HOME_SECTION_TITLE_CLASS}>{t("home.recentProjects")}</h2>
						<div>
							{recentProjects.map((project) => (
								<ProjectRow
									key={project.id}
									project={project}
									onClick={() => {
										if (project.kind === STANDALONE_PROJECT_KIND) {
											const session = mostRecentStandaloneSession(project.sessions);
											session
												? void navigate({ to: "/sessions/$sessionId", params: { sessionId: session.id } })
												: requestNewTask(STANDALONE_WORKSPACE_ID);
											return;
										}
										openProject(project.id);
									}}
									emptyTimeLabel={t("home.never")}
									justNowLabel={t("time.justNow")}
								/>
							))}
						</div>
					</section>
				</div>

				<CreateProjectFlow
					existingProjectNames={projects.map((project) => project.name)}
					existingProjectPaths={projects.map((project) => project.path)}
					mode="choose"
					onCloneProject={cloneProject}
					onCreateProject={createProject}
					onCreateStandaloneAgent={() => requestNewTask(STANDALONE_WORKSPACE_ID)}
					onInitializeProject={initializeProjectRepository}
					onOpenExistingProject={openExistingProject}
					sourceSignal={sourceSignal}
				/>
			</div>
		</div>
	);
}
