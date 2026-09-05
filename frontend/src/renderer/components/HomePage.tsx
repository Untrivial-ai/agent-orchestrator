import type { ProjectSource } from "@aoagents/product-ui";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Folder, Folders, FolderOpen, GitFork, Smartphone, Star } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useSystemRequirementsGate } from "../hooks/useSystemRequirementsGate";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { aoBridge } from "../lib/bridge";
import { getProjectLastOpenedAt } from "../lib/project-history";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { useShell } from "../lib/shell-context";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSummary } from "../types/workspace";
import { CreateProjectFlow } from "./CreateProjectFlow";
import { DaemonStartupLoader } from "./DaemonStartupLoader";
import { TopbarButton } from "./TopbarButton";
import { Badge } from "./ui/badge";

const GITHUB_REPOSITORY_URL = "https://github.com/Untrivial-ai/agent-orchestrator";
const RECENT_PROJECT_LIMIT = 3;
const HOME_BUTTON_CLASS =
	"flex w-full items-center gap-3 rounded-welcome-panel bg-[var(--color-bg-import-card)] px-4 py-3 text-left hover:bg-interactive-hover hover:text-foreground active:bg-interactive-hover active:scale-[0.99] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60";
const HOME_ICON_SLOT_CLASS =
	"grid size-8 shrink-0 place-items-center text-[var(--color-text-import-muted)] [&_svg]:size-4";
const HOME_PROJECT_ICON_CLASS =
	"grid size-8 shrink-0 place-items-center rounded-lg text-foreground/65 [&_svg]:size-4";

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

function ProjectRow({ project, onClick, emptyTimeLabel, justNowLabel }: { project: WorkspaceSummary; onClick: () => void; emptyTimeLabel: string; justNowLabel: string }) {
	const { t } = useTranslation();
	const lastOpenedAt = getProjectLastOpenedAt(project.id);
	const latestProjectFact = latestProjectTimestamp(project) || lastOpenedAt;

	return (
		<button
			className="group flex w-full items-center gap-3 rounded-welcome-panel px-4 py-3 text-left text-foreground/75 hover:bg-interactive-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
			onClick={onClick}
			type="button"
		>
			<span className={HOME_PROJECT_ICON_CLASS} aria-hidden="true">
				{project.folderMissing ? <AlertTriangle strokeWidth={1.8} className="text-warning" /> : <Folder strokeWidth={1.8} />}
			</span>
			<span className="min-w-0 text-[14px] leading-5">
				<span className="flex items-center gap-1.5">
					<span className="block truncate font-medium text-[var(--color-text-import-title)]">{project.name}</span>
					{project.folderMissing ? (
						<Badge variant="warning" className="h-4 shrink-0 px-1.5 text-2xs">{t("home.folderMissing")}</Badge>
					) : null}
				</span>
				<span className="block truncate text-[13px] text-muted-foreground">{project.path}</span>
			</span>
			<span className="ml-auto shrink-0 text-right text-[13px] text-muted-foreground">
				{relativeProjectTime(latestProjectFact, emptyTimeLabel, justNowLabel)}
			</span>
		</button>
	);
}

function HomeActionCard({
	ariaLabel,
	disabled,
	icon,
	label,
	onClick,
}: {
	ariaLabel: string;
	disabled?: boolean;
	icon: ReactNode;
	label: string;
	onClick?: () => void;
}) {
	return (
		<button
			aria-label={ariaLabel}
			className={`${HOME_BUTTON_CLASS} disabled:pointer-events-none disabled:opacity-50`}
			disabled={disabled}
			onClick={onClick}
			type="button"
		>
			<span className={HOME_ICON_SLOT_CLASS}>
				{icon}
			</span>
			<span className="min-w-0 text-[14px] font-medium leading-5 text-[var(--color-text-import-title)]">{label}</span>
		</button>
	);
}

export function HomePage() {
	const navigate = useNavigate();
	const { t } = useTranslation();
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
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

	if (workspaceStartupState === "error" || workspaceQuery.isError) {
		return (
			<div className="flex min-h-full items-center justify-center px-6 py-16">
				<p className="text-center text-xs text-passive">{t("shell.couldNotLoadProjects")}</p>
			</div>
		);
	}

	return (
		<div className="flex min-h-full items-center justify-center px-6 py-16">
			<div className="w-full max-w-[640px] -translate-y-3">
				<div className="space-y-6">
					<div className="flex items-center justify-between gap-4 px-3">
						<h1 className="text-[17px] font-medium tracking-[-0.01em] text-foreground/80">{t("home.jumpBack")}</h1>
						<TopbarButton
							className="!transition-none !border-0 shrink-0 font-mono text-[15px] tracking-[0.03em] hover:bg-interactive-hover hover:text-foreground active:bg-interactive-hover active:scale-[0.96]"
							onClick={() => void aoBridge.app.openExternal(`${GITHUB_REPOSITORY_URL}/stargazers`)}
							variant="accent"
						>
							<Star className="size-4" strokeWidth={1.8} aria-hidden="true" />
							{t("home.starUs")}
						</TopbarButton>
					</div>

					<div className="-mt-3 grid grid-cols-2 gap-3 px-3">
						<HomeActionCard
							ariaLabel={t("createProject.cloneFromGit")}
							icon={<GitFork className="size-4" aria-hidden="true" />}
							label={t("createProject.cloneFromGit")}
							onClick={() => requestSource("clone")}
						/>
						<HomeActionCard
							ariaLabel={t("createProject.openLocal")}
							icon={<FolderOpen className="size-4" aria-hidden="true" />}
							label={t("createProject.openLocal")}
							onClick={() => requestSource("local")}
						/>
						<HomeActionCard
							ariaLabel={t("createProject.addWorkspace")}
							icon={<Folders className="size-4" aria-hidden="true" />}
							label={t("createProject.addWorkspace")}
							onClick={() => requestSource("workspace")}
						/>
						<HomeActionCard
							ariaLabel={t("settings.connectMobile")}
							icon={<Smartphone aria-hidden="true" />}
							label={t("settings.connectMobile")}
							onClick={() => openGlobalSettings("mobile")}
						/>
					</div>

					<div className="-mt-3 space-y-1 px-3">
						<h2 className="text-[17px] font-medium tracking-[-0.01em] text-foreground/80">{t("home.recentProjects")}</h2>
						{recentProjects.map((project) => (
							<ProjectRow
								key={project.id}
								project={project}
								onClick={() => openProject(project.id)}
								emptyTimeLabel={t("home.never")}
								justNowLabel={t("time.justNow")}
							/>
						))}
					</div>
				</div>

				<CreateProjectFlow
					mode="choose"
					onCloneProject={cloneProject}
					onCreateProject={createProject}
					onInitializeProject={initializeProjectRepository}
					sourceSignal={sourceSignal}
				/>
			</div>
		</div>
	);
}
