/**
 * How full the conversation is, and whether the account is near a quota wall.
 *
 * This replaces a bare token total in the chat header. A total on its own is a
 * number with no scale: it cannot answer either question a user actually has,
 * which is "when will this conversation stop working" and "why did that turn fail
 * for a reason unrelated to what I asked". Both failures are otherwise
 * undiagnosable from the UI, which is what makes them worth a permanent readout
 * rather than an error message after the fact.
 *
 * State is encoded in form as well as in number: a fill that grows, and a colour
 * that shifts at thresholds. Reading the digits is then optional rather than
 * required, which matters for something glanced at rather than studied.
 */

import { AlertTriangle } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../ui/tooltip";
import type { ConversationRateLimits, ConversationUsage } from "../../types/conversation";

/**
 * Where the fill changes colour.
 *
 * Chosen for what a user can still do at each point rather than for round
 * numbers. Below 70% a long conversation is unremarkable. From 70% it is worth
 * knowing before starting a large task, since the remaining room is no longer
 * most of the window. From 90% the next turn is genuinely at risk, which is a
 * different message from "getting full" and gets the alarm colour.
 */
const CONTEXT_WARN = 0.7;
const CONTEXT_CRITICAL = 0.9;

/**
 * Quota thresholds, deliberately tighter than the context ones.
 *
 * A full context degrades gracefully: history gets compacted and the conversation
 * continues. Exhausted quota does not degrade at all, it just stops, and it stops
 * for a window measured in days on the accounts AO sees. So the warning arrives
 * earlier, while the user still has the option of pacing themselves.
 */
const QUOTA_WARN = 75;
const QUOTA_CRITICAL = 90;

type Severity = "normal" | "warn" | "critical";

function contextSeverity(fraction: number): Severity {
	if (fraction >= CONTEXT_CRITICAL) return "critical";
	if (fraction >= CONTEXT_WARN) return "warn";
	return "normal";
}

function quotaSeverity(percent: number): Severity {
	if (percent >= QUOTA_CRITICAL) return "critical";
	if (percent >= QUOTA_WARN) return "warn";
	return "normal";
}

/**
 * Normal usage is informational rather than session activity, so it uses AO's
 * logo blue. Warning and critical retain the established status colours: amber
 * means "a human should look", and red means the next turn is at risk.
 */
const FILL: Record<Severity, string> = {
	normal: "bg-logo-accent",
	warn: "bg-status-needs-you",
	critical: "bg-status-exited",
};

const TEXT: Record<Severity, string> = {
	normal: "text-muted-foreground",
	warn: "text-status-needs-you",
	critical: "text-status-exited",
};

/** Compact token counts. 18055 reads as 18.1k; the exact figure is in the tooltip. */
function formatTokens(tokens: number): string {
	if (tokens < 1000) return String(tokens);
	if (tokens < 1_000_000) {
		const thousands = tokens / 1000;
		// Keep one decimal below 100k, where the difference between 18.1k and 18k is
		// still a meaningful fraction of the window.
		return `${thousands < 100 ? thousands.toFixed(1) : Math.round(thousands)}k`;
	}
	return `${(tokens / 1_000_000).toFixed(1)}M`;
}

function formatCost(amount: number, currency = "USD"): string {
	try {
		return new Intl.NumberFormat(undefined, {
			style: "currency",
			currency,
			minimumFractionDigits: amount < 1 ? 3 : 2,
			maximumFractionDigits: amount < 1 ? 4 : 2,
		}).format(amount);
	} catch {
		return `${amount.toFixed(3)} ${currency}`;
	}
}

/**
 * A remaining duration as the largest useful unit. The provider's windows are
 * measured in days, so minute precision on a four-day reset would be noise.
 */
function formatResetIn(seconds: number): string {
	if (seconds <= 0) return "now";
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${Math.max(1, minutes)}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `${hours}h`;
	return `${Math.floor(hours / 24)}d`;
}

/**
 * The tighter of the provider's two windows, or undefined when it reported
 * neither. Negative is the daemon's "not reported" signal and must not be drawn:
 * a meter running backwards is worse than no meter.
 */
function worstWindow(
	limits: ConversationRateLimits,
): { percent: number; resetsIn?: number } | undefined {
	const windows = [
		{ percent: limits.primaryUsedPercent, resetsIn: limits.primaryResetsInSeconds },
		{ percent: limits.secondaryUsedPercent, resetsIn: limits.secondaryResetsInSeconds },
	].filter((w) => typeof w.percent === "number" && w.percent >= 0);
	if (windows.length === 0) return undefined;
	return windows.reduce((worst, w) => (w.percent > worst.percent ? w : worst));
}

/* -------------------------------------------------------------------------- */

export function ContextMeter({
	usage,
	rateLimits,
	className,
}: {
	usage?: ConversationUsage;
	rateLimits?: ConversationRateLimits;
	className?: string;
}) {
	const quota = rateLimits ? worstWindow(rateLimits) : undefined;
	// Only surfaced once it is actionable. A quota readout that is always on screen
	// becomes furniture, and this one has to be noticed on the day it matters.
	const showQuota = quota !== undefined && quota.percent >= QUOTA_WARN;

	if (!usage && !showQuota) return null;

	return (
		// Scoped provider, as IntakeFields does: this component is rendered in surfaces
		// that do not all sit under the route-level one, and a tooltip with no provider
		// throws rather than degrading.
		<TooltipProvider>
			<div className={cn("flex shrink-0 items-center gap-2", className)}>
				{usage ? <ContextReadout usage={usage} /> : null}
				{showQuota && quota ? <QuotaWarning quota={quota} limits={rateLimits} /> : null}
			</div>
		</TooltipProvider>
	);
}

function ContextReadout({ usage }: { usage: ConversationUsage }) {
	const { contextUsed, contextWindow } = usage;

	// No window means no honest fullness to draw. The tokens are still worth
	// showing -- they are what the header showed before -- but without a bar
	// implying a scale the provider never gave.
	if (!contextWindow || contextWindow <= 0) {
		return (
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="tabular-nums text-[11px] text-muted-foreground">
						{formatTokens(contextUsed || usage.totalTokens)} tokens
					</span>
				</TooltipTrigger>
				<TooltipContent>
					<p>This model does not report a context window, so how full the conversation is
					is unknown.</p>
					{usage.cost != null ? <p className="mt-1 tabular-nums">Provider-reported cost: {formatCost(usage.cost, usage.currency)}</p> : null}
				</TooltipContent>
			</Tooltip>
		);
	}

	// Clamped because a provider that reports slightly over its own window should
	// render as full rather than overflow the track.
	const fraction = Math.min(1, Math.max(0, contextUsed / contextWindow));
	const percent = Math.round(fraction * 100);
	const severity = contextSeverity(fraction);

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<div
					className="flex items-center gap-1.5"
					// A progressbar rather than a bare span: the fill is the primary
					// encoding, so a screen reader has to get the same value sighted
					// users get from its length.
					role="progressbar"
					aria-valuemin={0}
					aria-valuemax={100}
					aria-valuenow={percent}
					aria-label="Context window used"
				>
					<div className="h-1.5 w-16 overflow-hidden rounded-full bg-border">
						<div
							className={cn("h-full rounded-full transition-[width] duration-300", FILL[severity])}
							style={{ width: `${Math.max(fraction * 100, 2)}%` }}
						/>
					</div>
					<span className={cn("tabular-nums text-[11px]", TEXT[severity])}>{percent}%</span>
				</div>
			</TooltipTrigger>
			<TooltipContent>
				<p className="tabular-nums">
					{contextUsed.toLocaleString()} of {contextWindow.toLocaleString()} tokens of context
					used
				</p>
				{severity !== "normal" ? (
					<p className="mt-1">
						{severity === "critical"
							? "The next turn may not fit. Compacting or starting a new conversation will reclaim room."
							: "Room is running low. A long task may not fit."}
					</p>
				) : null}
				{usage.totalTokens > 0 ? (
					<p className="mt-1 tabular-nums text-muted-foreground">
						{usage.totalTokens.toLocaleString()} tokens spent in total
					</p>
				) : null}
				{usage.cost != null ? (
					<p className="mt-1 tabular-nums text-muted-foreground">
						Provider-reported cost: {formatCost(usage.cost, usage.currency)}
					</p>
				) : null}
			</TooltipContent>
		</Tooltip>
	);
}

function QuotaWarning({
	quota,
	limits,
}: {
	quota: { percent: number; resetsIn?: number };
	limits?: ConversationRateLimits;
}) {
	const severity = quotaSeverity(quota.percent);
	const percent = Math.round(quota.percent);
	const resets = quota.resetsIn && quota.resetsIn > 0 ? formatResetIn(quota.resetsIn) : undefined;

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span
					className={cn(
						"flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px] tabular-nums",
						severity === "critical"
							? "border-status-exited/40 text-status-exited"
							: "border-status-needs-you/40 text-status-needs-you",
					)}
				>
					<AlertTriangle aria-hidden="true" className="size-3" />
					{percent}% quota
				</span>
			</TooltipTrigger>
			<TooltipContent>
				<p>
					This account has used {percent}% of its
					{limits?.planLabel ? ` ${limits.planLabel}` : ""} rate limit
					{resets ? `, which resets in ${resets}` : ""}.
				</p>
				{/* Named explicitly because this is the failure a user cannot otherwise
				    explain: the turn was fine, the account was not. */}
				<p className="mt-1">
					{severity === "critical"
						? "Turns may start failing for reasons unrelated to what you asked."
						: "If this reaches the limit, turns will fail until the window resets."}
				</p>
			</TooltipContent>
		</Tooltip>
	);
}
