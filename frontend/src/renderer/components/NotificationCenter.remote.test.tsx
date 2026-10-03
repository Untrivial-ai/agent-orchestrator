import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { aoBridge } from "../lib/bridge";
import type { NotificationDTO, NotificationListStatus, NotificationsPage } from "../lib/notifications";
import { NotificationCenter, NotificationRuntime } from "./NotificationCenter";
import { TooltipProvider } from "./ui/tooltip";

const mocks = vi.hoisted(() => ({
	clearLocal: vi.fn(),
	clearAllLocal: vi.fn(),
	clearRemote: vi.fn(),
	markLocal: vi.fn(),
	markRemote: vi.fn(),
	navigate: vi.fn(),
	clientForHost: vi.fn(),
	fetchOlder: vi.fn(),
}));

function notification(title: string): NotificationDTO {
	return {
		id: "same-notification",
		sessionId: "same-session",
		projectId: "same-project",
		prUrl: "",
		type: "needs_input",
		title,
		body: `${title} body`,
		status: "unread",
		createdAt: "2026-09-30T10:00:00Z",
		target: { kind: "session", sessionId: "same-session" },
	};
}

const local = notification("Local ping");
const hostA = notification("A ping");
const hostB = notification("B ping");
let remoteA: NotificationDTO[] = [hostA];
let remoteB: NotificationDTO[] = [hostB];
let unreadOnlyA: NotificationDTO[] = [];
let nextCursorA: string | undefined;
let remoteSessionsReady = true;
let remoteWorkspaceFailed = false;
let remoteTerminatedA = false;
let remoteErrorB = false;
const page = (item: NotificationDTO): NotificationsPage => ({
	notifications: [item], unreadCount: 1, unresolvedCount: 1,
});
const remotePage = (items: NotificationDTO[]): NotificationsPage => ({
	notifications: items, unreadCount: items.length, unresolvedCount: items.length,
});
const query = (item: NotificationDTO) => ({
	data: { pageParams: [""], pages: [page(item)] },
	fetchNextPage: vi.fn(),
	hasNextPage: false,
	isError: false,
	isFetchNextPageError: false,
	isFetchingNextPage: false,
	isLoading: false,
});

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => mocks.navigate, useParams: () => ({}) }));
vi.mock("../hooks/useNotificationsQuery", () => ({
	useNotificationsQuery: (_status: NotificationListStatus) => query(local),
	useMarkAllNotificationsReadMutation: () => ({ isPending: false, mutateAsync: mocks.markLocal }),
	useClearNotificationMutation: () => ({ isPending: false, mutateAsync: mocks.clearLocal }),
	useClearAllNotificationsMutation: () => ({ isPending: false, mutateAsync: mocks.clearAllLocal }),
}));
vi.mock("../hooks/useRemoteNotifications", async (importOriginal) => ({
	...(await importOriginal<typeof import("../hooks/useRemoteNotifications")>()),
	connectRemoteNotificationStreams: () => () => undefined,
	remoteNotificationsQueryKey: (hostId: string, status: string) => ["remote-notifications", hostId, status],
	fetchRemoteNotificationsPage: (...args: unknown[]) => mocks.fetchOlder(...args),
	useRemoteNotificationHosts: (status: NotificationListStatus) => ({
		hosts: [
			{ hostId: "host-a", label: "Host A", data: { ...remotePage(status === "unread" ? [...remoteA, ...unreadOnlyA] : remoteA), nextCursor: status === "all" ? nextCursorA : undefined }, isError: false, isLoading: false },
			{ hostId: "host-b", label: "Host B", data: remoteErrorB ? undefined : remotePage(remoteB), isError: remoteErrorB, isLoading: false },
		],
		totalUnreadCount: status === "unread" ? remoteA.length + unreadOnlyA.length + remoteB.length : 0,
	}),
}));
vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: () => ({
		data: [{ id: "same-project", name: "Local project", sessions: [{ id: "same-session", title: "Local session", isTerminated: false }] }],
		isError: false, isSuccess: true, refetch: vi.fn(),
	}),
	useRemoteWorkspaces: () => ({
		data: [
			{ hostId: "host-a", id: "same-project", name: "Project A", sessions: [{ hostId: "host-a", id: "same-session", title: "Session A", isTerminated: remoteTerminatedA }] },
			{ hostId: "host-b", id: "same-project", name: "Project B", sessions: [{ hostId: "host-b", id: "same-session", title: "Session B", isTerminated: false }] },
		],
		failedHostIds: remoteWorkspaceFailed ? ["host-a"] : [], loadedProjectHostIds: ["host-a", "host-b"],
		loadedSessionHostIds: remoteSessionsReady ? ["host-a", "host-b"] : ["host-b"], refetch: vi.fn(),
	}),
	workspaceQueryKey: ["workspaces"],
}));
vi.mock("../hooks/useCloudNotifications", () => ({
	useCloudNotifications: () => ({ data: { items: [], unreadCount: 0 }, isLoading: false, markAllRead: vi.fn() }),
}));
vi.mock("../hooks/useRestoreSession", () => ({ useRestoreSession: () => vi.fn() }));
vi.mock("../lib/host-clients", () => ({ clientForHost: (hostId: string) => mocks.clientForHost(hostId) }));

beforeEach(() => {
	for (const mock of Object.values(mocks)) mock.mockReset();
	remoteA = [hostA];
	remoteB = [hostB];
	unreadOnlyA = [];
	nextCursorA = undefined;
	remoteSessionsReady = true;
	remoteWorkspaceFailed = false;
	remoteTerminatedA = false;
	remoteErrorB = false;
	mocks.markLocal.mockResolvedValue(1);
	mocks.markRemote.mockResolvedValue({ data: { updatedCount: 1 } });
	mocks.clearRemote.mockResolvedValue({ data: { notification: hostB }, response: { status: 200 } });
	mocks.clearLocal.mockResolvedValue(local);
	mocks.clearAllLocal.mockResolvedValue({ clearedCount: 1 });
	mocks.clientForHost.mockImplementation((hostId: string) => ({
		POST: (...args: unknown[]) => mocks.markRemote(hostId, ...args),
		DELETE: (...args: unknown[]) => mocks.clearRemote(hostId, ...args),
	}));
});

afterEach(() => vi.restoreAllMocks());

it("combines unread counts while keeping equal notification and session IDs tied to their hosts", async () => {
	const user = userEvent.setup();
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider><NotificationCenter /></TooltipProvider>
		</QueryClientProvider>,
	);

	await user.click(screen.getByRole("button", { name: "3 unread notifications" }));
	const panel = within(await screen.findByRole("dialog", { name: "Notifications" }));
	expect(panel.getAllByRole("listitem")).toHaveLength(3);
	expect(panel.getByText("Local ping")).toBeInTheDocument();
	expect(panel.getByText("A ping")).toBeInTheDocument();
	expect(panel.getByText("B ping")).toBeInTheDocument();

	await user.click(panel.getByText("B ping body"));
	expect(mocks.navigate).toHaveBeenCalledWith({
		to: "/host/$hostId/project/$projectId/session/$sessionId",
		params: { hostId: "host-b", projectId: "same-project", sessionId: "same-session" },
	});

	await user.click(screen.getByRole("button", { name: "3 unread notifications" }));
	await user.click(screen.getByRole("button", { name: "Clear notification: Host B: B ping" }));
	await waitFor(() => expect(mocks.clearRemote).toHaveBeenCalledTimes(1));
	expect(mocks.clearRemote).toHaveBeenCalledWith("host-b", "/api/v1/notifications/{id}", {
		params: { path: { id: "same-notification" } },
	});
	expect(mocks.clearLocal).not.toHaveBeenCalled();
	expect(mocks.navigate).toHaveBeenCalledTimes(1);
});

it("acknowledges each remote inbox on its own host when opened", async () => {
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider><NotificationCenter /></TooltipProvider>
		</QueryClientProvider>,
	);
	await userEvent.click(screen.getByRole("button", { name: "3 unread notifications" }));

	await waitFor(() => expect(mocks.markRemote).toHaveBeenCalledTimes(2));
	expect(mocks.markRemote).toHaveBeenCalledWith("host-a", "/api/v1/notifications/read-all", {
		body: { ids: ["same-notification"] },
	});
	expect(mocks.markRemote).toHaveBeenCalledWith("host-b", "/api/v1/notifications/read-all", {
		body: { ids: ["same-notification"] },
	});
	expect(mocks.markLocal).toHaveBeenCalledTimes(1);
	expect(mocks.markLocal).toHaveBeenCalledWith(["same-notification"]);
});

it("shows and acknowledges unread remote rows beyond the first history page", async () => {
	remoteA = [];
	remoteB = [];
	unreadOnlyA = [{ ...notification("Older unread A"), id: "older-unread" }];
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider><NotificationCenter /></TooltipProvider>
		</QueryClientProvider>,
	);
	await userEvent.click(screen.getByRole("button", { name: "2 unread notifications" }));
	expect(await screen.findByText("Older unread A")).toBeInTheDocument();
	await waitFor(() => expect(mocks.markRemote).toHaveBeenCalledWith("host-a", "/api/v1/notifications/read-all", {
		body: { ids: ["older-unread"] },
	}));
});

it("clears each connected host when Clear all is clicked", async () => {
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider><NotificationCenter /></TooltipProvider>
		</QueryClientProvider>,
	);
	await userEvent.click(screen.getByRole("button", { name: "3 unread notifications" }));
	await userEvent.click(screen.getByRole("button", { name: "Clear all" }));
	await waitFor(() => expect(mocks.clearRemote).toHaveBeenCalledTimes(2));
	expect(mocks.clearAllLocal).toHaveBeenCalledTimes(1);
	expect(mocks.clearRemote).toHaveBeenCalledWith("host-a", "/api/v1/notifications");
	expect(mocks.clearRemote).toHaveBeenCalledWith("host-b", "/api/v1/notifications");
});

it("waits for Retry after a remote read failure instead of repeatedly sending it", async () => {
	mocks.markRemote.mockImplementation(async (hostId: string) => {
		if (hostId === "host-b") throw new Error("offline");
		return { data: { updatedCount: 1 } };
	});
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider><NotificationCenter /></TooltipProvider>
		</QueryClientProvider>,
	);
	await userEvent.click(screen.getByRole("button", { name: "3 unread notifications" }));
	await screen.findByText("Host B: offline");
	expect(mocks.markRemote).toHaveBeenCalledTimes(2);
	await userEvent.click(screen.getByRole("button", { name: "Retry" }));
	await waitFor(() => expect(mocks.markRemote).toHaveBeenCalledTimes(3));
});

it("warns when one remote inbox fails without hiding healthy rows", async () => {
	remoteErrorB = true;
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const invalidate = vi.spyOn(client, "invalidateQueries");
	render(<QueryClientProvider client={client}><TooltipProvider><NotificationCenter /></TooltipProvider></QueryClientProvider>);
	await userEvent.click(screen.getByRole("button", { name: "3 unread notifications" }));
	expect(screen.getByText("A ping")).toBeInTheDocument();
	expect(screen.getByText("Could not load notifications.")).toBeInTheDocument();
	await userEvent.click(screen.getByRole("button", { name: "Retry" }));
	expect(invalidate).toHaveBeenCalledWith({ queryKey: ["remote-notifications", "host-b", "all"] });
});

it("loads and acknowledges older notifications from the correct host", async () => {
	nextCursorA = "older-a";
	const older = { ...notification("Older A ping"), id: "older-notification", createdAt: "2026-09-29T10:00:00Z" };
	mocks.fetchOlder.mockResolvedValue(remotePage([older]));
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider><NotificationCenter /></TooltipProvider>
		</QueryClientProvider>,
	);
	await userEvent.click(screen.getByRole("button", { name: "3 unread notifications" }));
	await userEvent.click(screen.getByRole("button", { name: "Load earlier from Host A" }));
	await screen.findByText("Older A ping");
	expect(mocks.fetchOlder).toHaveBeenCalledWith("host-a", "all", "older-a");
	await waitFor(() => expect(mocks.markRemote).toHaveBeenCalledWith("host-a", "/api/v1/notifications/read-all", {
		body: { ids: ["older-notification"] },
	}));
	expect(screen.queryByRole("button", { name: "Load earlier from Host A" })).not.toBeInTheDocument();
	await userEvent.click(screen.getByRole("button", { name: "Clear notification: Host A: Older A ping" }));
	await waitFor(() => expect(screen.queryByText("Older A ping")).not.toBeInTheDocument());
});

it("drops older pages when polling moves the first-page cursor", async () => {
	nextCursorA = "older-a";
	const older = { ...notification("Older A ping"), id: "older-notification" };
	mocks.fetchOlder.mockResolvedValue(remotePage([older]));
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const panel = () => <QueryClientProvider client={queryClient}><TooltipProvider><NotificationCenter /></TooltipProvider></QueryClientProvider>;
	const view = render(panel());
	await userEvent.click(screen.getByRole("button", { name: "3 unread notifications" }));
	await userEvent.click(screen.getByRole("button", { name: "Load earlier from Host A" }));
	await screen.findByText("Older A ping");
	remoteA = [{ ...notification("New A ping"), id: "new-a" }, hostA];
	nextCursorA = "new-boundary";
	view.rerender(panel());
	await waitFor(() => expect(screen.queryByText("Older A ping")).not.toBeInTheDocument());
	await userEvent.click(screen.getByRole("button", { name: "Load earlier from Host A" }));
	expect(mocks.fetchOlder).toHaveBeenLastCalledWith("host-a", "all", "new-boundary");
});

it("ignores an older-page response after Clear all", async () => {
	nextCursorA = "older-a";
	let resolveOlder!: (page: NotificationsPage) => void;
	mocks.fetchOlder.mockImplementation(() => new Promise<NotificationsPage>((resolve) => { resolveOlder = resolve; }));
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider><NotificationCenter /></TooltipProvider>
		</QueryClientProvider>,
	);
	await userEvent.click(screen.getByRole("button", { name: "3 unread notifications" }));
	await userEvent.click(screen.getByRole("button", { name: "Load earlier from Host A" }));
	await userEvent.click(screen.getByRole("button", { name: "Clear all" }));
	await act(async () => resolveOlder(remotePage([{ ...notification("Cleared older A"), id: "older-notification" }])));
	expect(screen.queryByText("Cleared older A")).not.toBeInTheDocument();
});

it("does not open or restore remote sessions until their status is trustworthy", async () => {
	remoteSessionsReady = false;
	remoteWorkspaceFailed = true;
	remoteTerminatedA = true;
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider><NotificationCenter /></TooltipProvider>
		</QueryClientProvider>,
	);
	await userEvent.click(screen.getByRole("button", { name: "3 unread notifications" }));
	await userEvent.click(screen.getByText("A ping body"));
	expect(mocks.navigate).not.toHaveBeenCalled();
	expect(screen.queryByRole("button", { name: "Restore session" })).not.toBeInTheDocument();
});

it("shows only new remote OS notifications and opens their host-qualified session on click", async () => {
	const shown = vi.spyOn(aoBridge.notifications, "show").mockResolvedValue(undefined);
	let click: ((id: string) => void) | undefined;
	vi.spyOn(aoBridge.notifications, "onClick").mockImplementation((handler) => {
		click = handler;
		return () => undefined;
	});
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const runtime = () => <QueryClientProvider client={queryClient}><NotificationRuntime /></QueryClientProvider>;
	const view = render(runtime());
	expect(shown).not.toHaveBeenCalled();

	const newest: NotificationDTO = {
		...notification("New B ping"),
		id: "new-notification",
		sessionId: "new-session",
		target: { kind: "session", sessionId: "new-session" },
	};
	remoteB = [newest];
	queryClient.setQueryData(["remote-notifications", "host-b", "unread"], remotePage(remoteB));
	view.rerender(runtime());
	await waitFor(() => expect(shown).toHaveBeenCalledTimes(1));
	expect(shown).toHaveBeenCalledWith(expect.objectContaining({
		id: "remote-notification:host-b:new-notification",
		title: "Host B: New B ping",
	}));
	remoteB = [hostB];
	view.rerender(runtime());
	expect(shown).toHaveBeenCalledTimes(1);
	queryClient.setQueryData(["remote-notifications", "host-b", "unread"], remotePage([]));
	await act(async () => click?.("remote-notification:host-b:new-notification"));
	expect(mocks.navigate).toHaveBeenCalledWith({
		to: "/host/$hostId/project/$projectId/session/$sessionId",
		params: { hostId: "host-b", projectId: "same-project", sessionId: "new-session" },
	});
});
