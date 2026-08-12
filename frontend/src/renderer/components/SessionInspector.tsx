import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";
import type { TFunction } from "i18next";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
	ArrowUpRight,
	Bot,
	ChevronDown,
	ChevronRight,
	ChevronUp,
	FileCode2,
	Files as FilesIcon,
	GitPullRequest,
	GitMerge,
	Info,
	MessageSquare,
	Play,
	Trash2,
	Loader2,
	X,
} from "lucide-react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { formatTimeCompact } from "../lib/format-time";
import { AgentAvatar } from "./AgentAvatar";
import {
	sessionScmSummaryQueryKey,
	useSessionScmSummary,
	type SessionPRSummary,
} from "../hooks/useSessionScmSummary";
import { useSessionWorkspaceFilesChangedCount } from "../hooks/useSessionWorkspaceFiles";
import { clearTerminateSessionState, useTerminateSession } from "../hooks/useTerminateSession";
import { prBrowserUrl, prCardPresentation, sessionPRDisplaySummaries } from "../lib/pr-display";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { findProjectOrchestrator, sortedPRs } from "../types/workspace";
import { getAgentActivityView, getSessionTimelinePillView } from "../lib/session-presentation";
import { aoBridge } from "../lib/bridge";
import { BrowserPanelView, type BrowserAnnotationQueueModel } from "./BrowserPanel";
import type { BrowserViewModel } from "../hooks/useBrowserView";
import { useUiStore } from "../stores/ui-store";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";
import { PRCardStatusSummary, PRSummaryMeta } from "./PRSummaryDisplay";
import { SessionTerminationPopover } from "./SessionTerminationPopover";
import { ReviewerSelect } from "./ReviewerSelect";
import { agentsQueryOptions } from "../hooks/useAgentsQuery";
import { Switch } from "./ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./ui/tooltip";
import { appI18n } from "../i18n";
import type { MessageKey } from "../i18n";
import { usesPreviewWorkspaceData as usePreviewData } from "../lib/preview-mode";

type ProjectConfig = components["schemas"]["ProjectConfig"];
type PRReviewState = components["schemas"]["PRReviewState"];
type ReviewsResponse = components["schemas"]["ListReviewsResponse"];
type ReviewRunFacts = components["schemas"]["ReviewRun"];
type OpenReviewerTerminal = (target: { handleId: string; harness: string }) => void;

export type InspectorView = "summary" | "browser" | "files";

const VIEW_DEFS: { id: InspectorView; labelKey: "inspector.summary" | "inspector.browser" | "inspector.files"; icon: ReactNode }[] = [
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

const prStateTone: Record<SessionPRSummary["state"], string> = {
	open: "border-border-strong bg-overlay text-muted-foreground",
	draft: "border-status-in-review/35 bg-status-in-review/10 text-status-in-review",
	merged: "border-border-strong bg-overlay text-success",
	closed: "border-error/40 bg-error/10 text-error",
};

const prStateLabelKeys: Record<SessionPRSummary["state"], MessageKey> = {
	open: "pr.state.open",
	draft: "pr.state.draft",
	merged: "pr.state.merged",
	closed: "pr.state.closed",
};

const inspectorShellClass = "@container/inspector flex h-full min-h-0 flex-col overflow-hidden";

const inspectorBodyBaseClass = "min-h-0 flex-1";

const inspectorScrollableBodyClass = "overflow-y-auto p-3 pb-4 @max-[300px]/inspector:px-2.5";

const inspectorEmptyClass = "text-xs text-settings-muted leading-normal";

const reviewerVerdictTone: Record<"neutral" | "running" | "success" | "danger", string> = {
	neutral: "text-muted-foreground",
	running: "text-working",
	success: "text-success",
	danger: "text-error",
};

function VerdictBadge({ label, tone }: { label: string; tone: "neutral" | "running" | "success" | "danger" }) {
	return (
		<span
			className={cn(
				"inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap text-2xs font-medium",
				reviewerVerdictTone[tone],
			)}
		>
			<span className="size-1.5 shrink-0 rounded-full bg-current" />
			{label}
		</span>
	);
}

/**
 * Tabbed inspector rail beside the terminal (Summary · Browser · Files).
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
	const views = VIEW_DEFS.map((entry) => ({ ...entry, label: t(entry.labelKey) }));
	const view: InspectorView = requestedView;

	if (!session) {
		return (
			<aside className={inspectorShellClass} aria-label={t("inspector.aria")}>
				<div className={cn(inspectorBodyBaseClass, inspectorScrollableBodyClass)}>
					<p className={inspectorEmptyClass}>{t("inspector.loadingSession")}</p>
				</div>
			</aside>
		);
	}

	return (
		<aside className={inspectorShellClass} aria-label={t("inspector.aria")}>
			<div className="flex h-inspector-tabs shrink-0 items-center gap-1 border-b border-border px-2.5" role="tablist">
				{views.map((entry) => (
					<button
						aria-label={entry.label}
						key={entry.id}
						type="button"
						role="tab"
						aria-selected={view === entry.id}
						className={cn(
							"inline-flex h-control-md shrink-0 items-center justify-center gap-1.5 rounded-md px-1.5 text-sm-md font-semibold text-passive transition-[background,color] duration-fast hover:bg-interactive-hover hover:text-foreground",
							view === entry.id && "bg-interactive-active text-foreground",
						)}
						onClick={() => setView(entry.id)}
						title={entry.label}
					>
						<span className="relative inline-flex shrink-0 [&_svg]:size-icon-md">
							{entry.icon}
							{entry.id === "browser" && browserUnseen ? (
								<span
									aria-hidden="true"
									className="absolute -right-1 -top-1 inline-flex size-dot-sm"
									data-testid="browser-unseen-indicator"
								>
									<span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary opacity-75" />
									<span className="relative inline-flex size-dot-sm rounded-full bg-primary ring-2 ring-background" />
								</span>
							) : null}
						</span>
						<span className="truncate @max-[350px]/inspector:hidden">
							{entry.id === "files" && filesChangedCount !== undefined
								? t("files.tabCount", { count: filesChangedCount })
								: entry.label}
						</span>
					</button>
				))}
			</div>

			<div
				className={cn(
					inspectorBodyBaseClass,
					view !== "browser" && view !== "files" && inspectorScrollableBodyClass,
					// Browser and Files own their viewport spacing. Keep their body
					// padding out of the class list entirely so a shorthand `p-3`
					// cannot win over `p-0` through generated utility ordering.
					view === "browser" &&
						!browserPoppedOut &&
						"session-inspector__body--browser p-0 overflow-hidden [&>[role=tabpanel]]:border-0 [&>[role=tabpanel]]:rounded-none",
					view === "files" && "p-0 overflow-hidden [&>[role=tabpanel]]:h-full",
				)}
			>
				{view === "summary" ? <SummaryView onOpenReviewerTerminal={onOpenReviewerTerminal} session={session} /> : null}
				{view === "browser" ? (
					<BrowserView
						browserPoppedOut={browserPoppedOut}
						browserAnnotationQueue={browserAnnotationQueue}
						browserView={browserView}
						isActive={isInspectorVisible && !browserPoppedOut}
						onTogglePopOut={onToggleBrowserPopOut}
						session={session}
					/>
				) : null}
				{view === "files" ? <FilesView filesView={filesView} onOpenFiles={onOpenFiles} /> : null}
			</div>
		</aside>
	);
}

function Section({
	action,
	children,
	className,
	surface = true,
	title,
}: {
	action?: ReactNode;
	children: ReactNode;
	className?: string;
	surface?: boolean;
	/** Omit where the surrounding tab already names the section. */
	title?: string;
}) {
	const heading =
		title || action ? (
			<div className="mb-1 flex items-center justify-between gap-2 text-2xs font-bold uppercase tracking-settings-section text-settings-muted">
				{title ? <span>{title}</span> : <span />}
				{action ?? null}
			</div>
		) : null;
	return (
		<section className={cn("mb-4 last:mb-0", className)} data-testid="inspector-section">
			{heading}
			{surface ? (
				<div className="overflow-hidden rounded-settings-row bg-settings-row px-3.5 py-1.5">
					{children}
				</div>
			) : (
				children
			)}
		</section>
	);
}

function SummaryView({
	session,
	onOpenReviewerTerminal,
}: {
	session: WorkspaceSession;
	onOpenReviewerTerminal?: OpenReviewerTerminal;
}) {
	const { t } = useTranslation();
	const query = useSessionScmSummary(session.id);
	const prSummaries = sessionPRDisplaySummaries(session, query.data);
	const prSectionTitle = prSummaries.length > 1 ? t("inspector.pullRequests", { count: prSummaries.length }) : t("inspector.pullRequest");
	const hasPRs = prSummaries.length > 0;
	const showCompletion = session.kind !== "orchestrator";

	return (
		<div role="tabpanel">
			<Section surface={false} title={prSectionTitle}>
				<div className="flex flex-col gap-1.5">
					{hasPRs ? (
						prSummaries.map((pr) => (
							<PRSummaryCard key={pr.url || pr.htmlUrl || pr.number} pr={pr} sessionId={session.id} />
						))
					) : (
						<p className={inspectorEmptyClass}>{t("inspector.noPROpened")}</p>
					)}
				</div>
			</Section>

			{hasPRs ? <ReviewsSection onOpenReviewerTerminal={onOpenReviewerTerminal} session={session} /> : null}

			{showCompletion ? <CompletionControls session={session} /> : null}

			<Section title={t("inspector.activity")}>
				<ActivityTimeline prs={prSummaries} session={session} />
				<ResumeAgentControl session={session} />
			</Section>
		</div>
	);
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

	if (session.isTerminated === true || session.activity?.state !== "exited") return null;

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

function CompletionControls({ session }: { session: WorkspaceSession }) {
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
		<Section title={t("inspector.completion")}>
			{canTerminateNow ? (
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
					<div className="flex items-center justify-between gap-3 py-1">
						<label className="min-w-0 text-xs font-medium text-settings-label" htmlFor={`merge-policy-${session.id}`}>
							{t("inspector.terminateOnMergeShort")}
						</label>
						<Switch
							aria-label={t("inspector.terminateOnMerge")}
							checked={Boolean(session.terminateOnPrMerge)}
							disabled={policy.isPending}
							id={`merge-policy-${session.id}`}
							onCheckedChange={(checked) => policy.mutate(checked)}
						/>
					</div>
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

function PRSummaryCard({ pr, sessionId }: { pr: SessionPRSummary; sessionId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const presentation = prCardPresentation(pr);
	const canMerge =
		pr.state === "open" &&
		presentation.primary.key === "merge" &&
		presentation.primary.tone === "success" &&
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
	return (
		<article className="rounded-lg border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3 py-2.5">
			{pr.title ? (
				<a
					className="inline text-sm font-semibold leading-snug tracking-tight text-settings-label underline-offset-2 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
					href={prBrowserUrl(pr)}
					rel="noopener noreferrer"
					target="_blank"
				>
					{pr.title}
				</a>
			) : null}
			<div className={cn("flex min-w-0 items-center gap-2", pr.title && "mt-1.5")}>
				<a
					aria-label={t("inspector.openPR", { number: pr.number })}
					className="inline-flex min-w-0 items-center gap-1 font-mono text-xs font-medium text-settings-label decoration-muted-foreground underline-offset-2 hover:text-settings-label hover:underline focus-visible:rounded-sm focus-visible:text-settings-label focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
					href={prBrowserUrl(pr)}
					rel="noopener noreferrer"
					target="_blank"
				>
					<GitPullRequest className="size-icon-sm shrink-0" aria-hidden="true" />
					<span>PR #{pr.number}</span>
					<ArrowUpRight aria-hidden="true" className="size-icon-2xs shrink-0" strokeWidth={2} />
				</a>
				<Badge
					variant="outline"
					className={cn("h-5 px-1.5 text-[9px] leading-none font-medium", prStateTone[pr.state])}
				>
					{t(prStateLabelKeys[pr.state])}
				</Badge>
			</div>
			<PRSummaryMeta className="mt-1.5" pr={pr} />
			{pr.state !== "merged" ? (
				<>
					<PRCardStatusSummary
						action={
							canMerge ? (
								<Button
									aria-label={t("pr.merge.actionFor", { number: pr.number })}
									className="gap-1 px-2"
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
						className="mt-2"
						pr={pr}
					/>
					{mergeError ? (
						<p className="mt-2 text-2xs leading-normal text-error" role="status">
							{mergeError}
						</p>
					) : null}
				</>
			) : null}
		</article>
	);
}

type TimelineTone = "now" | "good" | "warn" | "neutral";

const timelineNodeTone: Record<TimelineTone, string> = {
	neutral: "bg-passive shadow-timeline-dot",
	now: "bg-working shadow-timeline-dot-now",
	good: "bg-success shadow-timeline-dot",
	warn: "bg-warning shadow-timeline-dot",
};

function ActivityTimeline({ prs, session }: { prs: SessionPRSummary[]; session: WorkspaceSession }) {
	type HistoryEntry = {
		tone: TimelineTone;
		node: ReactNode;
		ts: string | null;
		/** Epoch ms for chronological sorting. */
		sortTs: number;
		/** Push order — descending tiebreaker so same-timestamp events keep
		 *  reverse-category order (Done > Merged > Opened > Draft > Created). */
		seq: number;
		markerTone?: string;
		markerBreathe?: boolean;
	};

	const history: HistoryEntry[] = [];
	let seq = 0;

	history.push({
		tone: "neutral",
		node: <>{appI18n.t("inspector.timeline.createdWorkspace")}</>,
		ts: formatTimeCompact(session.createdAt ?? session.updatedAt),
		sortTs: parseTimestamp(session.createdAt ?? session.updatedAt),
		seq: seq++,
	});

	for (const pr of prs.filter((pr) => pr.state === "draft")) {
		history.push({
			tone: "neutral",
			node: <PRTimelineLink pr={pr} verb={appI18n.t("inspector.timeline.draft")} />,
			ts: prStateTime(pr),
			sortTs: parseTimestamp(pr.stateChangedAt ?? pr.updatedAt),
			seq: seq++,
		});
	}

	for (const pr of prs.filter((pr) => pr.state !== "draft")) {
		history.push({
			tone: "neutral",
			node: <PRTimelineLink pr={pr} verb={appI18n.t("inspector.timeline.opened")} />,
			ts: prCreatedTime(pr),
			sortTs: parseTimestamp(pr.createdAt ?? pr.updatedAt),
			seq: seq++,
		});
	}

	for (const pr of prs.filter((pr) => pr.state === "merged")) {
		history.push({
			tone: "good",
			node: <PRTimelineLink pr={pr} verb={appI18n.t("inspector.timeline.merged")} />,
			ts: prStateTime(pr),
			sortTs: parseTimestamp(pr.stateChangedAt ?? pr.updatedAt),
			seq: seq++,
		});
	}

	if (session.status === "merged") {
		history.push({
			tone: "good",
			node: <>{appI18n.t("inspector.timeline.done")}</>,
			ts: latestMergedTime(prs),
			sortTs: latestMergedTimeMs(prs),
			seq: seq++,
		});
	}

	// Sort historical events in reverse-chronological order (newest first).
	// Tie-breaking by descending seq keeps the semantic grouping when timestamps
	// are equal or missing: Done > Merged > Opened > Draft > Created workspace.
	const sortedHistory = [...history].sort((a, b) => b.sortTs - a.sortTs || b.seq - a.seq);

	// Current activity is a live reading, not a historical event. Keep it above
	// the reverse-chronological history and do not imply that its last
	// hook time is when the state transition occurred.
	const activityView = getAgentActivityView(session.activity);
	const current = {
		tone: "now",
		node: (
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
		ts: null,
		markerTone: activityView.tone,
		markerBreathe: activityView.breathe,
	} satisfies {
		tone: TimelineTone;
		node: ReactNode;
		ts: null;
		markerTone: string;
		markerBreathe: boolean;
	};
	const events = [current, ...sortedHistory];

	return (
		<div className="relative pl-5">
			{events.map((event, index) => (
				<div key={index} className="relative pb-4 last:pb-0" data-testid="inspector-timeline-event">
					{index < events.length - 1 ? (
						<span
							aria-hidden="true"
							className={cn(
								"absolute -bottom-[10.5px] -left-3.5 w-px bg-border",
								event.tone === "now" ? "top-1/2" : "top-[10.5px]",
							)}
							data-testid="inspector-timeline-connector"
						/>
					) : null}
					<div className="relative flex min-h-icon-xs items-center">
						<span
							aria-hidden="true"
							className={cn(
								"absolute -left-4.5 size-icon-xs rounded-full",
								event.tone === "now" ? "top-1/2 -translate-y-1/2" : "top-1.5",
								timelineNodeTone[event.tone],
								event.markerBreathe && "animate-status-pulse",
							)}
							style={event.markerTone ? { background: event.markerTone } : undefined}
						/>
						<div className="text-xs leading-normal text-foreground [&_b]:font-semibold">{event.node}</div>
					</div>
					{event.ts ? <div className="mt-1 font-mono text-2xs text-passive">{event.ts}</div> : null}
				</div>
			))}
		</div>
	);
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

/** Parse an ISO timestamp to epoch milliseconds, returning 0 on failure/absence. */
function parseTimestamp(value?: string | null): number {
	if (!value) return 0;
	const ms = Date.parse(value);
	return Number.isFinite(ms) ? ms : 0;
}

function latestMergedTime(prs: SessionPRSummary[]): string | null {
	const ms = latestMergedTimeMs(prs);
	return ms > 0 ? formatTimeCompact(new Date(ms).toISOString()) : null;
}

function latestMergedTimeMs(prs: SessionPRSummary[]): number {
	let latest = 0;
	for (const pr of prs) {
		if (pr.state !== "merged") continue;
		const ms = parseTimestamp(pr.stateChangedAt ?? pr.updatedAt);
		if (ms > latest) latest = ms;
	}
	return latest;
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
		queryKey: ["session-reviews", session.id],
		enabled: hasPr,
		refetchInterval: (query) => {
			const data = query.state.data as ReviewsResponse | undefined;
			const reviews = data?.reviews ?? [];
			return reviews.some((review) => review.status === "running") ? 2500 : false;
		},
		queryFn: async () => {
			if (usePreviewData) return mockReviewsResponse(session);
			const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/reviews", {
				params: { path: { sessionId: session.id } },
			});
			if (error) throw new Error(apiErrorMessage(error, "Unable to load reviews"));
			return data ?? ({ reviewerHandleId: "", reviews: [], runs: [] } satisfies ReviewsResponse);
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
	const [autoInjectReview, setAutoInjectReview] = useState(session.autoInjectReview ?? true);
	useEffect(() => {
		setAutoInjectReview(session.autoInjectReview ?? true);
	}, [session.id, session.autoInjectReview]);
	const saveAutoInjectReview = useMutation({
		mutationFn: async (enabled: boolean) => {
			const { error } = await apiClient.PATCH("/api/v1/sessions/{sessionId}/auto-inject-review", {
				params: { path: { sessionId: session.id } },
				body: { autoInjectReview: enabled },
			});
			if (error) {
				throw new Error(apiErrorMessage(error, t("inspector.review.autoInjectError")));
			}
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
		onError: () => {
			setAutoInjectReview(session.autoInjectReview ?? true);
		},
	});

	const reviewStates = reviewsQuery.data?.reviews ?? [];
	const scmSummary = useSessionScmSummary(session.id);
	const prSummaries = sessionPRDisplaySummaries(session, scmSummary.data);
	const githubReviews = prSummaries.filter(
		(pr) =>
			pr.state === "open" &&
			((pr.review?.reviews?.length ?? 0) > 0 ||
				(pr.review?.unresolvedBy ?? []).some((reviewer) => reviewer.count > 0)),
	);

	return (
		<div className="p-2">
			{/* Running a review is an action; reading them is a list. The action stays
			    on top, then one list carrying both sources keyed by PR. */}
			<ReviewPanel
				config={projectConfigQuery.data}
				error={
					reviewsQuery.error ??
					triggerReview.error ??
					cancelReview.error ??
					killReview.error ??
					saveReviewer.error ??
					saveAutoInjectReview.error
				}
				isLoading={reviewsQuery.isLoading}
				isCancelling={cancelReview.isPending}
				isKilling={killReview.isPending}
				isSwitchingReviewer={saveReviewer.isPending}
				isTriggering={triggerReview.isPending}
				onCancel={() => cancelReview.mutate()}
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
			<div className="mt-2.5 flex items-center justify-between gap-3 rounded-lg border border-border bg-surface px-3 py-2.5">
				<div className="min-w-0">
					<div className="flex min-w-0 items-center gap-1.5">
						<p className="truncate text-xs font-medium text-foreground">{t("inspector.review.autoInject")}</p>
						<Tooltip>
							<TooltipTrigger asChild>
								<button
									type="button"
									className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
									aria-label={t("inspector.review.autoInjectDescription")}
								>
									<Info aria-hidden="true" className="size-icon-2xs" />
								</button>
							</TooltipTrigger>
							<TooltipContent className="max-w-60 leading-normal">
								{t("inspector.review.autoInjectDescription")}
							</TooltipContent>
						</Tooltip>
					</div>
				</div>
				<Switch
					aria-label={t("inspector.review.autoInject")}
					checked={autoInjectReview}
					disabled={saveAutoInjectReview.isPending}
					onCheckedChange={(enabled) => {
						setAutoInjectReview(enabled);
						saveAutoInjectReview.mutate(enabled);
					}}
				/>
			</div>
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

	if (isLoading && rows.length === 0) {
		return (
			<Section surface title={t("inspector.reviews")}>
				<p className={inspectorEmptyClass}>{t("inspector.loadingReviews")}</p>
			</Section>
		);
	}
	if (rows.length === 0) return null;

	return (
		<Section surface={false} title={t("inspector.reviews")}>
			<div className="flex flex-col gap-2">
				{rows.map(([number, { ao, github }], index) => {
					const aoRuns = ao ? (runsByPR.get(ao.prUrl) ?? []) : [];
					const aoReviewNotInjected = aoRuns.some((run) => run.autoInjectReview === false);
					const entries = github?.review?.reviews ?? [];
					const unresolvedBy = github?.review?.unresolvedBy ?? [];
					const unresolved = unresolvedBy.reduce((n, r) => n + r.count, 0);
					const scmReviewNotInjected =
						entries.some((review) => review.autoInjectReview === false) ||
						unresolvedBy.some((reviewer) =>
							reviewer.links.some((link) => link.autoInjectReview === false),
						);
					const meta = [
						ao ? aoReviewMeta(ao) : `#${number}`,
						unresolved > 0 ? t("inspector.unresolvedCount", { count: unresolved }) : null,
					]
						.filter(Boolean)
						.join(" · ");
					return (
						<ReviewDisclosure
							collapsible={rows.length > 1}
							defaultOpen={index === 0}
							key={number}
							meta={meta}
							title={(ao?.title ?? github?.title)?.trim() || `PR #${number}`}
							verdict={ao ? reviewVerdict(ao) : undefined}
						>
							{ao ? (
								<div className="flex min-w-0 flex-col gap-2">
									<ReviewSourceLabel
										icon={<Bot aria-hidden="true" />}
										marker={aoReviewNotInjected ? t("inspector.review.notInjected") : undefined}
									>
										{t("inspector.reviewBySource.ao")}
									</ReviewSourceLabel>
									<ReviewerRuns reviewState={ao} runs={aoRuns} />
								</div>
							) : null}
							{entries.length > 0 || unresolved > 0 ? (
								<div className="flex min-w-0 flex-col gap-2">
									<ReviewSourceLabel
										icon={<GitPullRequest aria-hidden="true" />}
										marker={scmReviewNotInjected ? t("inspector.review.notInjected") : undefined}
									>
										{t("inspector.reviewBySource.github")}
									</ReviewSourceLabel>
									{unresolved > 0 ? <GithubInlineComments reviewers={unresolvedBy} /> : null}
									{entries.length > 0 ? <GithubReviewHistory entries={entries} /> : null}
								</div>
							) : null}
						</ReviewDisclosure>
					);
				})}
			</div>
		</Section>
	);
}

/**
 * Heading for one source's reviews inside a PR row.
 *
 * A bare caption sat on the same visual plane as the rows beneath it, so it
 * read as one more line of text and the two groups ran together. It now takes
 * the Section heading's own weight and colour, and a rule carries it to the
 * edge — enough to bracket the group without spending the vertical space a
 * real section header would cost inside an already-nested row.
 */
function ReviewSourceLabel({ children, icon, marker }: { children: ReactNode; icon: ReactNode; marker?: string }) {
	return (
		<div className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
			<span className="flex size-5 shrink-0 items-center justify-center rounded-sm bg-muted/55 [&_svg]:size-icon-xs">
				{icon}
			</span>
			<span className="shrink-0 text-2xs font-semibold text-foreground">
				{children}
			</span>
			{marker ? (
				<Badge className="h-4 px-1.5 text-[9px] leading-none text-passive" variant="outline">
					{marker}
				</Badge>
			) : null}
			<span aria-hidden="true" className="ml-1 h-px min-w-0 flex-1 bg-border/80" />
		</div>
	);
}

function ReviewDisclosure({
	title,
	meta,
	verdict,
	defaultOpen,
	collapsible = true,
	children,
}: {
	title: string;
	meta: string;
	verdict?: ReturnType<typeof reviewVerdict>;
	defaultOpen: boolean;
	/** A lone PR is always open: there is nothing to choose between, so a
	    chevron would only offer the user a way to hide the one thing here. */
	collapsible?: boolean;
	children: ReactNode;
}) {
	const [open, setOpen] = useState(defaultOpen);
	if (!collapsible) {
		return (
			<article
				className="overflow-hidden rounded-lg border border-border bg-settings-row"
				data-testid="review-pr-row"
			>
				<div className="flex min-w-0 flex-col gap-1 border-b border-border/70 px-3 py-2.5">
					<span className="flex min-w-0 items-start justify-between gap-2">
						<span className="min-w-0 whitespace-normal break-words text-sm-md font-semibold leading-snug text-foreground" title={title}>
						{title}
					</span>
					{verdict ? <VerdictBadge label={verdict.label} tone={verdict.tone} /> : null}
				</span>
					<span className="truncate font-mono text-micro text-passive" title={meta}>
						{meta}
					</span>
				</div>
				<div className="flex flex-col gap-3 px-3 py-3">{children}</div>
			</article>
		);
	}
	return (
		<article className="overflow-hidden rounded-lg border border-border bg-settings-row">
			<button
				aria-expanded={open}
				data-testid="review-pr-row"
				className="flex w-full min-w-0 items-start gap-2 px-3 py-2.5 text-left transition-colors hover:bg-interactive-hover/30"
				onClick={() => setOpen((current) => !current)}
				type="button"
			>
				{open ? (
					<ChevronDown className="size-icon-sm shrink-0 text-passive" aria-hidden="true" />
				) : (
					<ChevronRight className="size-icon-sm shrink-0 text-passive" aria-hidden="true" />
				)}
				<span className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="whitespace-normal break-words text-sm-md font-semibold leading-snug text-foreground" title={title}>
						{title}
					</span>
					<span className="truncate font-mono text-micro text-passive" title={meta}>
						{meta}
					</span>
				</span>
				{verdict ? <VerdictBadge label={verdict.label} tone={verdict.tone} /> : null}
			</button>
			{open ? <div className="flex flex-col gap-3 px-3 py-3">{children}</div> : null}
		</article>
	);
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

// Preview-only pins so the reviews section can be seen mid-run and with a verdict
// left behind by an earlier commit — neither follows from a PR's review decision.
const MOCK_RUNNING_PR = 322;
const MOCK_STALE_PR = 324;

function mockReviewsResponse(session: WorkspaceSession): ReviewsResponse {
	const states: PRReviewState[] = sortedPRs(session).map((pr, index) => {
			const targetSha = `demo${pr.number}${index}`;
			const reviewedAt = new Date(Date.now() - (index + 1) * 11 * 60 * 1000).toISOString();
			const latestRun =
				pr.review === "approved" || pr.review === "changes_requested"
					? {
							autoInjectReview: session.autoInjectReview ?? true,
							batchId: `demo-batch-${session.id}`,
							body:
								pr.review === "approved"
									? "Demo review **approved** the README screenshot flow.\n\n- Layout is stable\n- Browser preview opens cleanly"
									: "Demo review found **polish feedback** for the terminal presentation.\n\n- Tighten toolbar density\n- Recheck contrast",
							createdAt: reviewedAt,
							githubReviewId: `${pr.number}01`,
							harness: "codex",
							id: `demo-review-run-${pr.number}`,
							prUrl: pr.url,
							reviewId: `demo-review-${pr.number}`,
							sessionId: session.id,
							status: "delivered",
							targetSha,
							verdict: pr.review === "approved" ? "approved" : "changes_requested",
						}
					: undefined;
			const run = (over: Record<string, unknown>) => ({
				autoInjectReview: session.autoInjectReview ?? true,
				batchId: `demo-batch-${session.id}`,
				body: "",
				createdAt: reviewedAt,
				githubReviewId: "",
				harness: "codex",
				id: `demo-review-run-${pr.number}`,
				prUrl: pr.url,
				reviewId: `demo-review-${pr.number}`,
				sessionId: session.id,
				status: "complete",
				targetSha,
				verdict: "",
				...over,
			});
			// A couple of PRs are pinned to states the review decision alone cannot
			// produce, so the preview shows every shape the panel can render.
			if (pr.number === MOCK_RUNNING_PR) {
				return {
					latestRun: run({ status: "running", id: `demo-review-run-${pr.number}-live` }),
					prNumber: pr.number,
					prUrl: pr.url,
					status: "running",
					targetSha,
					title: mockReviewTitle(pr.number),
				};
			}
			if (pr.number === MOCK_STALE_PR) {
				// Reviewed, then a new commit landed: the verdict is about code that
				// has since changed, so the panel demotes it to "Previous".
				return {
					previousRun: run({
						status: "delivered",
						verdict: "changes_requested",
						githubReviewId: `${pr.number}09`,
						body: "Demo review asked for a tighter activity sample before the last commit.",
						targetSha: `${targetSha}-old`,
					}),
					prNumber: pr.number,
					prUrl: pr.url,
					status: "needs_review",
					targetSha,
					title: mockReviewTitle(pr.number),
				};
			}
			return {
				latestRun,
				prNumber: pr.number,
				prUrl: pr.url,
				status:
					pr.review === "approved"
						? "up_to_date"
						: pr.review === "changes_requested"
							? "changes_requested"
							: pr.state === "draft"
								? "ineligible"
								: "needs_review",
				targetSha,
				title: mockReviewTitle(pr.number),
			};
	});
	// Earlier passes, so the history control has something to open. Two reviewers
	// on the same PR is the case the control exists for.
	const runs: ReviewRunFacts[] = states.flatMap((state) => {
		const base = {
			autoInjectReview: session.autoInjectReview ?? true,
			batchId: `demo-batch-${session.id}`,
			githubReviewId: "",
			prUrl: state.prUrl,
			reviewId: `demo-review-${state.prNumber}`,
			sessionId: session.id,
			status: "delivered",
			targetSha: state.targetSha,
		};
		return [
			{
				...base,
				id: `demo-hist-${state.prNumber}-a`,
				harness: "codex",
				verdict: "changes_requested",
				body: "Earlier codex pass asked for tests around the discount edge cases.",
				createdAt: new Date(Date.now() - 55 * 60 * 1000).toISOString(),
			},
			{
				...base,
				id: `demo-hist-${state.prNumber}-b`,
				harness: "claude-code",
				verdict: "approved",
				body: "Earlier claude-code pass found nothing blocking.",
				createdAt: new Date(Date.now() - 95 * 60 * 1000).toISOString(),
			},
		];
	});
	return { reviewerHandleId: `${session.id}-reviewer`, reviews: states, runs };
}

function mockReviewTitle(prNumber: number): string {
	switch (prNumber) {
		case 319:
			return "Browser preview rail renders inside AO";
		case 320:
			return "Review tab keeps stacked PR rows visible";
		case 321:
			return "Draft child PR waits for parent review";
		case 318:
			return "Terminal polish feedback";
		case 323:
			return "README screenshot assets ready";
		default:
			return `Demo pull request ${prNumber}`;
	}
}

function ReviewPanel({
	session,
	config,
	reviewStates,
	reviewerHandleId,
	isLoading,
	isTriggering,
	isCancelling,
	isKilling,
	isSwitchingReviewer,
	error,
	notice,
	agentCatalog,
	reviewerOverride,
	onReviewerOverrideChange,
	onTrigger,
	onCancel,
	onKill,
}: {
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
	onKill: () => void;
}) {
	const { t } = useTranslation();
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
	const harness = latest?.harness || config?.reviewers?.[0]?.harness || "claude-code";
	const projectDefaultLabel = t("newTask.projectDefault");
	const hasReviewerSession = reviewerHandleId.trim() !== "";
	const reviewRunning = openReviewStates.some((reviewState) => reviewState.status === "running");
	const reviewHasRun = reviewRunning || Boolean(latest);
	const runAction = reviewSessionRunAction(openReviewStates, isTriggering);
	const runDisabled =
		isTriggering ||
		isKilling ||
		isSwitchingReviewer ||
		openReviewStates.length === 0 ||
		openReviewStates.every((reviewState) => reviewState.status === "ineligible");
	const primaryReviewActionLabel = reviewRunning
		? isCancelling
			? t("inspector.review.cancelling")
			: t("inspector.review.cancel")
		: runAction;
	const killDisabled = isKilling || isTriggering || isSwitchingReviewer || !hasReviewerSession;

	return (
		<div className="mb-2.5 flex flex-col">
				<Section surface title={t("inspector.review.run")}>
					{error ? (
						<p className="m-0 rounded-md border border-error/28 bg-error/8 px-2.5 py-2 text-sm-md leading-normal text-error">
							{apiErrorMessage(error, t("inspector.reviewRequestFailed"))}
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
				<div className="review-run-controls-container min-w-0">
					<div className="review-run-controls flex min-w-0 items-center gap-1.5">
						<ReviewerSelect
							ariaLabel={t("inspector.selectReviewerAgent")}
							authorized={agentCatalog?.authorized}
							defaultHarness={harness}
							defaultOptionLabel={harness ? `${projectDefaultLabel} (${harness})` : projectDefaultLabel}
							defaultTriggerLabel={harness || projectDefaultLabel}
							disabled={reviewRunning || isKilling || isSwitchingReviewer || isTriggering || isCancelling}
							installed={agentCatalog?.installed}
							onChange={(next) => onReviewerOverrideChange(next as ReviewerHarness | "")}
							supported={agentCatalog?.supported}
							triggerClassName="review-run-agent-select h-control-md w-36 min-w-24 max-w-36 shrink text-xs"
							value={reviewerOverride}
						/>
						<div className="review-run-actions ml-auto flex shrink-0 items-center gap-1.5">
							<Button
								aria-label={primaryReviewActionLabel}
								className="shrink-0 gap-1 px-1.5 [&_svg]:size-icon-sm"
								disabled={reviewRunning ? isCancelling || isKilling || isSwitchingReviewer : runDisabled}
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
				</div>
				{reviewRunning ? (
					<div className="mt-3 flex items-center gap-2 border-t border-border pt-3">
						<Loader2 aria-hidden="true" className="size-icon-sm shrink-0 animate-spin text-muted-foreground" />
						<span className="min-w-0 flex-1 truncate text-2xs font-medium text-muted-foreground">
							{isCancelling ? t("inspector.review.cancelling") : `Review in progress · ${harness}`}
						</span>
					</div>
				) : null}
			</Section>
		</div>
	);
}

type GithubReviewEntry = NonNullable<NonNullable<SessionPRSummary["review"]>["reviews"]>[number];
type GithubUnresolvedReviewer = SessionPRSummary["review"]["unresolvedBy"][number];

const REVIEW_HISTORY_PAGE_SIZE = 3;

function GithubReviewHistory({ entries }: { entries: GithubReviewEntry[] }) {
	const sorted = [...entries].sort((a, b) => b.submittedAt.localeCompare(a.submittedAt));
	const latestKey = sorted[0] ? `${sorted[0].reviewerId}:${sorted[0].submittedAt}` : "";
	const [visibleCount, setVisibleCount] = useState(1);
	useEffect(() => setVisibleCount(1), [latestKey]);
	const visible = sorted.slice(0, visibleCount);
	const remaining = Math.max(0, sorted.length - visible.length);
	return (
		<div className="flex min-w-0 flex-col gap-2">
			{visible.map((entry) => (
				<GithubReviewRow entry={entry} key={`${entry.reviewerId}:${entry.submittedAt}`} />
			))}
			{remaining > 0 ? (
				<ReviewHistoryPager
					onCollapse={visibleCount > 1 ? () => setVisibleCount(1) : undefined}
					onLoadMore={() => setVisibleCount((count) => Math.min(sorted.length, count + REVIEW_HISTORY_PAGE_SIZE))}
					remaining={remaining}
				/>
			) : visibleCount > 1 ? (
				<ReviewHistoryPager onCollapse={() => setVisibleCount(1)} remaining={0} />
			) : null}
		</div>
	);
}

function GithubInlineComments({ reviewers }: { reviewers: GithubUnresolvedReviewer[] }) {
	const { t } = useTranslation();
	const active = reviewers.filter((reviewer) => reviewer.count > 0);
	const count = active.reduce((total, reviewer) => total + reviewer.count, 0);
	if (count === 0) return null;
	return (
		<div className="rounded-md border border-error/20 bg-error/6 px-2.5 py-2.5" data-testid="github-inline-comments">
			<div className="flex min-w-0 items-center gap-1.5 text-2xs font-semibold text-foreground">
				<MessageSquare aria-hidden="true" className="size-icon-xs shrink-0 text-error" />
				<span>{t("inspector.openComments")}</span>
				<span className="ml-auto shrink-0 font-mono text-micro font-normal text-error">
					{t("inspector.unresolvedCount", { count })}
				</span>
			</div>
			<div className="mt-2 flex min-w-0 flex-col gap-2">
				{active.map((reviewer) => (
					<div className="min-w-0" key={reviewer.reviewerId}>
						<div className="flex min-w-0 items-center gap-1.5 text-micro text-muted-foreground">
							<span className="min-w-0 truncate font-medium text-foreground">{reviewer.reviewerId}</span>
							{reviewer.isBot ? <span className="font-mono text-passive">{t("inspector.bot")}</span> : null}
						</div>
						<div className="mt-1 flex min-w-0 flex-wrap gap-1">
							{reviewer.links.map((link, index) => (
								<InlineCommentReference
									fallbackUrl={reviewer.reviewUrl}
									index={index}
									key={`${link.url ?? link.file ?? "comment"}:${link.line ?? index}`}
									link={link}
								/>
							))}
							{reviewer.links.length === 0 && reviewer.reviewUrl ? (
								<a
									className="inline-flex items-center gap-0.5 rounded-sm bg-background/35 px-1.5 py-1 font-medium text-muted-foreground no-underline transition-colors hover:text-foreground"
									href={reviewer.reviewUrl}
									rel="noopener noreferrer"
									target="_blank"
								>
									{t("inspector.viewOnPR")}
									<ArrowUpRight aria-hidden="true" className="size-2.5" />
								</a>
							) : null}
						</div>
					</div>
				))}
			</div>
		</div>
	);
}

function InlineCommentReference({
	fallbackUrl,
	index,
	link,
}: {
	fallbackUrl?: string;
	index: number;
	link: GithubUnresolvedReviewer["links"][number];
}) {
	const { t } = useTranslation();
	const label = link.file ? `${link.file}${link.line ? `:${link.line}` : ""}` : t("inspector.commentNumber", { number: index + 1 });
	const className =
		"inline-flex max-w-full min-w-0 items-center gap-1 rounded-sm bg-background/35 px-1.5 py-1 font-mono text-micro text-muted-foreground";
	const contents = (
		<>
			<FileCode2 aria-hidden="true" className="size-2.5 shrink-0" />
			<span className="truncate" title={label}>{label}</span>
		</>
	);
	const href = link.url || fallbackUrl;
	if (!href) return <span className={className}>{contents}</span>;
	return (
		<a
			className={cn(className, "no-underline transition-colors hover:bg-background/60 hover:text-foreground")}
			href={href}
			rel="noopener noreferrer"
			target="_blank"
		>
			{contents}
		</a>
	);
}

function ReviewHistoryPager({
	onCollapse,
	onLoadMore,
	remaining,
}: {
	onCollapse?: () => void;
	onLoadMore?: () => void;
	remaining: number;
}) {
	const { t } = useTranslation();
	return (
		<div className="flex min-w-0 gap-1.5">
			{onCollapse ? (
				<button
					className="flex min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md border border-dashed border-border px-2 py-1.5 text-micro font-medium text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover/30 hover:text-foreground"
					onClick={onCollapse}
					type="button"
				>
					<ChevronUp aria-hidden="true" className="size-icon-2xs shrink-0" />
					<span className="truncate">{t("inspector.showLatestReviewOnly")}</span>
				</button>
			) : null}
			{remaining > 0 && onLoadMore ? (
				<button
					className="flex min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md border border-dashed border-border px-2 py-1.5 text-micro font-medium text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover/30 hover:text-foreground"
					onClick={onLoadMore}
					type="button"
				>
					<ChevronDown aria-hidden="true" className="size-icon-2xs shrink-0" />
					<span className="truncate">{t("inspector.loadMoreReviews", { count: remaining })}</span>
				</button>
			) : null}
		</div>
	);
}

function ReviewMarkdownBody({ body, clamped, testId }: { body: string; clamped: boolean; testId: string }) {
	return (
		<div
			className={cn(
				"min-w-0 break-words text-2xs leading-relaxed text-muted-foreground",
				"[&_a]:font-medium [&_a]:text-foreground [&_a]:underline [&_a]:underline-offset-2",
				"[&_code]:rounded [&_code]:bg-muted/55 [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-foreground",
				"[&_li]:my-0.5 [&_ol]:my-1.5 [&_ol]:list-decimal [&_ol]:pl-4 [&_p]:my-1.5 [&_pre]:my-2",
				"[&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:border [&_pre]:border-border [&_pre]:bg-muted/35 [&_pre]:p-2",
				"[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_strong]:text-foreground [&_table]:my-2 [&_table]:w-full",
				"[&_table]:border-collapse [&_td]:border [&_td]:border-border [&_td]:px-2 [&_td]:py-1",
				"[&_th]:border [&_th]:border-border [&_th]:px-2 [&_th]:py-1 [&_th]:text-foreground",
				"[&_ul]:my-1.5 [&_ul]:list-disc [&_ul]:pl-4 [&>*:first-child]:mt-0 [&>*:last-child]:mb-0",
				clamped && "line-clamp-4",
			)}
			data-testid={testId}
		>
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
		</div>
	);
}

function GithubReviewRow({ entry }: { entry: GithubReviewEntry }) {
	const { t } = useTranslation();
	return (
		<ReviewSummaryCard
			actor={entry.reviewerId}
			body={entry.body}
			isBot={entry.isBot}
			testId="github-review-summary"
			timestamp={entry.submittedAt}
			url={entry.reviewUrl}
			verdict={githubVerdict(entry.verdict, t)}
		/>
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

/** Every recorded reviewer pass for one PR, newest first. */
function ReviewerRuns({
	reviewState,
	runs,
}: {
	reviewState: PRReviewState;
	runs: ReviewRunFacts[];
}) {
	const { t } = useTranslation();
	if (runs.length === 0) {
		return <p className={cn(inspectorEmptyClass, "m-0")}>{t("inspector.noPastReviewSummaries")}</p>;
	}
	const sorted = [...runs].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
	return (
		<ReviewRunList
			reviewState={reviewState}
			runs={sorted}
		/>
	);
}

/** Review history for a PR, with the harness identified on every pass. */
function ReviewRunList({ reviewState, runs }: { reviewState: PRReviewState; runs: ReviewRunFacts[] }) {
	const latestKey = runs[0]?.id ?? "";
	const [visibleCount, setVisibleCount] = useState(1);
	useEffect(() => setVisibleCount(1), [latestKey]);
	const visible = runs.slice(0, visibleCount);
	const remaining = Math.max(0, runs.length - visible.length);
	return (
		<div className={cn("flex min-w-0 flex-col gap-2", reviewState.status === "ineligible" && "opacity-70")}>
			{visible.map((run, index) => (
				<ReviewRunRow isEarlier={index > 0} key={run.id} prUrl={reviewState.prUrl} run={run} />
			))}
			{remaining > 0 ? (
				<ReviewHistoryPager
					onCollapse={visibleCount > 1 ? () => setVisibleCount(1) : undefined}
					onLoadMore={() => setVisibleCount((count) => Math.min(runs.length, count + REVIEW_HISTORY_PAGE_SIZE))}
					remaining={remaining}
				/>
			) : visibleCount > 1 ? (
				<ReviewHistoryPager onCollapse={() => setVisibleCount(1)} remaining={0} />
			) : null}
		</div>
	);
}


// A review body is a full write-up, several paragraphs long. Rendered whole it
// buries the verdict and every other pass below it, which defeats reading the
// history in one place. Clamp to the opening lines — reviewers lead with the
// conclusion — and let the row expand in place for the rest.
const REVIEW_SUMMARY_CLAMP_LINES = 4;

// Whether the clamp will actually hide anything. Cheaper and steadier than
// measuring scrollHeight, which needs a layout pass and reflows on resize; the
// cost of being slightly off is an expander that reveals a line or two.
function isClampedSummary(body: string): boolean {
	return body.split("\n").length > REVIEW_SUMMARY_CLAMP_LINES || body.length > 260;
}

function ReviewRunRow({ run, prUrl, isEarlier }: { run: ReviewRunFacts; prUrl: string; isEarlier: boolean }) {
	const { t } = useTranslation();
	// A terminated run's body is the reason it stopped, not findings.
	const raw = run.status === "cancelled" || run.status === "failed" ? "" : run.body?.trim();
	// Falls back to the PR itself: an AO pass only has a review-comment anchor
	// once it has been submitted to GitHub, and a row with no way out at all is
	// a dead end.
	const reviewUrl = aoReviewCommentUrl(run);
	const url = reviewUrl ?? (prUrl || null);
	return (
		<ReviewSummaryCard
			actor={run.harness || "reviewer"}
			body={raw}
			isEarlier={isEarlier}
			testId="review-run-summary"
			timestamp={run.createdAt}
			url={url}
			verdict={githubVerdict(run.verdict, t)}
		/>
	);
}

function ReviewSummaryCard({
	actor,
	body: rawBody,
	isBot = false,
	isEarlier = false,
	testId,
	timestamp,
	url,
	verdict,
}: {
	actor: string;
	body?: string;
	isBot?: boolean;
	isEarlier?: boolean;
	testId: string;
	timestamp: string;
	url?: string | null;
	verdict: ReturnType<typeof githubVerdict>;
}) {
	const { t } = useTranslation();
	const [expanded, setExpanded] = useState(false);
	const trimmed = rawBody?.trim();
	const body = trimmed ? trimmed.replace(/\n{3,}/g, "\n\n") : trimmed;
	const clamped = Boolean(body) && isClampedSummary(body!);

	return (
		<article className="flex min-w-0 flex-col gap-1 rounded-md bg-overlay/50 px-2.5 py-2.5">
			<span className="flex min-w-0 items-center gap-1.5">
				<span className="inline-flex min-w-0 items-center gap-1 text-micro font-medium text-muted-foreground">
					<AgentAvatar className="size-icon-sm shrink-0" decorative provider={actor} />
					<span className="truncate">{actor}</span>
				</span>
				{isBot ? <span className="shrink-0 font-mono text-micro text-passive">{t("inspector.bot")}</span> : null}
				<VerdictBadge {...verdict} />
				<span className="ml-auto inline-flex shrink-0 items-center gap-1.5 text-micro text-passive">
					{isEarlier ? <span>{t("inspector.earlierPass")}</span> : null}
					<span className="font-mono">{formatTimeCompact(timestamp)}</span>
				</span>
			</span>
			{body ? <ReviewMarkdownBody body={body} clamped={clamped && !expanded} testId={testId} /> : null}
			{clamped || url ? (
				<span className="mt-1 flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-micro text-passive">
					{clamped ? (
						<button
							className="font-medium transition-colors hover:text-foreground"
							onClick={() => setExpanded((open) => !open)}
							type="button"
						>
							{expanded ? t("inspector.showLess") : t("inspector.showMore")}
						</button>
					) : null}
					{clamped && url ? <span aria-hidden="true">·</span> : null}
					{url ? (
						<a
							className="inline-flex items-center gap-0.5 font-medium no-underline transition-colors hover:text-foreground"
							href={url}
							rel="noopener noreferrer"
							target="_blank"
						>
							{t("inspector.viewOnPR")}
							<ArrowUpRight aria-hidden="true" className="size-2.5 shrink-0" />
						</a>
					) : null}
				</span>
			) : null}
		</article>
	);
}

/* The reviews view is assembled from two queries that live in different
   components, so the derivations both need are module-level rather than
   recomputed (and allowed to drift) in each. */

/** Review states for PRs this session still has open. */
function openReviewStatesFor(session: WorkspaceSession, reviewStates: PRReviewState[]): PRReviewState[] {
	const openPRURLs = new Set(
		sortedPRs(session)
			.filter((pr) => pr.state === "open")
			.map((pr) => pr.url),
	);
	return reviewStates.filter((reviewState) => openPRURLs.has(reviewState.prUrl));
}

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
	return Boolean(
		run &&
			(run.verdict?.trim() ||
				run.status === "complete" ||
				run.status === "delivered" ||
				run.status === "failed" ||
				run.status === "cancelled"),
	);
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
		case "needs_review":
		case "ineligible":
			return { label: appI18n.t("inspector.review.notRun"), tone: "neutral" };
	}
	return { label: appI18n.t("inspector.review.notRun"), tone: "neutral" };
}

function reviewSessionRunAction(reviewStates: PRReviewState[], isTriggering: boolean): string {
	if (isTriggering || reviewStates.some((reviewState) => reviewState.status === "running")) {
		return appI18n.t("inspector.review.reviewing");
	}
	if (reviewStates.some((reviewState) => reviewState.status === "changes_requested" || reviewState.latestRun)) {
		return appI18n.t("inspector.review.rerun");
	}
	return appI18n.t("inspector.review.run");
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
