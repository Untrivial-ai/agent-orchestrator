import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computeSseRetryDelayMs } from "./sse-backoff";
import type { NotificationDTO, NotificationsCache } from "./notifications";

const {
	apiGetMock,
	getApiBaseUrlMock,
	onStatusMock,
	removeStatusMock,
	showNotificationMock,
	subscribeApiBaseUrlMock,
	unsubscribeBaseUrlMock,
} = vi.hoisted(() => ({
	apiGetMock: vi.fn(),
	getApiBaseUrlMock: vi.fn(() => "http://127.0.0.1:3001"),
	onStatusMock: vi.fn(),
	removeStatusMock: vi.fn(),
	showNotificationMock: vi.fn(),
	subscribeApiBaseUrlMock: vi.fn(),
	unsubscribeBaseUrlMock: vi.fn(),
}));

vi.mock("./api-client", () => ({
	apiClient: { GET: apiGetMock },
	apiErrorMessage: () => "Request failed",
	getApiBaseUrl: getApiBaseUrlMock,
	subscribeApiBaseUrl: subscribeApiBaseUrlMock,
}));

vi.mock("./bridge", () => ({
	aoBridge: {
		daemon: { onStatus: onStatusMock },
		notifications: { show: showNotificationMock },
	},
}));

import {
	applyResolvedNotification,
	createNotificationsTransport,
	fetchNotificationsPage,
	getCachedNotifications,
	getCachedUnreadCount,
	keepLatestNotificationsPage,
	markAllCachedNotificationsRead,
	mergeUnreadNotification,
	NOTIFICATION_PAGE_SIZE,
	recentNotificationsQueryKey,
	unreadNotificationsQueryKey,
} from "./notifications";

class EventSourceStub {
	static instances: EventSourceStub[] = [];
	url: string;
	closed = false;
	readyState = 0;
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	listeners = new Map<string, (event: MessageEvent<string>) => void>();

	constructor(url: string) {
		this.url = url;
		EventSourceStub.instances.push(this);
	}

	addEventListener(type: string, listener: EventListener) {
		this.listeners.set(type, listener as (event: MessageEvent<string>) => void);
	}

	dispatch(type: string, data: unknown) {
		this.listeners.get(type)?.({ data: JSON.stringify(data) } as MessageEvent<string>);
	}

	close() {
		this.closed = true;
		this.readyState = 2;
	}
}

function notification(overrides: Partial<NotificationDTO> = {}): NotificationDTO {
	return {
		id: "ntf_1",
		sessionId: "mer-1",
		projectId: "mer",
		prUrl: "",
		type: "needs_input",
		title: "checkout-flow needs input",
		body: "The agent is waiting for your response.",
		status: "unread",
		createdAt: "2026-06-16T10:00:00Z",
		target: { kind: "session", sessionId: "mer-1" },
		...overrides,
	};
}

function queryClient() {
	return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

// A covered or background Electron window still reports "visible" on Windows
// and Linux, so visibility and focus have to be stubbed independently.
function setWindowState({ focused, visible }: { focused: boolean; visible: boolean }) {
	vi.spyOn(document, "visibilityState", "get").mockReturnValue(visible ? "visible" : "hidden");
	vi.spyOn(document, "hasFocus").mockReturnValue(focused);
}

beforeEach(() => {
	apiGetMock.mockReset();
	EventSourceStub.instances = [];
	getApiBaseUrlMock.mockReset().mockReturnValue("http://127.0.0.1:3001");
	onStatusMock.mockReset().mockReturnValue(removeStatusMock);
	removeStatusMock.mockReset();
	showNotificationMock.mockReset().mockResolvedValue(undefined);
	subscribeApiBaseUrlMock.mockReset().mockReturnValue(unsubscribeBaseUrlMock);
	unsubscribeBaseUrlMock.mockReset();
	(globalThis as unknown as { EventSource: unknown }).EventSource = EventSourceStub;
});

afterEach(() => {
	delete (globalThis as unknown as { EventSource?: unknown }).EventSource;
	vi.restoreAllMocks();
});

describe("notification cache helpers", () => {
	it.each([
		{ cursor: "previous", nextCursor: "older", status: "all" as const, unreadCount: 4 },
		{ cursor: "", nextCursor: undefined, status: "unread" as const, unreadCount: 1 },
	])("requests a bounded $status page", async ({ cursor, nextCursor, status, unreadCount }) => {
		apiGetMock.mockResolvedValue({
			data: { notifications: [notification()], nextCursor, unreadCount, unresolvedCount: 3 },
		});

		await expect(fetchNotificationsPage(status, cursor)).resolves.toEqual({
			notifications: [notification()],
			nextCursor,
			unreadCount,
			unresolvedCount: 3,
		});

		expect(apiGetMock).toHaveBeenCalledWith("/api/v1/notifications", {
			params: {
				query: {
					cursor: cursor || undefined,
					limit: NOTIFICATION_PAGE_SIZE,
					status,
				},
			},
		});
	});

	it("merges unread notifications by id", () => {
		const qc = queryClient();

		expect(mergeUnreadNotification(qc, notification())).toBe(true);
		expect(mergeUnreadNotification(qc, notification())).toBe(false);

		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toHaveLength(1);
		expect(getCachedUnreadCount(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toBe(1);
	});

	// Opening the panel acknowledges what it rendered; history keeps the rows.
	it("clears the acknowledged rows and keeps them in All", () => {
		const qc = queryClient();
		mergeUnreadNotification(qc, notification());
		qc.setQueryData<NotificationsCache>(recentNotificationsQueryKey, {
			pageParams: [""],
			pages: [{ notifications: [notification()], unreadCount: 1, unresolvedCount: 1 }],
		});

		markAllCachedNotificationsRead(qc, ["ntf_1"]);

		expect(getCachedUnreadCount(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toBe(0);
		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(recentNotificationsQueryKey))).toEqual([
			expect.objectContaining({ id: "ntf_1", status: "read" }),
		]);
	});

	it("clears every unread row when acknowledging with no ids", () => {
		const qc = queryClient();
		mergeUnreadNotification(qc, notification({ id: "ntf_1" }));
		mergeUnreadNotification(qc, notification({ id: "ntf_2" }));
		qc.setQueryData<NotificationsCache>(recentNotificationsQueryKey, {
			pageParams: [""],
			pages: [
				{
					notifications: [notification({ id: "ntf_1" }), notification({ id: "ntf_2" })],
					unreadCount: 2,
					unresolvedCount: 2,
				},
			],
		});

		markAllCachedNotificationsRead(qc, []);

		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toEqual([]);
		expect(getCachedUnreadCount(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toBe(0);
		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(recentNotificationsQueryKey))).toEqual(
			expect.arrayContaining([
				expect.objectContaining({ id: "ntf_1", status: "read" }),
				expect.objectContaining({ id: "ntf_2", status: "read" }),
			]),
		);
		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(recentNotificationsQueryKey))).toHaveLength(2);
	});

	// Acknowledging must never discard the cursor to rows the panel has not
	// loaded yet: the server only cleared the ids we sent, so anything past the
	// loaded page has to stay reachable for the rest of the session.
	it("keeps unacknowledged pages and their cursor after acknowledgement", () => {
		const qc = queryClient();
		const loaded = Array.from({ length: NOTIFICATION_PAGE_SIZE }, (_, index) =>
			notification({ id: `ntf_${index + 1}`, type: "pr_merged" }),
		);
		qc.setQueryData<NotificationsCache>(unreadNotificationsQueryKey, {
			pageParams: [""],
			pages: [
				{
					notifications: loaded,
					nextCursor: "older",
					unreadCount: NOTIFICATION_PAGE_SIZE + 1,
					unresolvedCount: 0,
				},
			],
		});

		markAllCachedNotificationsRead(
			qc,
			loaded.map((item) => item.id),
		);

		const cache = qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey);
		expect(cache?.pages[0]?.nextCursor).toBe("older");
		expect(getCachedUnreadCount(cache)).toBe(1);
		expect(getCachedNotifications(cache).every((item) => item.status === "read")).toBe(true);

		// The still-unread row past the loaded page arrives on the next page and
		// must be visible rather than stranded.
		mergeUnreadNotification(qc, notification({ id: "ntf_101", type: "pr_merged" }));
		expect(
			getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey)).find(
				(item) => item.id === "ntf_101",
			)?.status,
		).toBe("unread");
	});

	// Later all-list pages can ack unread ids that were never loaded into the
	// unread cache. updatedCount must still move the badge, without wiping the
	// unread pagination cursor.
	it("decrements unreadCount from updatedCount when ids are absent from the unread cache", () => {
		const qc = queryClient();
		const loaded = Array.from({ length: NOTIFICATION_PAGE_SIZE }, (_, index) =>
			notification({ id: `ntf_${index + 1}`, status: "read", type: "pr_merged" }),
		);
		qc.setQueryData<NotificationsCache>(unreadNotificationsQueryKey, {
			pageParams: [""],
			pages: [
				{
					notifications: loaded,
					nextCursor: "older-unread",
					unreadCount: 1,
					unresolvedCount: 0,
				},
			],
		});
		qc.setQueryData<NotificationsCache>(recentNotificationsQueryKey, {
			pageParams: ["", "older"],
			pages: [
				{
					notifications: loaded.slice(0, 2),
					nextCursor: "older",
					unreadCount: 1,
					unresolvedCount: 0,
				},
				{
					notifications: [notification({ id: "ntf_101", type: "pr_merged" })],
					unreadCount: 1,
					unresolvedCount: 0,
				},
			],
		});

		markAllCachedNotificationsRead(qc, ["ntf_101"], 1);

		const unread = qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey);
		expect(unread?.pages[0]?.nextCursor).toBe("older-unread");
		expect(getCachedUnreadCount(unread)).toBe(0);
		expect(getCachedNotifications(unread)).toHaveLength(NOTIFICATION_PAGE_SIZE);

		const recent = qc.getQueryData<NotificationsCache>(recentNotificationsQueryKey);
		expect(getCachedUnreadCount(recent)).toBe(0);
		expect(getCachedNotifications(recent).find((item) => item.id === "ntf_101")?.status).toBe("read");
	});

	it("deduplicates and updates notifications across cached pages", () => {
		const qc = queryClient();
		qc.setQueryData<NotificationsCache>(unreadNotificationsQueryKey, {
			pageParams: ["", "older"],
			pages: [
				{ notifications: [notification({ id: "new" })], nextCursor: "older", unreadCount: 2, unresolvedCount: 2 },
				{ notifications: [notification({ id: "old" })], unreadCount: 2, unresolvedCount: 2 },
			],
		});

		expect(mergeUnreadNotification(qc, notification({ id: "old", title: "updated" }))).toBe(false);
		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toHaveLength(2);
		expect(
			getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey)).find(
				(item) => item.id === "old",
			)?.title,
		).toBe("updated");
		expect(getCachedUnreadCount(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toBe(2);
	});

	it("caps and rebases the cache when live events grow the newest page past 100", () => {
		const qc = queryClient();
		const invalidateSpy = vi.spyOn(qc, "invalidateQueries");
		const firstPage = Array.from({ length: NOTIFICATION_PAGE_SIZE }, (_, index) =>
			notification({ id: `ntf_${index + 1}` }),
		);
		qc.setQueryData<NotificationsCache>(unreadNotificationsQueryKey, {
			pageParams: [""],
			pages: [{ notifications: firstPage, unreadCount: firstPage.length, unresolvedCount: firstPage.length }],
		});

		mergeUnreadNotification(qc, notification({ id: "ntf_live" }));

		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toHaveLength(
			NOTIFICATION_PAGE_SIZE,
		);
		expect(invalidateSpy).toHaveBeenCalledWith({
			queryKey: unreadNotificationsQueryKey,
			exact: true,
			refetchType: "active",
		});
	});

	// Resolution is not acknowledgement: a resolved notification the user has not
	// looked at yet must still show up as unseen, with unresolvedCount updated.
	it("leaves the seen state alone when a notification resolves", () => {
		const qc = queryClient();
		mergeUnreadNotification(qc, notification());
		qc.setQueryData<NotificationsCache>(recentNotificationsQueryKey, {
			pageParams: [""],
			pages: [{ notifications: [notification()], unreadCount: 1, unresolvedCount: 1 }],
		});

		applyResolvedNotification(qc, notification({ resolvedAt: "2026-06-16T11:00:00Z" }));

		const unread = qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey);
		expect(getCachedNotifications(unread)).toEqual([
			expect.objectContaining({ id: "ntf_1", status: "unread", resolvedAt: "2026-06-16T11:00:00Z" }),
		]);
		expect(getCachedUnreadCount(unread)).toBe(1);
		expect(unread?.pages[0]?.unresolvedCount).toBe(0);

		const recent = qc.getQueryData<NotificationsCache>(recentNotificationsQueryKey);
		expect(getCachedNotifications(recent)).toEqual([
			expect.objectContaining({ id: "ntf_1", status: "unread", resolvedAt: "2026-06-16T11:00:00Z" }),
		]);
		expect(recent?.pages[0]?.unresolvedCount).toBe(0);
	});

	it("drops older pages after the panel closes while keeping the latest page", () => {
		const qc = queryClient();
		qc.setQueryData<NotificationsCache>(unreadNotificationsQueryKey, {
			pageParams: ["", "older"],
			pages: [
				{ notifications: [notification({ id: "new" })], nextCursor: "older", unreadCount: 2, unresolvedCount: 2 },
				{ notifications: [notification({ id: "old" })], unreadCount: 2, unresolvedCount: 2 },
			],
		});

		keepLatestNotificationsPage(qc);

		const cache = qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey);
		expect(cache?.pages).toHaveLength(1);
		expect(getCachedNotifications(cache).map((item) => item.id)).toEqual(["new"]);
	});
});

describe("createNotificationsTransport", () => {
	it("opens the notification stream and invalidates unread notifications on open", () => {
		const qc = queryClient();
		const invalidateSpy = vi.spyOn(qc, "invalidateQueries");

		createNotificationsTransport(qc).connect();
		EventSourceStub.instances[0].onopen?.();

		expect(EventSourceStub.instances).toHaveLength(1);
		expect(EventSourceStub.instances[0].url).toBe("http://127.0.0.1:3001/api/v1/notifications/stream");
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: unreadNotificationsQueryKey });
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: recentNotificationsQueryKey });
	});

	it("merges live notifications and shows one toast for a new id", () => {
		const qc = queryClient();
		createNotificationsTransport(qc).connect();
		const source = EventSourceStub.instances[0];

		source.dispatch("notification_created", notification());
		source.dispatch("notification_created", notification());

		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toHaveLength(1);
		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(recentNotificationsQueryKey))).toHaveLength(1);
		expect(showNotificationMock).toHaveBeenCalledTimes(1);
		expect(showNotificationMock).toHaveBeenCalledWith({
			id: "ntf_1",
			title: "checkout-flow needs input",
			body: "The agent is waiting for your response.",
			type: "needs_input",
		});
	});

	it("patches resolvedAt on live unread/all caches when AO closes the issue", () => {
		const qc = queryClient();
		createNotificationsTransport(qc).connect();
		const source = EventSourceStub.instances[0];
		source.dispatch("notification_created", notification());

		source.dispatch("notification_resolved", notification({ resolvedAt: "2026-06-16T11:00:00Z" }));

		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toEqual([
			expect.objectContaining({ id: "ntf_1", status: "unread", resolvedAt: "2026-06-16T11:00:00Z" }),
		]);
		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(recentNotificationsQueryKey))).toEqual([
			expect.objectContaining({ id: "ntf_1", status: "unread", resolvedAt: "2026-06-16T11:00:00Z" }),
		]);
		expect(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey)?.pages[0]?.unresolvedCount).toBe(0);
		expect(qc.getQueryData<NotificationsCache>(recentNotificationsQueryKey)?.pages[0]?.unresolvedCount).toBe(0);
	});

	it("suppresses the needs_input toast for the session the user is already watching", () => {
		setWindowState({ focused: true, visible: true });
		const qc = queryClient();
		createNotificationsTransport(qc, () => "mer-1").connect();

		EventSourceStub.instances[0].dispatch("notification_created", notification());

		expect(getCachedNotifications(qc.getQueryData<NotificationsCache>(unreadNotificationsQueryKey))).toHaveLength(1);
		expect(showNotificationMock).not.toHaveBeenCalled();
	});

	it.each([
		{
			activeSessionId: "other-session",
			focused: true,
			reason: "the notification is for a different session",
			visible: true,
		},
		{ activeSessionId: "mer-1", focused: true, reason: "the window is hidden", visible: false },
		{ activeSessionId: "mer-1", focused: false, reason: "the window is visible but unfocused", visible: true },
		{ activeSessionId: undefined, focused: true, reason: "no session is open", visible: true },
	])("still shows the needs_input toast when $reason", ({ activeSessionId, focused, visible }) => {
		setWindowState({ focused, visible });
		createNotificationsTransport(queryClient(), () => activeSessionId).connect();

		EventSourceStub.instances[0].dispatch("notification_created", notification());

		expect(showNotificationMock).toHaveBeenCalledTimes(1);
	});

	it.each(["ready_to_merge", "pr_merged", "pr_closed_unmerged"] as const)(
		"still shows the %s toast for the focused active session",
		(type) => {
			setWindowState({ focused: true, visible: true });
			createNotificationsTransport(queryClient(), () => "mer-1").connect();

			EventSourceStub.instances[0].dispatch("notification_created", notification({ type }));

			expect(showNotificationMock).toHaveBeenCalledTimes(1);
		},
	);

	it("does not make a new daemon port serve the dead port's backoff delay", () => {
		vi.useFakeTimers();
		vi.spyOn(Math, "random").mockReturnValue(0.5);
		createNotificationsTransport(queryClient()).connect();
		const onBaseUrlChange = subscribeApiBaseUrlMock.mock.calls[0][0] as () => void;

		// Grow the delay on the old port.
		for (let failure = 1; failure <= 4; failure += 1) {
			const source = EventSourceStub.instances.at(-1)!;
			source.readyState = 2;
			source.onerror?.();
			vi.advanceTimersByTime(computeSseRetryDelayMs(failure, () => 0.5));
		}

		getApiBaseUrlMock.mockReturnValue("http://127.0.0.1:4555");
		onBaseUrlChange();

		// The fresh port starts over at the initial delay.
		const moved = EventSourceStub.instances.at(-1)!;
		moved.readyState = 2;
		moved.onerror?.();
		const beforeRetry = EventSourceStub.instances.length;
		vi.advanceTimersByTime(computeSseRetryDelayMs(1, () => 0.5) - 1);
		expect(EventSourceStub.instances).toHaveLength(beforeRetry);
		vi.advanceTimersByTime(1);
		expect(EventSourceStub.instances).toHaveLength(beforeRetry + 1);
		vi.useRealTimers();
	});

	it("reconnects when the API base URL changes", () => {
		createNotificationsTransport(queryClient()).connect();
		const onBaseUrlChange = subscribeApiBaseUrlMock.mock.calls[0][0] as () => void;
		const first = EventSourceStub.instances[0];

		getApiBaseUrlMock.mockReturnValue("http://127.0.0.1:4555");
		onBaseUrlChange();

		expect(first.closed).toBe(true);
		expect(EventSourceStub.instances).toHaveLength(2);
		expect(EventSourceStub.instances[1].url).toBe("http://127.0.0.1:4555/api/v1/notifications/stream");
	});
});
