import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { NotificationDTO, NotificationsCache } from "../lib/notifications";
import { NotificationRuntime } from "./NotificationCenter";

const notifications: NotificationDTO[] = [
	{
		id: "ntf_1",
		sessionId: "sess-1",
		projectId: "proj-1",
		prUrl: "",
		type: "needs_input",
		title: "Needs input",
		body: "",
		status: "unread",
		createdAt: "2026-06-16T10:00:00Z",
		target: { kind: "session", sessionId: "sess-1" },
	},
	{
		id: "ntf_2",
		sessionId: "sess-2",
		projectId: "proj-1",
		prUrl: "",
		type: "ready_to_merge",
		title: "Ready to merge",
		body: "",
		status: "unread",
		createdAt: "2026-06-16T11:00:00Z",
		target: { kind: "session", sessionId: "sess-2" },
	},
];

const unreadCache: NotificationsCache = {
	pages: [{ notifications, nextCursor: undefined, unreadCount: notifications.length, unresolvedCount: notifications.length }],
	pageParams: [""],
};

const { quotaAlertState, setBadge, showNotification } = vi.hoisted(() => ({
	quotaAlertState: { data: [] as Array<{ id: string; title: string; body?: string }> },
	setBadge: vi.fn(),
	showNotification: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn(), useParams: () => ({}) }));

vi.mock("../hooks/useNotificationsQuery", () => ({
	useNotificationsQuery: () => ({ data: unreadCache, isError: false }),
}));

vi.mock("../hooks/useProviderQuota", () => ({ useQuotaAlerts: () => quotaAlertState }));

vi.mock("../lib/notifications", async (importOriginal) => ({
	...((await importOriginal()) as object),
	createNotificationsTransport: () => ({ connect: () => undefined }),
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		notifications: {
			setBadge,
			show: showNotification,
			devBounce: vi.fn(),
			onClick: () => () => undefined,
		},
	},
}));

function renderRuntimeOnly() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			{/* NotificationCenter (the bell) is intentionally NOT mounted here. */}
			<NotificationRuntime />
		</QueryClientProvider>,
	);
}

describe("NotificationRuntime badge sync", () => {
	it("syncs the launcher badge without the notification bell being mounted", async () => {
		setBadge.mockClear();

		renderRuntimeOnly();

		// The badge must update purely from the always-mounted runtime, independent
		// of NotificationCenter, which Linux hides from the topbar and only mounts
		// on the sessions board.
		await waitFor(() => expect(setBadge).toHaveBeenCalledWith(notifications.length));
	});

	it("delivers newly observed quota transitions through native notifications", async () => {
		showNotification.mockClear();
		quotaAlertState.data = [];
		const view = renderRuntimeOnly();
		quotaAlertState.data = [{ id: "quota_1", title: "Claude usage is critical", body: "Five hour has 9% remaining." }];
		view.rerender(
			<QueryClientProvider client={new QueryClient()}>
				<NotificationRuntime />
			</QueryClientProvider>,
		);

		await waitFor(() => expect(showNotification).toHaveBeenCalledWith({
			id: "quota_1",
			title: "Claude usage is critical",
			body: "Five hour has 9% remaining.",
			type: "quota",
		}));
	});
});
