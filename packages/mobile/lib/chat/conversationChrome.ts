import type { ConversationRateLimits, ConversationUsage, McpServer } from "./types";

export type Severity = "normal" | "warn" | "critical";

export function contextReadout(usage?: ConversationUsage): {
	percent?: number;
	fillPercent?: number;
	severity: Severity;
} | undefined {
	if (!usage) return undefined;
	if (usage.contextWindow <= 0 || usage.contextUsed <= 0) return { severity: "normal" };
	const fraction = Math.min(1, Math.max(0, usage.contextUsed / usage.contextWindow));
	return {
		percent: Math.round(fraction * 100),
		fillPercent: Math.max(2, fraction * 100),
		severity: fraction >= 0.9 ? "critical" : fraction >= 0.7 ? "warn" : "normal",
	};
}

export function contextUsageLabel(usage?: ConversationUsage): string {
	return usage && usage.contextWindow > 0 && usage.contextUsed > 0
		? `${usage.contextUsed.toLocaleString()} / ${usage.contextWindow.toLocaleString()} context tokens`
		: "Context unavailable";
}

export function compactContextUsageLabel(usage?: ConversationUsage): string {
	if (!usage || usage.contextWindow <= 0 || usage.contextUsed <= 0) return "Context unavailable";
	return `${compactTokenCount(usage.contextUsed)} / ${compactTokenCount(usage.contextWindow)} context tokens`;
}

export function compactTokenCount(tokens: number): string {
	if (tokens < 1_000) return String(tokens);
	let unit = tokens >= 1_000_000_000 ? 1_000_000_000 : tokens >= 1_000_000 ? 1_000_000 : 1_000;
	let rounded = Number((tokens / unit).toFixed(1));
	if (rounded >= 1_000 && unit < 1_000_000_000) {
		unit *= 1_000;
		rounded = Number((tokens / unit).toFixed(1));
	}
	return `${rounded}${unit === 1_000_000_000 ? "B" : unit === 1_000_000 ? "M" : "K"}`;
}

export function quotaWarning(limits?: ConversationRateLimits): {
	percent: number;
	severity: Exclude<Severity, "normal">;
	resetsInSeconds?: number;
	planLabel?: string;
} | undefined {
	if (!limits) return undefined;
	const windows = [
		{ percent: limits.primaryUsedPercent, resetsInSeconds: limits.primaryResetsInSeconds },
		{ percent: limits.secondaryUsedPercent, resetsInSeconds: limits.secondaryResetsInSeconds },
	].filter((window) => Number.isFinite(window.percent) && window.percent >= 0);
	if (!windows.length) return undefined;
	const worst = windows.reduce((current, candidate) => candidate.percent > current.percent ? candidate : current);
	if (worst.percent < 75) return undefined;
	return {
		percent: Math.round(worst.percent),
		severity: worst.percent >= 90 ? "critical" : "warn",
		resetsInSeconds: worst.resetsInSeconds,
		planLabel: limits.planLabel,
	};
}

export function elapsedLabel(startedAt: string | undefined, nowMs: number): string | undefined {
	if (!startedAt) return undefined;
	const elapsed = Math.max(0, nowMs - Date.parse(startedAt));
	if (!Number.isFinite(elapsed)) return undefined;
	const seconds = Math.floor(elapsed / 1000);
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
	return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function workingElapsedLabel(startedAt: string | undefined, nowMs: number): string | undefined {
	const label = elapsedLabel(startedAt, nowMs);
	return label === "0s" ? "1s" : label;
}

export function resetLabel(seconds?: number): string | undefined {
	if (seconds === undefined || seconds < 0) return undefined;
	if (seconds < 60) return `${Math.ceil(seconds)}s`;
	if (seconds < 3600) return `${Math.ceil(seconds / 60)}m`;
	if (seconds < 86_400) return `${Math.ceil(seconds / 3600)}h`;
	return `${Math.ceil(seconds / 86_400)}d`;
}

export function mcpServerFailureLabel(server: McpServer): string {
	const details = [server.failureReason, server.error].filter((value): value is string => Boolean(value?.trim()));
	return `${server.name}${details.length ? ` (${details.join(": ")})` : ""}`;
}
