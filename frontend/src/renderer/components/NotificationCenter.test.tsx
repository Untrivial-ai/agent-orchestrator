import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../lib/bridge";
import type { NotificationDTO, NotificationListStatus } from "../lib/notifications";
import { useUiStore } from "../stores/ui-store";
import { NotificationCenter, NotificationRuntime } from "./NotificationCenter";
import { TooltipProvider } from "./ui/tooltip";

const {
	connectMock,
	fetchNextPageMock,
	markAllMock,
	navigateMock,
	notificationQueryMock,
	paramsMock,
	restoreSessionMock,
	workspaceQueryMock,
} = vi.hoisted(() => ({
	connectMock: vi.fn(),
	fetchNextPageMock: vi.fn(),
	markAllMock: vi.fn(),
	navigateMock: vi.fn(),
	notificationQueryMock: vi.fn(),
	paramsMock: vi.fn(),
	restoreSessionMock: vi.fn(),
	workspaceQueryMock: vi.fn(),
}));

const allNotifications: NotificationDTO[] = [
	{
		id: "ntf_2",
		sessionId: "sess-2",
		projectId: "proj-1",
		prUrl: "https://github.com/acme/app/pull/67",
		type: "ready_to_merge",
		title: "Fix checkout totals · PR #67",
		body: "PR from session Checkout flow is ready to merge. CI passed with no blocking review feedback.",
		status: "unread",
		createdAt: "2026-07-21T11:00:00Z",
		target: { kind: "pr", sessionId: "sess-2", prUrl: "https://github.com/acme/app/pull/67" },
	},
	{
		id: "ntf_1",
		sessionId: "sess-1",
		projectId: "proj-1",
		prUrl: "",
		type: "needs_input",
		title: "Checkout flow needs input",
		body: "The agent is waiting for your response.",
		status: "unread",
		createdAt: "2026-07-21T10:00:00Z",
		target: { kind: "session", sessionId: "sess-1" },
	},
	{
		id: "ntf_4",
		sessionId: "sess-4",
		projectId: "proj-1",
		prUrl: "",
		type: "needs_input",
		title: "Docs sweep needs input",
		body: "The agent is still waiting for your response.",
		status: "read",
		createdAt: "2026-07-20T09:00:00Z",
		target: { kind: "session", sessionId: "sess-4" },
	},
	{
		id: "ntf_dead",
		sessionId: "sess-dead",
		projectId: "proj-1",
		prUrl: "",
		// needs_input: an agent is genuinely paused waiting on this session, so a
		// terminated backing session should still require restore before opening.
		type: "needs_input",
		title: "PR #9 merged",
		body: "The agent session has ended.",
		status: "read",
		createdAt: "2026-07-19T09:00:00Z",
		target: { kind: "session", sessionId: "sess-dead" },
	},
];

const unreadNotifications = allNotifications.filter((item) => item.status === "unread");

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigateMock, useParams: () => paramsMock() }));

vi.mock("../hooks/useNotificationsQuery", () => ({
	useMarkAllNotificationsReadMutation: () => ({ isPending: false, mutateAsync: markAllMock }),
	useNotificationsQuery: (status: NotificationListStatus, enabled?: boolean) => notificationQueryMock(status, enabled),
}));

vi.mock("../hooks/useRestoreSession", () => ({
	useRestoreSession: () => restoreSessionMock,
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: () => workspaceQueryMock(),
	workspaceQueryKey: ["workspaces"],
}));

vi.mock("../lib/notifications", async (importOriginal) => ({
	...((await importOriginal()) as object),
	createNotificationsTransport: (...args: unknown[]) => {
		connectMock(...args);
		return { connect: () => undefined };
	},
}));

function renderNotificationCenter() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<NotificationCenter />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

async function clickOpen() {
	const trigger = screen.getByRole("button", { name: /unread notifications/ });
	await userEvent.click(trigger);
	await screen.findByRole("dialog", { name: "Notifications" });
	return trigger;
}

function notificationQueryResult(
	status: NotificationListStatus,
	overrides: Partial<{
		hasNextPage: boolean;
		isError: boolean;
		isFetchNextPageError: boolean;
		isFetchingNextPage: boolean;
		isLoading: boolean;
	}> = {},
) {
	const hasNextPage = overrides.hasNextPage ?? false;
	const notifications = status === "unread" ? unreadNotifications : status === "all" ? allNotifications : [];
	return {
		data: {
			pageParams: [""],
			pages: [
				{
					notifications,
					nextCursor: hasNextPage ? "older" : undefined,
					unreadCount: unreadNotifications.length,
					unresolvedCount: 2,
				},
			],
		},
		fetchNextPage: fetchNextPageMock,
		hasNextPage,
		isError: false,
		isFetchNextPageError: false,
		isFetchingNextPage: false,
		isLoading: false,
		...overrides,
	};
}

const stableUnreadQuery = notificationQueryResult("unread");
const stableAllQuery = notificationQueryResult("all");

beforeEach(() => {
	connectMock.mockReset();
	paramsMock.mockReset().mockReturnValue({});
	useUiStore.setState({ visibleTerminalKindBySession: {} });
	fetchNextPageMock.mockReset().mockResolvedValue(undefined);
	markAllMock.mockReset().mockResolvedValue(0);
	navigateMock.mockReset();
	restoreSessionMock.mockReset().mockResolvedValue({ status: "success" });
	workspaceQueryMock.mockReset().mockReturnValue({
		data: [
			{
				id: "proj-1",
				name: "acme/app",
				sessions: [
					{ id: "sess-1", isTerminated: false, status: "needs_input", title: "Checkout flow" },
					{ id: "sess-2", isTerminated: false, status: "ready_to_merge", title: "Checkout flow" },
					{ id: "sess-4", isTerminated: false, status: "needs_input", title: "Docs sweep" },
					{ id: "sess-dead", isTerminated: true, status: "terminated", title: "Old PR" },
				],
			},
		],
		isError: false,
		isPending: false,
		isSuccess: true,
		refetch: vi.fn(),
	});
	notificationQueryMock.mockReset().mockImplementation((status: NotificationListStatus) =>
		status === "unread" ? stableUnreadQuery : status === "all" ? stableAllQuery : notificationQueryResult(status),
	);
	vi.spyOn(window, "open").mockImplementation(() => null);
});

// The runtime tells the transport which session the user is actually watching.
// Being on the session route is not enough: the pane shows one terminal at a
// time, so a shell or reviewer tab hides the agent while the URL is unchanged.
describe("NotificationRuntime", () => {
	function renderRuntime() {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<NotificationRuntime />
			</QueryClientProvider>,
		);
		return connectMock.mock.calls[0][1] as () => string | undefined;
	}

	it("reports the session while its agent terminal is the one on screen", () => {
		paramsMock.mockReturnValue({ sessionId: "sess-1" });
		useUiStore.setState({ visibleTerminalKindBySession: { "sess-1": "worker" } });

		expect(renderRuntime()()).toBe("sess-1");
	});

	it.each(["shell", "reviewer"] as const)("reports nothing while a %s terminal covers the agent", (kind) => {
		paramsMock.mockReturnValue({ sessionId: "sess-1" });
		useUiStore.setState({ visibleTerminalKindBySession: { "sess-1": kind } });

		expect(renderRuntime()()).toBeUndefined();
	});

	it("reports nothing off a session route", () => {
		paramsMock.mockReturnValue({});
		useUiStore.setState({ visibleTerminalKindBySession: { "sess-1": "worker" } });

		expect(renderRuntime()()).toBeUndefined();
	});

	// The transport connects once and outlives navigation, so the getter has to
	// read live state rather than close over the value it was created with.
	it("tracks tab switches without reconnecting the stream", () => {
		paramsMock.mockReturnValue({ sessionId: "sess-1" });
		useUiStore.setState({ visibleTerminalKindBySession: { "sess-1": "worker" } });
		const getVisibleAgentSessionId = renderRuntime();

		useUiStore.setState({ visibleTerminalKindBySession: { "sess-1": "shell" } });
		expect(getVisibleAgentSessionId()).toBeUndefined();

		useUiStore.setState({ visibleTerminalKindBySession: { "sess-1": "worker" } });
		expect(getVisibleAgentSessionId()).toBe("sess-1");
		expect(connectMock).toHaveBeenCalledTimes(1);
	});
});

describe("NotificationCenter", () => {
	it("subscribes to workspace metadata only while the panel is open", async () => {
		renderNotificationCenter();

		expect(workspaceQueryMock).not.toHaveBeenCalled();
		await clickOpen();
		expect(workspaceQueryMock).toHaveBeenCalledTimes(1);
	});

	it("uses the compact topbar bell and unread badge sizing", () => {
		renderNotificationCenter();
		const trigger = screen.getByRole("button", { name: /unread notifications/ });
		const bell = trigger.querySelector("svg");
		const badge = trigger.querySelector("span");

		expect(bell?.classList.contains("size-icon-base")).toBe(true);
		expect(badge?.classList.contains("h-3")).toBe(true);
		expect(badge?.classList.contains("min-w-3")).toBe(true);
		expect(badge?.classList.contains("text-[7px]")).toBe(true);
	});

	it("opens once on click without a hover/focus remount and dismisses outside", async () => {
		renderNotificationCenter();
		const trigger = screen.getByRole("button", { name: /unread notifications/ });
		fireEvent.mouseEnter(trigger);
		fireEvent.focus(trigger);
		expect(screen.queryByRole("dialog", { name: "Notifications" })).not.toBeInTheDocument();

		await clickOpen();

		expect(screen.queryByText(/last 7 days/i)).not.toBeInTheDocument();
		fireEvent.pointerDown(document.body);
		await waitFor(() => expect(screen.queryByRole("dialog", { name: "Notifications" })).not.toBeInTheDocument());
	});

	it("supports tab navigation inside the panel and restores focus to the bell", async () => {
		renderNotificationCenter();
		const trigger = screen.getByRole("button", { name: /unread notifications/ });
		trigger.focus();
		await userEvent.keyboard("{Enter}");

		const panel = await screen.findByRole("dialog", { name: "Notifications" });
		expect(panel).toContainElement(document.activeElement as HTMLElement | null);
		await userEvent.keyboard("{Escape}");
		await waitFor(() => expect(trigger).toHaveFocus());
	});

	it("shows one paginated list of all notifications without Unread/All tabs", async () => {
		renderNotificationCenter();
		await clickOpen();

		const panel = within(screen.getByRole("dialog", { name: "Notifications" }));
		expect(panel.getByRole("list")).toHaveClass("board-scrollbar", "overflow-y-auto");
		expect(panel.queryByRole("tab", { name: "Unread" })).not.toBeInTheDocument();
		expect(panel.queryByRole("tab", { name: "All" })).not.toBeInTheDocument();
		expect(panel.queryByText("Unseen")).not.toBeInTheDocument();
		expect(panel.queryByText("Unresolved")).not.toBeInTheDocument();

		const rows = panel.getAllByRole("listitem");
		expect(rows.map((row) => row.textContent)).toEqual([
			expect.stringContaining("Fix checkout totals · PR #67"),
			expect.stringContaining("Checkout flow needs input"),
			expect.stringContaining("Docs sweep needs input"),
			expect.stringContaining("PR #9 merged"),
		]);
	});

	// Opening acknowledges loaded unread ids only, so later unread pages stay
	// fetchable and highlighted when they appear in the all-list.
	it("acknowledges loaded unread ids on open and keeps showing them highlighted", async () => {
		renderNotificationCenter();
		await clickOpen();

		expect(markAllMock.mock.calls[0][0]).toEqual(expect.arrayContaining(["ntf_1", "ntf_2"]));
		expect(markAllMock.mock.calls[0][0]).toHaveLength(2);
		expect(screen.queryByRole("button", { name: "Mark all notifications read" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Mark notification read" })).not.toBeInTheDocument();
		expect(screen.getByText("Checkout flow needs input")).toBeInTheDocument();
		expect(screen.getByText("Checkout flow needs input").className).toContain("font-medium");
		expect(screen.getByLabelText("Fix checkout totals · PR #67").className).toContain("font-medium");
		expect(screen.getByText("Docs sweep needs input").className).not.toContain("font-medium");
	});

	it("retries mark-read after a transient failure without closing the panel", async () => {
		markAllMock.mockRejectedValueOnce(new Error("network down")).mockResolvedValueOnce(2);
		renderNotificationCenter();
		await clickOpen();

		expect(await screen.findByText("network down")).toBeInTheDocument();
		expect(markAllMock).toHaveBeenCalledTimes(1);

		await userEvent.click(screen.getByRole("button", { name: "Retry" }));

		await waitFor(() => expect(markAllMock).toHaveBeenCalledTimes(2));
		expect(markAllMock.mock.calls[1][0]).toEqual(expect.arrayContaining(["ntf_1", "ntf_2"]));
		expect(markAllMock.mock.calls[1][0]).toHaveLength(2);
		await waitFor(() => expect(screen.queryByText("network down")).not.toBeInTheDocument());
		expect(screen.getByText("Checkout flow needs input").className).toContain("font-medium");
	});

	it("keeps later unread pages highlighted when they load after open", async () => {
		const laterUnread: NotificationDTO = {
			id: "ntf_later",
			sessionId: "sess-1",
			projectId: "proj-1",
			prUrl: "",
			type: "needs_input",
			title: "Later unread needs input",
			body: "Still waiting after the first page.",
			status: "unread",
			createdAt: "2026-07-18T09:00:00Z",
			target: { kind: "session", sessionId: "sess-1" },
		};
		const allState = {
			pages: [
				{
					notifications: allNotifications,
					nextCursor: "older" as string | undefined,
					unreadCount: 3,
					unresolvedCount: 2,
				},
			],
		};
		fetchNextPageMock.mockImplementation(async () => {
			allState.pages = [
				{
					notifications: allNotifications,
					nextCursor: "older",
					unreadCount: 3,
					unresolvedCount: 2,
				},
				{
					notifications: [laterUnread],
					nextCursor: undefined,
					unreadCount: 3,
					unresolvedCount: 2,
				},
			];
		});
		// Unread cache only has the first page; unreadCount still includes the
		// later row that will arrive through the all list.
		const unreadPage = {
			notifications: unreadNotifications,
			nextCursor: "older-unread",
			unreadCount: 3,
			unresolvedCount: 2,
		};
		const unreadQuery = {
			...notificationQueryResult("unread", { hasNextPage: true }),
			data: {
				pageParams: [""],
				pages: [unreadPage],
			},
		};
		// Mirror mutation onSuccess: decrement the badge from updatedCount even
		// when the acknowledged id is absent from the unread cache.
		markAllMock.mockImplementation(async (ids: string[]) => {
			unreadPage.unreadCount = Math.max(0, unreadPage.unreadCount - ids.length);
			unreadPage.notifications = unreadPage.notifications.map((item) =>
				ids.includes(item.id) && item.status === "unread" ? { ...item, status: "read" as const } : item,
			);
			return ids.length;
		});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

		notificationQueryMock.mockImplementation((status: NotificationListStatus) => {
			if (status === "unread") return unreadQuery;
			return {
				data: {
					pageParams: allState.pages.map((_, index) => (index === 0 ? "" : "older")),
					pages: allState.pages,
				},
				fetchNextPage: fetchNextPageMock,
				hasNextPage: allState.pages.length < 2,
				isError: false,
				isFetchNextPageError: false,
				isFetchingNextPage: false,
				isLoading: false,
			};
		});

		const view = render(
			<QueryClientProvider client={queryClient}>
				<TooltipProvider>
					<NotificationCenter />
				</TooltipProvider>
			</QueryClientProvider>,
		);
		await clickOpen();
		expect(markAllMock.mock.calls[0][0]).toEqual(expect.arrayContaining(["ntf_1", "ntf_2"]));
		expect(markAllMock.mock.calls[0][0]).toHaveLength(2);
		await waitFor(() => expect(screen.getByRole("button", { name: "1 unread notifications" })).toBeInTheDocument());
		markAllMock.mockClear();

		const list = screen.getByRole("list");
		Object.defineProperties(list, {
			clientHeight: { configurable: true, value: 420 },
			scrollHeight: { configurable: true, value: 600 },
			scrollTop: { configurable: true, value: 130 },
		});
		fireEvent.scroll(list);
		await waitFor(() => expect(fetchNextPageMock).toHaveBeenCalled());

		view.rerender(
			<QueryClientProvider client={queryClient}>
				<TooltipProvider>
					<NotificationCenter />
				</TooltipProvider>
			</QueryClientProvider>,
		);

		expect(await screen.findByText("Later unread needs input")).toHaveClass("font-medium");
		await waitFor(() => expect(markAllMock).toHaveBeenCalledWith(["ntf_later"]));
		await waitFor(() => expect(screen.getByRole("button", { name: "Notifications" })).toBeInTheDocument());
		expect(screen.queryByRole("button", { name: /unread notifications/ })).not.toBeInTheDocument();
	});

	it("surfaces a failed all-list load instead of claiming success", async () => {
		notificationQueryMock.mockImplementation((status: NotificationListStatus) =>
			status === "all"
				? { ...notificationQueryResult(status, { isError: true }), data: undefined }
				: notificationQueryResult(status),
		);
		renderNotificationCenter();
		await userEvent.click(screen.getByRole("button", { name: /notifications/i }));

		expect(await screen.findByText("Could not load notifications.")).toBeInTheDocument();
		expect(screen.queryByText("No notifications yet.")).not.toBeInTheDocument();
	});

	it("shows the empty state when the all list loaded with nothing", async () => {
		notificationQueryMock.mockImplementation((status: NotificationListStatus) => ({
			...notificationQueryResult(status),
			data: { pageParams: [""], pages: [{ notifications: [], unreadCount: 0, unresolvedCount: 0 }] },
		}));
		renderNotificationCenter();
		await userEvent.click(screen.getByRole("button", { name: /notifications/i }));

		expect(await screen.findByText("No notifications yet.")).toBeInTheDocument();
	});

	it("navigates to the session from anywhere on the row, including the body text", async () => {
		renderNotificationCenter();
		await clickOpen();

		await userEvent.click(screen.getByText("The agent is waiting for your response."));
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "sess-1" },
		});
	});

	it("opens the session from the non-link portion of a PR row title", async () => {
		renderNotificationCenter();
		await clickOpen();

		await userEvent.click(screen.getByLabelText("Fix checkout totals · PR #67"));
		expect(window.open).not.toHaveBeenCalled();
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "sess-2" },
		});
	});

	it("opens the linked PR number in the system browser without opening the session", async () => {
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderNotificationCenter();
		await clickOpen();

		await userEvent.click(screen.getByRole("link", { name: "Open PR #67" }));

		expect(openExternal).toHaveBeenCalledWith("https://github.com/acme/app/pull/67");
		expect(navigateMock).not.toHaveBeenCalled();
		openExternal.mockRestore();
	});

	it("shows the PR title prominently and does not repeat the session name in metadata", async () => {
		renderNotificationCenter();
		await clickOpen();

		const title = screen.getByLabelText("Fix checkout totals · PR #67");
		const row = title.closest('[role="listitem"]');
		expect(row).not.toBeNull();
		expect(row).toHaveTextContent("acme/app");
		expect(row?.textContent?.match(/Checkout flow/g)).toHaveLength(1);
	});

	it("normalizes legacy ready notifications without rewriting stored history", async () => {
		const legacyReady = {
			...allNotifications[0],
			title: "Checkout flow is ready to merge",
			body: "CI passed with no blocking review feedback.",
		};
		notificationQueryMock.mockImplementation((status: NotificationListStatus) => ({
			...notificationQueryResult(status),
			data: {
				pageParams: [""],
				pages: [
					{
						notifications: [legacyReady],
						unreadCount: 1,
						unresolvedCount: 1,
					},
				],
			},
		}));
		renderNotificationCenter();
		await clickOpen();

		expect(screen.getByLabelText("PR #67 is ready to merge")).toBeInTheDocument();
		expect(
			screen.getByText(
				"PR from session Checkout flow is ready to merge. CI passed with no blocking review feedback.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Checkout flow is ready to merge")).not.toBeInTheDocument();
	});

	it("opens the session with the keyboard from a focused row", async () => {
		renderNotificationCenter();
		await clickOpen();

		const row = within(screen.getByRole("dialog", { name: "Notifications" })).getAllByRole("button", {
			name: /Checkout flow needs input/,
		})[0];
		row.focus();
		await userEvent.keyboard("{Enter}");

		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "sess-1" },
		});
	});

	it("does not open terminated sessions from the row and restores them instead", async () => {
		renderNotificationCenter();
		await clickOpen();

		await userEvent.click(screen.getByText("The agent session has ended."));
		expect(navigateMock).not.toHaveBeenCalled();
		expect(restoreSessionMock).not.toHaveBeenCalled();

		await userEvent.click(screen.getByRole("button", { name: "Restore session" }));
		await waitFor(() => expect(restoreSessionMock).toHaveBeenCalledWith("sess-dead"));
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "sess-dead" },
		});
	});

	// A PR outcome describes work that already finished — there's no paused
	// agent to resume, so a terminated session behind one should stay directly
	// viewable instead of gated behind restore.
	it("opens a terminated session directly for a PR-outcome notification, with no restore option", async () => {
		notificationQueryMock.mockImplementation((status: NotificationListStatus) =>
			status === "all"
				? {
						...notificationQueryResult(status),
						data: {
							pageParams: [""],
							pages: [
								{
									notifications: [
										{
											id: "ntf_dead_pr",
											sessionId: "sess-dead",
											projectId: "proj-1",
											prUrl: "https://github.com/acme/app/pull/9",
											type: "pr_merged",
											title: "PR #9 merged",
											body: "The agent session has ended.",
											status: "read",
											createdAt: "2026-07-19T09:00:00Z",
											target: { kind: "session", sessionId: "sess-dead" },
										},
									],
									unreadCount: 0,
									unresolvedCount: 0,
								},
							],
						},
					}
				: notificationQueryResult(status),
		);
		renderNotificationCenter();
		await clickOpen();

		expect(screen.queryByRole("button", { name: "Restore session" })).not.toBeInTheDocument();

		await userEvent.click(screen.getByText("The agent session has ended."));
		expect(restoreSessionMock).not.toHaveBeenCalled();
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "sess-dead" },
		});
	});

	it("does not open sessions while workspace facts are still loading", async () => {
		workspaceQueryMock.mockReturnValue({
			data: undefined,
			isError: false,
			isPending: true,
			isSuccess: false,
			refetch: vi.fn(),
		});
		renderNotificationCenter();
		await clickOpen();

		await userEvent.click(screen.getByText("The agent is waiting for your response."));
		expect(navigateMock).not.toHaveBeenCalled();
		expect(screen.queryByRole("button", { name: "Restore session" })).not.toBeInTheDocument();
	});

	it("does not treat terminated sessions as live when the workspace query fails", async () => {
		const refetch = vi.fn();
		workspaceQueryMock.mockReturnValue({
			data: undefined,
			isError: true,
			isPending: false,
			isSuccess: false,
			refetch,
		});
		renderNotificationCenter();
		await clickOpen();

		expect(screen.getByText("Could not load sessions. Restore and open may be unavailable.")).toBeInTheDocument();

		await userEvent.click(screen.getByText("The agent is waiting for your response."));
		expect(navigateMock).not.toHaveBeenCalled();
		await userEvent.click(screen.getByText("The agent session has ended."));
		expect(navigateMock).not.toHaveBeenCalled();
		expect(screen.queryByRole("button", { name: "Restore session" })).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(refetch).toHaveBeenCalledTimes(1);
	});

	it("loads earlier history near the end of the scroll viewport", async () => {
		notificationQueryMock.mockImplementation((status: NotificationListStatus) =>
			notificationQueryResult(status, { hasNextPage: true }),
		);
		renderNotificationCenter();
		await clickOpen();

		const list = screen.getByRole("list");
		Object.defineProperties(list, {
			clientHeight: { configurable: true, value: 420 },
			scrollHeight: { configurable: true, value: 600 },
			scrollTop: { configurable: true, value: 130 },
		});
		fireEvent.scroll(list);

		expect(fetchNextPageMock).toHaveBeenCalledTimes(1);
	});

	it("offers a retry when loading earlier notifications fails", async () => {
		notificationQueryMock.mockImplementation((status: NotificationListStatus) =>
			notificationQueryResult(status, {
				hasNextPage: true,
				isError: true,
				isFetchNextPageError: true,
			}),
		);
		renderNotificationCenter();
		await clickOpen();

		expect(screen.getByText("Couldn’t load earlier notifications.")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(fetchNextPageMock).toHaveBeenCalledTimes(1);
	});

	it("shows the full notification body instead of clamping it", async () => {
		renderNotificationCenter();
		await clickOpen();

		expect(screen.getByText("The agent is waiting for your response.")).not.toHaveClass("line-clamp-2");
	});
});
