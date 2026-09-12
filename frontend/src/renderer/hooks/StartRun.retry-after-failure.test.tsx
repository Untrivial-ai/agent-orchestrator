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

import { useStartRun } from "./useWorkflowRuns";

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

const serverError500 = { status: 500, message: "internal server error" };

const runningRun = {
	id: "run-1",
	taskId: "task-1",
	status: "running",
	attempt: 1,
	createdAt: "2026-01-01",
	sessionId: "sess-1",
};

beforeEach(() => {
	postMock.mockReset();
});

describe("StartRun — retry after failure evidence", () => {
	it("first StartRun returns 500 → run stays pending, no new run created", async () => {
		postMock.mockResolvedValue({ data: undefined, error: serverError500 });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useStartRun("task-1"), { wrapper });

		await expect(result.current.mutateAsync("run-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		// Only 1 POST call was made (the failed start), no second POST (no create)
		expect(postMock).toHaveBeenCalledOnce();
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs/{id}/start", {
			params: { path: { id: "run-1" } },
		});
	});

	it("second StartRun succeeds → run transitions to running", async () => {
		// First call fails
		postMock.mockResolvedValueOnce({ data: undefined, error: serverError500 });
		// Second call succeeds
		postMock.mockResolvedValueOnce({ data: { run: runningRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useStartRun("task-1"), { wrapper });

		// First attempt
		await expect(result.current.mutateAsync("run-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		// Reset mutation state for retry
		result.current.reset();
		await waitFor(() => expect(result.current.isIdle).toBe(true));

		// Second attempt
		const run = await result.current.mutateAsync("run-1");
		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(run.status).toBe("running");
		expect(run.sessionId).toBe("sess-1");
		expect(postMock).toHaveBeenCalledTimes(2);
	});

	it("both calls target the same run ID", async () => {
		postMock.mockResolvedValueOnce({ data: undefined, error: serverError500 });
		postMock.mockResolvedValueOnce({ data: { run: runningRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useStartRun("task-1"), { wrapper });

		await expect(result.current.mutateAsync("run-1")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		result.current.reset();
		await result.current.mutateAsync("run-1");

		// Both calls used the same run ID
		for (const call of postMock.mock.calls) {
			expect(call[1]).toEqual({ params: { path: { id: "run-1" } } });
		}
	});
});
