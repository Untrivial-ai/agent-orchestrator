import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CloudCpNotification } from "../lib/cloud-cp/types";
import { CloudCpError } from "../lib/cloud-cp/errors";
import { isCleared, useCloudNotifications } from "./useCloudNotifications";

const { cloudCpMock } = vi.hoisted(() => ({ cloudCpMock: vi.fn() }));

vi.mock("./useCloudCp", () => ({ useCloudCp: () => cloudCpMock() }));
vi.mock("./useCloudOrg", () => ({ useCloudOrg: () => ({ org: { id: "org-1" } }) }));
vi.mock("./useWorkspaceQuery", () => ({ cloudSessionsQueryKey: ["cloud-sessions"] }));
vi.mock("./useOrchestratorChildren", () => ({ orchestratorChildrenQueryKey: ["orchestrator-children"] }));
vi.mock("../lib/cloud-cp/stream-bridge", () => ({ subscribeNotificationEventsBridged: vi.fn(async () => undefined) }));
vi.mock("../lib/cloud-notification-hints", () => ({ subscribeCloudNotificationHints: () => () => undefined }));

const at = (value: string) => Date.parse(value);
const row = (overrides: Partial<{ id: string; createdAt: string; updatedAt: string; status: "unread" | "read" }> = {}) => ({
	id: "cntf_1",
	createdAt: "2026-07-21T10:00:00Z",
	updatedAt: "2026-07-21T10:00:00Z",
	status: "read" as const,
	...overrides,
});

describe("isCleared", () => {
	it("keeps rows visible when nothing was cleared", () => {
		expect(isCleared(row(), { before: 0, ids: {} })).toBe(false);
	});

	it("hides rows at or before the clear-all cutoff", () => {
		expect(isCleared(row(), { before: at("2026-07-21T10:00:00Z"), ids: {} })).toBe(true);
	});

	it("shows rows created after the clear-all cutoff", () => {
		expect(isCleared(row({ createdAt: "2026-07-21T11:00:00Z" }), { before: at("2026-07-21T10:00:00Z"), ids: {} })).toBe(false);
	});

	it("keeps a cleared row hidden while its mark-read is still in flight", () => {
		const clears = { before: 0, ids: { cntf_1: at("2026-07-21T10:00:00Z") } };
		expect(isCleared(row({ status: "unread" }), clears)).toBe(true);
	});

	it("keeps a cleared row hidden after mark-read bumps its updatedAt", () => {
		const clears = { before: 0, ids: { cntf_1: at("2026-07-21T10:00:00Z") } };
		expect(isCleared(row({ updatedAt: "2026-07-21T10:05:00Z" }), clears)).toBe(true);
	});

	it("shows a cleared row again when the control plane re-raises it", () => {
		const clears = { before: at("2026-07-21T10:00:00Z"), ids: { cntf_1: at("2026-07-21T10:00:00Z") } };
		expect(isCleared(row({ status: "unread", updatedAt: "2026-07-21T12:00:00Z" }), clears)).toBe(false);
	});

	it("keeps a revived row visible after it is read, even under the clear-all cutoff", () => {
		const clears = { before: at("2026-07-21T12:00:00Z"), ids: { cntf_1: null } };
		expect(isCleared(row({ updatedAt: "2026-07-21T12:30:00Z" }), clears)).toBe(false);
	});

	it("uses the per-row marker over the clear-all cutoff", () => {
		const clears = { before: at("2026-07-21T09:00:00Z"), ids: { cntf_1: at("2026-07-21T10:00:00Z") } };
		expect(isCleared(row({ createdAt: "2026-07-21T10:00:00Z" }), clears)).toBe(true);
	});
});

function cloudRow(overrides: Partial<CloudCpNotification> = {}): CloudCpNotification {
	return {
		id: "cntf_1",
		source: "cloud",
		orgId: "org-1",
		type: "ready_to_merge",
		title: "Pull request ready to merge",
		body: "acme/cloud#8 is ready to merge.",
		status: "read",
		createdAt: "2026-07-21T10:00:00Z",
		updatedAt: "2026-07-21T10:00:00Z",
		...overrides,
	};
}

describe("useCloudNotifications clearing", () => {
	let items: CloudCpNotification[];
	const listNotifications = vi.fn();
	const markNotificationsRead = vi.fn();

	function renderCloudNotifications(
		userId = "user-1",
		queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
	) {
		cloudCpMock.mockReturnValue({
			client: { listNotifications, markNotificationsRead },
			ready: true,
			baseUrl: "https://cloud.test",
			userId,
		});
		const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client: queryClient }, children);
		return renderHook(() => useCloudNotifications("all", { live: false }), { wrapper });
	}

	beforeEach(() => {
		window.localStorage.clear();
		items = [cloudRow()];
		listNotifications.mockReset().mockImplementation(async () => ({
			items,
			unreadCount: items.filter((item) => item.status === "unread").length,
			page: { hasMore: false },
			latestSequence: 0,
		}));
		markNotificationsRead.mockReset().mockResolvedValue({ updated: 1 });
	});

	it("hides a read row without asking the control plane to mark it read again", async () => {
		const { result } = renderCloudNotifications();
		await waitFor(() => expect(result.current.items).toHaveLength(1));

		await act(() => result.current.clearOne(items[0]));

		expect(markNotificationsRead).not.toHaveBeenCalled();
		expect(result.current.items).toHaveLength(0);
	});

	it("treats a 404 for an already-read row as cleared", async () => {
		items = [cloudRow({ status: "unread" })];
		markNotificationsRead.mockRejectedValue(new CloudCpError("The notification was not found.", { status: 404 }));
		const { result } = renderCloudNotifications();
		await waitFor(() => expect(result.current.items).toHaveLength(1));

		await act(() => result.current.clearOne(items[0]));

		expect(markNotificationsRead).toHaveBeenCalledWith("org-1", ["cntf_1"]);
		expect(result.current.items).toHaveLength(0);
	});

		it("puts the row back when marking it read fails", async () => {
			items = [cloudRow({ status: "unread" })];
			markNotificationsRead.mockRejectedValue(new CloudCpError("Service unavailable.", { status: 503 }));
			const { result } = renderCloudNotifications();
			await waitFor(() => expect(result.current.items).toHaveLength(1));

		await act(() => expect(result.current.clearOne(items[0])).rejects.toThrow("Service unavailable."));

			expect(result.current.items).toHaveLength(1);
		});

		it("puts all rows back when marking all read fails", async () => {
			items = [cloudRow({ status: "unread" })];
			markNotificationsRead.mockRejectedValue(new CloudCpError("Service unavailable.", { status: 503 }));
			const { result } = renderCloudNotifications();
			await waitFor(() => expect(result.current.items).toHaveLength(1));

			await act(() => expect(result.current.clearAll()).rejects.toThrow("Service unavailable."));

			expect(markNotificationsRead).toHaveBeenCalledWith("org-1");
			expect(result.current.items).toHaveLength(1);
			expect(window.localStorage.getItem("ao.cloudNotifications.cleared:https://cloud.test:org-1:user-1")).toBeNull();
		});

		it("scopes cleared rows to the signed-in cloud user", async () => {
			const first = renderCloudNotifications("user-1");
		await waitFor(() => expect(first.result.current.items).toHaveLength(1));
		await act(() => first.result.current.clearAll());
		expect(first.result.current.items).toHaveLength(0);
		first.unmount();

		const second = renderCloudNotifications("user-2");
			await waitFor(() => expect(second.result.current.items).toHaveLength(1));
		});

		it("does not show another user's cached rows when accounts share an org", async () => {
			const firstRow = cloudRow({ id: "cntf_private_a", title: "Private for A" });
			const secondRow = cloudRow({ id: "cntf_private_b", title: "Private for B" });
			let finishSecondFetch: () => void = () => undefined;
			const secondFetchPending = new Promise<void>((resolve) => { finishSecondFetch = resolve; });
			listNotifications
				.mockResolvedValueOnce({ items: [firstRow], unreadCount: 0, page: { hasMore: false }, latestSequence: 0 })
				.mockImplementationOnce(async () => {
					await secondFetchPending;
					return { items: [secondRow], unreadCount: 0, page: { hasMore: false }, latestSequence: 0 };
				});
			const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
			const first = renderCloudNotifications("user-1", queryClient);
			await waitFor(() => expect(first.result.current.items).toEqual([firstRow]));
			first.unmount();

			const second = renderCloudNotifications("user-2", queryClient);
			expect(second.result.current.items).toEqual([]);
			expect(second.result.current.data).toBeUndefined();
			await act(async () => { finishSecondFetch(); });
			await waitFor(() => expect(second.result.current.items).toEqual([secondRow]));
		});

	it("keeps a re-raised row visible after it is read again", async () => {
		const { result } = renderCloudNotifications();
		await waitFor(() => expect(result.current.items).toHaveLength(1));
		await act(() => result.current.clearAll());
		expect(result.current.items).toHaveLength(0);

		// Re-raised in place: back to unread with a newer updatedAt.
		items = [cloudRow({ status: "unread", updatedAt: "2026-07-21T12:00:00Z" })];
		await act(() => result.current.refetch());
		await waitFor(() => expect(result.current.items).toHaveLength(1));

		// Opening the panel marks it read, which must not hide it again.
		items = [cloudRow({ status: "read", updatedAt: "2026-07-21T12:05:00Z" })];
		await act(() => result.current.refetch());
		await waitFor(() => expect(result.current.items[0]?.status).toBe("read"));
		expect(result.current.items).toHaveLength(1);
	});
});
