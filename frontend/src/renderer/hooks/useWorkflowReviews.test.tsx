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

import { useReviewsByRun, useCreateRunReview, usePassReview, useRejectReview } from "./useWorkflowReviews";
import { useCreateRetryRun } from "./useWorkflowRuns";
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

const mockReview = {
	id: "review-1",
	runId: "run-1",
	source: "human",
	status: "pending",
	summary: "Looks good",
	issues: "",
	createdAt: "2026-01-01T00:00:00Z",
};

const passedReview = { ...mockReview, status: "passed", completedAt: "2026-01-01T01:00:00Z" };
const rejectedReview = { ...mockReview, status: "rejected", issues: "Needs fixes", completedAt: "2026-01-01T01:00:00Z" };
const newRetryRun = { id: "run-2", taskId: "task-1", status: "pending", attempt: 2 };
const conflict409 = { status: 409, message: "conflict" };

beforeEach(() => {
	postMock.mockReset();
	getMock.mockReset();
});

// ──────────────────────────────────────────────────────────────────────
// useReviewsByRun
// ──────────────────────────────────────────────────────────────────────
describe("useReviewsByRun", () => {
	it("fetches reviews from GET /workflow/runs/{id}/reviews", async () => {
		getMock.mockResolvedValue({ data: { reviews: [mockReview] }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useReviewsByRun("run-1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(getMock).toHaveBeenCalledWith("/api/v1/workflow/runs/{id}/reviews", {
			params: { path: { id: "run-1" } },
		});
		expect(result.current.data).toHaveLength(1);
		expect(result.current.data![0].id).toBe("review-1");
	});

	it("is disabled when runId is null", () => {
		getMock.mockResolvedValue({ data: { reviews: [] }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useReviewsByRun(null), { wrapper });

		expect(result.current.fetchStatus).toBe("idle");
		expect(getMock).not.toHaveBeenCalled();
	});

	it("returns empty array when data.reviews is undefined", async () => {
		getMock.mockResolvedValue({ data: {}, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useReviewsByRun("run-1"), { wrapper });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual([]);
	});

	it("enters error state on API error", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { status: 500, message: "server error" } });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useReviewsByRun("run-1"), { wrapper });

		await waitFor(() => expect(result.current.isError).toBe(true));
	});
});

// ──────────────────────────────────────────────────────────────────────
// useCreateRunReview
// ──────────────────────────────────────────────────────────────────────
describe("useCreateRunReview", () => {
	it("calls POST /workflow/runs/{id}/reviews with source:human", async () => {
		postMock.mockResolvedValue({ data: { review: mockReview }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRunReview("run-1", "task-1"), { wrapper });

		await result.current.mutateAsync({ summary: "Looks good" });
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs/{id}/reviews", {
			params: { path: { id: "run-1" } },
			body: { source: "human", summary: "Looks good" },
		});
	});

	it("sends source:human even when summary/issues omitted", async () => {
		postMock.mockResolvedValue({ data: { review: mockReview }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRunReview("run-1", "task-1"), { wrapper });

		await result.current.mutateAsync({});
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs/{id}/reviews", {
			params: { path: { id: "run-1" } },
			body: { source: "human" },
		});
	});

	it("always sends source:human, never AI or other values", async () => {
		postMock.mockResolvedValue({ data: { review: mockReview }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRunReview("run-1", "task-1"), { wrapper });

		await result.current.mutateAsync({ summary: "Test", issues: "Bug" });
		const body = postMock.mock.calls[0][1].body;
		expect(body.source).toBe("human");
	});

	it("returns review on success", async () => {
		postMock.mockResolvedValue({ data: { review: mockReview }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRunReview("run-1", "task-1"), { wrapper });

		const review = await result.current.mutateAsync({ summary: "Looks good" });
		expect(review.id).toBe("review-1");
		expect(review.status).toBe("pending");
	});

	it("enters error state on API error", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { status: 500, message: "fail" } });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRunReview("run-1", "task-1"), { wrapper });

		await expect(result.current.mutateAsync({})).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("on success invalidates reviews, runs, and task queries", async () => {
		postMock.mockResolvedValue({ data: { review: mockReview }, error: undefined });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useCreateRunReview("run-1", "task-1"), { wrapper });
		await result.current.mutateAsync({});

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.reviews("run-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.task("task-1") });
		spy.mockRestore();
	});

	it("on error invalidates reviews, runs, and task queries", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useCreateRunReview("run-1", "task-1"), { wrapper });
		await expect(result.current.mutateAsync({})).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.reviews("run-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.task("task-1") });
		spy.mockRestore();
	});
});

// ──────────────────────────────────────────────────────────────────────
// usePassReview
// ──────────────────────────────────────────────────────────────────────
describe("usePassReview", () => {
	it("calls POST /workflow/reviews/{id}/pass with no body", async () => {
		postMock.mockResolvedValue({ data: { review: passedReview }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => usePassReview("review-1", "run-1", "task-1"), { wrapper });

		await result.current.mutateAsync();
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/reviews/{id}/pass", {
			params: { path: { id: "review-1" } },
		});
	});

	it("returns passed review on success", async () => {
		postMock.mockResolvedValue({ data: { review: passedReview }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => usePassReview("review-1", "run-1", "task-1"), { wrapper });

		const review = await result.current.mutateAsync();
		expect(review.status).toBe("passed");
	});

	it("enters error state on API error", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { status: 400, message: "bad" } });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => usePassReview("review-1", "run-1", "task-1"), { wrapper });

		await expect(result.current.mutateAsync()).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("on success invalidates reviews, runs, and task queries", async () => {
		postMock.mockResolvedValue({ data: { review: passedReview }, error: undefined });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => usePassReview("review-1", "run-1", "task-1"), { wrapper });
		await result.current.mutateAsync();

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.reviews("run-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.task("task-1") });
		spy.mockRestore();
	});

	it("on error invalidates reviews, runs, and task queries", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => usePassReview("review-1", "run-1", "task-1"), { wrapper });
		await expect(result.current.mutateAsync()).rejects.toThrow();

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.reviews("run-1") });
		spy.mockRestore();
	});
});

// ──────────────────────────────────────────────────────────────────────
// useRejectReview
// ──────────────────────────────────────────────────────────────────────
describe("useRejectReview", () => {
	it("calls POST /workflow/reviews/{id}/reject with issues body", async () => {
		postMock.mockResolvedValue({ data: { review: rejectedReview }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useRejectReview("review-1", "run-1", "task-1"), { wrapper });

		await result.current.mutateAsync("Needs fixes");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/reviews/{id}/reject", {
			params: { path: { id: "review-1" } },
			body: { issues: "Needs fixes" },
		});
	});

	it("returns rejected review on success", async () => {
		postMock.mockResolvedValue({ data: { review: rejectedReview }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useRejectReview("review-1", "run-1", "task-1"), { wrapper });

		const review = await result.current.mutateAsync("Needs fixes");
		expect(review.status).toBe("rejected");
		expect(review.issues).toBe("Needs fixes");
	});

	it("enters error state on API error", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { status: 500, message: "fail" } });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useRejectReview("review-1", "run-1", "task-1"), { wrapper });

		await expect(result.current.mutateAsync("bad")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("on success invalidates reviews, runs, and task queries", async () => {
		postMock.mockResolvedValue({ data: { review: rejectedReview }, error: undefined });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useRejectReview("review-1", "run-1", "task-1"), { wrapper });
		await result.current.mutateAsync("Issues");

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.reviews("run-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.task("task-1") });
		spy.mockRestore();
	});

	it("on error invalidates reviews, runs, and task queries", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useRejectReview("review-1", "run-1", "task-1"), { wrapper });
		await expect(result.current.mutateAsync("Issues")).rejects.toThrow();

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.reviews("run-1") });
		spy.mockRestore();
	});
});

// ──────────────────────────────────────────────────────────────────────
// useCreateRetryRun
// ──────────────────────────────────────────────────────────────────────
describe("useCreateRetryRun", () => {
	it("calls POST /workflow/runs/{id}/retry with resume mode", async () => {
		postMock.mockResolvedValue({ data: { run: newRetryRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRetryRun("run-1", "task-1"), { wrapper });

		await result.current.mutateAsync("resume");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs/{id}/retry", {
			params: { path: { id: "run-1" } },
			body: { mode: "resume" },
		});
	});

	it("calls POST /workflow/runs/{id}/retry with fresh mode", async () => {
		postMock.mockResolvedValue({ data: { run: newRetryRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRetryRun("run-1", "task-1"), { wrapper });

		await result.current.mutateAsync("fresh");
		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs/{id}/retry", {
			params: { path: { id: "run-1" } },
			body: { mode: "fresh" },
		});
	});

	it("returns new pending run on success", async () => {
		postMock.mockResolvedValue({ data: { run: newRetryRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRetryRun("run-1", "task-1"), { wrapper });

		const run = await result.current.mutateAsync("fresh");
		expect(run.status).toBe("pending");
		expect(run.id).toBe("run-2");
	});

	it("does NOT auto-start the new run", async () => {
		postMock.mockResolvedValue({ data: { run: newRetryRun }, error: undefined });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRetryRun("run-1", "task-1"), { wrapper });

		await result.current.mutateAsync("resume");
		expect(postMock).toHaveBeenCalledTimes(1);
		expect(postMock.mock.calls[0][0]).toBe("/api/v1/workflow/runs/{id}/retry");
	});

	it("enters error state on API error", async () => {
		postMock.mockResolvedValue({ data: undefined, error: { status: 500, message: "fail" } });

		const { wrapper } = createWrapper();
		const { result } = renderHook(() => useCreateRetryRun("run-1", "task-1"), { wrapper });

		await expect(result.current.mutateAsync("resume")).rejects.toThrow();
		await waitFor(() => expect(result.current.isError).toBe(true));
	});

	it("on success invalidates runs and task queries", async () => {
		postMock.mockResolvedValue({ data: { run: newRetryRun }, error: undefined });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useCreateRetryRun("run-1", "task-1"), { wrapper });
		await result.current.mutateAsync("fresh");

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.task("task-1") });
		spy.mockRestore();
	});

	it("on error invalidates runs and task queries", async () => {
		postMock.mockResolvedValue({ data: undefined, error: conflict409 });

		const { queryClient, wrapper } = createWrapper();
		const spy = vi.spyOn(queryClient, "invalidateQueries");

		const { result } = renderHook(() => useCreateRetryRun("run-1", "task-1"), { wrapper });
		await expect(result.current.mutateAsync("resume")).rejects.toThrow();

		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.runs("task-1") });
		expect(spy).toHaveBeenCalledWith({ queryKey: workflowQueryKeys.task("task-1") });
		spy.mockRestore();
	});
});
