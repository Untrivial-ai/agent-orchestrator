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
		return (
			<div
				role="status"
				aria-live="polite"
				className={cn(
					"flex h-7 items-center gap-1 rounded-md bg-muted/55 pl-2 text-xs text-muted-foreground",
					cancellable ? "pr-0.5" : "pr-2",
					className,
				)}
				title={cancelError || `${phaseCopy[transition.phase]} Switching to ${targetTitleLabel(transition.targetMode)}.`}
			>
				<Loader2 aria-hidden="true" className="size-3.5 shrink-0 animate-spin" />
				<span className="whitespace-nowrap">
					{phaseCopy[transition.phase]} <span className="text-foreground">{targetTitleLabel(transition.targetMode)}</span>
				</span>
				{cancellable ? (
					<Button
						type="button"
						size="sm"
						variant="ghost"
						className="ml-1 h-6 gap-1 px-1.5 text-[11px] text-muted-foreground hover:text-foreground"
						disabled={cancelling}
						onClick={onCancel}
						aria-label={`Cancel switch to ${targetTitleLabel(transition.targetMode)}`}
					>
						{cancelling ? <Loader2 aria-hidden="true" className="size-3 animate-spin" /> : <X aria-hidden="true" className="size-3" />}
						{cancelling ? "Cancelling" : "Cancel"}
					</Button>
				) : null}
				{cancelError ? (
					<span role="alert" className="ml-1 whitespace-nowrap pr-1.5 text-[11px] text-destructive">
						Cancel failed
					</span>
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
						title={tooltipLabel}
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

export function SessionInterfaceTransitionNotice({
	transition,
	onDismiss,
	dismissing,
	dismissError,
	onSwitchWithInterrupt,
	interrupting,
}: {
	transition?: SessionInterfaceTransition;
	onDismiss: () => void;
	dismissing?: boolean;
	dismissError?: string;
	onSwitchWithInterrupt?: () => void;
	interrupting?: boolean;
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
	return (
		<div
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
				{dismissError ? (
					<p role="alert" className="mt-1 text-[11px] leading-4 text-destructive">
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
	return <div className="inline-flex shrink-0 items-center gap-px">{children}</div>;
}
