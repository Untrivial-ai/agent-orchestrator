import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useParams } from "@tanstack/react-router";
import {
	ArrowUpRight,
	Bell,
	BellRing,
	CheckCheck,
	CircleAlert,
	GitMerge,
	GitPullRequestArrow,
	GitPullRequestClosed,
	Inbox,
	LoaderCircle,
	MessageSquareDot,
	RotateCcw,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMarkAllNotificationsReadMutation, useNotificationsQuery } from "../hooks/useNotificationsQuery";
import { useRestoreSession } from "../hooks/useRestoreSession";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { aoBridge } from "../lib/bridge";
import { openLinkInSystemBrowser } from "../lib/external-link-policy";
import { formatTimeCompact } from "../lib/format-time";
import {
	createNotificationsTransport,
	getCachedNotifications,
	getCachedUnreadCount,
	keepLatestNotificationsPage,
	type NotificationDTO,
	type NotificationsCache,
	recentNotificationsQueryKey,
	unreadNotificationsQueryKey,
} from "../lib/notifications";
import { useUiStore } from "../stores/ui-store";
import { useNavigateToSession } from "../lib/navigate-to-session";
import { captureRendererEvent } from "../lib/telemetry";
import { cn } from "../lib/utils";
import { TopbarButton } from "./TopbarButton";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type NotificationCenterProps = {
	style?: React.CSSProperties;
};

function useNotificationTargetNavigation() {
	const navigateToSession = useNavigateToSession();
	const openSession = useCallback(
		(notification: NotificationDTO) => {
			const sessionId = notification.target.sessionId || notification.sessionId;
			if (!sessionId) return;
			void captureRendererEvent("ao.renderer.notification_opened", { target: "session" });
			navigateToSession(notification.projectId, sessionId);
		},
		[navigateToSession],
	);

	const openPrimary = useCallback(
		(notification: NotificationDTO) => {
			if (notification.target.kind === "pr" && notification.target.prUrl) {
				void captureRendererEvent("ao.renderer.notification_opened", { target: "pr" });
				window.open(notification.target.prUrl, "_blank", "noopener,noreferrer");
				return;
			}
			openSession(notification);
		},
		[openSession],
	);

	return { openPrimary, openSession };
}

function useSessionTerminationLookup(): {
	retryWorkspace: () => void;
	sessionsReady: boolean;
	terminatedIds: Set<string>;
	workspaceError: boolean;
} {
	const { data: workspaces, isError, isSuccess, refetch } = useWorkspaceQuery();
	const terminatedIds = useMemo(() => {
		const ids = new Set<string>();
		for (const workspace of workspaces ?? []) {
			for (const session of workspace.sessions) {
				if (session.isTerminated === true || session.status === "terminated") {
					ids.add(session.id);
				}
			}
		}
		return ids;
	}, [workspaces]);
	// Only successful workspace data is trustworthy. Pending and error both leave
	// sessions non-navigable — a failed query must not treat terminated rows as live.
	return {
		retryWorkspace: () => {
			void refetch();
		},
		sessionsReady: isSuccess,
		terminatedIds,
		workspaceError: isError,
	};
}

export function NotificationRuntime() {
	const queryClient = useQueryClient();
	const { openPrimary } = useNotificationTargetNavigation();
	const unreadQuery = useNotificationsQuery("unread");
	const unreadCount = getCachedUnreadCount(unreadQuery.data);
	const params = useParams({ strict: false }) as { sessionId?: string };
	const routeSessionIdRef = useRef(params.sessionId);
	routeSessionIdRef.current = params.sessionId;

	// Being on the session route is not the same as watching the agent: its pane
	// renders one terminal at a time, so a shell or reviewer tab hides the agent
	// while the route is unchanged. Only report the session whose agent terminal
	// is the one on screen. Read the store imperatively — this feeds a getter for
	// the long-lived SSE connection, which needs the current value, not a render.
	const getVisibleAgentSessionId = useCallback(() => {
		const sessionId = routeSessionIdRef.current;
		if (!sessionId) return undefined;
		return useUiStore.getState().visibleTerminalKindBySession[sessionId] === "worker" ? sessionId : undefined;
	}, []);

	useEffect(
		() => createNotificationsTransport(queryClient, getVisibleAgentSessionId).connect(),
		[getVisibleAgentSessionId, queryClient],
	);

	// Keep the OS launcher badge in sync here rather than in NotificationCenter:
	// NotificationRuntime is always mounted in the shell, whereas the notification
	// bell is absent from the Linux topbar and only mounts on the sessions board.
	useEffect(() => {
		void aoBridge.notifications.setBadge(unreadCount);
	}, [unreadCount]);

	useEffect(() => {
		return aoBridge.notifications.onClick((id) => {
			const unread = queryClient.getQueryData<NotificationsCache>(unreadNotificationsQueryKey);
			const recent = queryClient.getQueryData<NotificationsCache>(recentNotificationsQueryKey);
			const notification = [...getCachedNotifications(unread), ...getCachedNotifications(recent)].find(
				(item) => item.id === id,
			);
			if (notification) openPrimary(notification);
		});
	}, [openPrimary, queryClient]);

	return null;
}

export function NotificationCenter({ style }: NotificationCenterProps) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [actionError, setActionError] = useState<string | null>(null);
	const [markReadError, setMarkReadError] = useState<string | null>(null);
	// Bumped by the mark-read Retry control so a failed ack can run again even
	// when visibleUnreadKey is unchanged (removing ids from the ref alone does not).
	const [ackRetryNonce, setAckRetryNonce] = useState(0);
	const [open, setOpen] = useState(false);
	// Opening marks unread as read, which would drop the highlight under the
	// cursor. Keep the open-time unread ids highlighted until the panel closes.
	const [highlightedIds, setHighlightedIds] = useState<Set<string>>(() => new Set());
	const [restoringSessionId, setRestoringSessionId] = useState<string | undefined>();
	const unreadQuery = useNotificationsQuery("unread");
	const allQuery = useNotificationsQuery("all", open);
	const markAllRead = useMarkAllNotificationsReadMutation();
	const restoreSession = useRestoreSession();
	const { retryWorkspace, sessionsReady, terminatedIds, workspaceError } = useSessionTerminationLookup();
	const { data: workspaces } = useWorkspaceQuery();
	// Resolve the human project + session names for each notification so the row
	// can show where it came from (the DTO only carries opaque ids).
	const sessionMeta = useMemo(() => {
		const map = new Map<string, { projectName: string; sessionName: string }>();
		for (const workspace of workspaces ?? []) {
			for (const session of workspace.sessions) {
				map.set(session.id, { projectName: workspace.name, sessionName: session.title });
			}
		}
		return map;
	}, [workspaces]);
	const notifications = useMemo(() => getCachedNotifications(allQuery.data), [allQuery.data]);
	const unreadCount = getCachedUnreadCount(unreadQuery.data);
	const { openSession } = useNotificationTargetNavigation();
	const markAllMutate = markAllRead.mutateAsync;

	// Concrete ids only — never `[]` — so unread pages past the first stay
	// reachable and arrive as unread (still highlighted) when the all-list loads them.
	const acknowledgedIdsRef = useRef<Set<string>>(new Set());
	const visibleUnreadIds = useMemo(() => {
		const ids = new Set<string>();
		for (const item of getCachedNotifications(unreadQuery.data)) {
			if (item.status === "unread") ids.add(item.id);
		}
		for (const item of notifications) {
			if (item.status === "unread") ids.add(item.id);
		}
		return [...ids];
	}, [notifications, unreadQuery.data]);
	const visibleUnreadKey = visibleUnreadIds.join("|");

	// Opening the panel acknowledges what has actually been loaded. Later unread
	// rows are acknowledged as they appear, keeping the unread pagination cursor.
	useEffect(() => {
		if (!open) {
			setHighlightedIds(new Set());
			setMarkReadError(null);
			acknowledgedIdsRef.current = new Set();
			return;
		}
		if (unreadQuery.isLoading) return;

		const visibleIds = visibleUnreadKey === "" ? [] : visibleUnreadKey.split("|");
		const newly = visibleIds.filter((id) => !acknowledgedIdsRef.current.has(id));
		if (newly.length === 0) return;

		for (const id of newly) acknowledgedIdsRef.current.add(id);
		setHighlightedIds((current) => {
			const next = new Set(current);
			let changed = false;
			for (const id of newly) {
				if (next.has(id)) continue;
				next.add(id);
				changed = true;
			}
			return changed ? next : current;
		});

		setMarkReadError(null);
		void captureRendererEvent("ao.renderer.notification_mark_read_requested", { scope: "all" });
		void markAllMutate(newly)
			.then(() => captureRendererEvent("ao.renderer.notification_mark_read_succeeded", { scope: "all" }))
			.catch((error: unknown) => {
				void captureRendererEvent("ao.renderer.notification_mark_read_failed", { scope: "all" });
				for (const id of newly) acknowledgedIdsRef.current.delete(id);
				setMarkReadError(error instanceof Error ? error.message : t("notify.couldNotMarkAllRead"));
			});
	}, [ackRetryNonce, markAllMutate, open, t, unreadQuery.isLoading, visibleUnreadKey]);

	const setPanelOpen = (nextOpen: boolean) => {
		setOpen(nextOpen);
		if (!nextOpen) {
			keepLatestNotificationsPage(queryClient, unreadNotificationsQueryKey);
			keepLatestNotificationsPage(queryClient, recentNotificationsQueryKey);
		}
	};

	const retryMarkRead = () => {
		setMarkReadError(null);
		setAckRetryNonce((nonce) => nonce + 1);
	};

	const openSessionAndDismiss = (notification: NotificationDTO) => {
		openSession(notification);
		setPanelOpen(false);
	};

	const restoreAndOpen = async (notification: NotificationDTO) => {
		const sessionId = notification.target.sessionId || notification.sessionId;
		if (!sessionId || restoringSessionId) return;
		setRestoringSessionId(sessionId);
		setActionError(null);
		try {
			const result = await restoreSession(sessionId);
			if (result.status === "success") {
				openSession(notification);
				setPanelOpen(false);
				return;
			}
			setActionError(result.status === "not_resumable" ? t("notify.restoreUnavailable") : result.message);
		} finally {
			setRestoringSessionId(undefined);
		}
	};

	const loadEarlierOnScroll = (event: React.UIEvent<HTMLDivElement>) => {
		const list = event.currentTarget;
		const remaining = list.scrollHeight - list.scrollTop - list.clientHeight;
		if (remaining > 80 || !allQuery.hasNextPage || allQuery.isFetchingNextPage) return;
		void allQuery.fetchNextPage();
	};

	const isEmpty = notifications.length === 0;

	return (
		<Popover onOpenChange={setPanelOpen} open={open}>
			<PopoverTrigger asChild>
				<TopbarButton
					aria-label={unreadCount > 0 ? t("notify.unreadCount", { count: unreadCount }) : t("notify.bell")}
					className="relative"
					style={style}
					variant="icon"
				>
					{unreadCount > 0 ? (
						<BellRing className="size-icon-base fill-current text-foreground" aria-hidden="true" />
					) : (
						<Bell className="size-icon-base" aria-hidden="true" />
					)}
					{unreadCount > 0 ? (
						<span className="pointer-events-none absolute right-px top-px grid h-3 min-w-3 place-items-center rounded-full bg-accent-strong px-0.5 font-mono text-[7px] font-semibold leading-none text-accent-foreground shadow-sm ring-1 ring-background">
							{unreadCount > 99 ? "99+" : unreadCount}
						</span>
					) : null}
				</TopbarButton>
			</PopoverTrigger>
			<PopoverContent
				align="end"
				aria-label={t("notify.title")}
				className="w-notification-width max-w-[calc(100vw-1rem)] overflow-hidden rounded-panel border-border-strong p-0 shadow-xl"
				sideOffset={8}
			>
				<div className="border-b border-border bg-[var(--color-overlay-subtle)] px-4 py-3.5">
					<p className="text-subtitle font-semibold tracking-tight text-foreground">{t("notify.title")}</p>
				</div>

				{markReadError ? (
					<div
						aria-live="polite"
						className="flex items-center justify-between gap-2 border-b border-border bg-error/5 px-4 py-2 text-caption text-error"
					>
						<span>{markReadError}</span>
						<button
							className="shrink-0 font-medium underline underline-offset-2 hover:text-foreground"
							onClick={retryMarkRead}
							type="button"
						>
							{t("notify.retry")}
						</button>
					</div>
				) : null}
				{actionError ? (
					<div className="border-b border-border bg-error/5 px-4 py-2 text-caption text-error">{actionError}</div>
				) : null}
				{workspaceError ? (
					<div
						aria-live="polite"
						className="flex items-center justify-between gap-2 border-b border-border bg-error/5 px-4 py-2 text-caption text-error"
					>
						<span>{t("notify.workspaceLoadFailed")}</span>
						<button
							className="shrink-0 font-medium underline underline-offset-2 hover:text-foreground"
							onClick={retryWorkspace}
							type="button"
						>
							{t("notify.retry")}
						</button>
					</div>
				) : null}
				{allQuery.isError && isEmpty ? (
					<NotificationEmpty icon={CircleAlert} message={t("notify.loadFailed")} />
				) : allQuery.isLoading && isEmpty ? (
					<NotificationEmpty icon={Inbox} message={t("notify.loading")} />
				) : isEmpty ? (
					<NotificationEmpty icon={CheckCheck} message={t("notify.emptyAll")} />
				) : (
					<div
						aria-busy={allQuery.isFetchingNextPage}
						className="board-scrollbar max-h-notification-max-height overflow-y-auto overscroll-contain py-1.5"
						onScroll={loadEarlierOnScroll}
						role="list"
					>
						{notifications.map((notification) => {
							const sessionId = notification.target.sessionId || notification.sessionId;
							const terminated = Boolean(sessionId) && terminatedIds.has(sessionId);
							// Restoring only makes sense when an agent is actually paused waiting
							// on input. PR outcomes (ready_to_merge, pr_merged, pr_closed_unmerged)
							// describe work that already finished — there is nothing to resume, so
							// a terminated session behind one of these should stay viewable, not
							// gated behind a restore action.
							const offerRestore = terminated && notification.type === "needs_input";
							return (
								<NotificationItem
									highlighted={highlightedIds.has(notification.id) || notification.status === "unread"}
									key={notification.id}
									meta={sessionId ? sessionMeta.get(sessionId) : undefined}
									notification={notification}
									onOpenSession={openSessionAndDismiss}
									onRestore={() => void restoreAndOpen(notification)}
									restoring={restoringSessionId === sessionId}
									restoreDisabled={restoringSessionId !== undefined}
									sessionsReady={sessionsReady}
									terminated={terminated}
									offerRestore={offerRestore}
								/>
							);
						})}
						{allQuery.isFetchNextPageError ? (
							<div
								aria-live="polite"
								className="flex items-center justify-center gap-2 px-4 py-3 text-caption text-error"
							>
								{t("notify.earlierLoadFailed")}
								<button
									className="font-medium underline underline-offset-2 hover:text-foreground"
									onClick={() => void allQuery.fetchNextPage()}
									type="button"
								>
									{t("notify.retry")}
								</button>
							</div>
						) : allQuery.isFetchingNextPage ? (
							<div
								aria-live="polite"
								className="flex items-center justify-center gap-2 px-4 py-3 text-caption text-passive"
							>
								<LoaderCircle className="size-icon-md animate-spin" aria-hidden="true" />
								{t("notify.loadingEarlier")}
							</div>
						) : null}
					</div>
				)}
			</PopoverContent>
		</Popover>
	);
}

function NotificationEmpty({ icon: Icon, message }: { icon: typeof Bell; message: string }) {
	return (
		<div className="grid min-h-40 place-items-center px-4 py-10 text-center">
			<div>
				<div className="mx-auto grid size-control-xl place-items-center rounded-full border border-border bg-surface text-passive">
					<Icon className={cn("size-icon-base", Icon === LoaderCircle && "animate-spin")} aria-hidden="true" />
				</div>
				<p className="mt-2.5 text-control text-muted-foreground">{message}</p>
			</div>
		</div>
	);
}

/**
 * The whole row is the click target for live sessions. A terminated session
 * behind a `needs_input` notification is not navigable — restore is the only
 * action, since there is a paused agent to resume. PR-outcome notifications
 * (`offerRestore` false) describe finished work, so a terminated session stays
 * viewable there instead of being gated behind restore. PR titles stay a real
 * link so a PR row can open the PR without a separate icon button.
 */
function NotificationItem({
	highlighted,
	meta,
	notification,
	offerRestore,
	onOpenSession,
	onRestore,
	restoring,
	restoreDisabled,
	sessionsReady,
	terminated,
}: {
	highlighted: boolean;
	meta?: { projectName: string; sessionName: string };
	notification: NotificationDTO;
	offerRestore: boolean;
	onOpenSession: (notification: NotificationDTO) => void;
	onRestore: () => void;
	restoring: boolean;
	restoreDisabled: boolean;
	sessionsReady: boolean;
	terminated: boolean;
}) {
	const { t } = useTranslation();
	const Icon = notificationIcon(notification.type);
	const sessionId = notification.target.sessionId || notification.sessionId;
	const canOpenSession = Boolean(sessionId) && sessionsReady && (!terminated || !offerRestore);
	const copy = notificationCopy(notification, meta?.sessionName);
	const titleLink = notificationPRTitleLink(notification, copy.title);
	const showSessionMeta = Boolean(meta?.sessionName) && !notificationMentions(copy, meta?.sessionName ?? "");
	const openRow = () => {
		if (canOpenSession) onOpenSession(notification);
	};
	return (
		<div role="listitem">
			<div
				className={cn(
					"group grid grid-cols-notification items-start gap-3 px-4 py-3 text-left transition-[background-color,opacity,transform] duration-fast will-change-transform",
					highlighted && "notification-row-enter",
					canOpenSession
						? "cursor-pointer hover:bg-interactive-hover active:scale-[0.99] active:bg-interactive-active"
						: "cursor-default",
					!highlighted && "opacity-55 hover:opacity-80",
				)}
				onClick={openRow}
				onKeyDown={(event) => {
					if (!canOpenSession) return;
					if (event.key !== "Enter" && event.key !== " ") return;
					event.preventDefault();
					openRow();
				}}
				role={canOpenSession ? "button" : undefined}
				tabIndex={canOpenSession ? 0 : undefined}
				title={canOpenSession ? t("notify.openSessionTitle") : undefined}
			>
				<div
					className={cn(
						"grid size-notification-icon shrink-0 place-items-center transition-transform duration-fast group-hover:brightness-110 group-active:scale-90",
						notificationIconClass(notification.type),
					)}
				>
					<Icon className="size-5" strokeWidth={2} aria-hidden="true" />
				</div>
				<div className="min-w-0">
					{/* Match the 26px icon band so the title centers with the left glyph. */}
					<div className="flex min-h-notification-icon items-center">
						<span
							aria-label={copy.title}
							className={cn(
								"min-w-0 break-words text-control leading-snug text-foreground",
								highlighted && "font-medium",
							)}
						>
							{titleLink ? (
								<>
									{titleLink.before}
									<a
										aria-label={t("inspector.openPR", { number: titleLink.number })}
										className="inline-flex items-center gap-0.5 underline-offset-2 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
										href={titleLink.url}
										onClick={(event) => {
											event.preventDefault();
											event.stopPropagation();
											void captureRendererEvent("ao.renderer.notification_opened", { target: "pr" });
											void openLinkInSystemBrowser(titleLink.url);
										}}
										rel="noopener noreferrer"
										target="_blank"
									>
										{titleLink.label}
										<ArrowUpRight aria-hidden="true" className="size-icon-2xs shrink-0" strokeWidth={2} />
									</a>
									{titleLink.after}
								</>
							) : (
								copy.title
							)}
						</span>
					</div>
					{copy.body ? (
						<p className="mt-0.5 whitespace-pre-wrap break-words text-caption leading-snug text-muted-foreground">
							{copy.body}
						</p>
					) : null}
					{meta && (meta.projectName || showSessionMeta) ? (
						<p className="mt-1 flex min-w-0 items-center gap-1.5 text-caption leading-none text-passive">
							{meta.projectName ? (
								<span className="truncate font-medium text-muted-foreground">{meta.projectName}</span>
							) : null}
							{meta.projectName && showSessionMeta ? <span aria-hidden="true">·</span> : null}
							{showSessionMeta ? <span className="truncate">{meta.sessionName}</span> : null}
						</p>
					) : null}
				</div>
				{/* Time + restore share the same icon-height band so they stay level. */}
				<div className="flex h-notification-icon shrink-0 items-center gap-1">
					<time className="shrink-0 font-mono text-[9px] leading-none text-passive" dateTime={notification.createdAt}>
						{formatTimeCompact(notification.createdAt)}
					</time>
					{offerRestore && sessionId ? (
						<Tooltip delayDuration={0}>
							<TooltipTrigger asChild>
								<button
									aria-label={t("shell.restoreSession")}
									className="grid size-notification-icon place-items-center rounded-md text-passive transition-colors hover:bg-interactive-active hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
									disabled={restoreDisabled}
									onClick={(event) => {
										event.stopPropagation();
										onRestore();
									}}
									type="button"
								>
									<RotateCcw className={cn("size-icon-md", restoring && "animate-spin")} aria-hidden="true" />
								</button>
							</TooltipTrigger>
							<TooltipContent side="top">
								{restoring ? t("shell.restoringSession") : t("shell.restoreSession")}
							</TooltipContent>
						</Tooltip>
					) : null}
				</div>
			</div>
		</div>
	);
}

type NotificationCopy = Pick<NotificationDTO, "body" | "title">;

function notificationPRTitleLink(
	notification: NotificationDTO,
	title: string,
): { after: string; before: string; label: string; number: string; url: string } | null {
	const url = notification.target.kind === "pr" ? notification.target.prUrl : notification.prUrl;
	if (!url) return null;
	const match = /\bPR\s*#(\d+)\b/i.exec(title);
	if (!match || match.index === undefined) return null;
	return {
		before: title.slice(0, match.index),
		label: match[0],
		number: match[1],
		after: title.slice(match.index + match[0].length),
		url,
	};
}

function notificationCopy(notification: NotificationDTO, sessionName?: string): NotificationCopy {
	if (notification.type !== "ready_to_merge") {
		return { title: notification.title, body: notification.body };
	}

	const session = sessionName?.trim();
	if (!session) {
		return { title: notification.title, body: notification.body };
	}

	const legacySessionTitle = `${session} is ready to merge`;
	const title =
		notification.title.trim().toLocaleLowerCase() === legacySessionTitle.toLocaleLowerCase()
			? readyNotificationFallbackTitle(notification)
			: notification.title;

	return {
		title,
		body: `PR from session ${session} is ready to merge. CI passed with no blocking review feedback.`,
	};
}

function readyNotificationFallbackTitle(notification: NotificationDTO): string {
	const titleNumber = notification.title.match(/\bPR\s*#(\d+)\b/i)?.[1];
	const urlNumber = notification.prUrl.match(/\/pull\/(\d+)(?:\/|$)/)?.[1];
	const number = titleNumber ?? urlNumber;
	return number ? `PR #${number} is ready to merge` : "Pull request is ready to merge";
}

function notificationMentions(notification: NotificationCopy, value: string): boolean {
	const needle = value.trim().toLocaleLowerCase();
	if (!needle) return false;
	return `${notification.title}\n${notification.body}`.toLocaleLowerCase().includes(needle);
}

function notificationIcon(type: string) {
	switch (type) {
		case "needs_input":
			return MessageSquareDot;
		case "ready_to_merge":
			return GitPullRequestArrow;
		case "pr_merged":
			return GitMerge;
		case "pr_closed_unmerged":
			return GitPullRequestClosed;
		default:
			return Bell;
	}
}

// Bare colored glyph per type (no tile). Merged uses GitHub's PR-merged purple;
// the accent token is a near-black surface color in this theme and reads as
// invisible, so it must not be used for a glyph.
function notificationIconClass(type: string): string {
	switch (type) {
		case "needs_input":
			return "text-warning";
		case "ready_to_merge":
			return "text-success";
		case "pr_merged":
			return "text-[#a371f7]";
		case "pr_closed_unmerged":
			return "text-error";
		default:
			return "text-muted-foreground";
	}
}
