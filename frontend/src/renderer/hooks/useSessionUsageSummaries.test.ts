import { beforeEach, describe, expect, it, vi } from "vitest";

const getMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
}));

import {
	fetchSessionUsage,
	fetchSessionUsageSummaries,
	sessionUsageDetailQueryOptions,
	sessionUsageQueryRoot,
	sessionUsageQueryOptions,
} from "./useSessionUsageSummaries";

describe("session usage summaries", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({ data: { sessions: [] } });
	});

	it("fetches one project batch and relies on event invalidation", async () => {
		await fetchSessionUsageSummaries("reverb");

		expect(getMock).toHaveBeenCalledOnce();
		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/sessions", {
			params: { query: { projectId: "reverb" } },
		});
		expect(sessionUsageQueryOptions("reverb")).not.toHaveProperty("refetchInterval");
	});

	it("fetches detailed usage beneath the shared usage query root", async () => {
		getMock.mockResolvedValueOnce({
			data: {
				harnesses: [],
				incomplete: false,
				sessionId: "sess-1",
				totals: {
					cacheReadTokens: 0,
					cacheWriteTokens: 0,
					estimatedCost: null,
					inputTokens: 0,
					outputTokens: 0,
					reasoningTokens: 0,
					uncachedInputTokens: 0,
				},
			},
		});

		const result = await fetchSessionUsage("sess-1");

		expect(result.sessionId).toBe("sess-1");
		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/sessions/{sessionId}", {
			params: { path: { sessionId: "sess-1" } },
		});
		expect(sessionUsageDetailQueryOptions("sess-1").queryKey).toEqual([
			...sessionUsageQueryRoot,
			"detail",
			"sess-1",
		]);
	});
});
