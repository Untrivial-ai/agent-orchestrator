import { useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Repeat2, TriangleAlert, X } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	agentSwitchesQueryKey,
	agentSwitchNeedsRecovery,
	agentSwitchNeedsSourceRecovery,
	agentSwitchNeedsSourceStopRecovery,
	agentSwitchNeedsSourceRestore,
	isTerminalAgentSwitch,
} from "../hooks/useAgentSwitches";
import {
	createSwitchAgentIdempotencyKey,
	clearSwitchAgentState,
	type SwitchAgentHarness,
	useSwitchAgent,
	useRecoverAgentSwitch,
	useSwitchAgentState,
} from "../hooks/useSwitchAgent";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { AGENT_LABELS, AGENT_OPTIONS, agentLabel } from "../lib/agent-options";
import type { AgentSwitchSummary, WorkspaceSession } from "../types/workspace";
import { AgentAvatar } from "./AgentAvatar";
import { AgentModelPicker } from "./AgentModelPicker";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
} from "./ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

export const SWITCH_AGENT_OPTIONS = [
	{ value: "claude-code", label: "Claude Code" },
	{ value: "codex", label: "Codex" },
] as const satisfies ReadonlyArray<{ value: SwitchAgentHarness; label: string }>;

const ALL_SWITCH_AGENT_OPTIONS = AGENT_OPTIONS.map((value) => ({ value, label: AGENT_LABELS[value] }));

export function canSwitchAgentHarness(value: string): value is SwitchAgentHarness {
	return SWITCH_AGENT_OPTIONS.some((option) => option.value === value);
}

function SwitchTargetPicker({
	currentHarness,
	disabled,
	onChange,
	value,
}: {
	currentHarness: string;
	disabled: boolean;
	onChange: (value: SwitchAgentHarness) => void;
	value: SwitchAgentHarness;
}) {
	const { t } = useTranslation();
	const options = ALL_SWITCH_AGENT_OPTIONS.map((option) => ({
		...option,
		disabled: !canSwitchAgentHarness(option.value) || option.value === currentHarness,
	}));
	const selected = options.find((option) => option.value === value);
	return (
		<SettingsOptionMenu
			aria-label={t("switchAgent.targetLabel")}
			disabled={disabled}
			menuAlign="start"
			menuClassName="settings-agent-menu-surface"
			menuItemClassName="settings-agent-menu-item"
			onChange={(nextValue) => {
				if (canSwitchAgentHarness(nextValue) && nextValue !== currentHarness) onChange(nextValue);
			}}
			options={options}
			renderMenuItem={(option) => {
				const supported = canSwitchAgentHarness(option.value);
				const current = option.value === currentHarness;
				return (
					<span className="flex w-full min-w-0 items-center gap-2">
						<AgentAvatar className="size-icon-base" decorative provider={option.value} />
						<span className="min-w-0 flex-1 truncate">{option.label}</span>
						{!supported ? (
							<span className="shrink-0 text-micro text-settings-muted">
								<span className="sr-only">, </span>
								{t("switchAgent.comingSoon")}
							</span>
						) : current ? (
							<span className="shrink-0 text-micro text-settings-muted">
								<span className="sr-only">, </span>
								{t("switchAgent.current")}
							</span>
						) : null}
					</span>
				);
			}}
			renderTrigger={() => (
				<span className="flex min-w-0 items-center gap-2">
					<AgentAvatar className="size-icon-base" decorative provider={value} />
					<span className="min-w-0 truncate text-control text-foreground" title={selected?.label}>
						{selected?.label}
					</span>
				</span>
			)}
			triggerClassName="composer-chip composer-toolbar-option w-full justify-between"
			value={value}
		/>
	);
}

type SwitchAgentDialogProps = {
	agentSwitch?: AgentSwitchSummary;
	container: HTMLElement;
	open: boolean;
	session: WorkspaceSession;
	onOpenChange: (open: boolean) => void;
};

export function SwitchAgentDialog({ agentSwitch, container, open, session, onOpenChange }: SwitchAgentDialogProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const defaultTargetHarness: SwitchAgentHarness = session.provider === "claude-code" ? "codex" : "claude-code";
	const [targetHarness, setTargetHarness] = useState<SwitchAgentHarness>(defaultTargetHarness);
	const [model, setModel] = useState("");
	const [mode, setMode] = useState("");
	const [modelWarning, setModelWarning] = useState<string | undefined>();
	const switchAgent = useSwitchAgent();
	const recoverAgentSwitch = useRecoverAgentSwitch();
	const switchMutation = useSwitchAgentState(session.id);
	const admissionPending = switchMutation.isPending;
	// Agent-switch history has its own bounded polling fallback. Prefer that
	// observation over the compact workspace projection so a settled recovery
	// cannot leave this dialog pinned to an older recovery-required snapshot.
	const durableSwitch = agentSwitch ?? session.activeAgentSwitch;
	const recoveryRequired = durableSwitch ? agentSwitchNeedsRecovery(durableSwitch) : false;
	const sourceStopRecoveryRequired = durableSwitch ? agentSwitchNeedsSourceStopRecovery(durableSwitch) : false;
	const sourceRestoreRequired = durableSwitch ? agentSwitchNeedsSourceRestore(durableSwitch) : false;
	const sourceRecoveryRequired = durableSwitch ? agentSwitchNeedsSourceRecovery(durableSwitch) : false;
	const sourceLabel = durableSwitch
		? agentLabel(durableSwitch.fromHarness)
		: agentLabel(session.provider);
	const recoveryTitleKey = sourceStopRecoveryRequired
		? "switchAgent.sourceStopRecovery.title"
		: sourceRestoreRequired
			? "switchAgent.sourceRecovery.title"
			: "switchAgent.recovery.title";
	const recoveryDescriptionKey = sourceStopRecoveryRequired
		? "switchAgent.sourceStopRecovery.description"
		: sourceRestoreRequired
			? "switchAgent.sourceRecovery.description"
			: "switchAgent.recovery.description";
	const sourceRecoveryActionKey = sourceStopRecoveryRequired
		? recoverAgentSwitch.isPending
			? "switchAgent.sourceStopRecovery.checking"
			: "switchAgent.sourceStopRecovery.action"
		: recoverAgentSwitch.isPending
			? "switchAgent.sourceRecovery.restoring"
			: "switchAgent.sourceRecovery.action";
	const durableSwitching = Boolean(
		durableSwitch && !isTerminalAgentSwitch(durableSwitch) && !recoveryRequired,
	);
	const [refreshingRecovery, setRefreshingRecovery] = useState(false);
	const operationPending = admissionPending || recoverAgentSwitch.isPending;
	useEffect(() => {
		setTargetHarness(session.provider === "claude-code" ? "codex" : "claude-code");
		setModel("");
		setMode("");
		setModelWarning(undefined);
	}, [session.provider]);
	useEffect(() => {
		if (open && durableSwitching) onOpenChange(false);
	}, [durableSwitching, onOpenChange, open]);
	const clearFailedAttempt = () => {
		if (!switchMutation.error) return;
		clearSwitchAgentState(queryClient, session.id);
	};

	const changeTarget = (nextTarget: SwitchAgentHarness) => {
		clearFailedAttempt();
		setTargetHarness(nextTarget);
		setModel("");
		setMode("");
		setModelWarning(undefined);
	};

	const submit = (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		if (admissionPending || durableSwitching || recoveryRequired) return;
		switchAgent.mutate(
			{
				session,
				targetHarness,
				model: model.trim() || mode.trim(),
				idempotencyKey: createSwitchAgentIdempotencyKey(),
			},
			{ onSuccess: () => onOpenChange(false) },
		);
	};

	const error = switchMutation.error;
	const refreshRecovery = async () => {
		setRefreshingRecovery(true);
		try {
			await Promise.all([
				queryClient.invalidateQueries({ queryKey: agentSwitchesQueryKey(session.id) }),
				queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
			]);
		} finally {
			setRefreshingRecovery(false);
		}
	};

	return (
		<Dialog
			modal={false}
			onOpenChange={(nextOpen) => {
			if (!nextOpen && operationPending) return;
				onOpenChange(nextOpen);
			}}
			open={open}
		>
			<DialogContent
				portalContainer={container}
				overlay={
					<div
						aria-hidden="true"
						className="agent-switch-terminal-scrim absolute inset-0 z-20 animate-overlay-in motion-reduce:animate-none"
						data-testid="switch-agent-terminal-backdrop"
					/>
				}
				showCloseButton={false}
				className="absolute left-1/2 top-1/2 z-overlay w-[min(var(--size-dialog-md),calc(100%-var(--space-8)))] max-w-none -translate-x-1/2 -translate-y-1/2 gap-0 overflow-hidden rounded-xl border border-border-strong bg-surface/95 p-0 text-foreground shadow-xl shadow-black/20 data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none"
			>
					<DialogClose asChild>
						<button
							aria-label={t("switchAgent.close")}
							className="settings-dialog-close-button settings-close-button"
							disabled={operationPending}
							type="button"
						>
							<X className="size-icon-base" aria-hidden="true" />
						</button>
					</DialogClose>
					<DialogTitle className="settings-dialog-title px-4 pr-12 pt-3">
						{t("switchAgent.title")}
					</DialogTitle>
					<DialogDescription className="px-4 pr-12 pt-0.5 text-caption leading-4 text-muted-foreground">
						{t("switchAgent.description", { current: agentLabel(session.provider) })}
					</DialogDescription>

					{recoveryRequired ? (
						<div className="flex flex-col gap-4 px-4 pb-4 pt-4">
							<div className="flex items-start gap-3 rounded-lg border border-warning/40 bg-warning/5 px-3 py-3">
								<TriangleAlert aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-warning" />
								<div className="min-w-0">
									<p className="font-mono text-control font-medium text-foreground">
										{t(recoveryTitleKey, { source: sourceLabel })}
									</p>
									<p className="mt-1 text-caption leading-4 text-muted-foreground">
										{t(recoveryDescriptionKey, { source: sourceLabel })}
									</p>
									{sourceRecoveryRequired && recoverAgentSwitch.error instanceof Error ? (
										<p className="mt-2 text-caption leading-4 text-error" role="alert">
											{recoverAgentSwitch.error.message}
										</p>
									) : null}
								</div>
							</div>
							{sourceRecoveryRequired && durableSwitch ? (
								<Button
									className="self-end"
									disabled={recoverAgentSwitch.isPending}
									onClick={() =>
										recoverAgentSwitch.mutate({
											sessionId: session.id,
											switchId: durableSwitch.id,
										})
									}
									type="button"
									variant="outline"
								>
									{recoverAgentSwitch.isPending ? (
										<LoaderCircle aria-hidden="true" className="size-icon-sm animate-spin" />
									) : null}
									{t(sourceRecoveryActionKey, { source: sourceLabel })}
								</Button>
							) : (
								<Button
									className="self-end"
									disabled={refreshingRecovery}
									onClick={() => void refreshRecovery()}
									type="button"
									variant="outline"
								>
									{refreshingRecovery ? <LoaderCircle aria-hidden="true" className="size-icon-sm animate-spin" /> : null}
									{t("settings.project.refresh")}
								</Button>
							)}
						</div>
					) : (
						<form className="flex flex-col gap-3 px-4 pb-4 pt-4" onSubmit={submit}>
						{error || modelWarning ? (
							<div>
								{error ? (
									<p className="text-caption leading-4 text-error" role="alert">
										{error}
									</p>
								) : null}
								{!error && modelWarning ? (
									<p className="text-caption text-warning">{modelWarning}</p>
								) : null}
							</div>
						) : null}

						<div className="composer-toolbar p-0!">
							<div className="composer-run-controls" role="group" aria-label={t("newTask.runsWith")}>
								<div className="composer-toolbar-slot">
									<SwitchTargetPicker
										currentHarness={session.provider}
										disabled={admissionPending}
										onChange={changeTarget}
										value={targetHarness}
									/>
								</div>
								<span className="composer-toolbar-divider" aria-hidden="true" />
								<div className="composer-toolbar-slot">
									<AgentModelPicker
										agentId={targetHarness}
										agentLabel={agentLabel(targetHarness)}
										disabled={admissionPending}
										mode={mode}
										onModeChange={(value) => {
											clearFailedAttempt();
											setMode(value);
											setModel("");
										}}
										onModelChange={(value) => {
											clearFailedAttempt();
											setModel(value);
											setMode("");
										}}
										onWarningChange={setModelWarning}
										projectId={session.workspaceId}
										value={model}
									/>
								</div>
							</div>
							<Tooltip>
								<TooltipTrigger asChild>
									<span className="inline-flex">
										<Button
											aria-label={admissionPending ? t("newTask.starting") : t("switchAgent.confirm")}
											className="size-(--size-settings-action-height)"
											disabled={admissionPending}
											size="none"
											type="submit"
											variant="primary"
										>
											{admissionPending ? (
												<LoaderCircle className="size-icon-base animate-spin" aria-hidden="true" />
											) : (
												<Repeat2 className="size-4 stroke-[1.8]" aria-hidden="true" />
											)}
										</Button>
									</span>
								</TooltipTrigger>
								<TooltipContent side="bottom">
									{admissionPending ? t("newTask.starting") : t("switchAgent.confirm")}
								</TooltipContent>
							</Tooltip>
						</div>
						</form>
					)}
			</DialogContent>
		</Dialog>
	);
}
