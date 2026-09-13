import { type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import type { ProjectOrchestratorAction } from "../hooks/useProjectOrchestratorAction";
import { getAgentActivityView } from "../lib/session-presentation";
import { TopbarActionError, TopbarButton } from "./TopbarButton";
import { OrchestratorActivityIndicator } from "./OrchestratorActivityIndicator";
import { OrchestratorIcon } from "./icons";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

export function ProjectBoardActions({ actions, placement, quiet = false, style }: {
	actions: ProjectOrchestratorAction;
	placement: "header" | "empty";
	quiet?: boolean;
	style?: CSSProperties;
}) {
	const { t } = useTranslation();
	const { orchestrator, isSpawning, isProjectRestarting, isProvisioning, spawnError, canCreateAsTui,
		openNewTask, openOrchestrator } = actions;
	const header = placement === "header";
	const busy = isSpawning || isProjectRestarting || isProvisioning;
	const activity = orchestrator ? getAgentActivityView(orchestrator.activity, t).label : undefined;
	const actionLabel = orchestrator ? t("shell.openOrchestrator") : t("shell.spawnOrchestrator");
	const busyLabel = isProjectRestarting ? t("shell.restartingDots") : isProvisioning
		? t("shell.provisioningDots", { defaultValue: "Setting up…" }) : isSpawning ? t("shell.spawningDots") : undefined;
	const orchestratorButton = (
		<Tooltip>
			<TooltipTrigger asChild>
				<span className="inline-flex" style={style}>
					<TopbarButton
						aria-label={activity ? t("shell.orchestratorWithActivity", { activity }) : actionLabel}
						aria-busy={busy}
						className={header ? "topbar-control--labeled" : undefined}
						data-priority={header ? "secondary" : undefined}
						disabled={busy}
						onClick={() => openOrchestrator()}
						variant={quiet ? "secondary" : "primary"}
					>
						<OrchestratorIcon className="size-icon-md" aria-hidden="true" />
						<span data-compact-label={header ? "" : undefined}>{busyLabel ?? t("shell.orchestrator")}</span>
						{orchestrator ? <OrchestratorActivityIndicator session={orchestrator} /> : null}
					</TopbarButton>
				</span>
			</TooltipTrigger>
			<TooltipContent side="bottom">{busyLabel ?? actionLabel}</TooltipContent>
		</Tooltip>
	);
	const newTaskButton = (
		<Tooltip>
			<TooltipTrigger asChild>
				<span className="inline-flex" style={style}>
					<TopbarButton
						aria-label={t("shell.newTask")}
						className={header ? "topbar-control--labeled" : undefined}
						data-priority={header ? "primary" : undefined}
						disabled={isProjectRestarting || isProvisioning}
						onClick={openNewTask}
						variant={quiet ? "secondary" : "accent"}
					>
						<Plus className="size-icon-md" aria-hidden="true" />
						<span data-compact-label={header ? "" : undefined}>{t(header ? "newTask.task" : "shell.newTask")}</span>
					</TopbarButton>
				</span>
			</TooltipTrigger>
			<TooltipContent side="bottom">{t("shell.newTask")}</TooltipContent>
		</Tooltip>
	);
	const feedback = spawnError && !quiet ? (
		<div className={header ? "contents" : "mt-3 flex flex-col items-center gap-2"}>
			<TopbarActionError role={header ? "alert" : "status"} className={header ? "max-w-content-max truncate" : "text-caption leading-body"} title={spawnError}>
				{spawnError}
			</TopbarActionError>
			{canCreateAsTui ? <TopbarButton disabled={busy} onClick={() => openOrchestrator("tui")} style={style}>{t("newTask.createAsTui")}</TopbarButton> : null}
		</div>
	) : null;
	return header ? <>{feedback}{newTaskButton}{orchestratorButton}</> : <>
		<div className="mt-5 flex items-center gap-2">{orchestratorButton}{newTaskButton}</div>
		{feedback}
	</>;
}
