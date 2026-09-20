import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const {
	applyNotificationDeletedMock,
	applyNotificationsClearedMock,
	applyOptimisticNotificationDeleteMock,
	clearAllNotificationsMock,
	deleteNotificationMock,
	reconcileNotificationsMock,
	rollbackOptimisticNotificationDeleteMock,
} = vi.hoisted(() => ({
	applyNotificationDeletedMock: vi.fn(),
	applyNotificationsClearedMock: vi.fn(),
	applyOptimisticNotificationDeleteMock: vi.fn(),
	clearAllNotificationsMock: vi.fn(),
	deleteNotificationMock: vi.fn(),
	reconcileNotificationsMock: vi.fn(),
	rollbackOptimisticNotificationDeleteMock: vi.fn(),
}));

vi.mock("../lib/notifications", async (importOriginal) => ({
	...((await importOriginal()) as object),
	applyNotificationDeleted: applyNotificationDeletedMock,
	applyNotificationsCleared: applyNotificationsClearedMock,
	applyOptimisticNotificationDelete: applyOptimisticNotificationDeleteMock,
	clearAllNotifications: clearAllNotificationsMock,
	deleteNotification: deleteNotificationMock,
	reconcileNotifications: reconcileNotificationsMock,
	rollbackOptimisticNotificationDelete: rollbackOptimisticNotificationDeleteMock,
}));

import type { NotificationDTO } from "../lib/notifications";
import { useClearAllNotificationsMutation, useClearNotificationMutation } from "./useNotificationsQuery";

const notification: NotificationDTO = {
	id: "ntf_1",
	sessionId: "mer-1",
	projectId: "mer",
	prUrl: "",
	type: "needs_input",
	title: "Needs input",
	body: "Waiting",
	status: "unread",
	createdAt: "2026-06-16T10:00:00Z",
	target: { kind: "session", sessionId: "mer-1" },
};

describe("useClearAllNotificationsMutation", () => {
	beforeEach(() => {
		applyNotificationsClearedMock.mockReset();
		clearAllNotificationsMock.mockReset();
		reconcileNotificationsMock.mockReset().mockResolvedValue(undefined);
	});

	it("cancels stale fetches, applies the generation, then reconciles history", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const cancelSpy = vi.spyOn(queryClient, "cancelQueries");
		const clear = { clearId: "clear-2", clearEpoch: "epoch-1", clearSequence: 2, clearedCount: 3 };
		clearAllNotificationsMock.mockResolvedValue(clear);
		const wrapper = ({ children }: PropsWithChildren) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const { result } = renderHook(() => useClearAllNotificationsMutation(), { wrapper });

		await act(async () => {
			await result.current.mutateAsync();
		});

		expect(cancelSpy).toHaveBeenCalledTimes(2);
		expect(applyNotificationsClearedMock).toHaveBeenCalledWith(queryClient, clear);
		expect(reconcileNotificationsMock).toHaveBeenCalledWith(queryClient);
		expect(cancelSpy.mock.invocationCallOrder[1]).toBeLessThan(
			applyNotificationsClearedMock.mock.invocationCallOrder[0],
		);
		expect(applyNotificationsClearedMock.mock.invocationCallOrder[0]).toBeLessThan(
			reconcileNotificationsMock.mock.invocationCallOrder[0],
		);
	});
});

describe("useClearNotificationMutation", () => {
	beforeEach(() => {
		applyNotificationDeletedMock.mockReset();
		applyOptimisticNotificationDeleteMock.mockReset();
		deleteNotificationMock.mockReset();
		reconcileNotificationsMock.mockReset().mockResolvedValue(undefined);
		rollbackOptimisticNotificationDeleteMock.mockReset();
	});

	function renderMutation(queryClient: QueryClient) {
		const wrapper = ({ children }: PropsWithChildren) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		return renderHook(() => useClearNotificationMutation(), { wrapper });
	}

	it("removes optimistically, confirms after canceling stale fetches, and reconciles", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const cancelSpy = vi.spyOn(queryClient, "cancelQueries");
		deleteNotificationMock.mockResolvedValue(notification);
		const { result } = renderMutation(queryClient);

		await act(async () => {
			await result.current.mutateAsync(notification);
		});

		expect(deleteNotificationMock).toHaveBeenCalledWith(notification);
		expect(applyOptimisticNotificationDeleteMock).toHaveBeenCalledWith(queryClient, notification);
		expect(applyNotificationDeletedMock).toHaveBeenCalledWith(queryClient, notification);
		expect(cancelSpy).toHaveBeenCalledTimes(2);
		expect(reconcileNotificationsMock).toHaveBeenCalledWith(queryClient);
	});

	it("rolls back a failed request before reconciling", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		deleteNotificationMock.mockRejectedValue(new Error("delete failed"));
		const { result } = renderMutation(queryClient);

		await act(async () => {
			await expect(result.current.mutateAsync(notification)).rejects.toThrow("delete failed");
		});

		expect(rollbackOptimisticNotificationDeleteMock).toHaveBeenCalledWith(queryClient, "ntf_1");
		expect(applyNotificationDeletedMock).not.toHaveBeenCalled();
		expect(reconcileNotificationsMock).toHaveBeenCalledWith(queryClient);
	});
});
