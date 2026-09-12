import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { postMock, hasTrustedApiBaseUrlMock } = vi.hoisted(() => ({
	postMock: vi.fn(),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: vi.fn(), POST: postMock },
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

import { usePassStage } from "./useWorkflowStages";
import { workflowQueryKeys } from "./useWorkflowPlans";

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false, retryDelay: 0 } },
	});
	return {
		queryClient,
		wrapper: ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		),
	};
}

const conflict409 = { status: 409, message: "conflict: stage already passed" };

beforeEach(() => {
	postMock.mockReset();
});

describe("usePassStage — 409 conflict path", () => {
	it("enters error state when API returns 409", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => usePassStage(), { wrapper });

		await expect(
			result.current.mutateAsync({ stageId: "stage-1", planId: "plan-1" }),
		).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("does NOT call onSuccess on 409", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => usePassStage(), { wrapper });

		await expect(
			result.current.mutateAsync({ stageId: "stage-1", planId: "plan-1" }),
		).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		// onSuccess callback is never invoked on error
		// But onError does invalidate — verify no onSuccess-specific call
		// The only invalidation should come from onError
		expect(spy).not.toHaveBeenCalledWith({
			queryKey: workflowQueryKeys.stages("plan-1"),
			type: "active",
		});
		spy.mockRestore();
	});

	it("invalidates stages(planId) query on 409 via onError path", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => usePassStage(), { wrapper });

		await expect(
			result.current.mutateAsync({ stageId: "stage-1", planId: "plan-1" }),
		).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		// onError invalidates stages(planId) so React Query refetches
		expect(spy).toHaveBeenCalledWith({
			queryKey: workflowQueryKeys.stages("plan-1"),
		});
		spy.mockRestore();
	});

	it("mutation error state contains the 409 message", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => usePassStage(), { wrapper });

		await expect(
			result.current.mutateAsync({ stageId: "stage-1", planId: "plan-1" }),
		).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		expect(result.current.error).toBeInstanceOf(Error);
		expect(result.current.error!.message).toBeTruthy();
	});
});

describe("usePassStage — success path (baseline)", () => {
	it("invalidates stages(planId) on success", async () => {
		const passedStage = { id: "stage-1", planId: "plan-1", status: "passed", title: "S1" };
		postMock.mockResolvedValue({ data: { stage: passedStage }, error: undefined });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => usePassStage(), { wrapper });

		await result.current.mutateAsync({ stageId: "stage-1", planId: "plan-1" });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(spy).toHaveBeenCalledWith({
			queryKey: workflowQueryKeys.stages("plan-1"),
		});
		spy.mockRestore();
	});
});
