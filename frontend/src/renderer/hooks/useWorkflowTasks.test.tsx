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

import { useWorkflowTasks, useWorkflowTask } from "./useWorkflowTasks";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	getMock.mockReset();
});

describe("useWorkflowTasks", () => {
	it("fetches tasks for a stage", async () => {
		const tasks = [{ id: "t1", title: "Task 1", status: "pending", stageId: "s1" }];
		getMock.mockResolvedValue({ data: { tasks }, error: undefined });

		const { result } = renderHook(() => useWorkflowTasks("s1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual(tasks);
		expect(getMock).toHaveBeenCalledWith("/api/v1/workflow/stages/{id}/tasks", { params: { path: { id: "s1" } } });
	});

	it("is disabled when stageId is null", () => {
		const { result } = renderHook(() => useWorkflowTasks(null), { wrapper });
		expect(result.current.fetchStatus).toBe("idle");
		expect(getMock).not.toHaveBeenCalled();
	});
});

describe("useWorkflowTask", () => {
	it("fetches a single task", async () => {
		const task = { id: "t1", title: "Task 1", status: "running", taskType: "coding" };
		getMock.mockResolvedValue({ data: { task }, error: undefined });

		const { result } = renderHook(() => useWorkflowTask("t1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual(task);
		expect(getMock).toHaveBeenCalledWith("/api/v1/workflow/tasks/{id}", { params: { path: { id: "t1" } } });
	});

	it("is disabled when taskId is null", () => {
		const { result } = renderHook(() => useWorkflowTask(null), { wrapper });
		expect(result.current.fetchStatus).toBe("idle");
	});
});
