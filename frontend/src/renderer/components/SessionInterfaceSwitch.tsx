import {
	ArrowRightLeft,
	CheckCircle2,
	Loader2,
	MessageSquare,
	SquareTerminal,
	TriangleAlert,
	X,
} from "lucide-react";
import type { ReactNode } from "react";
import type {
	SessionInterfaceMode,
	SessionInterfaceTransition,
	SessionInterfaceTransitionPolicy,
} from "../hooks/useSessionInterfaceTransition";
import {
	interfaceTransitionIsActive,
	interfaceTransitionIsCancellable,
} from "../hooks/useSessionInterfaceTransition";
import { cn } from "../lib/utils";
import { TopbarButton } from "./TopbarButton";
import { Button } from "./ui/button";
import { DropdownMenuItem } from "./ui/dropdown-menu";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "./ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

function targetLabel(target: SessionInterfaceMode): string {
	return target === "chat" ? "chat UI" : "terminal UI";
}

function targetTitleLabel(target: SessionInterfaceMode): string {
	return target === "chat" ? "Chat UI" : "Terminal UI";
}

const phaseCopy: Record<SessionInterfaceTransition["phase"], string> = {
	requested: "Preparing switch…",
	preflighting: "Checking interface…",
	draining: "Waiting to switch…",
	source_stopping: "Stopping controller…",
	source_stopped: "Controller stopped…",
	target_starting: "Resuming agent…",
	activating: "Activating interface…",
	completed: "Interface switched",
	failed: "Interface switch failed",
	cancelled: "Interface switch cancelled",
	recovery_required: "Interface switch needs attention",
};

export function SessionInterfaceSwitchButton({
	target,
	supported,
	disabledReason,
	pending,
	transition,
	cancelling,
	cancelError,
	onClick,
	onCancel,
	className,
}: {
	target: SessionInterfaceMode;
	supported: boolean;
	disabledReason?: string;
	pending?: boolean;
	transition?: SessionInterfaceTransition;
	cancelling?: boolean;
	cancelError?: string;
	onClick: () => void;
	onCancel?: () => void;
	className?: string;
}) {
	if (transition && interfaceTransitionIsActive(transition)) {
		const cancellable = interfaceTransitionIsCancellable(transition) && Boolean(onCancel);
		const statusLabel =
			cancelError ||
			`${phaseCopy[transition.phase]} Switching to ${targetTitleLabel(transition.targetMode)}.`;
		const cancelLabel = `Cancel switch to ${targetTitleLabel(transition.targetMode)}`;
		return (
			<div
				role="status"
				aria-live="polite"
				aria-label={statusLabel}
				className={cn("relative inline-flex size-7 shrink-0 items-center justify-center text-muted-foreground", className)}
				title={statusLabel}
			>
				{/* TerminalTabFrame is a Tailwind `group`; tab hover swaps spinner → cancel. */}
				<Loader2
					aria-hidden="true"
					className={cn(
						"size-3.5 animate-spin",
						cancellable && "pointer-events-none group-hover:opacity-0",
					)}
				/>
				{cancellable ? (
					<button
						type="button"
						aria-label={cancelLabel}
						title={cancelling ? "Cancelling…" : cancelLabel}
						disabled={cancelling}
						onClick={(event) => {
							event.stopPropagation();
							onCancel?.();
						}}
						className={cn(
							"absolute inset-0 inline-flex items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/50 group-hover:opacity-100",
							cancelling && "opacity-100",
						)}
					>
						{cancelling ? (
							<Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
						) : (
							<X aria-hidden="true" className="size-3.5" />
						)}
					</button>
				) : null}
			</div>
		);
	}

	const label = `Switch to ${targetLabel(target)}`;
	const tooltipLabel = supported ? `${label} using this agent's native conversation` : disabledReason || label;
	const TargetIcon = target === "chat" ? MessageSquare : SquareTerminal;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span className="inline-flex">
					<TopbarButton
						aria-label={label}
						className={cn(!supported && "opacity-50", className)}
						disabled={!supported || pending}
						onClick={onClick}
						type="button"
						variant="icon"
					>
						{pending ? (
							<Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
						) : (
							<TargetIcon aria-hidden="true" className="size-icon-md" />
						)}
					</TopbarButton>
				</span>
			</TooltipTrigger>
			<TooltipContent side="bottom">{tooltipLabel}</TooltipContent>
		</Tooltip>
	);
}

export function SessionInterfaceSwitchDialog({
	open,
	target,
	waitingForInput,
	busy,
	error,
	onOpenChange,
	onChoose,
}: {
	open: boolean;
	target: SessionInterfaceMode;
	waitingForInput?: boolean;
	busy?: boolean;
	error?: string;
	onOpenChange: (open: boolean) => void;
	onChoose: (policy: SessionInterfaceTransitionPolicy) => void;
}) {
	const targetName = targetTitleLabel(target);
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-md gap-0 overflow-hidden rounded-xl border-border bg-popover p-0">
				<DialogHeader className="border-b border-border px-5 py-4">
					<DialogTitle className="flex items-center gap-2 text-sm">
						<ArrowRightLeft aria-hidden="true" className="size-4 text-muted-foreground" />
						Switch to {targetName}?
					</DialogTitle>
					<DialogDescription className="pt-1 text-xs leading-5">
						The same AO session, worktree, and agent-native conversation continue in the other interface.
						Completed messages and tool work stay in the agent's context.
					</DialogDescription>
				</DialogHeader>

				<div className="grid gap-2 px-5 py-4">
					<button
						type="button"
						className="rounded-lg border border-border bg-background px-3.5 py-3 text-left transition-colors hover:border-border-strong hover:bg-muted disabled:opacity-50"
						disabled={busy}
						onClick={() => onChoose("drain")}
					>
						<strong className="block text-sm font-medium text-foreground">Finish work, then switch</strong>
						<span className="mt-1 block text-xs leading-5 text-muted-foreground">
							Wait for the running turn and anything already queued to finish. New AO messages wait safely
							for {targetName}.
						</span>
					</button>
					<button
						type="button"
						className="rounded-lg border border-border bg-background px-3.5 py-3 text-left transition-colors hover:border-warning/60 hover:bg-muted disabled:opacity-50"
						disabled={busy}
						onClick={() => onChoose("interrupt")}
					>
						<strong className="block text-sm font-medium text-foreground">Stop now and switch</strong>
						<span className="mt-1 block text-xs leading-5 text-muted-foreground">
							Cancel the running turn before switching. Files already changed remain in the worktree, but
							unfinished output and queued Chat turns are cancelled.
							{target === "chat" ? " Any unsent Terminal UI draft is discarded." : null}
						</span>
					</button>
					{waitingForInput ? (
						<p className="text-[11px] leading-4 text-warning">
							This turn is waiting for your input. “Finish work” will wait until you answer it; use “Stop
							now” to switch immediately.
						</p>
					) : null}
					{target === "tui" ? (
						<p className="text-[11px] leading-4 text-warning">
							Any unsent Chat draft or staged attachments are discarded when the switch completes.
						</p>
					) : null}
					{error ? (
						<p role="alert" className="text-xs leading-5 text-destructive">
							{error}
						</p>
					) : null}
				</div>

				<DialogFooter className="border-t border-border px-5 py-3">
					<Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
						Keep current interface
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

export function interfaceTransitionOffersHistoryRecovery(transition?: SessionInterfaceTransition): boolean {
	return Boolean(
		transition?.sourceMode === "tui" &&
			transition.targetMode === "chat" &&
			((transition.phase === "failed" &&
				(transition.errorCode === "TARGET_HISTORY_UNSETTLED" ||
					transition.errorCode === "TARGET_HISTORY_UNTRUSTED_TEXT_MISMATCH")) ||
				(transition.phase === "recovery_required" &&
					transition.errorCode === "DAEMON_RESTARTED" &&
					transition.historyPolicy === "provider_history")),
	);
}

export function SessionInterfaceTransitionNotice({
	transition,
	onDismiss,
	dismissing,
	dismissError,
	onSwitchWithInterrupt,
	interrupting,
	onRetry,
	retrying,
	onUseProviderHistory,
	recoveryError,
}: {
	transition?: SessionInterfaceTransition;
	onDismiss: () => void;
	dismissing?: boolean;
	dismissError?: string;
	onSwitchWithInterrupt?: () => void;
	interrupting?: boolean;
	onRetry?: () => void;
	retrying?: boolean;
	onUseProviderHistory?: () => void;
	recoveryError?: string;
}) {
	if (
		!transition ||
		transition.noticeAcknowledgedAt ||
		(transition.phase !== "failed" && transition.phase !== "recovery_required")
	) {
		return null;
	}
	const recovered =
		transition.phase === "recovery_required" && transition.errorCode === "DAEMON_RESTARTED";
	const legacyTextMismatch =
		transition.phase === "failed" &&
		transition.errorCode === "TARGET_HISTORY_UNTRUSTED_TEXT_MISMATCH" &&
		transition.sourceMode === "tui" &&
		transition.targetMode === "chat";
	const interruptedProviderRecovery =
		recovered &&
		transition.historyPolicy === "provider_history" &&
		transition.sourceMode === "tui" &&
		transition.targetMode === "chat";
	const historyUnsettled = interfaceTransitionOffersHistoryRecovery(transition);
	return (
		<div
			role={recovered ? "status" : "alert"}
			aria-live={recovered ? "polite" : "assertive"}
			aria-atomic="true"
			className={cn(
				"absolute left-1/2 top-3 z-20 flex w-[min(34rem,calc(100%-1.5rem))] -translate-x-1/2 items-start gap-2 rounded-lg border bg-popover px-3 py-2.5 shadow-md",
				recovered ? "border-success/30" : "border-warning/30",
			)}
		>
			{recovered ? (
				<CheckCircle2 aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-success" />
			) : (
				<TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-warning" />
			)}
			<div className="min-w-0 flex-1">
				<strong className="block text-xs font-medium text-foreground">
					{recovered ? "Interface switch recovered" : phaseCopy[transition.phase]}
				</strong>
				<p className="mt-0.5 text-[11px] leading-4 text-muted-foreground">
					{transition.errorDetail ||
						(recovered
							? "AO restored the session in its last committed interface."
							: transition.phase === "recovery_required"
								? "Restart AO to reconcile this session before sending more work."
								: "The original interface remains available. You can retry the switch.")}
				</p>
				{transition.phase === "failed" &&
				(transition.errorCode === "DRAIN_DRAFT_PRESENT" ||
					transition.errorCode === "DRAIN_DECISION_PENDING") &&
				onSwitchWithInterrupt ? (
					<Button
						type="button"
						size="sm"
						variant="outline"
						className="mt-2 h-7 text-[11px]"
						disabled={interrupting}
						onClick={onSwitchWithInterrupt}
					>
						{interrupting ? <Loader2 aria-hidden="true" className="size-3 animate-spin" /> : null}
						{transition.errorCode === "DRAIN_DRAFT_PRESENT"
							? "Discard draft and switch"
							: "Cancel request and switch"}
					</Button>
				) : null}
				{historyUnsettled && onRetry ? (
					<div className="mt-2 flex flex-wrap items-center gap-2">
						<Button
							type="button"
							size="sm"
							variant="outline"
							className="h-7 text-[11px]"
							disabled={retrying || dismissing}
							onClick={onRetry}
						>
							{retrying ? <Loader2 aria-hidden="true" className="size-3 animate-spin" /> : null}
							Retry switch to Chat UI
						</Button>
						{(legacyTextMismatch || interruptedProviderRecovery) && onUseProviderHistory ? (
							<Button
								type="button"
								size="sm"
								variant="outline"
								className="h-7 text-[11px]"
								disabled={retrying || dismissing}
								onClick={onUseProviderHistory}
							>
								Use provider history and switch
							</Button>
						) : null}
						<Button
							type="button"
							size="sm"
							variant="ghost"
							className="h-7 text-[11px]"
							disabled={retrying || dismissing}
							onClick={onDismiss}
						>
							Stay in Terminal
						</Button>
					</div>
				) : null}
				{recoveryError ? (
					<p className="mt-1 text-[11px] leading-4 text-destructive">
						Recovery attempt failed: {recoveryError}
					</p>
				) : null}
				{dismissError ? (
					<p className="mt-1 text-[11px] leading-4 text-destructive">
						Could not dismiss this message. Try again.
					</p>
				) : null}
			</div>
			<button
				type="button"
				className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
				onClick={onDismiss}
				disabled={dismissing}
				aria-label="Dismiss interface switch message"
			>
				{dismissing ? (
					<Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
				) : (
					<X aria-hidden="true" className="size-3.5" />
				)}
			</button>
		</div>
	);
}

export function SessionInterfaceActionGroup({ children }: { children: ReactNode }) {
	return <div className="inline-flex shrink-0 items-center gap-2">{children}</div>;
}

export function SessionInterfaceSwitchMenuItem({
	target,
	supported,
	disabledReason,
	pending,
	onClick,
}: {
	target: SessionInterfaceMode;
	supported: boolean;
	disabledReason?: string;
	pending?: boolean;
	onClick: () => void;
}) {
	const label = `Switch to ${targetLabel(target)}`;
	const TargetIcon = target === "chat" ? MessageSquare : SquareTerminal;
	return (
		<DropdownMenuItem
			disabled={!supported || pending}
			onSelect={onClick}
			title={supported ? label : disabledReason || label}
		>
			{pending ? (
				<Loader2 aria-hidden="true" className="size-icon-lg animate-spin" />
			) : (
				<TargetIcon aria-hidden="true" className="size-icon-lg" />
			)}
			{label}
		</DropdownMenuItem>
	);
}
