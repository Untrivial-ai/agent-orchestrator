import type { InfiniteData, QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { aoBridge } from "./bridge";
import { apiClient, apiErrorMessage, getApiBaseUrl, subscribeApiBaseUrl } from "./api-client";
import { computeSseRetryDelayMs } from "./sse-backoff";

export type NotificationDTO = components["schemas"]["NotificationResponse"];
export type NotificationsPage = components["schemas"]["ListNotificationsResponse"];
export type NotificationsCache = InfiniteData<NotificationsPage>;
export type NotificationListStatus = "unread" | "all";

export const unreadNotificationsQueryKey = ["notifications", "history", "unread"] as const;
export const recentNotificationsQueryKey = ["notifications", "history", "all"] as const;
export const NOTIFICATION_PAGE_SIZE = 100;

const EVENTSOURCE_CLOSED = 2;

/**
 * Only these two kinds describe something still waiting on the user.
 * `pr_merged` / `pr_closed_unmerged` report something that already happened.
 * Mirrors NotificationType.NeedsResolution on the backend — used here only to
 * keep `unresolvedCount` accurate on the unread/all caches.
 */
const UNRESOLVABLE_TYPES = new Set(["needs_input", "ready_to_merge"]);

type NotificationsQueryKey = typeof unreadNotificationsQueryKey | typeof recentNotificationsQueryKey;

export function notificationsQueryKey(status: NotificationListStatus): NotificationsQueryKey {
	return status === "unread" ? unreadNotificationsQueryKey : recentNotificationsQueryKey;
}

function isUnresolved(notification: NotificationDTO): boolean {
	return UNRESOLVABLE_TYPES.has(notification.type) && !notification.resolvedAt;
}

export async function fetchNotificationsPage(status: NotificationListStatus, cursor = ""): Promise<NotificationsPage> {
	const { data, error } = await apiClient.GET("/api/v1/notifications", {
		params: {
			query: {
				status,
				limit: NOTIFICATION_PAGE_SIZE,
				cursor: cursor || undefined,
			},
		},
	});
	if (error) throw new Error(apiErrorMessage(error, "Could not load notifications"));
	const notifications = sortNotifications(data?.notifications ?? []);
	return {
		notifications,
		nextCursor: data?.nextCursor,
		unreadCount: data?.unreadCount ?? notifications.filter((item) => item.status === "unread").length,
		unresolvedCount: data?.unresolvedCount ?? notifications.filter(isUnresolved).length,
	};
}

/**
 * Fired when the panel opens — seeing the notifications is the acknowledgement.
 *
 * Empty `ids` marks every unread row (the all-history panel still shows them).
 * Non-empty `ids` marks exactly those rows so incremental clients can keep later
 * unread pages reachable.
 */
export async function markAllNotificationsRead(ids: string[]): Promise<number> {
	const { data, error } = await apiClient.POST("/api/v1/notifications/read-all", {
		body: ids.length === 0 ? {} : { ids },
	});
	if (error) throw new Error(apiErrorMessage(error, "Could not mark notifications read"));
	return data?.updatedCount ?? 0;
}

export function mergeUnreadNotification(queryClient: QueryClient, notification: NotificationDTO): boolean {
	if (notification.status !== "unread") return false;
	const inserted = mergeNotificationIntoCache(queryClient, unreadNotificationsQueryKey, notification);
	rebaseOversizedFirstPage(queryClient, unreadNotificationsQueryKey);
	return inserted;
}

function mergeRecentNotification(queryClient: QueryClient, notification: NotificationDTO): boolean {
	const inserted = mergeNotificationIntoCache(queryClient, recentNotificationsQueryKey, notification);
	rebaseOversizedFirstPage(queryClient, recentNotificationsQueryKey);
	return inserted;
}

/**
 * AO resolved the issue behind a notification. Update the row in unread/all
 * caches; the seen state is a separate axis and is deliberately left untouched.
 */
export function applyResolvedNotification(queryClient: QueryClient, notification: NotificationDTO): void {
	for (const queryKey of [unreadNotificationsQueryKey, recentNotificationsQueryKey] as const) {
		queryClient.setQueryData<NotificationsCache>(queryKey, (current) => {
			if (!current) return current;
			return {
				...current,
				pages: current.pages.map((page) => ({
					...page,
					notifications: page.notifications.map((item) => (item.id === notification.id ? notification : item)),
					unresolvedCount: Math.max(0, page.unresolvedCount - 1),
				})),
			};
		});
	}
}

function mergeNotificationIntoCache(
	queryClient: QueryClient,
	queryKey: NotificationsQueryKey,
	notification: NotificationDTO,
): boolean {
	let inserted = false;
	queryClient.setQueryData<NotificationsCache>(queryKey, (current) => {
		if (!current || current.pages.length === 0) {
			inserted = true;
			return {
				pageParams: [""],
				pages: [
					{
						notifications: [notification],
						unreadCount: notification.status === "unread" ? 1 : 0,
						unresolvedCount: isUnresolved(notification) ? 1 : 0,
					},
				],
			};
		}

		const existing = getCachedNotifications(current).find((item) => item.id === notification.id);
		const unreadDelta = (notification.status === "unread" ? 1 : 0) - (existing?.status === "unread" ? 1 : 0);
		const unresolvedDelta =
			(isUnresolved(notification) ? 1 : 0) - (existing && isUnresolved(existing) ? 1 : 0);
		const pages = current.pages.map((page) => ({
			...page,
			notifications: page.notifications.map((item) => (item.id === notification.id ? notification : item)),
			unreadCount: Math.max(0, page.unreadCount + unreadDelta),
			unresolvedCount: Math.max(0, page.unresolvedCount + unresolvedDelta),
		}));

		if (existing) {
			return { ...current, pages };
		}

		inserted = true;
		pages[0] = {
			...pages[0],
			notifications: sortNotifications([notification, ...pages[0].notifications]),
		};
		return { ...current, pages };
	});
	return inserted;
}

/**
 * Marks notifications read in the React Query caches.
 *
 * Empty `ids` means every unread row — matching `POST /read-all` with no body.
 * That is safe for the all-history panel: read rows remain visible there.
 *
 * Non-empty `ids` marks exactly those rows and keeps unread pagination cursors
 * intact so later pages stay reachable when a client acknowledges incrementally.
 *
 * `updatedCount` is the mutation's server tally. Prefer it over locally cleared
 * rows: later all-list pages can acknowledge unread ids that were never loaded
 * into the unread cache, which would otherwise leave the bell badge stuck.
 */
export function markAllCachedNotificationsRead(
	queryClient: QueryClient,
	ids: string[],
	updatedCount?: number,
): void {
	if (ids.length === 0) {
		queryClient.setQueryData<NotificationsCache>(unreadNotificationsQueryKey, (current) => {
			if (!current) {
				return { pageParams: [""], pages: [{ notifications: [], unreadCount: 0, unresolvedCount: 0 }] };
			}
			return {
				pageParams: [""],
				pages: [
					{
						notifications: [],
						unreadCount: 0,
						unresolvedCount: current.pages[0]?.unresolvedCount ?? 0,
					},
				],
			};
		});
		queryClient.setQueryData<NotificationsCache>(recentNotificationsQueryKey, (current) => {
			if (!current) return current;
			return {
				...current,
				pages: current.pages.map((page) => ({
					...page,
					notifications: page.notifications.map((item) =>
						item.status === "read" ? item : { ...item, status: "read" as const },
					),
					unreadCount: 0,
				})),
			};
		});
		return;
	}

	const acknowledged = new Set(ids);
	for (const queryKey of [unreadNotificationsQueryKey, recentNotificationsQueryKey] as const) {
		queryClient.setQueryData<NotificationsCache>(queryKey, (current) => {
			if (!current) return current;

			let clearedAcrossPages = 0;
			const pages = current.pages.map((page) => {
				const notifications = page.notifications.map((item) => {
					if (!acknowledged.has(item.id) || item.status === "read") return item;
					clearedAcrossPages++;
					return { ...item, status: "read" as const };
				});
				return { ...page, notifications };
			});
			const delta = updatedCount ?? clearedAcrossPages;

			return {
				...current,
				pages: pages.map((page) => ({
					...page,
					unreadCount: Math.max(0, page.unreadCount - delta),
				})),
			};
		});
	}
}

export function getCachedNotifications(cache: NotificationsCache | undefined): NotificationDTO[] {
	if (!cache) return [];
	const byID = new Map<string, NotificationDTO>();
	for (const page of cache.pages) {
		for (const notification of page.notifications) {
			if (!byID.has(notification.id)) byID.set(notification.id, notification);
		}
	}
	return sortNotifications([...byID.values()]);
}

export function getCachedUnreadCount(cache: NotificationsCache | undefined): number {
	return (
		cache?.pages[0]?.unreadCount ?? getCachedNotifications(cache).filter((item) => item.status === "unread").length
	);
}

export function keepLatestNotificationsPage(
	queryClient: QueryClient,
	queryKey: NotificationsQueryKey = unreadNotificationsQueryKey,
): void {
	queryClient.setQueryData<NotificationsCache>(queryKey, (current) => {
		if (!current || current.pages.length <= 1) return current;
		return {
			pages: [current.pages[0]],
			pageParams: [current.pageParams[0]],
		};
	});
	rebaseOversizedFirstPage(queryClient, queryKey);
}

/**
 * A `needs_input` toast is redundant only when the user can already see the
 * prompt, which takes three things: the agent's terminal for that session is
 * the one on screen, this window is visible, and this window has focus.
 *
 * Each check covers a way "looks visible" lies. Visibility alone is not enough
 * — on Windows and Linux an unfocused or fully covered Electron window still
 * reports `visibilityState === "visible"`. The route alone is not enough
 * either: the session pane renders one terminal at a time, so an open shell or
 * reviewer tab hides the agent while the URL still names that session. The
 * caller resolves that, passing the session only while its agent pane shows.
 *
 * Only `needs_input` is suppressed. PR outcomes (`ready_to_merge`,
 * `pr_merged`, `pr_closed_unmerged`) are not visible in the terminal pane, so
 * they still deserve a toast even for the session in the foreground.
 */
function suppressToastForWatchedSession(
	notification: NotificationDTO,
	visibleAgentSessionId: string | undefined,
): boolean {
	if (notification.type !== "needs_input") return false;
	if (!notification.sessionId || notification.sessionId !== visibleAgentSessionId) return false;
	return document.visibilityState === "visible" && document.hasFocus();
}

export function createNotificationsTransport(
	queryClient: QueryClient,
	/** The session whose agent terminal is currently on screen, if any. */
	getVisibleAgentSessionId: () => string | undefined = () => undefined,
) {
	return {
		connect() {
			let retryTimer: ReturnType<typeof setTimeout> | undefined;
			let source: EventSource | undefined;
			let sourceBaseUrl: string | undefined;

			const invalidateNotifications = () => {
				void queryClient.invalidateQueries({ queryKey: unreadNotificationsQueryKey });
				void queryClient.invalidateQueries({ queryKey: recentNotificationsQueryKey });
			};

			// Consecutive scheduled rebuilds since the stream last opened; paces
			// the retry instead of knocking on a flat cadence forever (#4323).
			let retries = 0;

			const scheduleRetry = () => {
				if (retryTimer) return;
				retries += 1;
				retryTimer = setTimeout(() => {
					retryTimer = undefined;
					connectSource();
				}, computeSseRetryDelayMs(retries));
			};

			const connectSource = () => {
				if (typeof EventSource === "undefined") return;
				const baseUrl = getApiBaseUrl();
				if (source && sourceBaseUrl === baseUrl && source.readyState !== EVENTSOURCE_CLOSED) return;
				// A new daemon port is a fresh target; it should not inherit the
				// delay the dead port earned.
				if (sourceBaseUrl && sourceBaseUrl !== baseUrl) retries = 0;
				source?.close();
				source = undefined;
				sourceBaseUrl = baseUrl;
				try {
					source = new EventSource(`${baseUrl.replace(/\/+$/, "")}/api/v1/notifications/stream`);
					source.onopen = () => {
						retries = 0;
						invalidateNotifications();
					};
					source.onerror = () => {
						if (source?.readyState === EVENTSOURCE_CLOSED) scheduleRetry();
					};
					source.addEventListener("notification_created", (event) => {
						const notification = parseNotificationEvent(event);
						if (!notification) return;
						const inserted = mergeUnreadNotification(queryClient, notification);
						mergeRecentNotification(queryClient, notification);
						if (inserted && !suppressToastForWatchedSession(notification, getVisibleAgentSessionId())) {
							void aoBridge.notifications.show({
								id: notification.id,
								title: notification.title,
								body: notification.body || undefined,
								type: notification.type,
							});
						}
					});
					// AO closed the underlying issue (the session got its input, the
					// PR stopped waiting on a merge). Patch the row live so an open
					// panel reflects that without waiting for a refetch.
					source.addEventListener("notification_resolved", (event) => {
						const notification = parseNotificationEvent(event);
						if (!notification) return;
						applyResolvedNotification(queryClient, notification);
					});
				} catch {
					source = undefined;
				}
			};

			const removeDaemonListener = aoBridge.daemon.onStatus(() => {
				connectSource();
				invalidateNotifications();
			});
			const removeBaseUrlListener = subscribeApiBaseUrl(() => {
				connectSource();
				invalidateNotifications();
			});
			connectSource();

			return () => {
				if (retryTimer) clearTimeout(retryTimer);
				removeDaemonListener();
				removeBaseUrlListener();
				source?.close();
			};
		},
	};
}

function parseNotificationEvent(event: Event): NotificationDTO | null {
	const data = (event as MessageEvent<string>).data;
	if (typeof data !== "string" || data === "") return null;
	try {
		return JSON.parse(data) as NotificationDTO;
	} catch {
		return null;
	}
}

function sortNotifications(notifications: NotificationDTO[]): NotificationDTO[] {
	return [...notifications].sort((a, b) => {
		const byTime = Date.parse(b.createdAt) - Date.parse(a.createdAt);
		return byTime || b.id.localeCompare(a.id);
	});
}

function rebaseOversizedFirstPage(queryClient: QueryClient, queryKey: NotificationsQueryKey): void {
	const cache = queryClient.getQueryData<NotificationsCache>(queryKey);
	if (!cache || cache.pages[0]?.notifications.length <= NOTIFICATION_PAGE_SIZE) return;
	const query = queryClient.getQueryCache().find({ queryKey, exact: true });
	if (!query?.isActive()) {
		queryClient.setQueryData<NotificationsCache>(queryKey, (current) => {
			if (!current?.pages[0]) return current;
			return {
				...current,
				pages: [
					{
						...current.pages[0],
						notifications: current.pages[0].notifications.slice(0, NOTIFICATION_PAGE_SIZE),
					},
					...current.pages.slice(1),
				],
			};
		});
	}
	void queryClient.invalidateQueries({ queryKey, exact: true, refetchType: "active" });
}
