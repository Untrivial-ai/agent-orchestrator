import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { getMock, hasTrustedApiBaseUrlMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

import { useWorkflowRuns } from "./useWorkflowRuns";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	getMock.mockReset();
});

describe("useWorkflowRuns", () => {
	it("fetches runs for a task", async () => {
		const runs = [{ id: "r1", taskId: "t1", attempt: 1, status: "succeeded" }];
		getMock.mockResolvedValue({ data: { runs }, error: undefined });

		const { result } = renderHook(() => useWorkflowRuns("t1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual(runs);
		expect(getMock).toHaveBeenCalledWith("/api/v1/workflow/tasks/{id}/runs", { params: { path: { id: "t1" } } });
	});

	it("is disabled when taskId is null", () => {
		const { result } = renderHook(() => useWorkflowRuns(null), { wrapper });
		expect(result.current.fetchStatus).toBe("idle");
		expect(getMock).not.toHaveBeenCalled();
	});

	it("returns empty array when no runs", async () => {
		getMock.mockResolvedValue({ data: { runs: [] }, error: undefined });

		const { result } = renderHook(() => useWorkflowRuns("t1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual([]);
	});
});
