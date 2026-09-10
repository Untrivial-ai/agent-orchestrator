import { useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Repeat2, TriangleAlert } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { clearSwitchAgentState } from "../hooks/useSwitchAgent";
import type { AgentSwitchPresentation } from "../lib/agent-switch-presentation";
import { cn } from "../lib/utils";
import { sessionIsActive, type AgentSwitchSummary, type WorkspaceSession } from "../types/workspace";
import { canSwitchAgentHarness, SwitchAgentDialog } from "./SwitchAgentDialog";
import { TopbarButton } from "./TopbarButton";
import { DropdownMenuItem } from "./ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type TerminalSwitchAgentButtonProps = {
	agentSwitch?: AgentSwitchSummary;
	container?: HTMLElement | null;
	disabled?: boolean;
	onOpenChange: ((open: boolean) => void) | undefined;
	open: boolean;
	presentation?: AgentSwitchPresentation;
	session: WorkspaceSession;
	switchError: string | null;
	variant?: "icon" | "menu-item";
};

export function TerminalSwitchAgentButton({
	agentSwitch,
	container,
	disabled,
	onOpenChange,
	open,
	presentation,
	session,
	switchError,
	variant = "icon",
}: TerminalSwitchAgentButtonProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const controlPresentation = presentation?.outcome === "success" ? undefined : presentation;
	const switching = controlPresentation?.outcome === "in_progress";
	const warning = controlPresentation?.outcome === "failure" || controlPresentation?.outcome === "recovery";
	const blocksNewSwitch = switching || disabled;

	useEffect(() => {
		if (switchError) onOpenChange?.(true);
	}, [onOpenChange, switchError]);

	if (
		session.kind !== "worker" ||
		session.isTerminated ||
		!canSwitchAgentHarness(session.provider) ||
		(!controlPresentation && !sessionIsActive(session))
	) {
		return null;
	}

	const label = controlPresentation
		? t(controlPresentation.compactLabelKey, controlPresentation.values)
		: t("switchAgent.action");
	const handleOpenChange = (nextOpen: boolean) => {
		onOpenChange?.(nextOpen);
		if (!nextOpen && switchError) {
			clearSwitchAgentState(queryClient, session.id);
		}
	};

	const icon = warning ? (
		<TriangleAlert aria-hidden="true" className="size-icon-sm" />
	) : switching ? (
		<LoaderCircle aria-hidden="true" className="agent-switch-toolbar-spinner size-icon-sm animate-spin" />
	) : (
		<Repeat2 aria-hidden="true" className="size-4 stroke-[1.8]" />
	);

	return (
		<>
			{variant === "menu-item" ? (
				<DropdownMenuItem
					className={cn(warning && "text-warning focus:text-warning [&_svg]:text-warning")}
					disabled={blocksNewSwitch}
					onSelect={() => handleOpenChange(true)}
				>
					{icon}
					{label}
				</DropdownMenuItem>
			) : (
				<Tooltip>
					<TooltipTrigger asChild>
						<TopbarButton
							aria-busy={switching && controlPresentation?.animate ? true : undefined}
							aria-label={label}
							className={cn(
								warning && "text-warning hover:bg-warning/10 hover:text-warning",
							)}
							disabled={blocksNewSwitch}
							onClick={() => {
								if (!open) handleOpenChange(true);
							}}
							onPointerDown={(event) => {
								if (!open) return;
								event.preventDefault();
								event.stopPropagation();
							}}
							type="button"
							variant="icon"
						>
							{icon}
						</TopbarButton>
					</TooltipTrigger>
					<TooltipContent>{label}</TooltipContent>
				</Tooltip>
			)}
			{open && container && variant !== "menu-item" ? (
				<SwitchAgentDialog
					agentSwitch={agentSwitch}
					container={container}
					onOpenChange={handleOpenChange}
					open
					session={session}
				/>
			) : null}
		</>
	);
}
