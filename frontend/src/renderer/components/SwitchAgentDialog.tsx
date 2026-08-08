import { ArrowLeftRight, LoaderCircle, X } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	createSwitchAgentIdempotencyKey,
	clearSwitchAgentState,
	type SwitchAgentHarness,
	useSwitchAgent,
	useSwitchAgentState,
} from "../hooks/useSwitchAgent";
import {
	findActiveAgentSwitch,
	isTerminalAgentSwitch,
	type AgentSwitch,
	useAgentSwitches,
} from "../hooks/useAgentSwitches";
import { AGENT_LABELS, AGENT_OPTIONS, agentLabel } from "../lib/agent-options";
import type { WorkspaceSession } from "../types/workspace";
import { AgentAvatar } from "./AgentAvatar";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

export const SWITCH_AGENT_OPTIONS = [
	{ value: "claude-code", label: "Claude Code" },
	{ value: "codex", label: "Codex" },
] as const satisfies ReadonlyArray<{ value: SwitchAgentHarness; label: string }>;

const ALL_SWITCH_AGENT_OPTIONS = AGENT_OPTIONS.map((value) => ({ value, label: AGENT_LABELS[value] }));

export function canSwitchAgentHarness(value: string): value is SwitchAgentHarness {
	return SWITCH_AGENT_OPTIONS.some((option) => option.value === value);
}

const AGENT_SWITCH_STATE_KEYS = {
	preparing_handoff: "switchAgent.state.preparingHandoff",
	stopping_source: "switchAgent.state.stoppingSource",
	source_stopped: "switchAgent.state.sourceStopped",
	starting_target: "switchAgent.state.startingTarget",
	target_ready: "switchAgent.state.targetReady",
	delivering_context: "switchAgent.state.deliveringContext",
	completed: "switchAgent.state.completed",
	failed: "switchAgent.state.failed",
} as const;

type SwitchAgentDialogProps = {
	open: boolean;
	session: WorkspaceSession;
	onOpenChange: (open: boolean) => void;
};

export function SwitchAgentDialog({
	open,
	session,
	onOpenChange,
}: SwitchAgentDialogProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const noteId = useId();
	const targetId = useId();
	const historyId = useId();
	const defaultTargetHarness: SwitchAgentHarness = session.provider === "claude-code" ? "codex" : "claude-code";
	const [targetHarness, setTargetHarness] = useState<SwitchAgentHarness>(defaultTargetHarness);
	const [note, setNote] = useState("");
	const [idempotencyKey, setIdempotencyKey] = useState(createSwitchAgentIdempotencyKey);
	const submittedRef = useRef(false);
	const switchAgent = useSwitchAgent();
	const switchMutation = useSwitchAgentState(session.id);
	const switchesQuery = useAgentSwitches(session.id);
	const switches = switchesQuery.data ?? [];
	const activeSwitch = findActiveAgentSwitch(switches);
	const pendingInput = switchMutation.input;
	const switchInProgress = Boolean(activeSwitch || (switchMutation.isPending && pendingInput));
	const terminalHistory = switches.filter(isTerminalAgentSwitch).slice(0, 5);
	const checkingStatus = switchesQuery.isPending;

	const resetAttemptAfterEdit = () => {
		if (!submittedRef.current && !switchMutation.error) return;
		submittedRef.current = false;
		setIdempotencyKey(createSwitchAgentIdempotencyKey());
		clearSwitchAgentState(queryClient, session.id);
		switchAgent.reset();
	};

	const submit = () => {
		if (switchAgent.isPending || switchMutation.isPending || checkingStatus || activeSwitch) return;
		submittedRef.current = true;
		switchAgent.mutate({ session, targetHarness, note, idempotencyKey });
		onOpenChange(false);
	};

	const error = switchAgent.error instanceof Error ? switchAgent.error.message : switchMutation.error;
	const historyError = switchesQuery.error instanceof Error ? switchesQuery.error.message : null;
	const stateLabel = (agentSwitch: AgentSwitch) => {
		if (agentSwitch.state === "failed" && agentSwitch.errorCode === "delivery_unconfirmed") {
			return t("switchAgent.state.deliveryUnconfirmed");
		}
		const translationKey =
			agentSwitch.state in AGENT_SWITCH_STATE_KEYS
				? AGENT_SWITCH_STATE_KEYS[agentSwitch.state as keyof typeof AGENT_SWITCH_STATE_KEYS]
				: undefined;
		return translationKey ? t(translationKey) : agentSwitch.state;
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent showCloseButton={false} className={settingsDialogContentClass}>
				<DialogClose asChild>
					<button
						type="button"
						className="settings-dialog-close-button settings-close-button"
						aria-label={t("switchAgent.close")}
					>
						<X className="size-5" aria-hidden="true" />
					</button>
				</DialogClose>

				<form
					className="contents"
					onSubmit={(event) => {
						event.preventDefault();
						submit();
					}}
				>
					<div className={settingsDialogHeaderClass}>
						<DialogTitle className="settings-dialog-title">{t("switchAgent.title")}</DialogTitle>
						<DialogDescription className="text-control leading-4 text-settings-muted">
							{t("switchAgent.description", { current: agentLabel(session.provider) })}
						</DialogDescription>
					</div>

					<div className={settingsDialogBodyClass}>
						{checkingStatus ? (
							<div className="inline-flex items-center gap-2 text-control text-settings-muted" role="status">
								<LoaderCircle className="size-icon-sm animate-spin" aria-hidden="true" />
								{t("switchAgent.checkingStatus")}
							</div>
						) : switchInProgress ? (
							<div aria-live="polite" className="flex items-start gap-2 py-1" role="status">
								<LoaderCircle className="mt-0.5 size-icon-sm shrink-0 animate-spin" aria-hidden="true" />
								<div>
									<div className="text-control font-medium text-foreground">
										{t("switchAgent.progressTitle", {
											source: agentLabel(activeSwitch?.fromHarness ?? pendingInput?.session.provider ?? session.provider),
											target: agentLabel(activeSwitch?.targetHarness ?? pendingInput?.targetHarness ?? targetHarness),
										})}
									</div>
									<p className="mt-0.5 text-caption text-settings-muted">
										{activeSwitch ? stateLabel(activeSwitch) : t("switchAgent.switching")}
									</p>
								</div>
							</div>
						) : (
							<>
								<div className="flex flex-col gap-1.5">
									<label className="settings-field-label" htmlFor={targetId}>
										{t("switchAgent.targetLabel")}
									</label>
									<Select
										disabled={switchInProgress}
										onValueChange={(value) => {
											if (!canSwitchAgentHarness(value) || value === session.provider) return;
											resetAttemptAfterEdit();
											setTargetHarness(value);
										}}
										value={targetHarness}
									>
										<SelectTrigger id={targetId} className="settings-field-control w-full">
											<SelectValue />
										</SelectTrigger>
										<SelectContent
											align="start"
											className="max-h-64 w-(--radix-select-trigger-width) [&_[data-slot=select-scroll-down-button]]:hidden [&_[data-slot=select-scroll-up-button]]:hidden"
											position="popper"
										>
											{ALL_SWITCH_AGENT_OPTIONS.map((option) => {
												const supported = canSwitchAgentHarness(option.value);
												const current = option.value === session.provider;
												return (
													<SelectItem
														className="[&>span:last-child]:w-full"
														disabled={!supported || current}
														key={option.value}
														value={option.value}
													>
														<span className="flex w-full items-center gap-2">
															<AgentAvatar className="size-icon-lg" decorative provider={option.value} />
															<span className="min-w-0 flex-1 truncate">{option.label}</span>
															{!supported ? (
																<>
																	<span className="sr-only">, </span>
																	<span className="shrink-0 text-micro text-settings-muted">
																		{t("switchAgent.comingSoon")}
																	</span>
																</>
															) : current ? (
																<>
																	<span className="sr-only">, </span>
																	<span className="shrink-0 text-micro text-settings-muted">
																		{t("switchAgent.current")}
																	</span>
																</>
															) : null}
														</span>
													</SelectItem>
												);
											})}
										</SelectContent>
									</Select>
								</div>

								<div className="flex flex-col items-start gap-1.5">
									<label className="settings-field-label" htmlFor={noteId}>
										{t("switchAgent.noteLabel")}
									</label>
									<textarea
										id={noteId}
										className="settings-field-control min-h-(--size-textarea-min) resize-y py-2.5"
										disabled={switchInProgress}
										maxLength={4096}
										onChange={(event) => {
											resetAttemptAfterEdit();
											setNote(event.target.value);
										}}
										placeholder={t("switchAgent.notePlaceholder")}
										value={note}
									/>
								</div>
							</>
						)}

						{terminalHistory.length > 0 ? (
							<section aria-labelledby={historyId} className="flex flex-col gap-1.5">
								<h3 className="settings-field-label" id={historyId}>
									{t("switchAgent.historyTitle")}
								</h3>
								<ul className="max-h-36 divide-y divide-border/60 overflow-y-auto" data-testid="agent-switch-history">
									{terminalHistory.map((entry) => (
										<li className="flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0" key={entry.id}>
											<span className="truncate text-caption text-foreground/80">
													{t("switchAgent.historyEntry", {
														source: agentLabel(entry.fromHarness),
														target: agentLabel(entry.targetHarness),
													})}
											</span>
											<span className="shrink-0 text-micro text-settings-muted">{stateLabel(entry)}</span>
										</li>
									))}
								</ul>
							</section>
						) : null}

						{error ? (
							<p className="text-caption leading-4 text-error" role="alert">
								{error}
							</p>
						) : null}
						{historyError ? (
							<p className="text-caption leading-4 text-error" role="alert">
								{historyError}
							</p>
						) : null}
					</div>

					<div className={settingsDialogFooterClass}>
						<DialogClose asChild>
							<button className="settings-footer-button" type="button">
								{switchInProgress || checkingStatus ? t("switchAgent.closeButton") : t("confirm.cancel")}
							</button>
						</DialogClose>
						{!switchInProgress && !checkingStatus ? (
							<button
								className="settings-footer-button settings-footer-button-primary"
								disabled={switchAgent.isPending}
								type="submit"
							>
								{switchAgent.isPending ? (
									<LoaderCircle className="size-icon-sm animate-spin" aria-hidden="true" />
								) : (
									<ArrowLeftRight className="size-icon-sm" aria-hidden="true" />
								)}
								{switchAgent.isPending ? t("switchAgent.switching") : t("switchAgent.confirm")}
							</button>
						) : null}
					</div>
				</form>
			</DialogContent>
		</Dialog>
	);
}
