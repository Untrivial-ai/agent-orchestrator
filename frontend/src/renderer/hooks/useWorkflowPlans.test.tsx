import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { getMock, postMock, hasTrustedApiBaseUrlMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

import { workflowQueryKeys, useWorkflowPlans, useWorkflowPlan, useCreatePlan, useConfirmPlan, useStartPlan, useCompletePlan, useCancelPlan } from "./useWorkflowPlans";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	getMock.mockReset();
	postMock.mockReset();
});

describe("workflowQueryKeys", () => {
	it("returns correct key for plans", () => {
		expect(workflowQueryKeys.plans("p1")).toEqual(["workflow", "plans", "p1"]);
	});

	it("returns correct key for plan", () => {
		expect(workflowQueryKeys.plan("plan-1")).toEqual(["workflow", "plan", "plan-1"]);
	});

	it("returns correct key for stages", () => {
		expect(workflowQueryKeys.stages("plan-1")).toEqual(["workflow", "stages", "plan-1"]);
	});

	it("returns correct key for tasks", () => {
		expect(workflowQueryKeys.tasks("stage-1")).toEqual(["workflow", "tasks", "stage-1"]);
	});

	it("returns correct key for runs", () => {
		expect(workflowQueryKeys.runs("task-1")).toEqual(["workflow", "runs", "task-1"]);
	});
});

describe("useWorkflowPlans", () => {
	it("fetches plans for a project", async () => {
		const plans = [{ id: "p1", title: "Test Plan", status: "draft", projectId: "proj-1" }];
		getMock.mockResolvedValue({ data: { plans }, error: undefined });

		const { result } = renderHook(() => useWorkflowPlans("proj-1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual(plans);
		expect(getMock).toHaveBeenCalledWith("/api/v1/projects/{id}/plans", { params: { path: { id: "proj-1" } } });
	});

	it("throws on API error", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "not found" } });

		const { result } = renderHook(() => useWorkflowPlans("proj-1"), { wrapper });

		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("returns empty array when no plans", async () => {
		getMock.mockResolvedValue({ data: { plans: [] }, error: undefined });

		const { result } = renderHook(() => useWorkflowPlans("proj-1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual([]);
	});
});

describe("useWorkflowPlan", () => {
	it("fetches a single plan", async () => {
		const plan = { id: "plan-1", title: "My Plan", status: "confirmed" };
		getMock.mockResolvedValue({ data: { plan }, error: undefined });

		const { result } = renderHook(() => useWorkflowPlan("plan-1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual(plan);
		expect(getMock).toHaveBeenCalledWith("/api/v1/workflow/plans/{id}", { params: { path: { id: "plan-1" } } });
	});

	it("is disabled when planId is null", () => {
		const { result } = renderHook(() => useWorkflowPlan(null), { wrapper });
		expect(result.current.fetchStatus).toBe("idle");
		expect(getMock).not.toHaveBeenCalled();
	});
});

describe("useCreatePlan", () => {
	it("calls POST to create plan and invalidates plans list", async () => {
		const plan = { id: "new-1", title: "New", projectId: "proj-1" };
		postMock.mockResolvedValue({ data: { plan }, error: undefined });

		const { result } = renderHook(() => useCreatePlan(), { wrapper });

		await result.current.mutateAsync({ title: "New", projectId: "proj-1" } as never);
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/plans", { body: { title: "New", projectId: "proj-1" } });
	});
});

describe("useConfirmPlan", () => {
	it("calls POST to confirm plan", async () => {
		const plan = { id: "plan-1", projectId: "proj-1", status: "confirmed" };
		postMock.mockResolvedValue({ data: { plan }, error: undefined });

		const { result } = renderHook(() => useConfirmPlan(), { wrapper });

		await result.current.mutateAsync("plan-1");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/plans/{id}/confirm", { params: { path: { id: "plan-1" } } });
	});
});

describe("useStartPlan", () => {
	it("calls POST to start plan", async () => {
		const plan = { id: "plan-1", projectId: "proj-1", status: "in_progress" };
		postMock.mockResolvedValue({ data: { plan }, error: undefined });

		const { result } = renderHook(() => useStartPlan(), { wrapper });

		await result.current.mutateAsync("plan-1");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/plans/{id}/start", { params: { path: { id: "plan-1" } } });
	});
});

describe("useCompletePlan", () => {
	it("calls POST to complete plan", async () => {
		const plan = { id: "plan-1", projectId: "proj-1", status: "completed" };
		postMock.mockResolvedValue({ data: { plan }, error: undefined });

		const { result } = renderHook(() => useCompletePlan(), { wrapper });

		await result.current.mutateAsync("plan-1");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/plans/{id}/complete", { params: { path: { id: "plan-1" } } });
	});
});

describe("useCancelPlan", () => {
	it("calls POST to cancel plan", async () => {
		const plan = { id: "plan-1", projectId: "proj-1", status: "cancelled" };
		postMock.mockResolvedValue({ data: { plan }, error: undefined });

		const { result } = renderHook(() => useCancelPlan(), { wrapper });

		await result.current.mutateAsync("plan-1");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/plans/{id}/cancel", { params: { path: { id: "plan-1" } } });
	});
});
