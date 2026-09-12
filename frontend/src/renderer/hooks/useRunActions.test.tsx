import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { postMock, getMock, hasTrustedApiBaseUrlMock } = vi.hoisted(() => ({
	postMock: vi.fn(),
	getMock: vi.fn(),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

import { useCreateRun, useStartRun, useCancelRun } from "./useWorkflowRuns";
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

const pendingRun = { id: "run-1", taskId: "task-1", status: "pending", attempt: 1, createdAt: "2026-01-01" };
const runningRun = { id: "run-1", taskId: "task-1", status: "running", attempt: 1, createdAt: "2026-01-01", sessionId: "sess-1" };
const conflict409 = { status: 409, message: "conflict" };

beforeEach(() => {
	postMock.mockReset();
	getMock.mockReset();
});

describe("useCreateRun", () => {
	it("calls POST /workflow/runs with taskId (not /tasks/{id}/runs)", async () => {
		postMock.mockResolvedValue({ data: { run: pendingRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRun("task-1"), { wrapper });

		await result.current.mutateAsync();
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs", {
			body: { taskId: "task-1" },
		});
		// Negative contract: must NOT call the task-scoped collection endpoint
		for (const call of postMock.mock.calls) {
			expect(call[0]).not.toBe("/api/v1/workflow/tasks/{id}/runs");
		}
	});

	it("returns pending run on success", async () => {
		postMock.mockResolvedValue({ data: { run: pendingRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRun("task-1"), { wrapper });

		const run = await result.current.mutateAsync();
		expect(run.status).toBe("pending");
		expect(run.id).toBe("run-1");
	});

	it("enters error state on API error", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { status: 500, message: "server error" } });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRun("task-1"), { wrapper });

		await expect(result.current.mutateAsync()).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("does NOT auto-start after create", async () => {
		postMock.mockResolvedValue({ data: { run: pendingRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRun("task-1"), { wrapper });

		await result.current.mutateAsync();
		// Only 1 POST call (create), not 2 (create+start)
		expect(postMock).toHaveBeenCalledTimes(1);
	});

	it("on 409 invalidates runs and task queries", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useCreateRun("task-1"), { wrapper });

		await expect(result.current.mutateAsync()).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.task("task-1") });
		spy.mockRestore();
	});
});

describe("useStartRun", () => {
	it("calls POST /workflow/runs/{id}/start", async () => {
		postMock.mockResolvedValue({ data: { run: runningRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useStartRun("task-1"), { wrapper });

		await result.current.mutateAsync("run-1");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs/{id}/start", {
			params: { path: { id: "run-1" } },
		});
	});

	it("returns running run on success", async () => {
		postMock.mockResolvedValue({ data: { run: runningRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useStartRun("task-1"), { wrapper });

		const run = await result.current.mutateAsync("run-1");
		expect(run.status).toBe("running");
	});

	it("enters error state on API error (run stays pending)", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { status: 500, message: "start failed" } });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useStartRun("task-1"), { wrapper });

		await expect(result.current.mutateAsync("run-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("on error invalidates runs and task queries", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useStartRun("task-1"), { wrapper });

		await expect(result.current.mutateAsync("run-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		spy.mockRestore();
	});
});

describe("useCancelRun", () => {
	it("calls POST /workflow/runs/{id}/cancel", async () => {
		const cancelledRun = { ...pendingRun, status: "cancelled" };
		postMock.mockResolvedValue({ data: { run: cancelledRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCancelRun("task-1"), { wrapper });

		await result.current.mutateAsync("run-1");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs/{id}/cancel", {
			params: { path: { id: "run-1" } },
		});
	});

	it("returns cancelled run on success", async () => {
		const cancelledRun = { ...pendingRun, status: "cancelled" };
		postMock.mockResolvedValue({ data: { run: cancelledRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCancelRun("task-1"), { wrapper });

		const run = await result.current.mutateAsync("run-1");
		expect(run.status).toBe("cancelled");
	});

	it("on error invalidates runs and task queries", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useCancelRun("task-1"), { wrapper });

		await expect(result.current.mutateAsync("run-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		spy.mockRestore();
	});
});
