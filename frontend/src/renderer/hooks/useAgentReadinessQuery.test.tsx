import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { agentReadiness } from "../test/agent-readiness-fixtures";

const { getMock, postMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: () => "request failed",
}));

import {
	agentReadinessQueryOptions,
	agentReadinessQueryKey,
	cacheAgentReadiness,
	mergeAgentReadiness,
	SETTINGS_READINESS_POLL_INTERVAL_MS,
	useAgentReadinessQuery,
	useEnsureAgentReadiness,
	useSettingsAgentReadinessPolling,
} from "./useAgentReadinessQuery";

function wrapper(queryClient: QueryClient) {
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

beforeEach(() => {
	getMock.mockReset().mockResolvedValue({
		data: { agents: [agentReadiness("codex", "Codex")] },
		error: undefined,
	});
	postMock.mockReset().mockResolvedValue({
		data: { agents: [agentReadiness("codex", "Codex")] },
		error: undefined,
	});
});

describe("agent readiness query", () => {
	it("leaves freshness policy to the daemon", () => {
		expect(agentReadinessQueryOptions.staleTime).toBe(Number.POSITIVE_INFINITY);
	});

	it("reads the cached daemon snapshot without invoking ensure", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useAgentReadinessQuery(), { wrapper: wrapper(queryClient) });

		await waitFor(() => expect(result.current.data?.agents[0]?.id).toBe("codex"));
		expect(getMock).toHaveBeenCalledWith("/api/v1/agents/readiness", {
			signal: expect.any(AbortSignal),
		});
		expect(postMock).not.toHaveBeenCalled();
	});

	it("ensures normalized relevant harness ids and updates the display copy", async () => {
		const queryClient = new QueryClient();
		renderHook(
			() => useEnsureAgentReadiness({ agentIds: ["codex", "claude-code", "codex"] }),
			{ wrapper: wrapper(queryClient) },
		);

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/agents/readiness/ensure", {
				body: { agentIds: ["claude-code", "codex"], purpose: "display" },
			}),
		);
		expect(queryClient.getQueryData(agentReadinessQueryKey)).toEqual({
			agents: [agentReadiness("codex", "Codex")],
		});
	});

	it("merges targeted ensures without discarding other harness snapshots", () => {
		const claude = agentReadiness("claude-code", "Claude Code");
		const staleCodex = agentReadiness("codex", "Codex", { freshness: "stale" });
		const freshCodex = agentReadiness("codex", "Codex");

		expect(mergeAgentReadiness({ agents: [claude, staleCodex] }, { agents: [freshCodex] })).toEqual({
			agents: [claude, freshCodex],
		});
	});

	it("does not let an older ensure response overwrite newer authentication", () => {
		const newer = agentReadiness("codex", "Codex");
		newer.authentication.checkedAt = "2026-09-20T12:00:20Z";
		newer.authentication.attemptedAt = "2026-09-20T12:00:20Z";
		const older = agentReadiness("codex", "Codex", { authentication: "unauthorized" });
		older.authentication.checkedAt = "2026-09-20T12:00:10Z";
		older.authentication.attemptedAt = "2026-09-20T12:00:10Z";

		const merged = mergeAgentReadiness({ agents: [newer] }, { agents: [older] });

		expect(merged.agents[0]?.authentication.state).toBe("authorized");
		expect(merged.agents[0]?.effectiveReadiness).toBe("ready");
	});

	it("does not let an older GET response overwrite a newer ensure result", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		let resolveGet!: (value: { data: { agents: ReturnType<typeof agentReadiness>[] }; error: undefined }) => void;
		getMock.mockReturnValueOnce(new Promise((resolve) => { resolveGet = resolve; }));
		const older = agentReadiness("codex", "Codex", { authentication: "unauthorized" });
		older.authentication.checkedAt = "2026-09-20T12:00:10Z";
		older.authentication.attemptedAt = "2026-09-20T12:00:10Z";
		const newer = agentReadiness("codex", "Codex");
		newer.authentication.checkedAt = "2026-09-20T12:00:20Z";
		newer.authentication.attemptedAt = "2026-09-20T12:00:20Z";

		const rendered = renderHook(() => useAgentReadinessQuery(), { wrapper: wrapper(queryClient) });
		await waitFor(() => expect(getMock).toHaveBeenCalledOnce());
		act(() => cacheAgentReadiness(queryClient, { agents: [newer] }));
		await act(async () => resolveGet({ data: { agents: [older] }, error: undefined }));

		await waitFor(() => expect(rendered.result.current.isSuccess).toBe(true));
		expect(rendered.result.current.data?.agents[0]?.authentication.state).toBe("authorized");
		expect(rendered.result.current.data?.agents[0]?.effectiveReadiness).toBe("ready");
		rendered.unmount();
		queryClient.clear();
	});

	it("polls installed harness authentication with the settings policy", async () => {
		vi.useFakeTimers();
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const rendered = renderHook(
			() => useSettingsAgentReadinessPolling({ agentIds: ["codex", "claude-code", "codex"] }),
			{ wrapper: wrapper(queryClient) },
		);
		try {
			await act(async () => { await Promise.resolve(); });
			expect(postMock).toHaveBeenCalledWith("/api/v1/agents/readiness/ensure", {
				body: { agentIds: ["claude-code", "codex"], purpose: "settings" },
				signal: expect.any(AbortSignal),
			});
			const initialCalls = postMock.mock.calls.length;
			await act(async () => {
				await vi.advanceTimersByTimeAsync(SETTINGS_READINESS_POLL_INTERVAL_MS);
			});
			expect(postMock.mock.calls.length).toBeGreaterThan(initialCalls);
		} finally {
			rendered.unmount();
			queryClient.clear();
			vi.useRealTimers();
		}
	});

	it("does not poll when no installed harness ids are present", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const rendered = renderHook(
			() => useSettingsAgentReadinessPolling({ agentIds: [] }),
			{ wrapper: wrapper(queryClient) },
		);
		await act(async () => { await Promise.resolve(); });
		expect(postMock).not.toHaveBeenCalled();
		rendered.unmount();
		queryClient.clear();
	});

	it("cancels a pending settings poll without updating the shared cache", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		let requestSignal: AbortSignal | undefined;
		let aborted = false;
		postMock.mockImplementationOnce((_path, options) => new Promise((_resolve, reject) => {
			requestSignal = options.signal;
			requestSignal?.addEventListener("abort", () => {
				aborted = true;
				reject(new DOMException("Aborted", "AbortError"));
			});
		}));
		const rendered = renderHook(
			() => useSettingsAgentReadinessPolling({ agentIds: ["codex"] }),
			{ wrapper: wrapper(queryClient) },
		);

		await waitFor(() => expect(requestSignal).toBeDefined());
		rendered.unmount();
		await waitFor(() => expect(aborted).toBe(true));
		expect(queryClient.getQueryData(agentReadinessQueryKey)).toBeUndefined();
		queryClient.clear();
	});
});
