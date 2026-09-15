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
	rollbackOptimisticNotificationDeleteMock,
} = vi.hoisted(() => ({
	applyNotificationDeletedMock: vi.fn(),
	applyNotificationsClearedMock: vi.fn(),
	applyOptimisticNotificationDeleteMock: vi.fn(),
	clearAllNotificationsMock: vi.fn(),
	deleteNotificationMock: vi.fn(),
	rollbackOptimisticNotificationDeleteMock: vi.fn(),
}));

vi.mock("../lib/notifications", async (importOriginal) => ({
	...((await importOriginal()) as object),
	applyNotificationDeleted: applyNotificationDeletedMock,
	applyNotificationsCleared: applyNotificationsClearedMock,
	applyOptimisticNotificationDelete: applyOptimisticNotificationDeleteMock,
	clearAllNotifications: clearAllNotificationsMock,
	deleteNotification: deleteNotificationMock,
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
	});

	it("cancels stale fetches, applies the generation, then reconciles history", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const cancelSpy = vi.spyOn(queryClient, "cancelQueries");
		const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
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
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["notifications", "history"] });
		expect(cancelSpy.mock.invocationCallOrder[1]).toBeLessThan(
			applyNotificationsClearedMock.mock.invocationCallOrder[0],
		);
		expect(applyNotificationsClearedMock.mock.invocationCallOrder[0]).toBeLessThan(
			invalidateSpy.mock.invocationCallOrder[0],
		);
	});
});

describe("useClearNotificationMutation", () => {
	beforeEach(() => {
		applyNotificationDeletedMock.mockReset();
		applyOptimisticNotificationDeleteMock.mockReset();
		deleteNotificationMock.mockReset();
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
		const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
		deleteNotificationMock.mockResolvedValue(notification);
		const { result } = renderMutation(queryClient);

		await act(async () => {
			await result.current.mutateAsync(notification);
		});

		expect(deleteNotificationMock).toHaveBeenCalledWith("ntf_1");
		expect(applyOptimisticNotificationDeleteMock).toHaveBeenCalledWith(queryClient, notification);
		expect(applyNotificationDeletedMock).toHaveBeenCalledWith(queryClient, notification);
		expect(cancelSpy).toHaveBeenCalledTimes(2);
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["notifications", "history"] });
	});

	it("rolls back a failed request before reconciling", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
		deleteNotificationMock.mockRejectedValue(new Error("delete failed"));
		const { result } = renderMutation(queryClient);

		await act(async () => {
			await expect(result.current.mutateAsync(notification)).rejects.toThrow("delete failed");
		});

		expect(rollbackOptimisticNotificationDeleteMock).toHaveBeenCalledWith(queryClient, "ntf_1");
		expect(applyNotificationDeletedMock).not.toHaveBeenCalled();
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["notifications", "history"] });
	});
});
