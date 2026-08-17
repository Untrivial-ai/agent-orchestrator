import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId, useState, type ReactNode } from "react";
import type { TFunction } from "i18next";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
	InspectorActivityTimelineView,
	InspectorPullRequestCardView,
	InspectorReviewsView,
	InspectorSection as Section,
	SessionInspectorShellView,
	SessionInspectorSummaryView,
	inspectorEmptyClass,
	type InspectorPullRequest,
	type InspectorInlineComment,
	type InspectorGithubReview,
	type InspectorReviewGroup,
	type InspectorReviewLabels,
	type InspectorTimelineEvent,
	type InspectorView,
} from "@aoagents/product-ui";
import {
	ArrowUpRight,
	ChevronDown,
	ChevronRight,
	Files as FilesIcon,
	GitPullRequest,
	GitMerge,
	Info,
	Play,
	Trash2,
	Loader2,
	MessageSquare,
	X,
} from "lucide-react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { formatTimeCompact } from "../lib/format-time";
import { AgentAvatar } from "./AgentAvatar";
import { ProductExternalLink } from "./ProductExternalLink";
import {
	sessionScmSummaryQueryKey,
	useSessionScmSummary,
	type SessionPRSummary,
} from "../hooks/useSessionScmSummary";
import { useSessionUsage, type SessionUsage } from "../hooks/useSessionUsage";
import { useSessionWorkspaceFilesChangedCount } from "../hooks/useSessionWorkspaceFiles";
import { clearTerminateSessionState, useTerminateSession } from "../hooks/useTerminateSession";
import { prBrowserUrl, prCardPresentation, prNounKeys, sessionPRDisplaySummaries } from "../lib/pr-display";
import { formatTokenCount } from "../lib/format-token-count";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { findProjectOrchestrator, sortedPRs } from "../types/workspace";
import { getAgentActivityView, getSessionTimelinePillView } from "../lib/session-presentation";
import { aoBridge } from "../lib/bridge";
import { BrowserPanelView, type BrowserAnnotationQueueModel } from "./BrowserPanel";
import type { BrowserViewModel } from "../hooks/useBrowserView";
import { useUiStore } from "../stores/ui-store";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";
import { SessionTerminationPopover } from "./SessionTerminationPopover";
import { ReviewerSelect } from "./ReviewerSelect";
import { agentLabel } from "../lib/agent-options";
import { agentsQueryOptions } from "../hooks/useAgentsQuery";
import { Switch } from "./ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./ui/tooltip";
import { appI18n } from "../i18n";
import type { MessageKey } from "../i18n";
import { usesPreviewWorkspaceData as usePreviewData } from "../lib/preview-mode";
import {
	openReviewStatesFor,
	reviewIsRunning,
	reviewRunDisabled,
	reviewSessionRunAction,
	sessionReviewsQueryOptions,
	type PRReviewState,
	type ReviewRunFacts,
} from "../lib/session-reviews";

type ProjectConfig = components["schemas"]["ProjectConfig"];
type OpenReviewerTerminal = (target: { handleId: string; harness: string }) => void;

export type { InspectorView } from "@aoagents/product-ui";

const VIEW_DEFS: {
	id: InspectorView;
	labelKey: "inspector.summary" | "inspector.reviewTab" | "inspector.browser" | "inspector.files";
	icon: ReactNode;
}[] = [
	{
		id: "summary",
		labelKey: "inspector.summary",
		icon: (
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true">
				<line x1="8" y1="7" x2="20" y2="7" />
				<line x1="8" y1="12" x2="20" y2="12" />
				<line x1="8" y1="17" x2="16" y2="17" />
				<circle cx="4" cy="7" r="1" />
				<circle cx="4" cy="12" r="1" />
				<circle cx="4" cy="17" r="1" />
			</svg>
		),
	},
	{
		id: "reviews",
		labelKey: "inspector.reviewTab",
		icon: <MessageSquare aria-hidden="true" />,
	},
	{
		id: "browser",
		labelKey: "inspector.browser",
		icon: (
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true">
				<circle cx="12" cy="12" r="9" />
				<line x1="3" y1="12" x2="21" y2="12" />
				<path d="M12 3a14 14 0 0 1 0 18 14 14 0 0 1 0-18" />
			</svg>
		),
	},
	{
		id: "files",
		labelKey: "inspector.files",
		icon: <FilesIcon aria-hidden="true" />,
	},
];

const prStateLabelKeys: Record<SessionPRSummary["state"], MessageKey> = {
	open: "pr.state.open",
	draft: "pr.state.draft",
	merged: "pr.state.merged",
	closed: "pr.state.closed",
};

/**
 * Tabbed inspector rail beside the terminal (Summary · Reviews · Browser · Files).
 */
export function SessionInspector({
	session,
	onOpenReviewerTerminal,
	browserPoppedOut = false,
	browserAnnotationQueue,
	isInspectorVisible = true,
	onToggleBrowserPopOut,
	onOpenFiles,
	filesView,
	browserView,
	view: viewProp,
	onViewChange,
}: {
	session?: WorkspaceSession;
	onOpenReviewerTerminal?: OpenReviewerTerminal;
	browserPoppedOut?: boolean;
	browserAnnotationQueue?: BrowserAnnotationQueueModel;
	isInspectorVisible?: boolean;
	onToggleBrowserPopOut?: (next: boolean) => void;
	onOpenFiles?: () => void;
	filesView?: ReactNode;
	browserView?: BrowserViewModel;
	/** Controlled active tab. Omit to let the inspector own its own selection. */
	view?: InspectorView;
	onViewChange?: (view: InspectorView) => void;
}) {
	const { t } = useTranslation();
	const [internalView, setInternalView] = useState<InspectorView>("summary");
	const requestedView = viewProp ?? internalView;
	// Badge the Browser tab when a preview target arrived without us opening it.
	const browserUnseen = useUiStore((state) =>
		session ? Boolean(state.inspectorSessions[session.id]?.browserUnseen) : false,
	);
	const filesChangedCount = useSessionWorkspaceFilesChangedCount(session?.id);
	const setView = (next: InspectorView) => {
		setInternalView(next);
		onViewChange?.(next);
		if (next === "files") onOpenFiles?.();
	};
	const view: InspectorView = requestedView;
	const tabs = VIEW_DEFS.map((entry) => {
		const label = t(entry.labelKey);
		return {
			...entry,
			badge: entry.id === "browser" && browserUnseen,
			displayLabel:
				entry.id === "files" && filesChangedCount !== undefined
					? t("files.tabCount", { count: filesChangedCount })
					: label,
			label,
		};
	});
	return (
		<SessionInspectorShellView
			activeView={view}
			ariaLabel={t("inspector.aria")}
			browserPoppedOut={browserPoppedOut}
			browserView={
				session ? (
					<BrowserView
						browserPoppedOut={browserPoppedOut}
						browserAnnotationQueue={browserAnnotationQueue}
						browserView={browserView}
						isActive={isInspectorVisible && !browserPoppedOut}
						onTogglePopOut={onToggleBrowserPopOut}
						session={session}
					/>
				) : undefined
			}
			filesView={session ? <FilesView filesView={filesView} onOpenFiles={onOpenFiles} /> : undefined}
			headerActions={<span aria-hidden="true" className="session-inspector-actions-spacer" />}
			isVisible={isInspectorVisible}
			loadingText={session ? undefined : t("inspector.loadingSession")}
			onViewChange={setView}
			reviewsView={
				session ? <ReviewsView onOpenReviewerTerminal={onOpenReviewerTerminal} session={session} /> : undefined
			}
			summaryView={
				session ? <SummaryView onOpenReviews={() => setView("reviews")} session={session} /> : undefined
			}
			tabs={tabs}
		/>
	);
}

function SummaryView({ onOpenReviews, session }: { onOpenReviews: () => void; session: WorkspaceSession }) {
	const { t } = useTranslation();
	const query = useSessionScmSummary(session.id);
	const developerMode = useUiStore((state) => state.developerMode);
	const usageQuery = useSessionUsage(session.id, developerMode);
	const showUsage =
		developerMode &&
		!usageQuery.isLoading &&
		!usageQuery.isError &&
		hasMeaningfulSessionUsage(usageQuery.data);
	const showUsageError = developerMode && usageQuery.isError;
	const prSummaries = sessionPRDisplaySummaries(session, query.data);
	const prSectionTitle = prSummaries.length > 1 ? t("inspector.pullRequests", { count: prSummaries.length }) : t("inspector.pullRequest");
	const hasPRs = prSummaries.length > 0;
	return (
		<SessionInspectorSummaryView
			activity={
				<>
					<ActivityTimeline prs={prSummaries} session={session} />
					<ResumeAgentControl session={session} />
				</>
			}
			activityTitle={t("inspector.activity")}
			completion={<SessionControls session={session} />}
			pullRequestCards={
				<div className="flex flex-col gap-1.5">
					{hasPRs ? (
						prSummaries.map((pr) => (
							<PRSummaryCard
								key={pr.url || pr.htmlUrl || pr.number}
								onOpenReviews={onOpenReviews}
								pr={pr}
								sessionId={session.id}
							/>
						))
					) : (
						<p className={inspectorEmptyClass}>{t("inspector.noPROpened")}</p>
					)}
				</div>
			}
			pullRequestTitle={prSectionTitle}
			usage={
				showUsageError ? (
					<Section title={t("inspector.usage.title")}>
						<p className={inspectorEmptyClass} role="alert">
							{t("inspector.usage.totalTokensUnavailable")}
						</p>
					</Section>
				) : showUsage && usageQuery.data ? (
					<Section title={t("inspector.usage.title")}>
						<UsageCostTelemetry usage={usageQuery.data} />
					</Section>
				) : null
			}
		/>
	);
}

function ReviewsView({
	session,
	onOpenReviewerTerminal,
}: {
	session: WorkspaceSession;
	onOpenReviewerTerminal?: OpenReviewerTerminal;
}) {
	return (
		<div role="tabpanel">
			<ReviewsSection onOpenReviewerTerminal={onOpenReviewerTerminal} session={session} />
		</div>
	);
}

function InspectorPolicyRow({
	id,
	label,
	ariaLabel = label,
	description,
	tooltipClassName,
	checked,
	disabled,
	onCheckedChange,
}: {
	id: string;
	label: string;
	ariaLabel?: string;
	description?: string;
	tooltipClassName?: string;
	checked: boolean;
	disabled: boolean;
	onCheckedChange: (checked: boolean) => void;
}) {
	return (
		<div className="flex items-center justify-between gap-3 py-1" data-slot="inspector-policy-row">
			<div className="flex min-w-0 items-center gap-1.5">
				<label className="min-w-0 text-xs font-medium text-settings-label" htmlFor={id}>
					{label}
				</label>
				{description ? (
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								aria-label={description}
								className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
								type="button"
							>
								<Info aria-hidden="true" className="size-icon-2xs" />
							</button>
						</TooltipTrigger>
						<TooltipContent className={cn("leading-normal", tooltipClassName)}>{description}</TooltipContent>
					</Tooltip>
				) : null}
			</div>
			<Switch
				aria-label={ariaLabel}
				checked={checked}
				disabled={disabled}
				id={id}
				onCheckedChange={onCheckedChange}
				size="sm"
			/>
		</div>
	);
}

function UsageCostTelemetry({ usage }: { usage: SessionUsage }) {
	const { t } = useTranslation();
	const totalTokens = usageTokenTotal(usage.totals);
	const exactTotal = totalTokens?.toLocaleString("en-US");

	return (
		<div>
			<div className="grid grid-cols-2 gap-4">
				<div className="min-w-0">
					<p className="text-2xs text-settings-muted">{t("inspector.usage.totalTokens")}</p>
					<p
						aria-label={
							totalTokens === null
								? t("inspector.usage.totalTokensUnavailable")
								: t("inspector.usage.totalTokensAria", { count: exactTotal })
						}
						className="mt-0.5 truncate font-mono text-md-sm font-medium text-settings-label"
						title={totalTokens === null ? undefined : t("inspector.usage.tokensExact", { count: exactTotal })}
					>
						{totalTokens === null ? t("inspector.usage.noUsageYet") : formatTelemetryTokenValue(totalTokens)}
					</p>
				</div>
				<div className="min-w-0 text-right">
					<p className="text-2xs text-settings-muted">{t("inspector.usage.totalCost")}</p>
					<p
						className="mt-0.5 truncate text-sm-md text-settings-muted"
						title={t("inspector.usage.costComingSoon")}
					>
						{t("inspector.usage.comingSoon")}
					</p>
				</div>
			</div>

			<div className="mt-3">
				<div
					className="rounded-lg border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-2.5 py-2.5"
					data-testid="session-usage-metrics"
				>
					<UsageMetrics totals={usage.totals} />
				</div>
			</div>

			{usage.harnesses.length > 0 ? (
				<div className="mt-3 border-t border-(--color-border-settings-input) pt-2">
					<div className="grid grid-cols-[minmax(0,1fr)_4.5rem_5.5rem] items-center gap-2 px-1 pb-1 text-2xs text-settings-muted">
						<span>{t("inspector.usage.agent")}</span>
						<span className="text-right">{t("inspector.usage.tokens")}</span>
						<span className="text-right">{t("inspector.usage.cost")}</span>
					</div>
					{usage.harnesses.map((harness, index) => (
						<UsageProviderRow
							harness={harness}
							key={`${harness.harness}:${index}`}
						/>
					))}
				</div>
			) : null}
		</div>
	);
}

function AutoInjectCIPolicyControl({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [enabled, setEnabled] = useState(session.autoInjectCI ?? true);
	useEffect(() => {
		setEnabled(session.autoInjectCI ?? true);
	}, [session.id, session.autoInjectCI]);
	const save = useMutation({
		mutationFn: async (autoInjectCI: boolean) => {
			if (usePreviewData) return;
			const { error, response } = await apiClient.PATCH("/api/v1/sessions/{sessionId}/auto-inject-ci", {
				params: { path: { sessionId: session.id } },
				body: { autoInjectCI },
			});
			if (error) throw new Error(apiErrorMessage(error, t("inspector.ci.autoInjectError", { status: response.status })));
		},
		onMutate: async (autoInjectCI) => {
			await queryClient.cancelQueries({ queryKey: workspaceQueryKey });
			const previous = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
			queryClient.setQueryData<WorkspaceSummary[]>(workspaceQueryKey, (current) =>
				updateSessionAutoInjectCI(current, session.id, autoInjectCI),
			);
			return { previous };
		},
		onError: (_error, _next, context) => {
			setEnabled(session.autoInjectCI ?? true);
			if (context?.previous) queryClient.setQueryData(workspaceQueryKey, context.previous);
		},
		onSettled: () => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});
	const error = save.error instanceof Error ? save.error.message : null;

	return (
		<>
			<InspectorPolicyRow
				checked={enabled}
				description={t("inspector.ci.autoInjectDescription")}
				disabled={save.isPending}
				id={`auto-inject-ci-${session.id}`}
				label={t("inspector.ci.autoInject")}
				onCheckedChange={(next) => {
					setEnabled(next);
					save.mutate(next);
				}}
				tooltipClassName="max-w-64"
			/>
			{error ? (
				<p className="mt-1 text-2xs leading-normal text-error" role="status">
					{error}
				</p>
			) : null}
		</>
	);
}

function UsageProviderRow({ harness }: { harness: SessionUsage["harnesses"][number] }) {
	const { t } = useTranslation();
	const harnessName = formatHarnessName(harness.harness);

	return (
		<UsageDisclosureRow
			detailsLabel={t("inspector.usage.providerDetails", { name: harnessName })}
			name={harnessName}
			nameClassName="text-sm-md"
			regionLabel={t("inspector.usage.providerPeek", { name: harnessName })}
			totals={harness.totals}
		>
			<ProviderUsageDetails harness={harness} />
		</UsageDisclosureRow>
	);
}

function ProviderUsageDetails({ harness }: { harness: SessionUsage["harnesses"][number] }) {
	const { t } = useTranslation();

	return (
		<>
			<div className="pb-2">
				<UsageMetrics totals={harness.totals} />
			</div>

			<div className="border-t border-(--color-border-settings-input) pt-2">
				<div className="grid grid-cols-[minmax(0,1fr)_4.5rem_5.5rem] items-center gap-2 px-1 pb-1 text-2xs text-settings-muted">
					<span>{t("inspector.usage.models", { count: harness.models.length })}</span>
					<span className="text-right">{t("inspector.usage.tokens")}</span>
					<span className="text-right">{t("inspector.usage.cost")}</span>
				</div>
				{harness.models.length > 0 ? (
					harness.models.map((model, index) => (
						<UsageModelRow key={`${model.modelId}:${index}`} model={model} />
					))
				) : (
					<p className="px-1 py-2 text-2xs text-settings-muted">{t("inspector.usage.noModelTelemetry")}</p>
				)}
			</div>
		</>
	);
}

function AutoInjectReviewPolicyControl({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [enabled, setEnabled] = useState(session.autoInjectReview ?? true);
	useEffect(() => {
		setEnabled(session.autoInjectReview ?? true);
	}, [session.id, session.autoInjectReview]);
	const save = useMutation({
		mutationFn: async (autoInjectReview: boolean) => {
			const { error } = await apiClient.PATCH("/api/v1/sessions/{sessionId}/auto-inject-review", {
				params: { path: { sessionId: session.id } },
				body: { autoInjectReview },
			});
			if (error) throw new Error(apiErrorMessage(error, t("inspector.review.autoInjectError")));
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
		onError: () => {
			setEnabled(session.autoInjectReview ?? true);
		},
	});
	const error = save.error instanceof Error ? save.error.message : null;

	return (
		<>
			<InspectorPolicyRow
				checked={enabled}
				description={t("inspector.review.autoInjectDescription")}
				disabled={save.isPending}
				id={`auto-inject-review-${session.id}`}
				label={t("inspector.review.autoInject")}
				onCheckedChange={(next) => {
					setEnabled(next);
					save.mutate(next);
				}}
				tooltipClassName="max-w-60"
			/>
			{error ? (
				<p className="mt-1 text-2xs leading-normal text-error" role="status">
					{error}
				</p>
			) : null}
		</>
	);
}

function updateSessionAutoInjectCI(
	workspaces: WorkspaceSummary[] | undefined,
	sessionId: string,
	autoInjectCI: boolean,
): WorkspaceSummary[] | undefined {
	return workspaces?.map((workspace) => ({
		...workspace,
		sessions: workspace.sessions.map((candidate) =>
			candidate.id === sessionId ? { ...candidate, autoInjectCI } : candidate,
		),
	}));
}

function UsageModelRow({
	model,
}: {
	model: SessionUsage["harnesses"][number]["models"][number];
}) {
	const { t } = useTranslation();
	const modelName = model.modelId;

	return (
		<UsageDisclosureRow
			detailsLabel={t("inspector.usage.modelDetails", { name: modelName })}
			name={modelName}
			nameClassName="font-mono text-2xs"
			regionLabel={t("inspector.usage.modelPeek", { name: modelName })}
			totals={model.totals}
		>
			<UsageMetrics totals={model.totals} />
		</UsageDisclosureRow>
	);
}

function UsageDisclosureRow({
	children,
	detailsLabel,
	name,
	nameClassName,
	regionLabel,
	totals,
}: {
	children: ReactNode;
	detailsLabel: string;
	name: string;
	nameClassName: string;
	regionLabel: string;
	totals: SessionUsage["totals"];
}) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const detailID = useId();
	const totalTokens = usageTokenTotal(totals);
	const exactTotal = totalTokens?.toLocaleString("en-US");

	return (
		<div className="p-2">
			<button
				aria-controls={detailID}
				aria-expanded={open}
				aria-label={detailsLabel}
				className="grid w-full grid-cols-[minmax(0,1fr)_4.5rem_5.5rem] items-center gap-2 rounded-md px-1 py-2 text-left outline-none transition-colors hover:bg-interactive-hover focus-visible:bg-interactive-hover focus-visible:ring-1 focus-visible:ring-ring"
				onClick={() => setOpen((current) => !current)}
				type="button"
			>
				<span className={`flex min-w-0 items-center gap-1 text-settings-label ${nameClassName}`}>
					{open ? (
						<ChevronDown aria-hidden="true" className="size-3 shrink-0 text-settings-muted" />
					) : (
						<ChevronRight aria-hidden="true" className="size-3 shrink-0 text-settings-muted" />
					)}
					<span className="truncate">{name}</span>
				</span>
				<span
					className="text-right font-mono text-2xs text-settings-label"
					title={totalTokens === null ? undefined : t("inspector.usage.tokensExact", { count: exactTotal })}
				>
					{totalTokens === null ? "—" : formatTelemetryTokenValue(totalTokens)}
				</span>
				<UsageCostPlaceholder />
			</button>
			{open ? (
				<div
					aria-label={regionLabel}
					className="mx-1 mb-2 border-l border-(--color-border-settings-input) py-1.5 pl-2.5"
					id={detailID}
					role="region"
				>
					{children}
				</div>
			) : null}
		</div>
	);
}

function UsageCostPlaceholder() {
	const { t } = useTranslation();
	const label = t("inspector.usage.metricUnavailable", { label: t("inspector.usage.cost") });
	return (
		<span aria-label={label} className="text-right font-mono text-2xs text-settings-muted" title={label}>
			—
		</span>
	);
}

function UsageMetrics({ totals }: { totals: SessionUsage["totals"] }) {
	const { t } = useTranslation();
	return (
		<dl className="grid grid-cols-2 gap-x-4 gap-y-2 @max-[300px]/inspector:grid-cols-1">
			<UsageMetric label={t("inspector.usage.inputTokens")} metric={totals.inputTokens} />
			<UsageMetric label={t("inspector.usage.outputTokens")} metric={totals.outputTokens} />
			<UsageMetric label={t("inspector.usage.cacheReadTokens")} metric={totals.cacheReadTokens} />
			<UsageMetric label={t("inspector.usage.cacheWriteTokens")} metric={totals.cacheWriteTokens} />
			<UsageMetric label={t("inspector.usage.reasoningTokens")} metric={totals.reasoningTokens} />
			<UsageMetric label={t("inspector.usage.uncachedInputTokens")} metric={totals.uncachedInputTokens} />
		</dl>
	);
}

function UsageMetric({
	label,
	metric,
}: {
	label: string;
	metric: SessionUsage["totals"]["inputTokens"];
}) {
	const { t } = useTranslation();
	const exactValue = metric?.toLocaleString("en-US");
	const accessibleLabel =
		metric === null
			? t("inspector.usage.metricUnavailable", { label })
			: t("inspector.usage.metricAria", { label, count: exactValue });
	return (
		<div className="min-w-0">
			<dt className="truncate text-2xs text-settings-muted">{label}</dt>
			<dd
				aria-label={accessibleLabel}
				className="mt-0.5 truncate font-mono text-sm-md text-settings-label"
				title={
					metric === null
						? t("inspector.usage.metricUnavailable", { label })
						: t("inspector.usage.tokensExact", { count: exactValue })
				}
			>
				{metric === null ? "—" : formatTelemetryTokenValue(metric)}
			</dd>
		</div>
	);
}

const usageMetricKeys = [
	"inputTokens",
	"uncachedInputTokens",
	"cacheReadTokens",
	"cacheWriteTokens",
	"outputTokens",
	"reasoningTokens",
] as const;

function usageScopes(usage: SessionUsage): SessionUsage["totals"][] {
	return [
		usage.totals,
		...usage.harnesses.flatMap((harness) => [
			harness.totals,
			...harness.models.map((model) => model.totals),
		]),
	];
}

function hasMeaningfulSessionUsage(usage?: SessionUsage): usage is SessionUsage {
	if (!usage) return false;
	return usageScopes(usage).some((totals) =>
		usageMetricKeys.some((key) => (totals[key] ?? 0) > 0),
	);
}

function formatTelemetryTokenValue(totalTokens: number): string {
	return formatTokenCount(totalTokens).replace(/ tok$/, "");
}

function usageTokenTotal(totals: SessionUsage["totals"]): number | null {
	if (totals.inputTokens === null && totals.outputTokens === null) return null;
	return (totals.inputTokens ?? 0) + (totals.outputTokens ?? 0);
}

function formatHarnessName(harness: string): string {
	const knownNames: Record<string, string> = {
		"claude-code": "Claude",
		claude: "Claude",
		codex: "Codex",
		glm: "GLM",
		kimi: "Kimi",
	};
	if (knownNames[harness]) return knownNames[harness];
	return harness
		.split(/[-_]/)
		.filter(Boolean)
		.map((part) => part.charAt(0).toUpperCase() + part.slice(1))
		.join(" ");
}

function ResumeAgentControl({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const resume = useMutation({
		mutationFn: async () => {
			if (usePreviewData) return;
			const { data, error, response } = await apiClient.POST("/api/v1/sessions/{sessionId}/resume-agent", {
				params: { path: { sessionId: session.id } },
			});
			if (error) throw new Error(apiErrorMessage(error, `Failed to resume agent (${response.status})`));
			return data;
		},
		onSuccess: async (data) => {
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			if (data?.resumeMode === "saved_prompt") {
				void aoBridge.notifications
					.show({
						id: `resume-agent-fallback:${session.id}:${Date.now()}`,
						title: t("inspector.startedFromPrompt"),
						body: t("inspector.resumeFallbackBody"),
					})
					.catch((err) => {
						console.warn("Unable to show resume fallback notification", err);
					});
			}
		},
	});

	if (session.isTerminated === true || session.activity?.state !== "exited" || session.activeAgentSwitch) return null;

	const error = resume.error instanceof Error ? resume.error.message : null;
	return (
		<div className="mt-3 border-t border-(--color-border-settings-input) pt-3">
			<Button
				className="w-full"
				disabled={resume.isPending}
				onClick={() => resume.mutate()}
				size="sm"
				type="button"
				variant="outline"
			>
				<Play className="size-icon-sm" aria-hidden="true" />
				{resume.isPending ? t("inspector.resumingAgent") : t("inspector.resumeAgent")}
			</Button>
			{error ? (
				<p className="mt-2 text-2xs leading-normal text-error" role="status">
					{error}
				</p>
			) : null}
		</div>
	);
}

function SessionControls({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const [confirmOpen, setConfirmOpen] = useState(false);
	const terminate = useTerminateSession();
	const policy = useMutation({
		mutationFn: async (terminateOnPrMerge: boolean) => {
			if (usePreviewData) return;
			const { error, response } = await apiClient.PATCH("/api/v1/sessions/{sessionId}/merge-policy", {
				params: { path: { sessionId: session.id } },
				body: { terminateOnPrMerge },
			});
			if (error) throw new Error(apiErrorMessage(error, `Failed to update merge policy (${response.status})`));
		},
		onMutate: async (terminateOnPrMerge) => {
			await queryClient.cancelQueries({ queryKey: workspaceQueryKey });
			const previous = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
			queryClient.setQueryData<WorkspaceSummary[]>(workspaceQueryKey, (current) =>
				updateSessionMergePolicy(current, session.id, terminateOnPrMerge),
			);
			return { previous };
		},
		onError: (_error, _next, context) => {
			if (context?.previous) queryClient.setQueryData(workspaceQueryKey, context.previous);
		},
		onSettled: () => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});
	const policyError = policy.error instanceof Error ? policy.error.message : null;
	const canTerminateNow = session.status === "merged";

	const confirmTermination = () => {
		const workspaces = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey) ?? [];
		const orchestrator = findProjectOrchestrator(workspaces, session.workspaceId);
		setConfirmOpen(false);
		terminate.mutate(session);
		if (orchestrator) {
			void navigate({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId: session.workspaceId, sessionId: orchestrator.id },
			});
			return;
		}
		void navigate({ to: "/projects/$projectId", params: { projectId: session.workspaceId } });
	};

	if (session.isTerminated === true) return null;

	return (
		<Section title={t("inspector.sessionControls")}>
			<AutoInjectCIPolicyControl session={session} />
			{session.kind === "orchestrator" ? null : canTerminateNow ? (
				<div className="flex items-center justify-between gap-3 py-1">
					<span className="min-w-0 text-xs font-medium text-settings-label">{t("inspector.terminateShort")}</span>
					<SessionTerminationPopover
						onConfirm={confirmTermination}
						onOpenChange={setConfirmOpen}
						open={confirmOpen}
						session={session}
						trigger={
							<button
								aria-label={t("inspector.terminate")}
								className="inline-flex size-control-md items-center justify-center rounded-sm text-passive transition-colors hover:bg-error/10 hover:text-error focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
								onClick={() => clearTerminateSessionState(queryClient, session.id)}
								type="button"
							>
								<Trash2 className="size-icon-sm" aria-hidden="true" />
							</button>
						}
					/>
				</div>
			) : (
				<>
					<InspectorPolicyRow
						ariaLabel={t("inspector.terminateOnMerge")}
						checked={Boolean(session.terminateOnPrMerge)}
						description={t("inspector.terminateOnMergeDescription")}
						disabled={policy.isPending}
						id={`merge-policy-${session.id}`}
						label={t("inspector.terminateOnMergeShort")}
						onCheckedChange={(checked) => policy.mutate(checked)}
						tooltipClassName="max-w-60"
					/>
					{policyError ? (
						<p className="mt-1 text-2xs leading-normal text-error" role="status">
							{policyError}
						</p>
					) : null}
				</>
			)}
		</Section>
	);
}

function updateSessionMergePolicy(
	workspaces: WorkspaceSummary[] | undefined,
	sessionId: string,
	terminateOnPrMerge: boolean,
): WorkspaceSummary[] | undefined {
	return workspaces?.map((workspace) => ({
		...workspace,
		sessions: workspace.sessions.map((candidate) =>
			candidate.id === sessionId ? { ...candidate, terminateOnPrMerge } : candidate,
		),
	}));
}

function PRSummaryCard({ onOpenReviews, pr, sessionId }: { onOpenReviews: () => void; pr: SessionPRSummary; sessionId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const presentation = prCardPresentation(pr);
	const canMerge =
		pr.state === "open" &&
		pr.ci.state === "passing" &&
		pr.review.decision === "approved" &&
		pr.mergeability.state === "mergeable" &&
		Boolean(pr.url && pr.headSha);
	const mergePr = useMutation({
		mutationFn: async () => {
			if (usePreviewData) return;
			const { error } = await apiClient.POST("/api/v1/prs/{id}/merge", {
				params: { path: { id: String(pr.number) } },
				body: { prUrl: pr.url, expectedHeadSha: pr.headSha },
			});
			if (error) throw new Error(apiErrorMessage(error, t("pr.merge.failed", { number: pr.number })));
		},
		onSuccess: async () => {
			await Promise.all([
				queryClient.invalidateQueries({ queryKey: sessionScmSummaryQueryKey(sessionId) }),
				queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
			]);
		},
	});
	const mergeError = mergePr.error instanceof Error ? mergePr.error.message : null;
	const viewModel: InspectorPullRequest = {
		...pr,
		card: presentation,
		href: prBrowserUrl(pr),
		stateLabel: t(prStateLabelKeys[pr.state]),
		reviewDetailsAction: pr.review.decision !== "none" ? (
				<button className="whitespace-nowrap text-2xs text-settings-muted underline-offset-2 hover:underline" onClick={onOpenReviews} type="button">
				{t("pr.review.viewDetails")} ↗
			</button>
		) : undefined,
	};
	return (
		<InspectorPullRequestCardView
			countNounLabel={(count, noun) => `${count} ${t(prNounKeys[noun], { count })}`}
			externalIcon={<ArrowUpRight aria-hidden="true" className="size-icon-2xs shrink-0" strokeWidth={2} />}
			externalLink={ProductExternalLink}
			mergeAction={
				canMerge ? (
					<Button
						aria-label={t("pr.merge.actionFor", { number: pr.number })}
						className="gap-1 bg-success px-2 text-xs text-background hover:bg-success/80"
						disabled={mergePr.isPending}
						onClick={() => mergePr.mutate()}
						size="sm"
						type="button"
					>
						{mergePr.isPending ? (
							<Loader2 className="size-icon-sm animate-spin" aria-hidden="true" />
						) : (
							<GitMerge className="size-icon-sm" aria-hidden="true" />
						)}
						{mergePr.isPending ? t("pr.merge.merging") : t("pr.merge.action")}
					</Button>
				) : undefined
			}
			mergeError={mergeError}
			openLabel={t("inspector.openPR", { number: pr.number })}
			pr={viewModel}
			pullRequestIcon={<GitPullRequest className="size-icon-sm shrink-0" aria-hidden="true" />}
		/>
	);
}

type SortableTimelineEvent = InspectorTimelineEvent & { sortTime: number };

function ActivityTimeline({ prs, session }: { prs: SessionPRSummary[]; session: WorkspaceSession }) {
	const events: SortableTimelineEvent[] = [];
	const pushEvent = (event: InspectorTimelineEvent, timestamp?: string | null) => {
		events.push({ ...event, sortTime: timelineSortTime(timestamp) });
	};
	const createdAt = session.createdAt ?? session.updatedAt;

	pushEvent(
		{
			tone: "neutral",
			content: <>{appI18n.t("inspector.timeline.createdWorkspace")}</>,
			timestamp: formatTimeCompact(createdAt),
		},
		createdAt,
	);

	for (const pr of prs.filter((pr) => pr.state === "draft")) {
		pushEvent(
			{
				tone: "neutral",
				content: <PRTimelineLink pr={pr} verb={appI18n.t("inspector.timeline.draft")} />,
				timestamp: prStateTime(pr),
			},
			pr.stateChangedAt,
		);
	}

	for (const pr of prs.filter((pr) => pr.state !== "draft")) {
		pushEvent(
			{
				tone: "neutral",
				content: <PRTimelineLink pr={pr} verb={appI18n.t("inspector.timeline.opened")} />,
				timestamp: prCreatedTime(pr),
			},
			pr.createdAt,
		);
	}

	for (const pr of prs.filter((pr) => pr.state === "merged")) {
		pushEvent(
			{
				tone: "good",
				content: <PRTimelineLink pr={pr} verb={appI18n.t("inspector.timeline.merged")} />,
				timestamp: prStateTime(pr),
			},
			pr.stateChangedAt,
		);
	}

	if (session.status === "merged") {
		const mergedAt = latestMergedTimestamp(prs);
		pushEvent(
			{
				tone: "good",
				content: <>{appI18n.t("inspector.timeline.done")}</>,
				timestamp: mergedAt ? formatTimeCompact(mergedAt) : null,
			},
			mergedAt,
		);
	}

	const activityView = getAgentActivityView(session.activity);
	const activityAt = session.activity?.lastActivityAt ?? session.updatedAt ?? session.createdAt;
	pushEvent(
		{
			tone: "now",
			content: (
				<span className="inline-flex flex-wrap items-center gap-1.5">
					<span className="inline-flex align-middle">
						<InspectorActivityPill activity={session.activity} />
					</span>
					{session.status === "no_signal" ? (
						<span className="inline-flex align-middle">
							<TimelinePill {...getSessionTimelinePillView("no_signal")} />
						</span>
					) : null}
					{scmTimelineStates(session).map((state) => (
						<span key={state} className="inline-flex align-middle">
							<InspectorScmPill state={state} />
						</span>
					))}
				</span>
			),
			timestamp: activityAt ? formatTimeCompact(activityAt) : null,
			markerTone: activityView.tone,
			markerBreathe: activityView.breathe,
		} satisfies InspectorTimelineEvent,
		activityAt,
	);

	return <InspectorActivityTimelineView events={[...events].sort((a, b) => b.sortTime - a.sortTime)} />;
}

function PRTimelineLink({ pr, verb }: { pr: SessionPRSummary; verb: string }) {
	return (
		<a
			aria-label={`${verb} PR #${pr.number}`}
			className="inline-flex min-w-0 items-center gap-1 rounded-xs text-foreground underline-offset-2 transition-colors hover:text-accent hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent/50"
			href={prBrowserUrl(pr)}
			rel="noopener noreferrer"
			target="_blank"
		>
			<span>{verb} </span>
			<b>PR #{pr.number}</b>
			<ArrowUpRight aria-hidden="true" className="size-icon-2xs shrink-0" strokeWidth={2} />
		</a>
	);
}

function prStateTime(pr: SessionPRSummary): string | null {
	return pr.stateChangedAt ? formatTimeCompact(pr.stateChangedAt) : null;
}

function prCreatedTime(pr: SessionPRSummary): string | null {
	return pr.createdAt ? formatTimeCompact(pr.createdAt) : null;
}

function latestMergedTimestamp(prs: SessionPRSummary[]): string | null {
	let latest: { timestamp: string; milliseconds: number } | undefined;
	for (const pr of prs) {
		if (pr.state !== "merged" || !pr.stateChangedAt) continue;
		const milliseconds = Date.parse(pr.stateChangedAt);
		if (!Number.isFinite(milliseconds)) continue;
		if (!latest || milliseconds > latest.milliseconds) {
			latest = { timestamp: pr.stateChangedAt, milliseconds };
		}
	}
	return latest?.timestamp ?? null;
}

function timelineSortTime(timestamp: string | null | undefined): number {
	if (!timestamp) return Number.NEGATIVE_INFINITY;
	const milliseconds = Date.parse(timestamp);
	return Number.isFinite(milliseconds) ? milliseconds : Number.NEGATIVE_INFINITY;
}

type ScmTimelineState = "ci_failed" | "changes_requested" | "conflict";

function conflictPill() {
	return { label: appI18n.t("inspector.conflict"), tone: "var(--color-danger)", breathe: false };
}

function InspectorActivityPill({ activity }: { activity?: WorkspaceSession["activity"] }) {
	return <TimelinePill {...getAgentActivityView(activity)} />;
}

function InspectorScmPill({ state }: { state: ScmTimelineState }) {
	if (state === "conflict") return <TimelinePill {...conflictPill()} />;
	return <TimelinePill {...getSessionTimelinePillView(state)} />;
}

function TimelinePill({ label, tone }: { label: string; tone: string; breathe: boolean }) {
	return (
		<span className="inline-flex shrink-0 whitespace-nowrap text-xs font-semibold" style={{ color: tone }}>
			{label}
		</span>
	);
}

function scmTimelineStates(session: WorkspaceSession): ScmTimelineState[] {
	const states: ScmTimelineState[] = [];
	const seen = new Set<ScmTimelineState>();
	const add = (state: ScmTimelineState) => {
		if (seen.has(state)) return;
		seen.add(state);
		states.push(state);
	};

	if (session.status === "ci_failed") add("ci_failed");
	if (session.status === "changes_requested") add("changes_requested");
	for (const pr of session.prs) {
		if (pr.ci === "failing") add("ci_failed");
		if (pr.review === "changes_requested") add("changes_requested");
		if (pr.mergeability === "conflicting") add("conflict");
	}

	return states;
}

/** Reviewer harness the daemon accepts, typed from the generated schema. */
type ReviewerHarness = NonNullable<components["schemas"]["TriggerReviewRequest"]["harness"]>;
type AgentInfo = components["schemas"]["AgentInfo"];
type AgentCatalog = { supported?: AgentInfo[]; installed?: AgentInfo[]; authorized?: AgentInfo[] };

const WORKER_DEFAULT_REVIEWERS: Partial<Record<WorkspaceSession["provider"], ReviewerHarness>> = {
	"claude-code": "claude-code",
	codex: "codex",
	opencode: "opencode",
	muse: "muse",
	kimchi: "kimchi",
};

function resolveDefaultReviewerHarness(config: ProjectConfig | undefined, workerHarness: WorkspaceSession["provider"]): ReviewerHarness {
	const configuredHarness = config?.reviewers?.[0]?.harness;
	if (configuredHarness) return configuredHarness as ReviewerHarness;
	return WORKER_DEFAULT_REVIEWERS[workerHarness] ?? "claude-code";
}

function ReviewsSection({
	session,
	onOpenReviewerTerminal,
}: {
	session: WorkspaceSession;
	onOpenReviewerTerminal?: OpenReviewerTerminal;
}) {
	const { t } = useTranslation();
	const hasPr = sortedPRs(session).length > 0;
	const queryClient = useQueryClient();
	const [reviewNotice, setReviewNotice] = useState<string | null>(null);
	useEffect(() => {
		if (!reviewNotice) return;
		const timer = window.setTimeout(() => setReviewNotice(null), 10_000);
		return () => window.clearTimeout(timer);
	}, [reviewNotice]);
	const reviewsQuery = useQuery({
		...sessionReviewsQueryOptions(session, hasPr),
		refetchInterval: (query) => {
			const reviews = query.state.data?.reviews ?? [];
			if (reviews.some((review) => review.status === "running")) return 2500;
			return session.autoReviewEnabled === true ? 10_000 : false;
		},
	});
	const agentsQuery = useQuery(agentsQueryOptions);
	const projectConfigQuery = useQuery({
		queryKey: ["project-config", session.workspaceId],
		enabled: hasPr,
		queryFn: async () => {
			if (usePreviewData) return mockProjectConfig();
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}", {
				params: { path: { id: session.workspaceId } },
			});
			if (error) return undefined;
			return projectConfig(data?.project);
		},
	});
	// The reviewer preference belongs to the worker session, not this component
	// or the whole project. Keep local state responsive while the daemon persists
	// it, and resync when the inspector moves to another session.
	const [reviewerOverride, setReviewerOverride] = useState<ReviewerHarness | "">(
		session.reviewerHarness ?? "",
	);
	useEffect(() => {
		setReviewerOverride(session.reviewerHarness ?? "");
	}, [session.id, session.reviewerHarness]);
	const saveReviewer = useMutation({
		mutationFn: async (harness: ReviewerHarness | "") => {
			const { data, error } = await apiClient.POST("/api/v1/sessions/{sessionId}/reviews/switch", {
				params: { path: { sessionId: session.id } },
				body: { harness: harness || undefined },
			});
			if (error) throw new Error(apiErrorMessage(error, "Unable to save reviewer"));
			if (data) queryClient.setQueryData(["session-reviews", session.id], data);
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: ["session-reviews", session.id] });
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});
	const saveAutoReview = useMutation({
		mutationFn: async (enabled: boolean) => {
			const { error } = await apiClient.PUT("/api/v1/sessions/{sessionId}/auto-review", {
				params: { path: { sessionId: session.id } },
				body: { enabled },
			});
			if (error) throw new Error(apiErrorMessage(error, t("inspector.reviewRequestFailed")));
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});
	const triggerReview = useMutation({
		mutationFn: async () => {
			// No override sends no body at all, leaving the default path on the wire
			// exactly as it was.
			const { data, error, response } = await apiClient.POST("/api/v1/sessions/{sessionId}/reviews/trigger", {
				params: { path: { sessionId: session.id } },
				...(reviewerOverride ? { body: { harness: reviewerOverride } } : {}),
			});
			if (error) throw new Error(apiErrorMessage(error, t("inspector.unableStartReview")));
			return { data, reused: response?.status === 200 };
		},
		onMutate: () => {
			setReviewNotice(null);
		},
		onSuccess: ({ data, reused }) => {
			void queryClient.invalidateQueries({ queryKey: ["session-reviews", session.id] });
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			const started = data?.reviews?.find((review) => review.status === "running" && review.latestRun);
			if (reused || !started?.latestRun) {
				setReviewNotice(t("inspector.reviewAlreadyRanForCommit"));
				return;
			}
			if (data?.reviewerHandleId) {
				const harness = started.latestRun.harness || "reviewer";
				onOpenReviewerTerminal?.({ handleId: data.reviewerHandleId, harness });
			}
		},
	});
	const cancelReview = useMutation({
		mutationFn: async () => {
			const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/reviews/cancel", {
				params: { path: { sessionId: session.id } },
			});
			if (error) throw new Error(apiErrorMessage(error, t("inspector.unableCancelReview")));
		},
		onSuccess: () => {
			setReviewNotice(null);
			void queryClient.invalidateQueries({ queryKey: ["session-reviews", session.id] });
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});
	const killReview = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/sessions/{sessionId}/reviews/kill", {
				params: { path: { sessionId: session.id } },
			});
			if (error) throw new Error(apiErrorMessage(error, t("inspector.unableKillReviewSession")));
			return data;
		},
		onSuccess: (data) => {
			setReviewNotice(null);
			if (data) queryClient.setQueryData(["session-reviews", session.id], data);
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});
	const reviewStates = reviewsQuery.data?.reviews ?? [];
	const autoReviewEnabled = session.autoReviewEnabled === true;
	const scmSummary = useSessionScmSummary(session.id);
	const prSummaries = sessionPRDisplaySummaries(session, scmSummary.data);
	const githubReviews = prSummaries.filter(
		(pr) =>
			pr.state === "open" &&
			((pr.review?.reviews?.length ?? 0) > 0 ||
				(pr.review?.unresolvedBy ?? []).some((reviewer) => reviewer.count > 0)),
	);
	const hasPRs = sortedPRs(session).length > 0;

	return (
		<div className="p-2">
			{hasPRs ? null : (
				<Section surface title={t("inspector.review.controls")}>
					<AutoInjectReviewPolicyControl session={session} />
				</Section>
			)}
			{/* Running a review is an action; reading them is a list. The action stays
			    on top, then one list carrying both sources keyed by PR. */}
			<ReviewPanel
				autoReviewEnabled={autoReviewEnabled}
				config={projectConfigQuery.data}
				error={
					reviewsQuery.error ??
					triggerReview.error ??
					cancelReview.error ??
					killReview.error ??
					saveReviewer.error ??
					saveAutoReview.error
				}
				isLoading={reviewsQuery.isLoading}
				isCancelling={cancelReview.isPending}
				isAutoReviewSaving={saveAutoReview.isPending}
				isKilling={killReview.isPending}
				isSwitchingReviewer={saveReviewer.isPending}
				isTriggering={triggerReview.isPending}
				onCancel={() => cancelReview.mutate()}
				onAutoReviewChange={(enabled) => saveAutoReview.mutate(enabled)}
				onKill={() => killReview.mutate()}
				onTrigger={() => triggerReview.mutate()}
				reviewerHandleId={reviewsQuery.data?.reviewerHandleId ?? ""}
				reviewStates={reviewStates}
				notice={reviewNotice}
				agentCatalog={agentsQuery.data}
				reviewerOverride={reviewerOverride}
				onReviewerOverrideChange={(next) => {
					setReviewerOverride(next);
					saveReviewer.mutate(next);
				}}
				session={session}
			/>
			<MergedReviewsSection
				githubPRs={githubReviews}
				isLoading={scmSummary.isLoading}
				reviewStates={reviewStates}
				runs={reviewsQuery.data?.runs ?? []}
				session={session}
			/>
		</div>
	);
}

/**
 * AO's own reviewer passes and the reviews humans and bots left on GitHub, in
 * one list keyed by PR. They were two sections, which made the same PR appear
 * twice and left the reader joining them up by number; a review is a review,
 * and what matters is who wrote it. Each group inside a PR names its source —
 * "AO codex" against the agent that ran, "On GitHub" for everyone else.
 */
function MergedReviewsSection({
	githubPRs,
	isLoading,
	reviewStates,
	runs,
	session,
}: {
	githubPRs: SessionPRSummary[];
	isLoading: boolean;
	reviewStates: PRReviewState[];
	runs: ReviewRunFacts[];
	session: WorkspaceSession;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const openReviewStates = openReviewStatesFor(session, reviewStates);
	const runsByPR = runsByPRFrom(openReviewStates, runs);
	const aoStates = triggeredReviewStatesFrom(openReviewStates, runs);

	// Union by PR number, newest PR first. A PR can appear on either side alone.
	const byNumber = new Map<number, { ao?: PRReviewState; github?: SessionPRSummary }>();
	for (const state of aoStates) {
		byNumber.set(state.prNumber, { ...byNumber.get(state.prNumber), ao: state });
	}
	for (const pr of githubPRs) {
		byNumber.set(pr.number, { ...byNumber.get(pr.number), github: pr });
	}
	const rows = [...byNumber.entries()].sort(([a], [b]) => b - a);
	const labels = reviewLabels(t);
	const requestRereview = async (review: InspectorGithubReview) => {
		const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/reviews/rerequest", {
			params: { path: { sessionId: session.id } },
			body: { pullRequestUrl: review.pullRequestUrl, reviewerId: review.reviewerId },
		});
		if (error) throw new Error(apiErrorMessage(error, "Unable to request re-review"));
	};
	const resolveInlineComment = async (comment: InspectorInlineComment) => {
		const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/reviews/comments/resolve", {
			params: { path: { sessionId: session.id } },
			body: { pullRequestUrl: comment.pullRequestUrl, commentUrl: comment.url ?? "" },
		});
		if (error) throw new Error(apiErrorMessage(error, "Unable to resolve review comment"));
		void queryClient.invalidateQueries({ queryKey: sessionScmSummaryQueryKey(session.id) });
		void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
	};
	const sendInlineCommentToWorker = async (comment: InspectorInlineComment & { reviewerId?: string }) => {
		const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/send", {
			params: { path: { sessionId: session.id } },
			body: { message: formatInlineReviewCommentMessage(comment) },
		});
		if (error) throw new Error(apiErrorMessage(error, "Unable to send review comment to worker agent"));
	};
	const groups: InspectorReviewGroup[] = rows.map(([number, { ao, github }]) => {
		const aoRuns = ao ? [...(runsByPR.get(ao.prUrl) ?? [])].sort((a, b) => b.createdAt.localeCompare(a.createdAt)) : [];
		const entries = github?.review?.reviews ?? [];
		const unresolved = (github?.review?.unresolvedBy ?? []).reduce((count, reviewer) => count + reviewer.count, 0);
		const reviewRuns = aoRuns.map((run) => {
			const reviewUrl = aoReviewCommentUrl(run);
			return {
				body: run.body,
				createdAtLabel: formatTimeCompact(run.createdAt),
				harness: run.harness || "reviewer",
				id: run.id,
				status: run.status,
				url: reviewUrl ?? (ao?.prUrl || null),
				verdict: githubVerdict(run.verdict, t),
			};
		});
		const unresolvedByReviewer = new Map(
			(github?.review?.unresolvedBy ?? []).map((reviewer) => [reviewer.reviewerId, reviewer]),
		);
		const externalEntries = entries.map((entry) => {
			const reviewer = unresolvedByReviewer.get(entry.reviewerId);
			unresolvedByReviewer.delete(entry.reviewerId);
			return {
				body: entry.body,
				id: entry.reviewUrl || `${entry.reviewerId}:${entry.submittedAt}`,
				pullRequestUrl: github?.url,
				inlineComments: (reviewer?.links ?? []).map((link) => ({
					autoInjectReview: link.autoInjectReview,
					body: link.body,
					file: link.file,
					line: link.line,
					pullRequestUrl: github?.url,
					url: link.url || reviewer?.reviewUrl,
				})),
				isBot: entry.isBot,
				reviewerId: entry.reviewerId,
				reviewUrl: entry.reviewUrl,
				submittedAt: entry.submittedAt,
				submittedAtLabel: formatTimeCompact(entry.submittedAt),
				verdict: githubVerdict(entry.verdict, t),
			};
		});
		for (const reviewer of unresolvedByReviewer.values()) {
			externalEntries.push({
				body: undefined,
				id: `unresolved:${reviewer.reviewerId}:${number}`,
				pullRequestUrl: github?.url,
				inlineComments: reviewer.links.map((link) => ({
					autoInjectReview: link.autoInjectReview,
					body: link.body,
					file: link.file,
					line: link.line,
					pullRequestUrl: github?.url,
					url: link.url || reviewer.reviewUrl,
				})),
				isBot: reviewer.isBot,
				reviewerId: reviewer.reviewerId,
				reviewUrl: reviewer.reviewUrl,
				submittedAt: "",
				submittedAtLabel: "",
				verdict: githubVerdict("none", t),
			});
		}
		return {
			ao: ao
				? {
						dimmed: ao.status === "ineligible",
						historical:
							ao.status === "needs_review" &&
							Boolean(ao.previousRun) &&
							(!ao.latestRun || ao.latestRun.status === "failed" || ao.latestRun.status === "cancelled"),
						notInjected: aoRuns.some((run) => run.autoInjectReview === false),
						runs: reviewRuns,
					}
				: undefined,
			github: github
				? {
						entries: externalEntries,
						notInjected:
							entries.some((review) => review.autoInjectReview === false) ||
							(github.review?.unresolvedBy ?? []).some((reviewer) =>
								reviewer.links.some((link) => link.autoInjectReview === false),
							),
						unresolved,
						unresolvedBy: (github.review?.unresolvedBy ?? []).map((reviewer) => ({
							count: reviewer.count,
							isBot: reviewer.isBot,
							links: reviewer.links.map((link) => ({
								autoInjectReview: link.autoInjectReview,
								body: link.body,
								file: link.file,
								line: link.line,
								pullRequestUrl: github?.url,
								url: link.url,
							})),
							reviewerId: reviewer.reviewerId,
							reviewUrl: reviewer.reviewUrl,
						})),
					}
				: undefined,
			meta: [
				ao ? aoReviewMeta(ao) : `#${number}`,
				unresolved > 0 ? t("inspector.unresolvedCount", { count: unresolved }) : null,
			]
				.filter(Boolean)
				.join(" · "),
			number,
			title: (ao?.title ?? github?.title)?.trim() || `PR #${number}`,
			verdict: ao ? reviewVerdict(ao) : undefined,
		};
	});
	return (
		<InspectorReviewsView
			externalLink={ProductExternalLink}
			groups={groups}
			isLoading={isLoading}
			labels={labels}
			onRequestRereview={requestRereview}
			onResolveInlineComment={resolveInlineComment}
			onSendInlineComment={sendInlineCommentToWorker}
			renderAvatar={(harness) => (
				<AgentAvatar className="size-icon-sm shrink-0" decorative provider={harness} />
			)}
			renderMarkdown={renderReviewMarkdown}
		/>
	);
}

function reviewLabels(t: TFunction): InspectorReviewLabels {
	return {
		aoSource: t("inspector.reviewBySource.ao"),
		bot: t("inspector.bot"),
		earlierPass: t("inspector.earlierPass"),
		githubSource: t("inspector.reviewBySource.github"),
		loadingReviews: t("inspector.loadingReviews"),
		loadMoreReviews: (count) => t("inspector.loadMoreReviews", { count }),
		noPastReviewSummaries: t("inspector.noPastReviewSummaries"),
		notInjected: t("inspector.review.notInjected"),
		openComments: t("inspector.openComments"),
		openInlineComments: (count) => t("inspector.openInlineComments", { count }),
		requestRereviewPR: t("inspector.requestRereviewPR"),
		reviews: t("inspector.reviews"),
		reviewedAt: (time) => t("inspector.reviewedAt", { time }),
		resolvedComments: (count) => t("inspector.resolvedComments", { count }),
		rereviewRequested: t("inspector.rereviewRequested"),
		rereviewRequestFailed: t("inspector.rereviewRequestFailed"),
		resolveComment: t("inspector.resolveComment"),
		resolvedReview: t("inspector.resolvedReview"),
		resolveReviewFailed: t("inspector.resolveReviewFailed"),
		sendToWorkerAgent: t("inspector.sendToWorkerAgent"),
		sentToWorkerAgent: t("inspector.sentToWorkerAgent"),
		sendToWorkerAgentError: t("inspector.sendToWorkerAgentError"),
		showLatestReviewOnly: t("inspector.showLatestReviewOnly"),
		showLess: t("inspector.showLess"),
		showMore: t("inspector.showMore"),
		commentNumber: (number) => t("inspector.commentNumber", { number }),
		unresolvedCount: (count) => t("inspector.unresolvedCount", { count }),
		viewInFile: t("inspector.viewInFile"),
		viewOnPR: t("inspector.viewOnPR"),
	};
}

function renderReviewMarkdown(body: string) {
	return (
		<ReactMarkdown
			components={{
				a: ({ href, children }) => (
					<a href={href} target="_blank" rel="noopener noreferrer">
						{children}
					</a>
				),
			}}
			remarkPlugins={[remarkGfm]}
		>
			{body}
		</ReactMarkdown>
	);
}

function formatInlineReviewCommentMessage(comment: InspectorInlineComment & { reviewerId?: string }): string {
	const reviewer = sanitizeWorkerMessagePart(comment.reviewerId?.trim() || "unknown reviewer");
	const file = sanitizeWorkerMessagePart(comment.file?.trim() || "");
	const location = file ? `${file}${comment.line ? `:${comment.line}` : ""}` : "general PR comment";
	const body = sanitizeWorkerMessagePart(comment.body?.trim() || "No comment body provided.");
	const url = sanitizeWorkerMessagePart(comment.url?.trim() || "");
	const lines = [
		`A reviewer left an unresolved inline comment on your PR. Address it, commit the fix, and push the branch to GitHub.`,
		"",
		`Reviewer: @${reviewer}`,
		`Location: ${location}`,
		"",
		"Comment:",
		body,
	];
	if (url) {
		lines.push("", `Comment URL: ${url}`);
	}
	lines.push("", "You should not need to re-fetch review data unless you need additional context beyond what AO has provided here.");
	return lines.join("\n");
}

function sanitizeWorkerMessagePart(value: string): string {
	return value.replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, "");
}

function projectConfig(project: components["schemas"]["ProjectOrDegraded"] | undefined): ProjectConfig | undefined {
	if (!project || !("config" in project)) return undefined;
	return project.config;
}

function mockProjectConfig(): ProjectConfig {
	return {
		worker: { agent: "codex" },
		orchestrator: { agent: "codex" },
		reviewers: [{ harness: "codex" }],
	};
}

function ReviewPanel({
	autoReviewEnabled,
	session,
	config,
	reviewStates,
	reviewerHandleId,
	isLoading,
	isTriggering,
	isCancelling,
	isAutoReviewSaving,
	isKilling,
	isSwitchingReviewer,
	error,
	notice,
	agentCatalog,
	reviewerOverride,
	onReviewerOverrideChange,
	onTrigger,
	onCancel,
	onAutoReviewChange,
	onKill,
}: {
	autoReviewEnabled: boolean;
	session: WorkspaceSession;
	config?: ProjectConfig;
	reviewStates: PRReviewState[];
	reviewerHandleId: string;
	isLoading: boolean;
	isTriggering: boolean;
	isCancelling: boolean;
	isKilling: boolean;
	isSwitchingReviewer: boolean;
	error: unknown;
	notice: string | null;
	agentCatalog?: AgentCatalog;
	reviewerOverride: ReviewerHarness | "";
	onReviewerOverrideChange: (next: ReviewerHarness | "") => void;
	onTrigger: () => void;
	onCancel: () => void;
	onAutoReviewChange: (enabled: boolean) => void;
	isAutoReviewSaving: boolean;
	onKill: () => void;
}) {
	const { t } = useTranslation();
	const latestAutoFailure = reviewStates
		.map((review) => review.latestRun)
		.filter(
			(run): run is ReviewRunFacts =>
				Boolean(
					autoReviewEnabled &&
						run?.triggerSource === "auto" &&
						run.status === "failed" &&
						run.body?.trim(),
				),
		)
		.sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0];
	const [dismissedAutoFailureId, setDismissedAutoFailureId] = useState<string | null>(null);
	useEffect(() => {
		if (!latestAutoFailure || latestAutoFailure.id === dismissedAutoFailureId) return;
		const timer = window.setTimeout(() => setDismissedAutoFailureId(latestAutoFailure.id), 10_000);
		return () => window.clearTimeout(timer);
	}, [dismissedAutoFailureId, latestAutoFailure]);
	if (sortedPRs(session).length === 0) {
		return <p className={inspectorEmptyClass}>{t("inspector.noPROpened")}</p>;
	}
	if (isLoading) {
		return <p className={inspectorEmptyClass}>{t("inspector.loadingReviews")}</p>;
	}

	const openReviewStates = openReviewStatesFor(session, reviewStates);
	// Whichever PR happens to come first is not the reviewer to name. With one PR
	// reviewed earlier by claude-code and another running under codex, taking the
	// first run reported the wrong agent as the one working. Prefer the run
	// actually in flight, then the newest recorded one.
	const runningRun = openReviewStates.find((review) => review.status === "running")?.latestRun;
	const newestRun = openReviewStates
		.map((review) => review.latestRun)
		.filter((run): run is NonNullable<typeof run> => Boolean(run))
		.sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0];
	const latest = runningRun ?? newestRun;
	const resolvedDefaultHarness = resolveDefaultReviewerHarness(config, session.provider);
	const effectiveReviewerHarness = reviewerOverride || resolvedDefaultHarness;
	const activeReviewerHarness = latest?.harness || effectiveReviewerHarness;
	const autoReviewFailure =
		latestAutoFailure && latestAutoFailure.id !== dismissedAutoFailureId ? latestAutoFailure.body.trim() : null;
	const hasReviewerSession = reviewerHandleId.trim() !== "";
	const reviewRunning = reviewIsRunning(openReviewStates);
	const reviewHasRun = reviewRunning || Boolean(latest);
	const runAction = reviewSessionRunAction(openReviewStates, isTriggering);
	const runDisabled = isKilling || isSwitchingReviewer || reviewRunDisabled(openReviewStates, isTriggering);
	const primaryReviewActionLabel = reviewRunning
		? isCancelling
			? t("inspector.review.cancelling")
			: t("inspector.review.cancel")
		: runAction;
	const killDisabled = autoReviewEnabled || isKilling || isTriggering || isSwitchingReviewer || !hasReviewerSession;

	return (
		<div className="mb-2.5 flex flex-col">
				<Section surface title={t("inspector.review.controls")}>
					{error ? (
						<p className="m-0 rounded-md border border-error/28 bg-error/8 px-2.5 py-2 text-sm-md leading-normal text-error">
							{apiErrorMessage(error, t("inspector.reviewRequestFailed"))}
					</p>
				) : null}
				{autoReviewFailure ? (
					<p className="m-0 rounded-md border border-error/28 bg-error/8 px-2.5 py-2 text-sm-md leading-normal text-error" role="status">
						<span className="font-semibold">
							{t("inspector.autoReview")} {t("inspector.review.failed")}:
						</span>{" "}
						{autoReviewFailure}
					</p>
				) : null}
				{/* Neutral, not success: a notice is the trigger declining to run and
				    saying why, so nothing has succeeded. Green reads as "the review ran"
				    at a glance, and DESIGN.md reserves it for the success/mergeable
				    signal. The error variant above keeps red for actual failures.

				    Two lines of boxed prose was a lot of permanent rail for one
				    sentence the user only needs once. The short form confirms the
				    click landed; the sentence itself is a hover/focus away. */}
				{notice ? (
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<button
									aria-label={notice}
									className="mb-2 flex max-w-full shrink-0 items-start gap-1 self-start rounded-sm text-left text-2xs font-medium leading-normal text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
									type="button"
								>
									<Info aria-hidden="true" className="mt-px size-icon-2xs shrink-0" />
									{/* Wraps rather than truncates: this is a sentence now, and
									    clipping it mid-word would hide the part that identifies
									    which commit is meant. The rest still rides the tooltip. */}
									<span className="min-w-0">{t("inspector.reviewAlreadyRanShort")}</span>
								</button>
							</TooltipTrigger>
							<TooltipContent className="max-w-56 leading-normal">{notice}</TooltipContent>
						</Tooltip>
					</TooltipProvider>
				) : null}
				<div className="review-run-controls-container min-w-0 divide-y divide-border/70 text-xs">
					<div className="flex min-h-10 min-w-0 items-center justify-between gap-3 py-2">
						<span className="min-w-0 text-xs font-medium text-foreground">
							{t("inspector.selectReviewerAgent")}
						</span>
						<ReviewerSelect
							ariaLabel={t("inspector.selectReviewerAgent")}
							authorized={agentCatalog?.authorized}
							contentAlign="end"
							defaultHarness={resolvedDefaultHarness}
							defaultOptionLabel={agentLabel(resolvedDefaultHarness)}
							disabled={reviewRunning || autoReviewEnabled || isKilling || isSwitchingReviewer || isTriggering || isCancelling}
							installed={agentCatalog?.installed}
							onChange={(next) => onReviewerOverrideChange(next as ReviewerHarness | "")}
							supported={agentCatalog?.supported}
							triggerClassName="review-run-agent-select ml-auto h-control-md w-auto min-w-0 max-w-[11rem] shrink-0 justify-end px-2 text-right text-xs"
							value={reviewerOverride}
							excludedHarness={resolvedDefaultHarness}
							showDefaultOption
						/>
					</div>
					<InspectorPolicyRow
						checked={autoReviewEnabled}
						description={t("inspector.autoReviewDescription")}
						disabled={isAutoReviewSaving}
						id={`auto-review-${session.id}`}
						label={t("inspector.autoReview")}
						onCheckedChange={onAutoReviewChange}
						tooltipClassName="max-w-64"
					/>
					<div className="flex min-h-10 items-center justify-between gap-3 py-2">
						<span className="text-xs font-medium text-foreground">{t("inspector.review.session")}</span>
						<div className="flex shrink-0 items-center gap-1.5">
							<Button
								aria-label={primaryReviewActionLabel}
								className="shrink-0 gap-1 px-1.5 text-xs [&_svg]:size-icon-sm"
								disabled={reviewRunning ? isCancelling || isKilling || isSwitchingReviewer : runDisabled || autoReviewEnabled}
								onClick={reviewRunning ? onCancel : onTrigger}
								size="sm"
								title={primaryReviewActionLabel}
								type="button"
								variant={reviewRunning ? "ghost" : reviewHasRun ? "secondary" : "primary"}
							>
								{reviewRunning ? <X aria-hidden="true" /> : <Play aria-hidden="true" />}
								<span className="review-run-action-label">{primaryReviewActionLabel}</span>
							</Button>
							{hasReviewerSession ? (
								<Button
								aria-label={isKilling ? t("inspector.review.killingSession") : t("inspector.review.killSession")}
								className="h-control-md w-control-md shrink-0 p-0 text-error [&_svg]:size-icon-sm"
								disabled={killDisabled}
								onClick={onKill}
								size="sm"
								title={isKilling ? t("inspector.review.killingSession") : t("inspector.review.killSession")}
								type="button"
								variant="ghost"
							>
								<Trash2 aria-hidden="true" />
							</Button>
							) : null}
						</div>
					</div>
					<AutoInjectReviewPolicyControl session={session} />
					</div>
				{reviewRunning ? (
					<div className="mt-3 flex items-center gap-2 border-t border-border pt-3">
						<Loader2 aria-hidden="true" className="size-icon-sm shrink-0 animate-spin text-muted-foreground" />
						<span className="min-w-0 flex-1 truncate text-2xs font-medium text-muted-foreground">
							{isCancelling
								? t("inspector.review.cancelling")
								: `Review in progress · ${agentLabel(activeReviewerHarness)}`}
						</span>
					</div>
				) : null}
			</Section>
		</div>
	);
}

function githubVerdict(verdict: string, t: TFunction): { label: string; tone: "neutral" | "running" | "success" | "danger" } {
	switch (verdict) {
		case "approved":
			return { label: t("inspector.review.approved"), tone: "success" };
		case "changes_requested":
			return { label: t("inspector.review.changesRequested"), tone: "danger" };
		case "review_required":
			return { label: t("inspector.review.notRun"), tone: "neutral" };
		default:
			return { label: t("inspector.review.commented"), tone: "neutral" };
	}
}

/* The reviews view is assembled from two queries that live in different
   components, so the derivations both need are module-level rather than
   recomputed (and allowed to drift) in each. */

/** Every recorded reviewer pass per PR, so each reviewer keeps its own tab.
 *  Falls back to the state's own runs against a daemon predating the runs field. */
function runsByPRFrom(openReviewStates: PRReviewState[], runs: ReviewRunFacts[]): Map<string, ReviewRunFacts[]> {
	const byPR = new Map<string, ReviewRunFacts[]>();
	for (const run of runs.filter(
		(run) => (run.status === "complete" || run.status === "delivered") && Boolean(run.body?.trim()),
	)) {
		byPR.set(run.prUrl, [...(byPR.get(run.prUrl) ?? []), run]);
	}
	if (runs.length === 0) {
		for (const state of openReviewStates) {
			const fallback = [state.latestRun, state.previousRun].filter(
				(run): run is ReviewRunFacts =>
					Boolean(run) &&
					(run!.status === "complete" || run!.status === "delivered") &&
					Boolean(run!.body?.trim()),
			);
			if (fallback.length > 0) byPR.set(state.prUrl, fallback);
		}
	}
	return byPR;
}

function reviewRunHasOutcome(run: ReviewRunFacts | undefined): boolean {
	return Boolean(run?.verdict?.trim());
}

/** The PRs AO has an agent review outcome for. */
function triggeredReviewStatesFrom(openReviewStates: PRReviewState[], runs: ReviewRunFacts[]): PRReviewState[] {
	return openReviewStates.filter(
		(reviewState) =>
			reviewRunHasOutcome(reviewState.latestRun) ||
			reviewRunHasOutcome(reviewState.previousRun) ||
			runs.some((run) => run.prUrl === reviewState.prUrl && reviewRunHasOutcome(run)) ||
			reviewState.status === "up_to_date" ||
			reviewState.status === "changes_requested",
	);
}

function aoReviewMeta(reviewState: PRReviewState): string {
	if (
		reviewState.status === "needs_review" &&
		(!reviewState.latestRun ||
			reviewState.latestRun.status === "failed" ||
			reviewState.latestRun.status === "cancelled")
	) {
		return appI18n.t("inspector.latestCommitNotReviewedMeta", { number: reviewState.prNumber });
	}
	const displayRun = reviewState.latestRun ?? reviewState.previousRun;
	if (displayRun?.createdAt) {
		return `#${reviewState.prNumber} · ${formatTimeCompact(displayRun.createdAt)}`;
	}
	if (!displayRun && (reviewState.status === "needs_review" || reviewState.status === "ineligible")) {
		return appI18n.t("inspector.notRunMeta", { number: reviewState.prNumber });
	}
	return `#${reviewState.prNumber}`;
}

// GitHub anchors a posted review at #pullrequestreview-<id> on the PR page; we
// only have that link once the run has been delivered to GitHub.
function aoReviewCommentUrl(run: PRReviewState["latestRun"]): string | null {
	if (!run?.prUrl || !run.githubReviewId) return null;
	return `${run.prUrl}#pullrequestreview-${run.githubReviewId}`;
}

function reviewVerdict(reviewState: PRReviewState): {
	label: string;
	tone: "neutral" | "running" | "success" | "danger";
} {
	if (reviewState.status === "needs_review") {
		return { label: appI18n.t("inspector.review.needed"), tone: "neutral" };
	}
	if (reviewState.latestRun?.status === "failed") {
		return { label: appI18n.t("inspector.review.failed"), tone: "danger" };
	}
	if (reviewState.latestRun?.status === "cancelled") {
		return { label: appI18n.t("inspector.review.cancelled"), tone: "neutral" };
	}
	switch (reviewState.status) {
		case "running":
			return { label: appI18n.t("inspector.review.reviewing"), tone: "running" };
		case "up_to_date":
			return { label: appI18n.t("inspector.review.approved"), tone: "success" };
		case "changes_requested":
			return { label: appI18n.t("inspector.review.changesRequested"), tone: "danger" };
		case "ineligible":
			return { label: appI18n.t("inspector.review.notRun"), tone: "neutral" };
	}
	return { label: appI18n.t("inspector.review.notRun"), tone: "neutral" };
}

function BrowserView({
	session,
	isActive,
	browserPoppedOut,
	browserAnnotationQueue,
	onTogglePopOut,
	browserView,
}: {
	session: WorkspaceSession;
	isActive: boolean;
	browserPoppedOut: boolean;
	browserAnnotationQueue?: BrowserAnnotationQueueModel;
	onTogglePopOut?: (next: boolean) => void;
	browserView?: BrowserViewModel;
}) {
	// While maximized, the browser is a full-window overlay that covers the rail,
	// so the inspector's Browser tab has nothing to show (and must not mount a
	// second BrowserPanelView — it would fight the overlay over the shared native
	// view slot). Exit is via the overlay's own minimize button.
	const { t } = useTranslation();
	if (browserPoppedOut) {
		return (
			<div role="tabpanel">
				<div className={cn(inspectorEmptyClass, "flex flex-col items-center gap-2 py-10 px-5 text-center")}>
					<p className="text-md-sm text-muted-foreground">{t("inspector.browserInCenter")}</p>
					<Button onClick={() => onTogglePopOut?.(false)} size="sm" type="button" variant="outline">
						{t("inspector.returnToPanel")}
					</Button>
				</div>
			</div>
		);
	}

	if (!browserView || !browserAnnotationQueue) {
		return null;
	}

	return (
		<BrowserPanelView
			active={isActive}
			annotationQueue={browserAnnotationQueue}
			browserView={browserView}
			onTogglePopOut={(next) => onTogglePopOut?.(next)}
			poppedOut={false}
			session={session}
		/>
	);
}

function FilesView({ filesView, onOpenFiles }: { filesView?: ReactNode; onOpenFiles?: () => void }) {
	const { t } = useTranslation();
	if (filesView) {
		return (
			<div className="h-full min-h-0" role="tabpanel">
				{filesView}
			</div>
		);
	}
	return (
		<div role="tabpanel">
			<div className={cn(inspectorEmptyClass, "flex flex-col items-center gap-2 px-5 py-10 text-center")}>
				<p className="text-md-sm text-muted-foreground">{t("inspector.filesUnavailable")}</p>
				<Button disabled={!onOpenFiles} onClick={() => onOpenFiles?.()} size="sm" type="button" variant="outline">
					{t("inspector.openFiles")}
				</Button>
			</div>
		</div>
	);
}
