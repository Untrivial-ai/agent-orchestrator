import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { applyNotificationsClearedMock, clearAllNotificationsMock } = vi.hoisted(() => ({
	applyNotificationsClearedMock: vi.fn(),
	clearAllNotificationsMock: vi.fn(),
}));

vi.mock("../lib/notifications", async (importOriginal) => ({
	...((await importOriginal()) as object),
	applyNotificationsCleared: applyNotificationsClearedMock,
	clearAllNotifications: clearAllNotificationsMock,
}));

import { useClearAllNotificationsMutation } from "./useNotificationsQuery";

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
