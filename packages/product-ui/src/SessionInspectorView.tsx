import { type KeyboardEvent, type ReactNode, type RefObject, useEffect, useRef, useState } from "react";
import { motion, useReducedMotion } from "motion/react";
import type { ExternalLinkComponent } from "./external-link";
import {
	ArrowUpRightIcon,
	BotIcon,
	CheckIcon,
	ChevronIcon,
	GitPullRequestIcon,
	MoreHorizontalIcon,
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
	const prefersReducedMotion = useReducedMotion();
	const tabIndicatorTransition = prefersReducedMotion
		? { duration: 0 }
		: { type: "spring" as const, duration: 0.3, bounce: 0 };

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
			<div className="session-inspector__topbar flex h-inspector-tabs shrink-0 items-center border-b border-border pl-1">
				{isVisible ? (
					<div className="session-inspector__tablist flex min-w-0 flex-1 items-center justify-start gap-1" role="tablist">
						{tabs.map((tab, index) => (
							<button
								aria-label={tab.label}
								key={tab.id}
								type="button"
								role="tab"
								aria-selected={activeView === tab.id}
								tabIndex={activeView === tab.id ? 0 : -1}
								className={cn(
									"session-inspector__tab-button relative inline-flex size-control-md shrink-0 items-center justify-center rounded-md p-0 font-semibold text-passive transition-[color] duration-fast hover:bg-interactive-hover hover:text-foreground",
									activeView === tab.id && "text-foreground",
								)}
								onClick={() => onViewChange(tab.id)}
								onKeyDown={(event) => selectAdjacentTab(event, index)}
								title={tab.label}
							>
								{activeView === tab.id ? (
									<motion.span
										aria-hidden="true"
										className="absolute inset-0 rounded-md bg-interactive-active"
										initial={false}
										layoutId="session-inspector-tab-indicator"
										transition={tabIndicatorTransition}
									/>
								) : null}
								<span className="relative z-[1] inline-flex shrink-0 [&_svg]:size-icon-md">
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
								<span className="sr-only">
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
		<article className="min-w-0 w-full select-text rounded-lg border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3 py-2.5">
			{pr.title ? (
				<p className="m-0 text-sm font-semibold leading-snug tracking-tight text-settings-label">
					{pr.title}
				</p>
			) : null}
			<div className={cn("flex min-w-0 items-center gap-2", pr.title && "mt-1.5")}>
				<span className="inline-flex min-w-0 items-center gap-1 font-mono text-xs font-medium text-settings-label">
					{pullRequestIcon ?? <GitPullRequestIcon className="size-icon-sm shrink-0" />}
					<span>PR #{pr.number}</span>
				</span>
				<ExternalLink
					ariaLabel={openLabel}
					className="inline-flex shrink-0 items-center text-settings-label hover:text-settings-label focus-visible:rounded-sm focus-visible:text-settings-label focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
					href={pr.href}
				>
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
				<div key={index} className="relative select-text pb-4 last:pb-0" data-testid="inspector-timeline-event">
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
	inlineComments?: InspectorInlineComment[];
	resolvedComments?: InspectorInlineComment[];
	status: string;
	url?: string | null;
	verdict: InspectorVerdict;
};

export type InspectorReviewSummaryAction = {
	body: string;
	pullRequestUrl?: string;
	reviewerId: string;
	source: "agent" | "external";
	url?: string | null;
};

export type InspectorInlineComment = {
	autoInjectReview?: boolean;
	body?: string;
	file?: string;
	line?: number;
	pullRequestUrl?: string;
	reviewerId?: string;
	resolved?: boolean;
	url?: string;
};

export type InspectorGithubReview = {
	body?: string;
	canRequestRereview?: boolean;
	id: string;
	inlineComments?: InspectorInlineComment[];
	isBot?: boolean;
	pullRequestUrl?: string;
	resolvedComments?: InspectorInlineComment[];
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
		reviewId?: string;
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
	openInAOBrowser: string;
	openInSystemBrowser: string;
	openInlineComments: (count: number) => string;
	requestRereviewPR: string;
	reviewActions: string;
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
	workerAgentWorkingOnFeedback: string;
	showLatestReviewOnly: string;
	showLess: string;
	showMore: string;
	commentNumber: (number: number) => string;
	unresolvedCount: (count: number) => string;
	viewInFile: string;
	viewInFileWorkInProgress: string;
	viewOnPR: string;
};

export function InspectorReviewsView({
	externalLink,
	groups,
	isLoading,
	labels,
	onRequestRereview,
	onResolveInlineComment,
	onOpenInAOBrowser,
	onSendInlineComment,
	onSendReviewSummary,
	onViewInlineCommentInFile,
	renderAvatar,
	renderMarkdown,
}: {
	externalLink: ExternalLinkComponent;
	groups: InspectorReviewGroup[];
	isLoading: boolean;
	labels: InspectorReviewLabels;
	onRequestRereview?: (review: InspectorGithubReview) => Promise<void> | void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onOpenInAOBrowser?: (url: string) => void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendReviewSummary?: (summary: InspectorReviewSummaryAction) => Promise<void> | void;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
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
				{groups.map((group) => (
					<ReviewDisclosure
						defaultOpen={false}
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
									historical={group.ao.historical}
									labels={labels}
									externalLink={externalLink}
									onOpenInAOBrowser={onOpenInAOBrowser}
									onResolveInlineComment={onResolveInlineComment}
									onSendInlineComment={onSendInlineComment}
									renderAvatar={renderAvatar}
									renderMarkdown={renderMarkdown}
									runs={group.ao.runs}
									onSendReviewSummary={onSendReviewSummary}
									onViewInlineCommentInFile={onViewInlineCommentInFile}
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
									onOpenInAOBrowser={onOpenInAOBrowser}
									onRequestRereview={onRequestRereview}
									onResolveInlineComment={onResolveInlineComment}
									onSendInlineComment={onSendInlineComment}
									onSendReviewSummary={onSendReviewSummary}
									onViewInlineCommentInFile={onViewInlineCommentInFile}
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

function VerdictBadge({ className, verdict }: { className?: string; verdict: InspectorVerdict }) {
	return (
		<span className={cn("inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap text-2xs font-medium", reviewerVerdictTone[verdict.tone], className)}>
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
				className="relative overflow-visible rounded-lg border border-border bg-settings-row"
				data-testid="review-pr-row"
			>
				<div className="flex min-w-0 flex-col gap-1 border-b border-border/70 px-3 py-2.5">
					<span className="flex min-w-0 items-start justify-between gap-2 @max-[420px]/inspector:flex-col @max-[420px]/inspector:items-stretch">
						<span
							className="min-w-0 whitespace-normal break-words text-sm-md font-semibold leading-snug text-foreground"
							title={title}
						>
							{title}
						</span>
						{verdict ? <VerdictBadge className="@max-[420px]/inspector:self-start" verdict={verdict} /> : null}
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
		<article className="relative overflow-visible rounded-lg border border-border bg-settings-row">
			<button
				aria-expanded={open}
				data-testid="review-pr-row"
				className="flex w-full min-w-0 items-start gap-2 px-3 py-2.5 text-left transition-colors hover:bg-interactive-hover/30 @max-[420px]/inspector:grid @max-[420px]/inspector:grid-cols-[auto_minmax(0,1fr)]"
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
				{verdict ? <VerdictBadge className="@max-[420px]/inspector:col-start-2 @max-[420px]/inspector:row-start-2 @max-[420px]/inspector:justify-self-start" verdict={verdict} /> : null}
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
	onOpenInAOBrowser,
	onResolveInlineComment,
	onSendInlineComment,
	onSendReviewSummary,
	onViewInlineCommentInFile,
	renderAvatar,
	renderMarkdown,
	runs,
}: {
	dimmed?: boolean;
	externalLink: ExternalLinkComponent;
	historical?: boolean;
	labels: InspectorReviewLabels;
	onOpenInAOBrowser?: (url: string) => void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendReviewSummary?: (summary: InspectorReviewSummaryAction) => Promise<void> | void;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
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
			onOpenInAOBrowser={onOpenInAOBrowser}
			onResolveInlineComment={onResolveInlineComment}
			onSendInlineComment={onSendInlineComment}
			onSendReviewSummary={onSendReviewSummary}
			onViewInlineCommentInFile={onViewInlineCommentInFile}
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
	onOpenInAOBrowser,
	onResolveInlineComment,
	onSendInlineComment,
	onSendReviewSummary,
	onViewInlineCommentInFile,
	renderAvatar,
	renderMarkdown,
	runs,
}: {
	dimmed?: boolean;
	externalLink: ExternalLinkComponent;
	historical?: boolean;
	labels: InspectorReviewLabels;
	onOpenInAOBrowser?: (url: string) => void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendReviewSummary?: (summary: InspectorReviewSummaryAction) => Promise<void> | void;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
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
					onOpenInAOBrowser={onOpenInAOBrowser}
					onResolveInlineComment={onResolveInlineComment}
					onSendInlineComment={onSendInlineComment}
					onSendReviewSummary={onSendReviewSummary}
					onViewInlineCommentInFile={onViewInlineCommentInFile}
					inlineComments={run.inlineComments}
					resolvedComments={run.resolvedComments}
					source="agent"
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
					className="flex min-h-8 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md border border-dashed border-border px-2 py-1.5 text-xs font-medium leading-none text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover/30 hover:text-foreground"
					onClick={onCollapse}
					type="button"
				>
					<ChevronIcon className="size-icon-2xs shrink-0" direction="up" />
					<span className="truncate">{labels.showLatestReviewOnly}</span>
				</button>
			) : null}
			{remaining > 0 && onLoadMore ? (
				<button
					className="flex min-h-8 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md border border-dashed border-border px-2 py-1.5 text-xs font-medium leading-none text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover/30 hover:text-foreground"
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
	onOpenInAOBrowser,
	onRequestRereview,
	onResolveInlineComment,
	onSendInlineComment,
	onSendReviewSummary,
	onViewInlineCommentInFile,
	renderMarkdown,
}: {
	entries: InspectorGithubReview[];
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onOpenInAOBrowser?: (url: string) => void;
	onRequestRereview?: (review: InspectorGithubReview) => Promise<void> | void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendReviewSummary?: (summary: InspectorReviewSummaryAction) => Promise<void> | void;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
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
					onOpenInAOBrowser={onOpenInAOBrowser}
					onSendInlineComment={onSendInlineComment}
					onSendReviewSummary={onSendReviewSummary}
					onViewInlineCommentInFile={onViewInlineCommentInFile}
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
	onOpenInAOBrowser,
	onRequestRereview,
	onResolveInlineComment,
	onSendInlineComment,
	onSendReviewSummary,
	onViewInlineCommentInFile,
	renderMarkdown,
}: {
	defaultOpen: boolean;
	entry: InspectorGithubReview;
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onOpenInAOBrowser?: (url: string) => void;
	onRequestRereview?: (review: InspectorGithubReview) => Promise<void> | void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendReviewSummary?: (summary: InspectorReviewSummaryAction) => Promise<void> | void;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
	renderMarkdown: (body: string) => ReactNode;
}) {
	const [open, setOpen] = useState(defaultOpen);
	const body = entry.body?.trim();
	const openComments = (entry.inlineComments ?? []).filter((comment) => comment.body?.trim() || comment.file || comment.url);
	const resolvedComments = (entry.resolvedComments ?? []).filter((comment) => comment.body?.trim() || comment.file || comment.url);
	const { ref: bodyRef, isOverflowing: bodyOverflows } = useRenderedOverflow<HTMLDivElement>(body ?? "", "vertical", !open);
	const hasNestedContent = openComments.length > 0 || resolvedComments.length > 0;
	const collapsible = bodyOverflows || hasNestedContent;
	const headerContent = (
		<>
			<GithubAvatar className="size-6 shrink-0" login={entry.reviewerId} />
			<span className="flex min-w-0 flex-col gap-0.5">
				<span className="flex min-w-0 items-center gap-1.5 text-xs font-semibold text-foreground">
					<span className="min-w-0 break-words">{entry.reviewerId}</span>
					{entry.isBot ? <span className="shrink-0 font-mono text-micro text-passive">{labels.bot}</span> : null}
				</span>
				{entry.submittedAtLabel ? <span className="font-mono text-micro text-passive">{labels.reviewedAt(entry.submittedAtLabel)}</span> : null}
			</span>
			<span className={cn("flex shrink-0 items-center gap-2 whitespace-nowrap pr-1 text-2xs font-medium @max-[420px]/inspector:col-start-2 @max-[420px]/inspector:row-start-2 @max-[420px]/inspector:justify-self-start", reviewerVerdictTone[entry.verdict.tone])}>
				<span>{entry.verdict.label}</span>
				{openComments.length > 0 ? (
					<span className="rounded-sm px-0.5 text-muted-foreground" title={labels.openInlineComments(openComments.length)}>{openComments.length}</span>
				) : null}
			</span>
		</>
	);
	return (
		<article className="relative min-w-0 border-b border-border/70 py-2 first:pt-0 last:border-b-0 last:pb-0" data-testid="github-review-card">
			<div className="grid w-full min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-1 rounded-md pr-1" data-testid="external-review-header">
				{collapsible ? (
					<button aria-expanded={open} className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 rounded-md px-1.5 py-1.5 text-left transition-colors hover:bg-interactive-hover/30 @max-[420px]/inspector:grid-cols-[auto_minmax(0,1fr)] @max-[420px]/inspector:gap-x-2" onClick={() => setOpen((current) => !current)} type="button">
						{headerContent}
					</button>
				) : (
					<div className="grid min-w-0 select-text grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 px-1.5 py-1.5 text-left @max-[420px]/inspector:grid-cols-[auto_minmax(0,1fr)] @max-[420px]/inspector:gap-x-2">
						{headerContent}
					</div>
				)}
				<ReviewSummaryActions body={body ?? ""} externalLink={externalLink} labels={labels} onOpenInAOBrowser={onOpenInAOBrowser} onRequestRereview={entry.canRequestRereview ? () => onRequestRereview?.(entry) : undefined} onSendReviewSummary={onSendReviewSummary} pullRequestUrl={entry.pullRequestUrl} reviewerId={entry.reviewerId} source="external" url={entry.reviewUrl || entry.pullRequestUrl} />
			</div>
			<div className="flex min-w-0 flex-col gap-3 px-1 pt-2 text-left">
				{body ? (
					<ReviewMarkdownBody body={body} clamped={!open} elementRef={bodyRef} renderMarkdown={renderMarkdown} testId="github-review-summary" />
				) : null}
				{open ? <GithubInlineComments comments={openComments} externalLink={externalLink} labels={labels} onResolveInlineComment={onResolveInlineComment} onSendInlineComment={onSendInlineComment} onViewInlineCommentInFile={onViewInlineCommentInFile} reviewerId={entry.reviewerId} reviewUrl={entry.reviewUrl} /> : null}
				{open && resolvedComments.length > 0 ? <ResolvedInlineComments comments={resolvedComments} externalLink={externalLink} labels={labels} onViewInlineCommentInFile={onViewInlineCommentInFile} reviewerId={entry.reviewerId} reviewUrl={entry.reviewUrl} /> : null}
			</div>
		</article>
	);
}

function GithubInlineComments({
	comments,
	externalLink: ExternalLink,
	labels,
	onResolveInlineComment,
	onSendInlineComment,
	onViewInlineCommentInFile,
	reviewerId,
	reviewUrl,
}: {
	comments: InspectorInlineComment[];
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
	reviewerId: string;
	reviewUrl?: string;
}) {
	const [open, setOpen] = useState(true);
	if (comments.length === 0) return null;
	return (
		<section className="min-w-0" data-testid="github-inline-comments">
			<button aria-expanded={open} className="flex min-h-8 w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left text-xs font-medium leading-none text-muted-foreground transition-colors hover:bg-interactive-hover/20 hover:text-foreground" onClick={() => setOpen((current) => !current)} type="button">
				<ChevronIcon className="size-icon-2xs shrink-0" direction={open ? "down" : "right"} />
				<span>{labels.openComments} · {comments.length}</span>
			</button>
			{open ? <InlineCommentList comments={comments} externalLink={ExternalLink} labels={labels} onResolveInlineComment={onResolveInlineComment} onSendInlineComment={onSendInlineComment} onViewInlineCommentInFile={onViewInlineCommentInFile} reviewerId={reviewerId} reviewUrl={reviewUrl} /> : null}
		</section>
	);
}

function ResolvedInlineComments({
	comments,
	externalLink: ExternalLink,
	labels,
	onViewInlineCommentInFile,
	reviewerId,
	reviewUrl,
}: {
	comments: InspectorInlineComment[];
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
	reviewerId: string;
	reviewUrl?: string;
}) {
	const [open, setOpen] = useState(false);
	return (
		<section className="min-w-0 border-t border-border/60 pt-2" data-testid="github-resolved-comments">
			<button aria-expanded={open} className="flex min-h-8 w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left text-xs font-medium leading-none text-muted-foreground transition-colors hover:bg-interactive-hover/20 hover:text-foreground" onClick={() => setOpen((current) => !current)} type="button">
				<ChevronIcon className="size-icon-2xs shrink-0" direction={open ? "down" : "right"} />
				<span>{labels.resolvedComments(comments.length)}</span>
			</button>
			{open ? <InlineCommentList comments={comments} externalLink={ExternalLink} labels={labels} onViewInlineCommentInFile={onViewInlineCommentInFile} reviewerId={reviewerId} reviewUrl={reviewUrl} /> : null}
		</section>
	);
}

function InlineCommentList({
	comments,
	externalLink,
	labels,
	onResolveInlineComment,
	onSendInlineComment,
	onViewInlineCommentInFile,
	reviewerId,
	reviewUrl,
}: {
	comments: InspectorInlineComment[];
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
	reviewerId: string;
	reviewUrl?: string;
}) {
	const [manuallyResolvedCommentIds, setManuallyResolvedCommentIds] = useState<Set<string>>(() => new Set());
	const [manuallySentCommentIds, setManuallySentCommentIds] = useState<Set<string>>(() => new Set());
	const [resolvingCommentIds, setResolvingCommentIds] = useState<Set<string>>(() => new Set());
	const [resolveErrorCommentIds, setResolveErrorCommentIds] = useState<Set<string>>(() => new Set());
	const [sendingCommentIds, setSendingCommentIds] = useState<Set<string>>(() => new Set());
	const [sendErrorCommentIds, setSendErrorCommentIds] = useState<Set<string>>(() => new Set());
	const keyedComments = comments.map((comment, index) => ({
		...comment,
		id: `${reviewerId}:${comment.url ?? `${comment.file ?? ""}:${comment.line ?? ""}:${index}`}`,
		reviewerId: comment.reviewerId || reviewerId,
		url: comment.url || reviewUrl,
	}));
	return (
		<div className="divide-y divide-border/60">
			{keyedComments.map((comment) => (
				<InlineCommentRow
					comment={comment}
					externalLink={externalLink}
					key={comment.id}
					labels={labels}
					onResolve={!comment.resolved && onResolveInlineComment ? async () => {
						setResolvingCommentIds((current) => new Set(current).add(comment.id));
						setResolveErrorCommentIds((current) => {
							const next = new Set(current);
							next.delete(comment.id);
							return next;
						});
						try {
							await onResolveInlineComment(comment);
							setManuallyResolvedCommentIds((current) => new Set(current).add(comment.id));
						} catch {
							setResolveErrorCommentIds((current) => new Set(current).add(comment.id));
						} finally {
							setResolvingCommentIds((current) => {
								const next = new Set(current);
								next.delete(comment.id);
								return next;
							});
						}
					} : undefined}
					onSend={comment.autoInjectReview === false && onSendInlineComment ? async () => {
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
					onViewInFile={comment.file && onViewInlineCommentInFile ? () => onViewInlineCommentInFile(comment) : undefined}
					resolveError={resolveErrorCommentIds.has(comment.id)}
					resolvedSuccess={manuallyResolvedCommentIds.has(comment.id)}
					resolving={resolvingCommentIds.has(comment.id)}
					sendError={sendErrorCommentIds.has(comment.id)}
					sent={Boolean(comment.autoInjectReview) || manuallySentCommentIds.has(comment.id)}
					sending={sendingCommentIds.has(comment.id)}
				/>
			))}
		</div>
	);
}

function InlineCommentRow({
	comment,
	externalLink: ExternalLink,
	labels,
	onResolve,
	onSend,
	onViewInFile,
	resolveError = false,
	resolvedSuccess = false,
	resolving = false,
	sendError = false,
	sending = false,
	sent,
}: {
	comment: InspectorInlineComment & { id: string; reviewerId?: string };
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onResolve?: () => void;
	onSend?: () => void;
	onViewInFile?: () => void;
	resolveError?: boolean;
	resolvedSuccess?: boolean;
	resolving?: boolean;
	sendError?: boolean;
	sending?: boolean;
	sent: boolean;
}) {
	const [expanded, setExpanded] = useState(false);
	const [menuOpen, setMenuOpen] = useState(false);
	const body = comment.body?.trim();
	const fileLabel = comment.file ? `${comment.file}${comment.line ? `:${comment.line}` : ""}` : labels.commentNumber(1);
	const preview = body ? body.split("\n")[0] : "";
	const hasHiddenLines = Boolean(body && body.split("\n").slice(1).some((line) => line.trim()));
	const { ref: previewRef, isOverflowing: previewOverflows } = useRenderedOverflow<HTMLSpanElement>(preview, "horizontal", !expanded);
	const canExpand = hasHiddenLines || previewOverflows;
	useEffect(() => {
		if (!canExpand && expanded) setExpanded(false);
	}, [canExpand, expanded]);
	const copy = async (value?: string) => {
		if (!value) return;
		try {
			await navigator.clipboard?.writeText(value);
		} catch {
			// Clipboard access may be unavailable in tests or restricted contexts.
		}
	};
	return (
		<div className="relative flex min-w-0 flex-col gap-1.5 py-2.5 text-xs">
			<div
				{...(canExpand ? { "aria-expanded": expanded, role: "button", tabIndex: 0 } : {})}
				className={cn("w-full min-w-0 rounded-md py-1 text-left transition-colors", canExpand && "cursor-pointer hover:bg-interactive-hover/20")}
				onClick={canExpand ? () => setExpanded((current) => !current) : undefined}
				onKeyDown={canExpand ? (event) => {
					if (event.key === "Enter" || event.key === " ") {
						event.preventDefault();
						setExpanded((current) => !current);
					}
				} : undefined}
			>
				<span className="flex min-w-0 items-start gap-2">
					<span className="flex min-w-0 flex-1 items-center gap-1.5 font-mono text-2xs font-semibold text-foreground">
						{canExpand ? <ChevronIcon className="size-icon-2xs shrink-0 text-passive" direction={expanded ? "down" : "right"} /> : null}
						<span className="truncate" title={fileLabel}>{fileLabel}</span>
					</span>
					<span className="flex shrink-0 items-center justify-end" onClick={(event) => event.stopPropagation()}>
						{sent ? (
							<span aria-label={labels.sentToWorkerAgent} className="inline-flex size-7 items-center justify-center rounded-md text-success" role="status" title={labels.sentToWorkerAgent}>
								<CheckIcon className="size-icon-xs shrink-0" />
							</span>
						) : null}
					</span>
					<span className="relative flex shrink-0 items-start justify-end" onClick={(event) => event.stopPropagation()}>
						<button aria-expanded={menuOpen} aria-label="Comment actions" className="inline-flex size-7 items-center justify-center rounded-md border border-border/70 text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover hover:text-foreground" onClick={() => setMenuOpen((current) => !current)} type="button">
							<MoreHorizontalIcon className="size-icon-xs" />
						</button>
						{menuOpen ? (
							<div className="isolate absolute right-0 top-8 z-[100] flex w-40 flex-col rounded-md border border-border-strong bg-[var(--color-bg-settings-menu)] p-1 text-2xs shadow-[0_16px_40px_rgba(0,0,0,0.65)]">
								{onSend && !sent ? <button className="rounded px-2 py-1.5 text-left text-muted-foreground hover:bg-interactive-hover hover:text-foreground disabled:pointer-events-none disabled:opacity-60" disabled={sending} onClick={() => { setMenuOpen(false); onSend(); }} type="button">{labels.sendToWorkerAgent}</button> : null}
								{onResolve ? <button className="rounded px-2 py-1.5 text-left text-muted-foreground hover:bg-interactive-hover hover:text-foreground disabled:pointer-events-none disabled:opacity-60" disabled={resolving} onClick={() => void onResolve()} type="button">{labels.resolveComment}</button> : null}
								{onViewInFile ? <button className="rounded px-2 py-1.5 text-left text-muted-foreground hover:bg-interactive-hover hover:text-foreground" onClick={onViewInFile} type="button">{labels.viewInFile}</button> : null}
								{comment.url ? <ExternalLink className="rounded px-2 py-1.5 text-muted-foreground no-underline hover:bg-interactive-hover hover:text-foreground" href={comment.url}>Open on GitHub</ExternalLink> : null}
								{comment.url ? <button className="rounded px-2 py-1.5 text-left text-muted-foreground hover:bg-interactive-hover hover:text-foreground" onClick={() => void copy(comment.url)} type="button">Copy comment link</button> : null}
							</div>
						) : null}
					</span>
				</span>
				{body ? <span data-overflow-axis="horizontal" ref={previewRef} className={cn("mt-1 block min-w-0 text-muted-foreground", expanded ? "whitespace-pre-wrap break-words" : "truncate")}>{expanded ? body : preview}</span> : null}
			</div>
			{resolvedSuccess ? <p className="m-0 select-text text-2xs font-medium text-success">{labels.resolvedReview}</p> : null}
			{resolveError ? <p className="m-0 select-text text-2xs font-medium text-error">{labels.resolveReviewFailed}</p> : null}
			{sendError && !sent ? <p className="m-0 select-text text-2xs font-medium text-error">{labels.sendToWorkerAgentError}</p> : null}
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
	inlineComments = [],
	onOpenInAOBrowser,
	onResolveInlineComment,
	onSendInlineComment,
	onSendReviewSummary,
	onViewInlineCommentInFile,
	renderAvatar,
	renderMarkdown,
	resolvedComments = [],
	source,
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
	inlineComments?: InspectorInlineComment[];
	onOpenInAOBrowser?: (url: string) => void;
	onResolveInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendInlineComment?: (comment: InspectorInlineComment & { reviewerId?: string }) => Promise<void> | void;
	onSendReviewSummary?: (summary: InspectorReviewSummaryAction) => Promise<void> | void;
	onViewInlineCommentInFile?: (comment: InspectorInlineComment & { reviewerId?: string }) => void;
	renderAvatar: (harness: string) => ReactNode;
	renderMarkdown: (body: string) => ReactNode;
	resolvedComments?: InspectorInlineComment[];
	source: InspectorReviewSummaryAction["source"];
	testId: string;
	timestamp: string;
	url?: string | null;
	verdict: InspectorVerdict;
}) {
	const [expanded, setExpanded] = useState(false);
	const trimmed = rawBody?.trim();
	const body = trimmed ? trimmed.replace(/\n{3,}/g, "\n\n") : trimmed;
	const { ref: bodyRef, isOverflowing } = useRenderedOverflow<HTMLDivElement>(body ?? "", "vertical", !expanded);
	useEffect(() => setExpanded(false), [body]);
	return (
		<article className="flex min-w-0 flex-col gap-1 rounded-md bg-overlay/50 px-2.5 py-2.5">
			<span className="grid min-w-0 select-text grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-x-1.5 gap-y-1 @max-[420px]/inspector:grid-cols-[minmax(0,1fr)_auto]">
				<span className="inline-flex min-w-0 items-center gap-1.5">
					<span className="inline-flex min-w-0 flex-1 items-center gap-1 text-micro font-medium text-muted-foreground">
						{renderAvatar(actor)}
						<span className="truncate">{actor}</span>
						{isBot ? <span className="shrink-0 font-mono text-micro text-passive">{labels.bot}</span> : null}
					</span>
					<VerdictBadge className="shrink-0" verdict={verdict} />
				</span>
				<span className="ml-auto inline-flex min-w-0 shrink-0 flex-wrap items-center justify-end gap-x-1.5 gap-y-0.5 text-right text-micro text-passive @max-[420px]/inspector:col-span-2 @max-[420px]/inspector:col-start-1 @max-[420px]/inspector:row-start-2 @max-[420px]/inspector:ml-0 @max-[420px]/inspector:justify-self-start @max-[420px]/inspector:text-left">
					{isEarlier ? <span>{labels.earlierPass}</span> : null}
					<span className="font-mono">{timestamp}</span>
				</span>
				<ReviewSummaryActions body={body ?? ""} className="@max-[420px]/inspector:col-start-2 @max-[420px]/inspector:row-start-1" externalLink={externalLink} labels={labels} onOpenInAOBrowser={onOpenInAOBrowser} onSendReviewSummary={onSendReviewSummary} reviewerId={actor} source={source} url={url} />
			</span>
			{body ? (
				<ReviewMarkdownBody
					body={body}
					clamped={!expanded}
					elementRef={bodyRef}
					renderMarkdown={renderMarkdown}
					testId={testId}
				/>
			) : null}
			<ReviewLinks
				clamped={isOverflowing}
				expanded={expanded}
				labels={labels}
				onExpandedChange={() => setExpanded((open) => !open)}
			/>
			<GithubInlineComments
				comments={inlineComments}
				externalLink={externalLink}
				labels={labels}
				onResolveInlineComment={onResolveInlineComment}
				onSendInlineComment={onSendInlineComment}
				onViewInlineCommentInFile={onViewInlineCommentInFile}
				reviewerId={actor}
				reviewUrl={url ?? undefined}
			/>
			{resolvedComments.length > 0 ? (
				<ResolvedInlineComments
					comments={resolvedComments}
					externalLink={externalLink}
					labels={labels}
					onViewInlineCommentInFile={onViewInlineCommentInFile}
					reviewerId={actor}
					reviewUrl={url ?? undefined}
				/>
			) : null}
		</article>
	);
}

function ReviewMarkdownBody({
	body,
	clamped,
	elementRef,
	renderMarkdown,
	testId,
}: {
	body: string;
	clamped: boolean;
	elementRef?: RefObject<HTMLDivElement | null>;
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
			data-overflow-axis="vertical"
			ref={elementRef}
		>
			{renderMarkdown(body)}
		</div>
	);
}

function ReviewSummaryActions({
	body,
	className,
	externalLink: ExternalLink,
	labels,
	onOpenInAOBrowser,
	onRequestRereview,
	onSendReviewSummary,
	pullRequestUrl,
	reviewerId,
	source,
	url,
}: {
	body: string;
	className?: string;
	externalLink: ExternalLinkComponent;
	labels: InspectorReviewLabels;
	onOpenInAOBrowser?: (url: string) => void;
	onRequestRereview?: () => Promise<void> | void;
	onSendReviewSummary?: (summary: InspectorReviewSummaryAction) => Promise<void> | void;
	pullRequestUrl?: string;
	reviewerId: string;
	source: InspectorReviewSummaryAction["source"];
	url?: string | null;
}) {
	const [menuOpen, setMenuOpen] = useState(false);
	const menuRef = useClickAway<HTMLSpanElement>(menuOpen, () => setMenuOpen(false));
	const [rereviewState, setRereviewState] = useState<"idle" | "requesting" | "requested" | "error">("idle");
	const [sendState, setSendState] = useState<"idle" | "sending" | "sent" | "error">("idle");
	const canSend = Boolean(body && onSendReviewSummary);
	if (!url && !canSend && !onRequestRereview) return null;
	const requestRereview = async () => {
		if (!onRequestRereview || rereviewState === "requesting") return;
		setRereviewState("requesting");
		try {
			await onRequestRereview();
			setRereviewState("requested");
		} catch {
			setRereviewState("error");
		}
	};
	const send = async () => {
		if (!onSendReviewSummary || !body || sendState === "sending") return;
		setSendState("sending");
		try {
			await onSendReviewSummary({ body, pullRequestUrl, reviewerId, source, url });
			setSendState("sent");
		} catch {
			setSendState("error");
		}
	};
	return (
		<span className={cn("relative flex shrink-0", className)} onClick={(event) => event.stopPropagation()} ref={menuRef}>
			<button aria-expanded={menuOpen} aria-label={labels.reviewActions} className="inline-flex size-7 items-center justify-center rounded-md border border-border/70 text-muted-foreground transition-colors hover:border-border-strong hover:bg-interactive-hover hover:text-foreground" onClick={() => setMenuOpen((current) => !current)} type="button">
				<MoreHorizontalIcon className="size-icon-xs" />
			</button>
			{menuOpen ? (
				<span className="isolate absolute right-0 top-8 z-[100] flex w-48 flex-col rounded-md border border-border-strong bg-[var(--color-bg-settings-menu)] p-1 text-2xs shadow-[0_16px_40px_rgba(0,0,0,0.65)]">
					{canSend ? <button className={cn("rounded px-2 py-1.5 text-left hover:bg-interactive-hover disabled:pointer-events-none", sendState === "sent" ? "text-success" : sendState === "error" ? "text-error" : "text-muted-foreground hover:text-foreground")} disabled={sendState === "sending" || sendState === "sent"} onClick={() => void send()} type="button">{sendState === "sent" ? labels.sentToWorkerAgent : sendState === "error" ? labels.sendToWorkerAgentError : labels.sendToWorkerAgent}</button> : null}
					{onRequestRereview ? <button className={cn("rounded px-2 py-1.5 text-left hover:bg-interactive-hover disabled:pointer-events-none", rereviewState === "requested" ? "text-success" : rereviewState === "error" ? "text-error" : "text-muted-foreground hover:text-foreground")} disabled={rereviewState === "requesting" || rereviewState === "requested"} onClick={() => void requestRereview()} type="button">{rereviewState === "requested" ? labels.rereviewRequested : rereviewState === "error" ? labels.rereviewRequestFailed : labels.requestRereviewPR}</button> : null}
					{url && onOpenInAOBrowser ? <button className="rounded px-2 py-1.5 text-left text-muted-foreground hover:bg-interactive-hover hover:text-foreground" onClick={() => onOpenInAOBrowser(url)} type="button">{labels.openInAOBrowser}</button> : null}
					{url ? <ExternalLink className="rounded px-2 py-1.5 text-muted-foreground no-underline hover:bg-interactive-hover hover:text-foreground" href={url}>{labels.openInSystemBrowser}</ExternalLink> : null}
				</span>
			) : null}
		</span>
	);
}

function useClickAway<T extends HTMLElement>(open: boolean, onDismiss: () => void) {
	const ref = useRef<T | null>(null);
	const onDismissRef = useRef(onDismiss);
	onDismissRef.current = onDismiss;
	useEffect(() => {
		if (!open) return;
		const onPointerDown = (event: PointerEvent) => {
			if (!ref.current?.contains(event.target as Node)) onDismissRef.current();
		};
		document.addEventListener("pointerdown", onPointerDown);
		return () => document.removeEventListener("pointerdown", onPointerDown);
	}, [open]);
	return ref;
}

function ReviewLinks({
	clamped,
	expanded,
	labels,
	onExpandedChange,
}: {
	clamped: boolean;
	expanded: boolean;
	labels: InspectorReviewLabels;
	onExpandedChange: () => void;
}) {
	if (!clamped) return null;
	return (
		<span className="mt-1 flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-micro text-passive">
			<button className="font-medium transition-colors hover:text-foreground" onClick={onExpandedChange} type="button">
				{expanded ? labels.showLess : labels.showMore}
			</button>
		</span>
	);
}

function useRenderedOverflow<T extends HTMLElement>(contentKey: string, axis: "horizontal" | "vertical", active = true) {
	const ref = useRef<T | null>(null);
	const [isOverflowing, setIsOverflowing] = useState(false);
	useEffect(() => {
		if (!active) return;
		const element = ref.current;
		if (!element) return;
		const update = () => {
			const next = axis === "horizontal"
				? element.scrollWidth > element.clientWidth + 1
				: element.scrollHeight > element.clientHeight + 1;
			setIsOverflowing(next);
		};
		update();
		window.addEventListener("resize", update);
		const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
		observer?.observe(element);
		return () => {
			window.removeEventListener("resize", update);
			observer?.disconnect();
		};
	}, [active, axis, contentKey]);
	return { ref, isOverflowing };
}
