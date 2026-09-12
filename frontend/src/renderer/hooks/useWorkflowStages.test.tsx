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

import { useWorkflowStages, useStartStage, useReadyForApprovalStage, usePassStage, useBlockStage, useUnblockStage, useCancelStage } from "./useWorkflowStages";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	getMock.mockReset();
	postMock.mockReset();
});

describe("useWorkflowStages", () => {
	it("fetches stages for a plan", async () => {
		const stages = [{ id: "s1", title: "Stage 1", status: "pending", planId: "plan-1" }];
		getMock.mockResolvedValue({ data: { stages }, error: undefined });

		const { result } = renderHook(() => useWorkflowStages("plan-1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual(stages);
		expect(getMock).toHaveBeenCalledWith("/api/v1/workflow/plans/{id}/stages", { params: { path: { id: "plan-1" } } });
	});

	it("is disabled when planId is null", () => {
		const { result } = renderHook(() => useWorkflowStages(null), { wrapper });
		expect(result.current.fetchStatus).toBe("idle");
	});
});

describe("useStartStage", () => {
	it("calls POST to start stage", async () => {
		postMock.mockResolvedValue({ data: { stage: { id: "s1" } }, error: undefined });

		const { result } = renderHook(() => useStartStage(), { wrapper });
		await result.current.mutateAsync({ stageId: "s1", planId: "plan-1" });

		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/stages/{id}/start", { params: { path: { id: "s1" } } });
	});
});

describe("useReadyForApprovalStage", () => {
	it("calls POST to mark stage ready for approval", async () => {
		postMock.mockResolvedValue({ data: { stage: { id: "s1" } }, error: undefined });

		const { result } = renderHook(() => useReadyForApprovalStage(), { wrapper });
		await result.current.mutateAsync({ stageId: "s1", planId: "plan-1" });

		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/stages/{id}/ready", { params: { path: { id: "s1" } } });
	});
});

describe("usePassStage", () => {
	it("calls POST to pass stage", async () => {
		postMock.mockResolvedValue({ data: { stage: { id: "s1" } }, error: undefined });

		const { result } = renderHook(() => usePassStage(), { wrapper });
		await result.current.mutateAsync({ stageId: "s1", planId: "plan-1" });

		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/stages/{id}/pass", { params: { path: { id: "s1" } } });
	});
});

describe("useBlockStage", () => {
	it("calls POST to block stage", async () => {
		postMock.mockResolvedValue({ data: { stage: { id: "s1" } }, error: undefined });

		const { result } = renderHook(() => useBlockStage(), { wrapper });
		await result.current.mutateAsync({ stageId: "s1", planId: "plan-1" });

		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/stages/{id}/block", { params: { path: { id: "s1" } } });
	});
});

describe("useUnblockStage", () => {
	it("calls POST to unblock stage", async () => {
		postMock.mockResolvedValue({ data: { stage: { id: "s1" } }, error: undefined });

		const { result } = renderHook(() => useUnblockStage(), { wrapper });
		await result.current.mutateAsync({ stageId: "s1", planId: "plan-1" });

		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/stages/{id}/unblock", { params: { path: { id: "s1" } } });
	});
});

describe("useCancelStage", () => {
	it("calls POST to cancel stage", async () => {
		postMock.mockResolvedValue({ data: { stage: { id: "s1" } }, error: undefined });

		const { result } = renderHook(() => useCancelStage(), { wrapper });
		await result.current.mutateAsync({ stageId: "s1", planId: "plan-1" });

		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/stages/{id}/cancel", { params: { path: { id: "s1" } } });
	});
});
