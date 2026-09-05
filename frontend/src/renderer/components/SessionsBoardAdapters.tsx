import { useEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import {
	scmUserAvatarUrl,
	SessionCardView,
	SessionUsageMetricView,
	type BoardPullRequestLabels,
	type BoardSessionPresentation,
	type BoardColumnLabels,
	type BoardUsagePresentation,
	type ProductUITranslator,
} from "@aoagents/product-ui";
import { Check, Copy, GitBranch, LoaderCircle, RotateCcw, Trash2 } from "lucide-react";
import type { MessageKey } from "../i18n";
import { aoBridge } from "../lib/bridge";
import { formatTimeCompact } from "../lib/format-time";
import { formatEstimatedCost } from "../lib/format-cost";
import { formatTokenCount } from "../lib/format-token-count";
import { prBrowserUrl, sessionPRDisplaySummaries } from "../lib/pr-display";
import {
	agentSwitchStatusVisual,
	deriveSessionAgentSwitchPresentation,
} from "../lib/agent-switch-presentation";
import type { WorkspaceSession } from "../types/workspace";
import { canonicalTrackerIssueId } from "../types/workspace";
import { useSessionScmSummary } from "../hooks/useSessionScmSummary";
import type { SessionUsageSummary } from "../hooks/useSessionUsageSummaries";
import {
	clearTerminateSessionState,
	useTerminateSessionState,
} from "../hooks/useTerminateSession";
import { cn } from "../lib/utils";
import { AgentAvatar } from "./AgentAvatar";
import { ProductExternalLink } from "./ProductExternalLink";
import { SessionTerminationPopover } from "./SessionTerminationPopover";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

export function toBoardSessionPresentation(
	session: WorkspaceSession,
	t?: TFunction,
): BoardSessionPresentation {
	const switchPresentation = deriveSessionAgentSwitchPresentation(session);
	const switchVisual = switchPresentation ? agentSwitchStatusVisual(switchPresentation) : undefined;
	return {
		activity: session.activity,
		branch: session.branch,
		id: session.id,
		kanbanColumn: session.kanbanColumn,
		displayStatus: session.displayStatus,
		provider: session.provider,
		status: session.status,
		statusPresentation:
			t && switchPresentation && switchVisual
				? {
						className: switchVisual.className,
						indicatorClassName: `${switchVisual.indicatorClassName}${switchVisual.breathe ? " animate-status-pulse" : ""}`,
						label: t(switchPresentation.compactLabelKey, switchPresentation.values),
						tone: switchVisual.tone,
					}
				: undefined,
		title: session.title,
		trackerIssueId: canonicalTrackerIssueId(session.issueId),
		updatedAt: session.updatedAt,
		lastUserMessageAt: session.lastUserMessageAt,
	};
}

export function sessionsBoardLabels(t: TFunction): BoardColumnLabels {
	return {
		columnAria: (label) => t("shell.sessionsAria", { label }),
	};
}

export function BoardSessionCardAdapter({
	onOpen,
	onTerminate,
	session,
	usage,
}: {
	onOpen: () => void;
	onTerminate: () => void;
	session: WorkspaceSession;
	usage?: SessionUsageSummary;
}) {
	return (
		<DesktopSessionCard
			onOpen={onOpen}
			onTerminate={onTerminate}
			session={session}
			usage={usage}
		/>
	);
}

export function ArchivedSessionCardAdapter({
	isRestoreDisabled,
	isRestoring,
	restoreAction,
	restoreError,
	session,
	usage,
}: {
	isRestoreDisabled: boolean;
	isRestoring: boolean;
	restoreAction: (event: MouseEvent<HTMLButtonElement>) => void;
	restoreError?: string;
	session: WorkspaceSession;
	usage?: SessionUsageSummary;
}) {
	const branch = session.branch ?? "";
	return (
		<DesktopSessionCard
			action={
				<ArchiveRestoreButton
					isDisabled={isRestoreDisabled}
					isRestoring={isRestoring}
					label={`Restore ${session.title}`}
					onClick={restoreAction}
				/>
			}
			branchAction={branch ? <CopyActionButton label={`branch ${branch}`} value={branch} /> : undefined}
			footer={<ArchiveRestoreError message={restoreError} />}
			interactive={false}
			session={session}
			usage={usage}
		/>
	);
}

function DesktopSessionCard({
	action,
	branchAction,
	footer,
	interactive = true,
	onOpen,
	onTerminate,
	session,
	usage,
}: {
	action?: ReactNode;
	branchAction?: ReactNode;
	footer?: ReactNode;
	interactive?: boolean;
	onOpen?: () => void;
	onTerminate?: () => void;
	session: WorkspaceSession;
	usage?: SessionUsageSummary;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [confirmOpen, setConfirmOpen] = useState(false);
	const summaries = sessionPRDisplaySummaries(session, useSessionScmSummary(session.id).data);
	const termination = useTerminateSessionState(session.id);
	const showTerminate = interactive && session.isTerminated !== true && onTerminate;
	const keepTerminateVisible = session.status === "merged";
	const usagePresentation = toUsagePresentation(usage, t);
	const translate: ProductUITranslator = (key, values) => t(key as MessageKey, values);

	const terminationOverlay = showTerminate ? (
		<Tooltip>
			<TooltipTrigger asChild>
				<span className="inline-flex">
					<SessionTerminationPopover
						onConfirm={() => {
							setConfirmOpen(false);
							onTerminate();
						}}
						onOpenChange={setConfirmOpen}
						open={confirmOpen}
						session={session}
						trigger={
							<button
								aria-label={
									termination.isPending
										? t("shell.killingNamedAria", { title: session.title })
										: t("shell.terminateNamed", { title: session.title })
								}
								className={cn(
									"inline-flex size-control-md items-center justify-center rounded-sm text-passive transition-[color,background-color,opacity] hover:bg-error/10 hover:text-error focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
									keepTerminateVisible || termination.isPending
										? "opacity-100"
										: "pointer-events-none opacity-0 group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100",
								)}
								onClick={(event) => {
									event.stopPropagation();
									clearTerminateSessionState(queryClient, session.id);
								}}
								disabled={termination.isPending}
								type="button"
							>
								{termination.isPending ? (
									<LoaderCircle className="size-icon-sm animate-spin" aria-hidden="true" />
								) : (
									<Trash2 className="size-icon-sm" aria-hidden="true" />
								)}
							</button>
						}
					/>
				</span>
			</TooltipTrigger>
			<TooltipContent side="bottom">
				{termination.isPending ? t("shell.killingSession") : t("shell.terminateSession")}
			</TooltipContent>
		</Tooltip>
	) : undefined;

	return (
		<SessionCardView
			action={action}
			branchAction={branchAction}
			branchIcon={<GitBranch aria-hidden="true" className="size-icon-2xs shrink-0" />}
			error={termination.error ?? undefined}
			externalLink={ProductExternalLink}
			footer={footer}
			interactive={interactive}
			labels={{
				formatTime: formatTimeCompact,
				intakeIssue: (id) => t("shell.intakeIssue", { id }),
				pr: pullRequestLabels(t),
				updatedAt: (timestamp: string) =>
					t("shell.lastMessageAt", { time: formatTimeCompact(timestamp) }),
			}}
			onOpen={onOpen}
			overlay={terminationOverlay}
			prs={summaries.map((pr) => ({
				commentCount: pr.review.unresolvedBy.reduce((count, reviewer) => count + reviewer.count, 0),
				number: pr.number,
				reviewers: Array.from(
					new Map(
						pr.review.unresolvedBy.map((reviewer) => [reviewer.reviewerId, reviewer]),
					).values(),
				).map((reviewer) => ({
					avatarUrl: scmUserAvatarUrl(pr.provider, prBrowserUrl(pr), reviewer.reviewerId),
					id: reviewer.reviewerId,
				})),
				state: pr.state,
				url: prBrowserUrl(pr),
			}))}
			renderAvatar={(provider) => <AgentAvatar provider={provider} />}
			session={toBoardSessionPresentation(session, t)}
			translate={translate}
			renderUsage={(usage) => (
				<Tooltip>
					<TooltipTrigger asChild>
						<SessionUsageMetricView usage={usage} />
					</TooltipTrigger>
					<TooltipContent side="top">{usage.accessibleLabel}</TooltipContent>
				</Tooltip>
			)}
			usage={usagePresentation}
		/>
	);
}

function pullRequestLabels(t: TFunction): BoardPullRequestLabels {
	return {
		short: t("pr.short"),
		states: {
			closed: t("pr.state.closed"),
			draft: t("pr.state.draft"),
			merged: t("pr.state.merged"),
			open: t("pr.state.open"),
		},
	};
}

// Keep the board metric scannable by showing cost only. The full cost/token
// summary remains available from the hover tooltip and to screen readers.
function toUsagePresentation(
	usage: SessionUsageSummary | undefined,
	t: TFunction,
): BoardUsagePresentation | undefined {
	const processedTokens = usage?.processedTokens ?? null;
	if (!usage) {
		return undefined;
	}
	const cost = formatEstimatedCost(usage.estimatedCost);
	if (!cost) {
		if (processedTokens === null || processedTokens <= 0) {
			return undefined;
		}
		const compactTokens = formatTokenCount(processedTokens).replace(/ tok$/, "");
		const accessibleTokens = t("shell.usageTokens", {
			count: processedTokens.toLocaleString("en-US"),
		});
		return {
			accessibleLabel: accessibleTokens,
			compactLabel: compactTokens,
		};
	}
	if (processedTokens === null) {
		return { accessibleLabel: cost, compactLabel: cost };
	}
	const accessibleTokens = t("shell.usageTokens", {
		count: processedTokens.toLocaleString("en-US"),
	});
	return {
		accessibleLabel: `${cost} · ${accessibleTokens}`,
		compactLabel: cost,
	};
}

function ArchiveRestoreButton({
	label,
	onClick,
	isRestoring,
	isDisabled,
}: {
	label: string;
	onClick: (event: MouseEvent<HTMLButtonElement>) => void;
	isRestoring: boolean;
	isDisabled: boolean;
}) {
	const { t } = useTranslation();
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span className="inline-flex">
					<button
						aria-label={label}
						className="grid size-control-board-sm shrink-0 place-items-center rounded-md text-passive transition-colors hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 disabled:cursor-not-allowed disabled:opacity-35"
						disabled={isDisabled}
						onClick={onClick}
						type="button"
					>
						<RotateCcw className={cn("size-icon-md", isRestoring && "animate-spin")} aria-hidden="true" />
					</button>
				</span>
			</TooltipTrigger>
			<TooltipContent side="top">
				{isRestoring ? t("shell.restoringSession") : t("shell.restoreSession")}
			</TooltipContent>
		</Tooltip>
	);
}

function ArchiveRestoreError({ message }: { message?: string }) {
	return message ? (
		<div className="border-t border-border px-2 py-1.5 text-2xs text-destructive" role="alert">
			{message}
		</div>
	) : null;
}

function CopyActionButton({ label, value }: { label: string; value: string }) {
	const [copied, setCopied] = useState(false);
	const copiedTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	useEffect(
		() => () => {
			if (copiedTimeoutRef.current !== null) clearTimeout(copiedTimeoutRef.current);
		},
		[],
	);
	const buttonLabel = copied ? `Copied ${label}` : `Copy ${label}`;
	const copyValue = async (event: MouseEvent<HTMLButtonElement>) => {
		event.stopPropagation();
		try {
			await aoBridge.clipboard.writeText(value);
		} catch {
			return;
		}
		setCopied(true);
		if (copiedTimeoutRef.current !== null) clearTimeout(copiedTimeoutRef.current);
		copiedTimeoutRef.current = setTimeout(() => {
			setCopied(false);
			copiedTimeoutRef.current = null;
		}, 1_500);
	};
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					aria-label={buttonLabel}
					className="inline-flex size-4 shrink-0 items-center justify-center rounded-sm text-passive transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
					onClick={(event) => void copyValue(event)}
					type="button"
				>
					{copied ? (
						<Check className="size-icon-2xs text-success" aria-hidden="true" />
					) : (
						<Copy className="size-icon-2xs" aria-hidden="true" />
					)}
				</button>
			</TooltipTrigger>
			<TooltipContent side="bottom">{buttonLabel}</TooltipContent>
		</Tooltip>
	);
}
