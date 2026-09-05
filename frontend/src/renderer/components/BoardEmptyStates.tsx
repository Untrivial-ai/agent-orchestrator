import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { TopbarButton } from "./TopbarButton";
import { OrchestratorIcon } from "./icons";

// Project board with a registered project but no worker sessions: a quiet
// invitation instead of four empty columns. Actions mirror the board header
// (Orchestrator stays the primary, like the topbar) so the vocabulary holds.
export function ProjectBoardEmpty({
	hasOrchestrator,
	isProjectRestarting,
	isSpawning,
	onNewTask,
	onOpenOrchestrator,
	onOpenOrchestratorAsTui,
	spawnError,
}: {
	hasOrchestrator: boolean;
	isProjectRestarting: boolean;
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
						disabled={isSpawning || isProjectRestarting}
						onClick={onOpenOrchestrator}
						variant="primary"
					>
						<OrchestratorIcon className="size-icon-md" aria-hidden="true" />
						{busyLabel}
					</TopbarButton>
					<TopbarButton aria-label={t("shell.newTask")} disabled={isProjectRestarting} onClick={onNewTask} variant="accent">
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
							<TopbarButton disabled={isSpawning || isProjectRestarting} onClick={onOpenOrchestratorAsTui}>
								{t("newTask.createAsTui")}
							</TopbarButton>
						) : null}
					</div>
				)}
			</div>
		</div>
	);
}
