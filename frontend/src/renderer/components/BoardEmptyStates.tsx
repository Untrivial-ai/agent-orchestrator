import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useShell } from "../lib/shell-context";
import { CreateProjectFlow } from "./CreateProjectFlow";
import { GitHubOnboardingNotice } from "./GitHubOnboardingNotice";
import { TopbarButton } from "./TopbarButton";
import { WelcomePanel } from "./WelcomePanel";
import { OrchestratorIcon } from "./icons";

// Board empty states: first-launch welcome (`BoardWelcome`) and project board
// with no worker sessions yet (`ProjectBoardEmpty`).
export function BoardWelcome() {
	const { cloneProject, createProject, initializeProjectRepository } = useShell();
	return (
		<WelcomePanel>
			<div
				className="flex h-full min-h-0 items-center justify-center overflow-y-auto px-6 py-8"
				data-testid="board-welcome"
			>
				<div className="flex w-full flex-col items-center gap-3">
					<CreateProjectFlow
						embedded
						mode="choose"
						onCloneProject={cloneProject}
						onCreateProject={createProject}
						onInitializeProject={initializeProjectRepository}
					/>
					<GitHubOnboardingNotice />
				</div>
			</div>
		</WelcomePanel>
	);
}

// Project board with a registered project but no worker sessions yet: a quiet
// invitation instead of four empty columns. Actions mirror the board header
// (Orchestrator stays the primary, like the topbar) so the vocabulary holds.
export function ProjectBoardEmpty({
	hasOrchestrator,
	isProjectRestarting,
	isProvisioning = false,
	isSpawning,
	onNewTask,
	onOpenOrchestrator,
	onOpenOrchestratorAsTui,
	spawnError,
}: {
	hasOrchestrator: boolean;
	isProjectRestarting: boolean;
	isProvisioning?: boolean;
	isSpawning: boolean;
	onNewTask: () => void;
	onOpenOrchestrator: () => void;
	onOpenOrchestratorAsTui?: () => void;
	spawnError?: string | null;
}) {
	const { t } = useTranslation();
	const orchestratorLabel = hasOrchestrator ? t("shell.orchestrator") : t("shell.spawnOrchestrator");
	const busyLabel = isProjectRestarting
		? t("shell.restartingDots")
		: isProvisioning
			? t("shell.provisioningDots", { defaultValue: "Setting up…" })
			: isSpawning
				? t("shell.spawningDots")
				: orchestratorLabel;

	return (
		<div className="flex h-full min-h-0 items-center justify-center overflow-y-auto">
			<div className="flex w-full max-w-preview-content flex-col items-center pb-empty-offset-y text-center">
				<h2 className="text-subtitle font-semibold tracking-tight text-foreground">{t("board.empty.title")}</h2>
				<p className="mt-2 text-md-sm leading-relaxed text-muted-foreground">{t("board.empty.body")}</p>
				<div className="mt-5 flex items-center gap-2">
					<TopbarButton
						aria-label={orchestratorLabel}
						disabled={isSpawning || isProjectRestarting || isProvisioning}
						onClick={onOpenOrchestrator}
						variant="primary"
					>
						<OrchestratorIcon className="size-icon-md" aria-hidden="true" />
						{busyLabel}
					</TopbarButton>
					<TopbarButton aria-label={t("shell.newTask")} disabled={isProjectRestarting || isProvisioning} onClick={onNewTask} variant="accent">
						<Plus className="size-icon-md" aria-hidden="true" />
						{t("shell.newTask")}
					</TopbarButton>
				</div>
				{spawnError && (
					<div className="mt-3 flex flex-col items-center gap-2">
						<p className="text-caption leading-body text-error" role="status">
							{spawnError}
						</p>
						{onOpenOrchestratorAsTui ? (
							<TopbarButton disabled={isSpawning || isProjectRestarting || isProvisioning} onClick={onOpenOrchestratorAsTui}>
								{t("newTask.createAsTui")}
							</TopbarButton>
						) : null}
					</div>
				)}
			</div>
		</div>
	);
}
