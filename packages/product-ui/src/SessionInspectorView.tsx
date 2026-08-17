import { type KeyboardEvent, type ReactNode, useEffect, useState } from "react";
import type { ExternalLinkComponent } from "./external-link";
import {
	ArrowUpRightIcon,
	BotIcon,
	CheckIcon,
	ChevronIcon,
	GitPullRequestIcon,
} from "./icons";
import {
	PRCardStatusSummary,
	PRSummaryMeta,
	type CountNounLabel,
} from "./PRSummaryDisplay";
import type {
	PRCardPresentation,
	PRSummaryMetadata,
} from "./pull-request-models";
import { cn } from "./utils";
import { GithubAvatar } from "./GithubAvatar";

export type InspectorView = "summary" | "reviews" | "browser" | "files";

export type InspectorTab = {
	badge?: boolean;
	displayLabel?: string;
	icon: ReactNode;
	id: InspectorView;
	label: string;
};

const inspectorShellClass = "@container/inspector flex h-full min-h-0 flex-col overflow-hidden";
const inspectorBodyBaseClass = "min-h-0 flex-1";
const inspectorScrollableBodyClass = "board-scrollbar overflow-x-hidden overflow-y-auto p-3 pb-4 @max-[300px]/inspector:px-2.5";
export const inspectorEmptyClass = "text-xs text-settings-muted leading-normal";

export function SessionInspectorShellView({
	activeView,
	ariaLabel,
	browserPoppedOut,
	browserView,
	filesView,
	headerActions,
	isVisible = true,
	loadingText,
	onViewChange,
	reviewsView,
	summaryView,
	tabs,
}: {
	activeView: InspectorView;
	ariaLabel: string;
	browserPoppedOut: boolean;
	browserView?: ReactNode;
	filesView?: ReactNode;
	headerActions?: ReactNode;
	isVisible?: boolean;
	loadingText?: string;
	onViewChange: (view: InspectorView) => void;
	reviewsView?: ReactNode;
	summaryView?: ReactNode;
	tabs: InspectorTab[];
}) {
	if (loadingText) {
		return (
			<aside className={inspectorShellClass} aria-label={ariaLabel}>
				<div className={cn(inspectorBodyBaseClass, inspectorScrollableBodyClass)}>
					<p className={inspectorEmptyClass}>{loadingText}</p>
				</div>
			</aside>
		);
	}

	const selectAdjacentTab = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
		let nextIndex: number;
		switch (event.key) {
			case "ArrowLeft":
				nextIndex = (index - 1 + tabs.length) % tabs.length;
				break;
			case "ArrowRight":
				nextIndex = (index + 1) % tabs.length;
				break;
			case "Home":
				nextIndex = 0;
				break;
			case "End":
				nextIndex = tabs.length - 1;
				break;
			default:
				return;
		}
		event.preventDefault();
		onViewChange(tabs[nextIndex].id);
		event.currentTarget.parentElement
			?.querySelectorAll<HTMLButtonElement>('[role="tab"]')
			.item(nextIndex)
			.focus();
	};

	return (
		<aside className={inspectorShellClass} aria-label={ariaLabel}>
			<div className="session-inspector__topbar flex h-inspector-tabs shrink-0 items-center border-b border-border pl-2.5">
				{isVisible ? (
					<div className="session-inspector__tablist flex min-w-0 flex-1 items-center justify-start gap-0" role="tablist">
						{tabs.map((tab, index) => (
							<button
								aria-label={tab.label}
								key={tab.id}
								type="button"
								role="tab"
								aria-selected={activeView === tab.id}
								tabIndex={activeView === tab.id ? 0 : -1}
								className={cn(
									"session-inspector__tab-button inline-flex h-control-md shrink-0 items-center justify-center rounded-md px-1 font-semibold text-passive transition-[background,color] duration-fast hover:bg-interactive-hover hover:text-foreground",
									activeView === tab.id && "bg-interactive-active text-foreground",
								)}
								onClick={() => onViewChange(tab.id)}
								onKeyDown={(event) => selectAdjacentTab(event, index)}
								title={tab.label}
							>
								<span className="relative inline-flex shrink-0 [&_svg]:size-icon-md">
									{tab.icon}
									{tab.badge ? (
										<span
											aria-hidden="true"
											className="absolute right-0 top-0 inline-flex size-dot-sm"
											data-testid="browser-unseen-indicator"
										>
											<span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary opacity-75" />
											<span className="relative inline-flex size-dot-sm rounded-full bg-primary ring-2 ring-background" />
										</span>
									) : null}
								</span>
								<span className="session-inspector__responsive-label whitespace-nowrap text-2xs">
									{tab.displayLabel ?? tab.label}
								</span>
							</button>
						))}
					</div>
				) : null}
				{isVisible ? headerActions : null}
			</div>

			<div
				aria-hidden={!isVisible}
				className={cn(
					inspectorBodyBaseClass,
					!isVisible && "invisible pointer-events-none",
					activeView !== "browser" && activeView !== "files" && inspectorScrollableBodyClass,
					activeView === "browser" &&
						!browserPoppedOut &&
						"session-inspector__body--browser p-0 overflow-hidden [&>[role=tabpanel]]:border-0 [&>[role=tabpanel]]:rounded-none",
					activeView === "files" && "p-0 overflow-hidden [&>[role=tabpanel]]:h-full",
				)}
				inert={!isVisible}
			>
				{activeView === "summary" ? summaryView : null}
				{activeView === "reviews" ? reviewsView : null}
				{activeView === "browser" ? browserView : null}
				{activeView === "files" ? filesView : null}
			</div>
		</aside>
	);
}

export function InspectorSection({
	action,
	children,
	className,
	surface = true,
	title,
	titleClassName,
}: {
	action?: ReactNode;
	children: ReactNode;
	className?: string;
	surface?: boolean;
	title?: string;
	titleClassName?: string;
}) {
	const heading =
		title || action ? (
			<div className={cn("mb-1 flex items-center justify-between gap-2 text-2xs font-bold uppercase tracking-settings-section text-settings-muted", titleClassName)}>
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

export function SessionInspectorSummaryView({
	activity,
	activityTitle,
	completion,
	pullRequestCards,
	pullRequestTitle,
	reviews,
	usage,
}: {
	activity: ReactNode;
	activityTitle: string;
	completion?: ReactNode;
	pullRequestCards: ReactNode;
	pullRequestTitle: string;
	reviews?: ReactNode;
	usage?: ReactNode;
}) {
	return (
		<div role="tabpanel">
			<InspectorSection surface={false} title={pullRequestTitle}>
				<div className="flex flex-col gap-1.5">{pullRequestCards}</div>
			</InspectorSection>
			{reviews}
			{completion}
			<InspectorSection title={activityTitle}>{activity}</InspectorSection>
			{usage}
		</div>
	);
}

export type InspectorPullRequestState = "open" | "draft" | "merged" | "closed";

export type InspectorPullRequest = PRSummaryMetadata & {
	card: PRCardPresentation;
	href: string;
	number: number;
	state: InspectorPullRequestState;
	stateLabel: string;
	title?: string;
	reviewDetailsAction?: ReactNode;
};

const prStateTone: Record<InspectorPullRequestState, string> = {
	open: "border-border-strong bg-overlay text-muted-foreground",
	draft: "border-status-in-review/35 bg-status-in-review/10 text-status-in-review",
	merged: "border-border-strong bg-overlay text-success",
	closed: "border-error/40 bg-error/10 text-error",
};

export function InspectorPullRequestCardView({
	countNounLabel,
	externalIcon,
	externalLink: ExternalLink,
	mergeAction,
	mergeError,
	openLabel,
	pr,
	pullRequestIcon,
	statusNotice,
}: {
	countNounLabel: CountNounLabel;
	externalIcon?: ReactNode;
	externalLink: ExternalLinkComponent;
	mergeAction?: ReactNode;
	mergeError?: string | null;
	openLabel: string;
	pr: InspectorPullRequest;
	pullRequestIcon?: ReactNode;
	statusNotice?: ReactNode;
}) {
	return (
		<article className="min-w-0 w-full rounded-lg border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3 py-2.5">
			{pr.title ? (
				<ExternalLink
					className="inline text-sm font-semibold leading-snug tracking-tight text-settings-label underline-offset-2 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
					href={pr.href}
				>
					{pr.title}
				</ExternalLink>
			) : null}
			<div className={cn("flex min-w-0 items-center gap-2", pr.title && "mt-1.5")}>
				<ExternalLink
					ariaLabel={openLabel}
					className="inline-flex min-w-0 items-center gap-1 font-mono text-xs font-medium text-settings-label decoration-muted-foreground underline-offset-2 hover:text-settings-label hover:underline focus-visible:rounded-sm focus-visible:text-settings-label focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
					href={pr.href}
				>
					{pullRequestIcon ?? <GitPullRequestIcon className="size-icon-sm shrink-0" />}
					<span>PR #{pr.number}</span>
					{externalIcon ?? <ArrowUpRightIcon className="size-icon-2xs shrink-0" />}
				</ExternalLink>
				<span
					className={cn(
						"inline-flex h-5 shrink-0 items-center justify-center gap-1 overflow-hidden whitespace-nowrap rounded-full border border-transparent px-2 py-0.5 text-xs font-medium transition-[background-color,border-color,color,box-shadow] focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 [&>svg]:pointer-events-none [&>svg]:size-3",
						"border-border text-foreground hover:bg-muted",
						"h-5 px-1.5 text-[9px] leading-none font-medium",
						prStateTone[pr.state],
					)}
					data-slot="badge"
				>
					{pr.stateLabel}
				</span>
			</div>
			<PRSummaryMeta
				className="mt-1.5"
				countNounLabel={countNounLabel}
				externalLink={ExternalLink}
				pr={pr}
			/>
			{pr.state !== "merged" ? (
				<>
					<PRCardStatusSummary
						action={mergeAction}
						className="mt-2"
						externalLink={ExternalLink}
						presentation={pr.card}
						reviewDetailsAction={pr.reviewDetailsAction}
					/>
					{statusNotice}
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

export type InspectorTimelineTone = "now" | "good" | "warn" | "neutral";

export type InspectorTimelineEvent = {
	content: ReactNode;
	markerBreathe?: boolean;
	markerTone?: string;
	timestamp: string | null;
	tone: InspectorTimelineTone;
};

const timelineNodeTone: Record<InspectorTimelineTone, string> = {
	neutral: "bg-passive shadow-timeline-dot",
	now: "bg-working shadow-timeline-dot-now",
	good: "bg-success shadow-timeline-dot",
	warn: "bg-warning shadow-timeline-dot",
};

export function InspectorActivityTimelineView({ events }: { events: InspectorTimelineEvent[] }) {
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
						<div className="text-xs leading-normal text-foreground [&_b]:font-semibold">{event.content}</div>
					</div>
					{event.timestamp ? (
						<div className="mt-1 font-mono text-2xs text-passive">{event.timestamp}</div>
					) : null}
				</div>
			))}
		</div>
	);
}

export type InspectorVerdict = {
	label: string;
	tone: "neutral" | "running" | "success" | "danger";
};

export type InspectorReviewRun = {
	body?: string;
	createdAtLabel: string;
	harness: string;
	id: string;
	status: string;
	url?: string | null;
	verdict: InspectorVerdict;
};

export type InspectorInlineComment = {
	autoInjectReview?: boolean;
	body?: string;
	file?: string;
	line?: number;
	pullRequestUrl?: string;
	url?: string;
};

export type InspectorGithubReview = {
	body?: string;
	id: string;
	inlineComments?: InspectorInlineComment[];
	isBot?: boolean;
	pullRequestUrl?: string;
	reviewerId: string;
	reviewUrl?: string;
	submittedAt: string;
	submittedAtLabel: string;
	verdict: InspectorVerdict;
};

export type InspectorUnresolvedReviewer = {
	count: number;
	isBot?: boolean;
	links: {
		autoInjectReview?: boolean;
		body?: string;
		file?: string;
		line?: number;
		url?: string;
	}[];
	reviewerId: string;
	reviewUrl?: string;
};

export type InspectorReviewGroup = {
	ao?: {
		dimmed?: boolean;
		historical?: boolean;
		notInjected?: boolean;
		runs: InspectorReviewRun[];
	};
	github?: {
		entries: InspectorGithubReview[];
		notInjected?: boolean;
		unresolved: number;
		unresolvedBy: InspectorUnresolvedReviewer[];
	};
	meta: string;
	number: number;
	title: string;
	verdict?: InspectorVerdict;
};

export type InspectorReviewLabels = {
	aoSource: string;
	bot: string;
	earlierPass: string;
	githubSource: string;
	loadingReviews: string;
	loadMoreReviews: (count: number) => string;
	noPastReviewSummaries: string;
	notInjected: string;
	openComments: string;
	openInlineComments: (count: number) => string;
	requestRereviewPR: string;
	reviews: string;
	reviewedAt: (time: string) => string;
	resolvedComments: (count: number) => string;
	rereviewRequested: string;
	rereviewRequestFailed: string;
	resolveComment: string;
	resolvedReview: string;
	resolveReviewFailed: string;
	sendToWorkerAgent: string;
	sentToWorkerAgent: string;
	sendToWorkerAgentError: string;
	showLatestReviewOnly: string;
	showLess: string;
	showMore: string;
	commentNumber: (number: number) => string;
	unresolvedCount: (count: number) => string;
	viewInFile: string;
	viewOnPR: string;
};

export function InspectorReviewsView({
	externalLink,
	groups,
	isLoading,
	labels,
	onRequestRereview,
	onResolveInlineComment,
	onSendInlineComment,
	renderAvatar,
	renderMarkdown,
}: {
	externalLink: ExternalLinkComponent;
	groups: InspectorReviewGroup[];
	isLoading: boolean;
	labels: InspectorReviewLabels;
	onRequestRereview?: (review: InspectorGithubReview) => Promise<void> | void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	renderAvatar: (harness: string) => ReactNode;
	renderMarkdown: (body: string) => ReactNode;
}) {
	if (isLoading && groups.length === 0) {
		return (
			<InspectorSection surface title={labels.reviews}>
				<p className={inspectorEmptyClass}>{labels.loadingReviews}</p>
			</InspectorSection>
		);
	}
	if (groups.length === 0) return null;
	return (
		<InspectorSection surface={false} title={labels.reviews} titleClassName="text-foreground [&>span:first-child]:text-xs [&>span:first-child]:tracking-wide">
			<div className="flex flex-col gap-2">
				{groups.map((group, index) => (
					<ReviewDisclosure
						collapsible={groups.length > 1}
						defaultOpen={index === 0 || Boolean(group.github?.unresolved)}
						key={group.number}
						meta={group.meta}
						title={group.title}
						verdict={group.verdict}
					>
						{group.ao ? (
							<div className="flex min-w-0 flex-col gap-2">
								<ReviewSourceLabel
									icon={<BotIcon />}
								>
									{labels.aoSource}
								</ReviewSourceLabel>
								<ReviewRuns
									dimmed={group.ao.dimmed}
									externalLink={externalLink}
									historical={group.ao.historical}
									labels={labels}
									renderAvatar={renderAvatar}
									renderMarkdown={renderMarkdown}
									runs={group.ao.runs}
								/>
							</div>
						) : null}
						{group.github && (group.github.entries.length > 0 || group.github.unresolved > 0) ? (
							<div className="flex min-w-0 flex-col gap-2">
								<ReviewSourceLabel
									icon={<GitPullRequestIcon />}
								>
									{labels.githubSource}
								</ReviewSourceLabel>
								<GithubReviewHistory
									entries={group.github.entries}
									externalLink={externalLink}
									labels={labels}
									onSendInlineComment={onSendInlineComment}
									onRequestRereview={onRequestRereview}
									onResolveInlineComment={onResolveInlineComment}
									renderMarkdown={renderMarkdown}
								/>
							</div>
						) : null}
					</ReviewDisclosure>
				))}
			</div>
		</InspectorSection>
	);
}

const reviewerVerdictTone: Record<InspectorVerdict["tone"], string> = {
	neutral: "text-muted-foreground",
	running: "text-working",
	success: "text-success",
	danger: "text-error",
};

function VerdictBadge({ verdict }: { verdict: InspectorVerdict }) {
	return (
		<span className={cn("inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap text-2xs font-medium", reviewerVerdictTone[verdict.tone])}>
			<span className="size-1.5 shrink-0 rounded-full bg-current" />
			{verdict.label}
		</span>
	);
}

function ReviewSourceLabel({
	children,
	icon,
	marker,
}: {
	children: ReactNode;
	icon: ReactNode;
	marker?: string;
}) {
	return (
		<div className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
			<span className="flex size-5 shrink-0 items-center justify-center rounded-sm bg-muted/55 [&_svg]:size-icon-xs">
				{icon}
			</span>
			<span className="shrink-0 text-2xs font-semibold text-foreground">
				{children}
			</span>
			{marker ? (
				<span
					className={cn(
						"inline-flex h-5 shrink-0 items-center justify-center gap-1 overflow-hidden whitespace-nowrap rounded-full border border-transparent px-2 py-0.5 text-xs font-medium transition-[background-color,border-color,color,box-shadow] focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 [&>svg]:pointer-events-none [&>svg]:size-3",
						"border-border text-foreground hover:bg-muted",
						"h-4 px-1.5 text-[9px] leading-none text-passive",
					)}
					data-slot="badge"
				>
					{marker}
				</span>
			) : null}
			<span aria-hidden="true" className="ml-1 h-px min-w-0 flex-1 bg-border/80" />
		</div>
	);
}

function ReviewDisclosure({
	children,
	collapsible = true,
	defaultOpen,
	meta,
	title,
	verdict,
}: {
	children: ReactNode;
	collapsible?: boolean;
	defaultOpen: boolean;
	meta: string;
	title: string;
	verdict?: InspectorVerdict;
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
						<span
							className="min-w-0 whitespace-normal break-words text-sm-md font-semibold leading-snug text-foreground"
							title={title}
						>
							{title}
						</span>
						{verdict ? <VerdictBadge verdict={verdict} /> : null}
					</span>
					<span className="whitespace-normal break-words font-mono text-micro leading-snug text-passive" title={meta}>
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
				<ChevronIcon className="size-icon-sm shrink-0 text-passive" direction={open ? "down" : "right"} />
					<span className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="whitespace-normal break-words text-sm-md font-semibold leading-snug text-foreground" title={title}>
						{title}
					</span>
					<span className="whitespace-normal break-words font-mono text-micro leading-snug text-passive" title={meta}>
						{meta}
					</span>
				</span>
				{verdict ? <VerdictBadge verdict={verdict} /> : null}
			</button>
			{open ? <div className="flex flex-col gap-3 px-3 py-3">{children}</div> : null}
		</article>
	);
}

function ReviewRuns({
	dimmed,
	externalLink,
	historical,
	labels,
	renderAvatar,
	renderMarkdown,
	runs,
}: {
	dimmed?: boolean;
	externalLink: ExternalLinkComponent;
	historical?: boolean;
	labels: InspectorReviewLabels;
	renderAvatar: (harness: string) => ReactNode;
	renderMarkdown: (body: string) => ReactNode;
	runs: InspectorReviewRun[];
}) {
	if (runs.length === 0) {
		return <p className={cn(inspectorEmptyClass, "m-0")}>{labels.noPastReviewSummaries}</p>;
	}
	return (
		<ReviewRunHistory
			dimmed={dimmed}
			externalLink={externalLink}
			historical={historical}
			labels={labels}
			renderAvatar={renderAvatar}
			renderMarkdown={renderMarkdown}
			runs={runs}
		/>
	);
}

function ReviewRunHistory({
	dimmed,
	externalLink,
	historical,
	labels,
	renderAvatar,
	renderMarkdown,
	runs,
}: {
	dimmed?: boolean;
	externalLink: ExternalLinkComponent;
	historical?: boolean;
	labels: InspectorReviewLabels;
	renderAvatar: (harness: string) => ReactNode;
	renderMarkdown: (body: string) => ReactNode;
	runs: InspectorReviewRun[];
}) {
	const latestKey = runs[0]?.id ?? "";
	const [visibleCount, setVisibleCount] = useState(1);
	useEffect(() => setVisibleCount(1), [latestKey]);
	const visible = runs.slice(0, visibleCount);
	const remaining = Math.max(0, runs.length - visible.length);
	return (
		<div className={cn("flex min-w-0 flex-col gap-2", dimmed && "opacity-70")}>
			{visible.map((run, index) => (
				<ReviewSummaryCard
					actor={run.harness || "reviewer"}
					body={run.status === "cancelled" || run.status === "failed" ? "" : run.body}
					externalLink={externalLink}
					isEarlier={historical || index > 0}
					key={run.id}
					labels={labels}
					renderAvatar={renderAvatar}
					renderMarkdown={renderMarkdown}
					testId="review-run-summary"
					timestamp={run.createdAtLabel}
					url={run.url}
					verdict={run.verdict}
				/>
			))}
			<ReviewHistoryPager
				labels={labels}
				onCollapse={visibleCount > 1 ? () => setVisibleCount(1) : undefined}
				onLoadMore={
					remaining > 0
						? () => setVisibleCount((count) => Math.min(runs.length, count + REVIEW_HISTORY_PAGE_SIZE))
						: undefined
				}
				remaining={remaining}
			/>
		</div>
	);
}

const REVIEW_HISTORY_PAGE_SIZE = 3;

function ReviewHistoryPager({
	labels,
	onCollapse,
	onLoadMore,
	remaining,
}: {
	labels: InspectorReviewLabels;
	onCollapse?: () => void;
	onLoadMore?: () => void;
	remaining: number;
}) {
	if (!onCollapse && (!onLoadMore || remaining === 0)) return null;
	return (
		<div className="flex min-w-0 gap-1.5">
			{onCollapse ? (
				<button
					className="flex min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md border border-dashed border-border px-2 py-1.5 text-micro font-medium text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover/30 hover:text-foreground"
					onClick={onCollapse}
					type="button"
				>
					<ChevronIcon className="size-icon-2xs shrink-0" direction="up" />
					<span className="truncate">{labels.showLatestReviewOnly}</span>
				</button>
			) : null}
			{remaining > 0 && onLoadMore ? (
				<button
					className="flex min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md border border-dashed border-border px-2 py-1.5 text-micro font-medium text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover/30 hover:text-foreground"
					onClick={onLoadMore}
					type="button"
				>
					<ChevronIcon className="size-icon-2xs shrink-0" direction="down" />
					<span className="truncate">{labels.loadMoreReviews(remaining)}</span>
				</button>
			) : null}
		</div>
	);
}

function GithubReviewHistory({
	entries,
	externalLink,
	labels,
	onRequestRereview,
	onResolveInlineComment,
	onSendInlineComment,
	renderMarkdown,
}: {
	entries: InspectorGithubReview[];
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onRequestRereview?: (review: InspectorGithubReview) => Promise<void> | void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	renderMarkdown: (body: string) => ReactNode;
}) {
	const sorted = [...entries].sort((a, b) => b.submittedAt.localeCompare(a.submittedAt));
	if (entries.length === 0) return null;
	return (
		<div className="flex min-w-0 flex-col gap-2">
			{sorted.map((entry) => (
				<ExternalReviewCard
					defaultOpen={false}
					entry={entry}
					externalLink={externalLink}
					key={entry.id}
					labels={labels}
					onRequestRereview={onRequestRereview}
					onResolveInlineComment={onResolveInlineComment}
					onSendInlineComment={onSendInlineComment}
					renderMarkdown={renderMarkdown}
				/>
			))}
		</div>
	);
}

function ExternalReviewCard({
	defaultOpen,
	entry,
	externalLink,
	labels,
	onRequestRereview,
	onResolveInlineComment,
	onSendInlineComment,
	renderMarkdown,
}: {
	defaultOpen: boolean;
	entry: InspectorGithubReview;
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onRequestRereview?: (review: InspectorGithubReview) => Promise<void> | void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	renderMarkdown: (body: string) => ReactNode;
}) {
	const [open, setOpen] = useState(defaultOpen);
	const [rereviewRequested, setRereviewRequested] = useState(false);
	const [rereviewError, setRereviewError] = useState(false);
	const body = entry.body?.trim();
	const inlineComments = entry.inlineComments ?? [];
	const openInlineCount = inlineComments.filter((comment) => comment.body?.trim() || comment.file || comment.url).length;
	return (
		<article className="min-w-0 border-b border-border/70 py-3 first:pt-0 last:border-b-0 last:pb-0" data-testid="github-review-card">
			<button
				aria-expanded={open}
				className="flex w-full min-w-0 items-start gap-2 rounded-md px-1 py-1 text-left transition-colors hover:bg-interactive-hover/30"
				onClick={() => setOpen((current) => !current)}
				type="button"
			>
				<ChevronIcon className="mt-0.5 size-icon-2xs shrink-0 text-passive" direction={open ? "down" : "right"} />
				<span className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="flex min-w-0 items-center gap-1.5">
						<span className="inline-flex min-w-0 items-center gap-1 text-xs font-semibold text-foreground">
							<GithubAvatar className="size-5" login={entry.reviewerId} />
							<span className="truncate">{entry.reviewerId}</span>
						</span>
						{entry.isBot ? <span className="shrink-0 font-mono text-micro text-passive">{labels.bot}</span> : null}
					</span>
					<span className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 font-mono text-micro text-passive">
						{entry.submittedAtLabel ? <span>{labels.reviewedAt(entry.submittedAtLabel)}</span> : null}
					</span>
				</span>
				<VerdictBadge verdict={entry.verdict} />
			</button>
			{open ? (
				<div className="flex min-w-0 flex-col gap-3 px-1 pt-3">
					{onRequestRereview ? (
						<div className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-y border-border/50 py-2 @max-[300px]/inspector:flex-col @max-[300px]/inspector:items-stretch">
							{openInlineCount > 0 ? (
								<span className="font-mono text-2xs font-semibold text-error">{labels.unresolvedCount(openInlineCount)}</span>
							) : <span aria-hidden="true" />}
							{rereviewRequested ? (
								<span className="inline-flex h-control-md items-center gap-1.5 text-2xs font-medium text-success">
									<CheckIcon className="size-icon-xs shrink-0" />
									{labels.rereviewRequested}
								</span>
							) : (
								<button
									className="inline-flex h-7 min-w-0 max-w-full items-center justify-center rounded-md border border-border-strong px-1.5 text-center text-micro font-medium tracking-tight text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground @max-[300px]/inspector:w-full"
									onClick={async () => {
										setRereviewError(false);
										try {
											await onRequestRereview(entry);
											setRereviewRequested(true);
										} catch {
											setRereviewError(true);
										}
									}}
									type="button"
								>
									{labels.requestRereviewPR}
								</button>
							)}
						</div>
					) : null}
					{rereviewError ? <p className="m-0 text-2xs font-medium text-error">{labels.rereviewRequestFailed}</p> : null}
					{body ? (
						<ReviewMarkdownBody body={body} clamped={false} renderMarkdown={renderMarkdown} testId="github-review-summary" />
					) : null}
					<ReviewLinks
						clamped={false}
						expanded={false}
						externalLink={externalLink}
						labels={labels}
						onExpandedChange={() => undefined}
						url={entry.reviewUrl}
					/>
					{openInlineCount > 0 ? (
						<GithubInlineComments
							externalLink={externalLink}
							labels={labels}
							onSendInlineComment={onSendInlineComment}
							onResolveInlineComment={onResolveInlineComment}
							reviewers={[
								{
									count: openInlineCount,
									isBot: entry.isBot,
									links: inlineComments,
									reviewerId: entry.reviewerId,
									reviewUrl: entry.reviewUrl,
								},
							]}
							showReviewer={false}
						/>
					) : null}
				</div>
			) : null}
		</article>
	);
}

const RESOLVED_COMMENT_DISPLAY_MS = 1000;

function GithubInlineComments({
	externalLink: ExternalLink,
	labels,
	onResolveInlineComment,
	onSendInlineComment,
	reviewers,
	showReviewer = true,
}: {
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	reviewers: InspectorUnresolvedReviewer[];
	showReviewer?: boolean;
}) {
	// Manual sends are reflected immediately in local UI state after /send succeeds;
	// persisted autoInjectReview still comes from the next backend PR observation.
	const [manuallySentCommentIds, setManuallySentCommentIds] = useState<Set<string>>(() => new Set());
	const [sendingCommentIds, setSendingCommentIds] = useState<Set<string>>(() => new Set());
	const [sendErrorCommentIds, setSendErrorCommentIds] = useState<Set<string>>(() => new Set());
	const [resolvedCommentIds, setResolvedCommentIds] = useState<Set<string>>(() => new Set());
	const [removedResolvedCommentIds, setRemovedResolvedCommentIds] = useState<Set<string>>(() => new Set());
	const [resolveErrorCommentIds, setResolveErrorCommentIds] = useState<Set<string>>(() => new Set());
	const [visibleCount, setVisibleCount] = useState(1);
	const comments = reviewers.flatMap((reviewer) =>
		reviewer.links
			.filter((link) => link.body?.trim() || link.file || link.url)
			.map((link, index) => ({
				...link,
				id: `${reviewer.reviewerId}:${link.url ?? `${link.file ?? ""}:${link.line ?? ""}:${index}`}`,
				reviewerId: reviewer.reviewerId,
				url: link.url || reviewer.reviewUrl,
			})),
	);
	useEffect(() => {
		if (resolvedCommentIds.size === 0) return;
		const timeout = window.setTimeout(() => {
			setRemovedResolvedCommentIds((current) => {
				const next = new Set(current);
				for (const id of resolvedCommentIds) next.add(id);
				return next;
			});
		}, RESOLVED_COMMENT_DISPLAY_MS);
		return () => window.clearTimeout(timeout);
	}, [resolvedCommentIds]);
	const remainingComments = comments.filter((comment) => !removedResolvedCommentIds.has(comment.id));
	if (remainingComments.length === 0) return null;
	const visibleComments = remainingComments.slice(0, visibleCount);
	const hasMore = visibleComments.length < remainingComments.length;
	return (
		<section className="min-w-0 border-t border-border/70 pt-4" data-testid="github-inline-comments">
			<div className="flex min-w-0 items-center justify-between gap-2 pb-2 text-xs">
				<span className="font-semibold uppercase tracking-wide text-muted-foreground">{labels.openComments}</span>
			</div>
			<div className="divide-y divide-border/60">
				{visibleComments.map((comment, index) => (
					<InlineCommentRow
						comment={comment}
						commentNumber={index + 1}
						externalLink={ExternalLink}
						key={comment.id}
						labels={labels}
						onResolve={onResolveInlineComment ? async () => {
								setResolveErrorCommentIds((current) => {
									const next = new Set(current);
									next.delete(comment.id);
									return next;
								});
								try {
									await onResolveInlineComment(comment);
									setResolvedCommentIds((current) => new Set(current).add(comment.id));
								} catch {
									setResolveErrorCommentIds((current) => new Set(current).add(comment.id));
								}
							} : undefined}
						onSend={onSendInlineComment ? async () => {
							setSendingCommentIds((current) => new Set(current).add(comment.id));
							setSendErrorCommentIds((current) => {
								const next = new Set(current);
								next.delete(comment.id);
								return next;
							});
							try {
								await onSendInlineComment(comment);
								setManuallySentCommentIds((current) => new Set(current).add(comment.id));
							} catch {
								setSendErrorCommentIds((current) => new Set(current).add(comment.id));
							} finally {
								setSendingCommentIds((current) => {
									const next = new Set(current);
									next.delete(comment.id);
									return next;
								});
							}
						} : undefined}
						sendError={sendErrorCommentIds.has(comment.id)}
						resolveError={resolveErrorCommentIds.has(comment.id)}
						resolved={resolvedCommentIds.has(comment.id)}
						showReviewer={showReviewer}
						sent={Boolean(comment.autoInjectReview) || manuallySentCommentIds.has(comment.id)}
						sending={sendingCommentIds.has(comment.id)}
					/>
				))}
			</div>
			{remainingComments.length > 1 ? (
				<button
					className="mt-3 flex w-full items-center justify-center rounded-md border border-dashed border-border px-2 py-1.5 text-2xs font-medium text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover/30 hover:text-foreground"
					onClick={() => setVisibleCount(hasMore ? Math.min(visibleCount + 3, remainingComments.length) : 1)}
					type="button"
				>
					{hasMore ? labels.showMore : labels.showLess}
				</button>
			) : null}
		</section>
	);
}

function InlineCommentRow({
	comment,
	commentNumber,
	externalLink: ExternalLink,
	labels,
	onResolve,
	onSend,
	resolveError = false,
	resolved = false,
	sendError = false,
	sending = false,
	showReviewer = true,
	sent,
}: {
	comment: InspectorInlineComment & { reviewerId?: string };
	commentNumber: number;
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onResolve?: () => void;
	onSend?: () => void;
	resolveError?: boolean;
	resolved?: boolean;
	sendError?: boolean;
	sending?: boolean;
	showReviewer?: boolean;
	sent: boolean;
}) {
	const body = comment.body?.trim();
	return (
		<div className="flex min-w-0 flex-col gap-2 px-0 py-4 text-xs first:pt-2 last:pb-0">
			{showReviewer && comment.reviewerId ? (
				<span className="inline-flex min-w-0 items-center gap-1.5 font-medium text-muted-foreground">
					<GithubAvatar login={comment.reviewerId} />
					<span className="truncate">{comment.reviewerId}</span>
				</span>
			) : null}
			<div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-2xs font-mono text-passive">
				<span className="font-semibold text-foreground">#{commentNumber}</span>
				{comment.file ? <span className="min-w-0 break-words">{comment.file}{comment.line ? `:${comment.line}` : ""}</span> : null}
			</div>
			{body ? <p className="m-0 max-w-prose whitespace-pre-wrap break-words leading-relaxed text-muted-foreground">{body}</p> : null}
			<div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 border-t border-border/50 pt-2 text-2xs">
				{sent ? (
					<span className="inline-flex h-control-md items-center gap-1.5 rounded-md px-1.5 font-medium text-muted-foreground [&_svg]:size-icon-xs">
						<CheckIcon className="shrink-0 text-success" />
						{labels.sentToWorkerAgent}
					</span>
				) : (
					<button
						className="inline-flex h-7 items-center rounded-md border border-border/70 px-2 text-2xs font-medium text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-60"
						disabled={sending || !onSend}
						onClick={onSend}
						type="button"
					>
						{labels.sendToWorkerAgent}
					</button>
				)}
				{resolved ? (
					<span className="inline-flex h-7 items-center gap-1.5 rounded-md px-1.5 text-2xs font-medium text-muted-foreground">
						<CheckIcon className="size-icon-xs shrink-0 text-success" />
						{labels.resolvedReview}
					</span>
				) : (
					<button
						className="inline-flex h-7 items-center rounded-md border border-border/70 px-2 text-2xs font-medium text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover hover:text-foreground"
						disabled={!onResolve || !comment.url}
						onClick={onResolve}
						type="button"
					>
						{labels.resolveComment}
					</button>
				)}
				{comment.url ? (
					<ExternalLink className="inline-flex h-7 items-center rounded-md border border-border/70 px-2 text-2xs font-medium text-muted-foreground no-underline transition-colors hover:border-border-strong hover:bg-interactive-hover hover:text-foreground" href={comment.url}>
						{labels.viewInFile}
					</ExternalLink>
				) : null}
			</div>
			{sendError && !sent ? <p className="m-0 text-2xs font-medium text-error">{labels.sendToWorkerAgentError}</p> : null}
			{resolveError && !resolved ? <p className="m-0 text-2xs font-medium text-error">{labels.resolveReviewFailed}</p> : null}
		</div>
	);
}

function ReviewSummaryCard({
	actor,
	body: rawBody,
	externalLink,
	isBot = false,
	isEarlier = false,
	labels,
	renderAvatar,
	renderMarkdown,
	testId,
	timestamp,
	url,
	verdict,
}: {
	actor: string;
	body?: string;
	externalLink: ExternalLinkComponent;
	isBot?: boolean;
	isEarlier?: boolean;
	labels: InspectorReviewLabels;
	renderAvatar: (harness: string) => ReactNode;
	renderMarkdown: (body: string) => ReactNode;
	testId: string;
	timestamp: string;
	url?: string | null;
	verdict: InspectorVerdict;
}) {
	const [expanded, setExpanded] = useState(false);
	const trimmed = rawBody?.trim();
	const body = trimmed ? trimmed.replace(/\n{3,}/g, "\n\n") : trimmed;
	const clamped = body ? isClampedSummary(body) : false;
	return (
		<article className="flex min-w-0 flex-col gap-1 rounded-md bg-overlay/50 px-2.5 py-2.5">
			<span className="flex min-w-0 items-center gap-1.5">
				<span className="inline-flex min-w-0 items-center gap-1 text-micro font-medium text-muted-foreground">
					{renderAvatar(actor)}
					<span className="truncate">{actor}</span>
				</span>
				{isBot ? <span className="shrink-0 font-mono text-micro text-passive">{labels.bot}</span> : null}
				<VerdictBadge verdict={verdict} />
				<span className="ml-auto inline-flex shrink-0 items-center gap-1.5 text-micro text-passive">
					{isEarlier ? <span>{labels.earlierPass}</span> : null}
					<span className="font-mono">{timestamp}</span>
				</span>
			</span>
			{body ? (
				<ReviewMarkdownBody
					body={body}
					clamped={clamped && !expanded}
					renderMarkdown={renderMarkdown}
					testId={testId}
				/>
			) : null}
			<ReviewLinks
				clamped={clamped}
				expanded={expanded}
				externalLink={externalLink}
				labels={labels}
				onExpandedChange={() => setExpanded((open) => !open)}
				url={url}
			/>
		</article>
	);
}

function ReviewMarkdownBody({
	body,
	clamped,
	renderMarkdown,
	testId,
}: {
	body: string;
	clamped: boolean;
	renderMarkdown: (body: string) => ReactNode;
	testId: string;
}) {
	return (
		<div
			className={cn(
				"min-w-0 select-text break-words text-xs leading-relaxed text-muted-foreground",
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
			{renderMarkdown(body)}
		</div>
	);
}

function ReviewLinks({
	clamped,
	expanded,
	externalLink: ExternalLink,
	labels,
	onExpandedChange,
	renderViewLabel,
	url,
}: {
	clamped: boolean;
	expanded: boolean;
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onExpandedChange: () => void;
	renderViewLabel?: string;
	url?: string | null;
}) {
	if (!clamped && !url) return null;
	return (
		<span className="mt-1 flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-micro text-passive">
			{clamped ? (
				<button className="font-medium transition-colors hover:text-foreground" onClick={onExpandedChange} type="button">
					{expanded ? labels.showLess : labels.showMore}
				</button>
			) : null}
			{clamped && url ? <span aria-hidden="true">·</span> : null}
			{url ? (
				<ExternalLink
					className="inline-flex items-center gap-0.5 font-medium no-underline transition-colors hover:text-foreground"
					href={url}
				>
					{renderViewLabel ?? labels.viewOnPR}
					<ArrowUpRightIcon className="size-2.5 shrink-0" />
				</ExternalLink>
			) : null}
		</span>
	);
}

const REVIEW_SUMMARY_CLAMP_LINES = 4;

function isClampedSummary(body: string): boolean {
	return body.split("\n").length > REVIEW_SUMMARY_CLAMP_LINES || body.length > 260;
}
