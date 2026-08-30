import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: vi.fn() },
	apiErrorMessage: vi.fn(() => "failed"),
}));

import { useReviewerConversation } from "./useReviewerConversation";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

function page({
	id,
	sequence,
	hasMoreBefore,
	controller = "ready",
}: {
	id: string;
	sequence: number;
	hasMoreBefore: boolean;
	controller?: "ready" | "busy";
}) {
	return {
		conversationId: "review-conversation-1",
		sessionId: "worker-1",
		harness: "codex",
		mode: "chat",
		controller,
		latestSequence: 400,
		oldestSequence: sequence,
		hasMoreBefore,
		turns: [],
		activities: [],
		messages: [{
			id,
			sequence,
			revision: 1,
			role: "assistant",
			origin: "provider",
			text: id,
			streaming: false,
			createdAt: "2026-08-27T00:00:00Z",
		}],
	};
}

beforeEach(() => {
	getMock.mockReset();
});

describe("useReviewerConversation pagination", () => {
	it("loads and merges earlier reviewer messages through beforeSequence", async () => {
		getMock.mockImplementation(async (_path, options) => {
			const beforeSequence = options.params.query.beforeSequence;
			return {
				data: beforeSequence === undefined
					? page({ id: "newest", sequence: 201, hasMoreBefore: true })
					: page({ id: "oldest", sequence: 1, hasMoreBefore: false }),
				error: undefined,
			};
		});

		const { result } = renderHook(() => useReviewerConversation("review-1"), { wrapper });
		await waitFor(() => expect(result.current.snapshot).toBeDefined());

		expect(result.current.hasOlder).toBe(true);
		act(() => result.current.loadOlder());

		await waitFor(() => expect(result.current.snapshot?.items.map((item) => item.id)).toEqual(["oldest", "newest"]));
		expect(getMock).toHaveBeenNthCalledWith(2, "/api/v1/reviews/{reviewId}/conversation", {
			params: { path: { reviewId: "review-1" }, query: { beforeSequence: 201, limit: 200 } },
		});
		expect(result.current.hasOlder).toBe(false);
	});

	it("does not poll busy reviewer conversations while CDC owns invalidation", async () => {
		getMock.mockResolvedValue({
			data: page({
				id: "streaming",
				sequence: 1,
				hasMoreBefore: false,
				controller: "busy",
			}),
			error: undefined,
		});

		const { unmount } = renderHook(() => useReviewerConversation("review-1"), { wrapper });
		await waitFor(() => expect(getMock).toHaveBeenCalledTimes(1));
		await new Promise((resolve) => setTimeout(resolve, 750));
		expect(getMock).toHaveBeenCalledTimes(1);
		unmount();
	});
});
