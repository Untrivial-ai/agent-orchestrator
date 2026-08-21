import { AlertTriangle, Gauge, RefreshCw } from "lucide-react";
import type { TFunction } from "i18next";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import type { ProviderQuota, QuotaHistoryPoint } from "../../hooks/useProviderQuota";
import { useProviderQuota, useQuotaHistory, useRefreshAllProviderQuota, useRefreshProviderQuota } from "../../hooks/useProviderQuota";
import { cn } from "../../lib/utils";
import { CenterPanelShell } from "../CenterPanelShell";
import { Button } from "../ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "../ui/card";

const severityColor: Record<string, string> = {
	normal: "bg-logo-accent",
	warning: "bg-status-needs-you",
	critical: "bg-status-exited",
	exhausted: "bg-status-exited",
	unknown: "bg-muted-foreground/40",
};

const severityText: Record<string, string> = {
	normal: "text-muted-foreground",
	warning: "text-status-needs-you",
	critical: "text-status-exited",
	exhausted: "text-status-exited",
	unknown: "text-muted-foreground",
};

const statusKeys = {
	normal: "planUsage.status.normal",
	warning: "planUsage.status.warning",
	critical: "planUsage.status.critical",
	exhausted: "planUsage.status.exhausted",
	unknown: "planUsage.status.unknown",
} as const;

export function PlanUsagePage() {
	const { t } = useTranslation();
	const query = useProviderQuota();
	const refreshAll = useRefreshAllProviderQuota();
	useEffect(() => {
		refreshAll.mutate();
	}, [refreshAll.mutate]);
	return (
		<CenterPanelShell>
			<div className="h-full overflow-y-auto" data-testid="plan-usage-page">
				<div className="mx-auto flex w-full max-w-(--size-settings-content-width) flex-col gap-6 px-(--size-settings-panel-padding-x) pb-(--size-settings-panel-padding-bottom) pt-12">
					<header className="flex items-start gap-3 border-b border-border pb-5">
						<div className="mt-0.5 grid size-9 shrink-0 place-items-center rounded-lg border border-border bg-surface text-muted-foreground">
							<Gauge aria-hidden="true" className="size-4.5" />
						</div>
						<div className="min-w-0">
							<h1 className="text-heading font-bold tracking-tight-xl text-foreground">{t("planUsage.title")}</h1>
							<p className="mt-1 max-w-2xl text-md-sm leading-5 text-passive">{t("planUsage.description")}</p>
						</div>
					</header>

					{query.isLoading ? <PlanUsageSkeleton /> : null}
					{query.isError ? (
						<div className="rounded-lg border border-status-exited/30 bg-status-exited/5 p-4 text-sm text-status-exited" role="alert">
							{query.error instanceof Error ? query.error.message : t("planUsage.loadError")}
						</div>
					) : null}
					{query.isSuccess && query.data.length === 0 ? <EmptyPlanUsage /> : null}
					<div className="flex flex-col gap-4">
						{query.data?.map((provider) => <ProviderQuotaCard key={`${provider.provider}:${provider.accountId}`} quota={provider} />)}
					</div>
					<p className="border-t border-border pt-4 text-xs leading-relaxed text-passive">{t("planUsage.disclaimer")}</p>
				</div>
			</div>
		</CenterPanelShell>
	);
}

function ProviderQuotaCard({ quota }: { quota: ProviderQuota }) {
	const { t } = useTranslation();
	const refresh = useRefreshProviderQuota(quota.provider, quota.accountId);
	const history = useQuotaHistory(quota.provider, quota.accountId, quota.capabilities.supportsHistory);
	const title = quota.accountLabel || providerName(quota.provider);
	return (
		<Card className="gap-0 border border-border-strong bg-surface py-0 shadow-none ring-0">
			<CardHeader className="border-b border-border px-4 py-3.5">
				<CardTitle className="flex items-center gap-2">
					<span>{title}</span>
					{quota.planType ? <span className="rounded-full border border-border bg-raised px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-passive">{quota.planType}</span> : null}
				</CardTitle>
				<CardDescription className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
					<span className={severityText[quota.severity]}>{statusLabel(quota.severity, t)}</span>
					<span aria-hidden="true">·</span>
					<span>{freshnessLabel(quota.freshness, quota.observedAt, t)}</span>
					{quota.completeness === "partial" ? <><span aria-hidden="true">·</span><span>{t("planUsage.partial")}</span></> : null}
				</CardDescription>
				{quota.capabilities.supportsRead ? (
					<CardAction>
						<Button aria-label={t("planUsage.refreshProvider", { provider: title })} disabled={refresh.isPending} onClick={() => refresh.mutate()} size="icon-sm" variant="ghost">
							<RefreshCw aria-hidden="true" className={cn("size-3.5", refresh.isPending && "animate-spin")} />
						</Button>
					</CardAction>
				) : null}
			</CardHeader>
			<CardContent className="flex flex-col gap-4 px-4 py-4">
				{quota.refreshError ? <p className="rounded-md bg-status-exited/5 px-3 py-2 text-xs text-status-exited" role="alert">{t("planUsage.lastRefreshError", { error: quota.refreshError })}</p> : null}
				{refresh.isError ? <p className="text-xs text-status-exited" role="alert">{refresh.error instanceof Error ? refresh.error.message : t("planUsage.refreshError")}</p> : null}
				{quota.limits.length === 0 ? <p className="text-sm text-muted-foreground">{t("planUsage.waiting")}</p> : (
					<div className="grid gap-3 sm:grid-cols-2">
						{quota.limits.map((limit) => <QuotaLimitBar key={`${limit.id}:${limit.windowType}:${limit.scope}:${limit.scopeId ?? ""}`} limit={limit} />)}
					</div>
				)}
				{quota.balances.length > 0 ? (
					<div className="border-t border-border pt-3">
						<p className="mb-2 text-xs font-medium text-muted-foreground">{t("planUsage.credits")}</p>
						<div className="grid gap-2 sm:grid-cols-2">
							{quota.balances.map((balance) => <div className="rounded-md bg-muted/50 px-3 py-2" key={balance.id}><p className="text-xs text-muted-foreground">{balance.name || balance.id}</p><p className="mt-0.5 text-sm font-medium tabular-nums">{balance.unlimited ? t("planUsage.unlimited") : balance.value || t("planUsage.unavailable")}</p></div>)}
						</div>
					</div>
				) : null}
				{quota.capabilities.supportsHistory ? <QuotaHistory points={history.data ?? []} /> : null}
			</CardContent>
		</Card>
	);
}

function QuotaLimitBar({ limit }: { limit: ProviderQuota["limits"][number] }) {
	const { t } = useTranslation();
	const used = limit.usedPercent == null ? null : Math.min(100, Math.max(0, limit.usedPercent));
	const remaining = limit.remainingPercent;
	const label = limit.name || humanize(limit.id);
	return (
		<div className="rounded-lg border border-border bg-background/45 p-3">
			<div className="mb-1.5 flex items-start justify-between gap-3">
				<div className="min-w-0"><p className="truncate text-sm font-medium">{label}</p><p className="text-xs text-passive">{windowLabel(limit.windowDurationSeconds ?? undefined, limit.windowType)}</p></div>
				<p className={cn("text-right text-sm font-medium tabular-nums", severityText[limit.severity])}>{remaining == null ? t("planUsage.unavailable") : t("planUsage.remaining", { percent: Math.round(remaining) })}</p>
			</div>
			<div className="h-1.5 overflow-hidden rounded-full bg-border" role="progressbar" aria-label={t("planUsage.usedAria", { name: label })} aria-valuemin={0} aria-valuemax={100} aria-valuenow={used ?? undefined}>
				{used != null ? <div className={cn("h-full rounded-full transition-[width] duration-300", severityColor[limit.severity])} style={{ width: `${Math.max(used, used > 0 ? 2 : 0)}%` }} /> : null}
			</div>
			<div className="mt-1.5 flex justify-between gap-2 text-xs text-passive">
				<span>{used == null ? t("planUsage.notReported") : t("planUsage.used", { percent: Math.round(used) })}</span>
				<span>{limit.resetsAt ? t("planUsage.resets", { time: formatReset(limit.resetsAt) }) : t("planUsage.resetUnknown")}</span>
			</div>
			{limit.reachedReason ? <p className="mt-2 flex items-center gap-1.5 text-xs text-status-exited"><AlertTriangle aria-hidden="true" className="size-3.5" />{humanize(limit.reachedReason)}</p> : null}
		</div>
	);
}

function QuotaHistory({ points }: { points: QuotaHistoryPoint[] }) {
	const { t } = useTranslation();
	const values = points.filter((point): point is QuotaHistoryPoint & { usedPercent: number } => typeof point.usedPercent === "number");
	if (values.length < 2) return null;
	const width = 320, height = 64;
	const path = values.map((point, index) => `${index === 0 ? "M" : "L"}${(index / Math.max(1, values.length - 1)) * width},${height - (point.usedPercent / 100) * height}`).join(" ");
	return <div className="border-t border-border pt-3"><p className="mb-2 text-xs font-medium text-passive">{t("planUsage.history")}</p><div className="rounded-lg border border-border bg-background/45 px-3 py-2"><svg aria-label={t("planUsage.historyAria")} className="h-16 w-full overflow-visible" role="img" viewBox={`0 0 ${width} ${height}`}><path d={path} fill="none" stroke="currentColor" strokeWidth="2" className="text-logo-accent" vectorEffect="non-scaling-stroke" /></svg></div></div>;
}

function EmptyPlanUsage() {
	const { t } = useTranslation();
	return <div className="rounded-xl border border-dashed border-border p-8 text-center"><Gauge aria-hidden="true" className="mx-auto size-8 text-muted-foreground" /><h2 className="mt-3 text-sm font-medium">{t("planUsage.emptyTitle")}</h2><p className="mx-auto mt-1 max-w-md text-sm text-muted-foreground">{t("planUsage.emptyDescription")}</p></div>;
}

function PlanUsageSkeleton() { const { t } = useTranslation(); return <div className="flex flex-col gap-4" aria-label={t("planUsage.loading")}><div className="h-72 animate-pulse rounded-lg bg-muted" /><div className="h-48 animate-pulse rounded-lg bg-muted" /></div>; }

function providerName(provider: string) { return provider === "codex" ? "Codex" : provider === "claude" ? "Claude" : humanize(provider); }
function humanize(value: string) { return value.replace(/[_-]+/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase()); }
function windowLabel(seconds?: number, fallback?: string) { if (!seconds) return fallback ? humanize(fallback) : "Usage window"; const hours = seconds / 3600; return hours >= 24 ? `${Math.round(hours / 24)}-day window` : `${Math.round(hours)}-hour window`; }
function formatReset(value: string) { const seconds = Math.max(0, (new Date(value).getTime() - Date.now()) / 1000); if (seconds < 60) return "now"; if (seconds < 3600) return `${Math.ceil(seconds / 60)}m`; if (seconds < 86400) return `${Math.ceil(seconds / 3600)}h`; return `${Math.ceil(seconds / 86400)}d`; }
function freshnessLabel(freshness: string, observedAt: string, t: TFunction) { if (freshness === "stale") return t("planUsage.stale"); return t("planUsage.updated", { time: new Intl.RelativeTimeFormat(undefined, { numeric: "auto" }).format(Math.round((new Date(observedAt).getTime() - Date.now()) / 60000), "minute") }); }
function statusLabel(status: string, t: TFunction) { return t(statusKeys[status as keyof typeof statusKeys] ?? statusKeys.unknown); }
