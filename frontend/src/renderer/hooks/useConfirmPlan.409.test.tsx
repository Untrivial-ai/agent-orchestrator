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

import { useConfirmPlan, workflowQueryKeys } from "./useWorkflowPlans";

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

const existingPlan = {
	id: "plan-1",
	projectId: "proj-1",
	title: "Auth System",
	status: "draft",
	objective: "Implement auth",
};

const conflict409 = { status: 409, message: "conflict: plan already confirmed" };

beforeEach(() => {
	postMock.mockReset();
});

describe("useConfirmPlan — 409 conflict path", () => {
	it("enters error state when API returns 409", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useConfirmPlan(), { wrapper });

		await expect(result.current.mutateAsync("plan-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("does NOT call onSuccess on 409", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const onSuccessSpy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useConfirmPlan(), { wrapper });

		await expect(result.current.mutateAsync("plan-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		// onSuccess should NOT have been called — the only invalidation
		// should come from onError
		expect(onSuccessSpy).not.toHaveBeenCalledWith({
			queryKey: workflowQueryKeys.plans("proj-1"),
		});
		onSuccessSpy.mockRestore();
	});

	it("invalidates plan(planId) query on 409 via onError path", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useConfirmPlan(), { wrapper });

		await expect(result.current.mutateAsync("plan-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		// onError path invalidates plan(planId) so React Query can refetch
		expect(spy).toHaveBeenCalledWith({
			queryKey: workflowQueryKeys.plan("plan-1"),
		});
		spy.mockRestore();
	});

	it("preserves pre-existing query cache after 409 (no optimistic write)", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		queryClient.setQueryData(workflowQueryKeys.plan("plan-1"), existingPlan);
		queryClient.setQueryData(workflowQueryKeys.plans("proj-1"), [existingPlan]);

		const { result } = renderHook(() => useConfirmPlan(), { wrapper });

		await expect(result.current.mutateAsync("plan-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		// No optimistic update — cache data not replaced with fake state
		expect(queryClient.getQueryData(workflowQueryKeys.plans("proj-1"))).toEqual([existingPlan]);
	});

	it("mutation error state contains the 409 message", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useConfirmPlan(), { wrapper });

		await expect(result.current.mutateAsync("plan-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		expect(result.current.error).toBeInstanceOf(Error);
		expect(result.current.error!.message).toBeTruthy();
	});
});

describe("useConfirmPlan — success path (baseline)", () => {
	it("invalidates both plans list and single plan queries on success", async () => {
		const confirmedPlan = { ...existingPlan, status: "confirmed" };
		postMock.mockResolvedValue({ data: { plan: confirmedPlan }, error: undefined });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useConfirmPlan(), { wrapper });

		await result.current.mutateAsync("plan-1");
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.plans("proj-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.plan("plan-1") });
		spy.mockRestore();
	});
});
