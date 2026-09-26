/**
 * How full the conversation is, and whether the account is near a quota wall.
 *
 * This replaces a bare token total in the chat composer. A total on its own is a
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
import { formatTokenCount } from "../../lib/format-token-count";
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
const RING: Record<Severity, string> = {
	normal: "text-logo-accent",
	warn: "text-status-needs-you",
	critical: "text-status-exited",
};

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
	const hasContext = usage !== undefined && (usage.contextUsed > 0 || usage.contextWindow > 0);
	// Only surfaced once it is actionable. A quota readout that is always on screen
	// becomes furniture, and this one has to be noticed on the day it matters.
	const showQuota = quota !== undefined && quota.percent >= QUOTA_WARN;

	if (!hasContext && !showQuota) return null;

	return (
		// Scoped provider, as IntakeFields does: this component is rendered in surfaces
		// that do not all sit under the route-level one, and a tooltip with no provider
		// throws rather than degrading.
		<TooltipProvider>
			<div className={cn("flex shrink-0 items-center gap-2", className)}>
				{usage && hasContext ? <ContextReadout usage={usage} /> : null}
				{showQuota && quota ? <QuotaWarning quota={quota} limits={rateLimits} /> : null}
			</div>
		</TooltipProvider>
	);
}

function ContextReadout({ usage }: { usage: ConversationUsage }) {
	const { contextUsed, contextWindow } = usage;
	// A border separates the tooltip without the layered shadow's hover halos.
	const tooltipSurface = "border border-border shadow-none";

	// Failed turns can report 0 used with a valid model limit. Until there is a
	// positive reading, drawing 0% would claim headroom we have not measured.
	if (contextUsed <= 0 || contextWindow <= 0) {
		const used = contextUsed > 0 ? contextUsed.toLocaleString() : "unknown";
		const total = contextWindow > 0 ? contextWindow.toLocaleString() : "unknown";
		const compactUsed = contextUsed > 0 ? formatTokenCount(contextUsed).replace(/ tok$/, "") : "unknown";
		const compactTotal = contextWindow > 0 ? formatTokenCount(contextWindow).replace(/ tok$/, "") : "unknown";
		return (
			<Tooltip>
				<TooltipTrigger asChild>
					<span
						role="img"
						aria-label={`Context ${used} / ${total}`}
						className="relative inline-flex size-7 shrink-0 items-center justify-center rounded-full text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
						data-context-meter=""
						tabIndex={0}
					>
						<svg aria-hidden="true" className="size-5" viewBox="0 0 24 24">
							<circle cx="12" cy="12" fill="none" r="9" stroke="currentColor" strokeWidth="3" />
						</svg>
						<span aria-hidden="true" className="absolute text-[10px]">?</span>
					</span>
				</TooltipTrigger>
				<TooltipContent className={tooltipSurface}>
					<p className="font-medium">Context {compactUsed} / {compactTotal}</p>
					<p>{contextUsed <= 0 ? "The provider has not reported current context used." : "The provider has not reported a context window."} Conversation fullness is unknown.</p>
				</TooltipContent>
			</Tooltip>
		);
	}

	// Clamped because a provider that reports slightly over its own window should
	// render as full rather than overflow the track.
	const fraction = Math.min(1, Math.max(0, contextUsed / contextWindow));
	const percent = Math.round(fraction * 100);
	const severity = contextSeverity(fraction);
	const exactUsage = `${contextUsed.toLocaleString()} / ${contextWindow.toLocaleString()}`;
	const compactUsage = `${formatTokenCount(contextUsed).replace(/ tok$/, "")} / ${formatTokenCount(contextWindow).replace(/ tok$/, "")}`;

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<div
					className={cn("inline-flex size-7 shrink-0 items-center justify-center rounded-full focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring", RING[severity])}
					data-context-meter=""
					// A progressbar rather than a bare span: the fill is the primary
					// encoding, so a screen reader has to get the same value sighted
					// users get from its length.
					role="progressbar"
					tabIndex={0}
					aria-valuemin={0}
					aria-valuemax={100}
					aria-valuenow={percent}
					aria-label="Context window used"
					aria-valuetext={`${exactUsage} tokens (${percent}%)`}
				>
					<svg aria-hidden="true" className="size-5 -rotate-90" viewBox="0 0 24 24">
						<circle cx="12" cy="12" fill="none" r="9" stroke="currentColor" strokeWidth="3" className="text-border" />
						<circle
							cx="12" cy="12" fill="none" r="9" stroke="currentColor" strokeWidth="3"
							strokeLinecap="round" pathLength="100"
							strokeDasharray={`${Math.max(fraction * 100, 8)} 100`}
							className="transition-[stroke-dasharray] duration-300"
						/>
					</svg>
				</div>
			</TooltipTrigger>
			<TooltipContent className={tooltipSurface}>
				<p className="font-medium">Context window</p>
				<p className="tabular-nums">{compactUsage} tokens ({percent}%)</p>
				{severity !== "normal" ? (
					<p className="mt-1">
						{severity === "critical"
							? "The next turn may not fit. Compacting or starting a new conversation will reclaim room."
							: "Room is running low. A long task may not fit."}
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
