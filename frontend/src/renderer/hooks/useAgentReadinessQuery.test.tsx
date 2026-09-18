import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
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
	ensureAgentReadiness,
	mergeAgentReadiness,
	useAgentReadinessQuery,
	useEnsureAgentReadiness,
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
	it("rejects malformed ensure responses before they can enter the cache", async () => {
		postMock.mockResolvedValue({ data: { reviews: [] } });
		await expect(ensureAgentReadiness()).rejects.toThrow("Invalid agent readiness response");
	});

	it("loads a valid snapshot after a malformed ensure response", async () => {
		postMock.mockResolvedValue({ data: { reviews: [] } });
		let finishGet!: (value: unknown) => void;
		getMock.mockReturnValue(new Promise((resolve) => { finishGet = resolve; }));
		const queryClient = new QueryClient();
		const { result, unmount } = renderHook(() => {
			useEnsureAgentReadiness();
			return useAgentReadinessQuery();
		}, { wrapper: wrapper(queryClient) });
		await waitFor(() => expect(postMock).toHaveBeenCalled());
		expect(queryClient.getQueryData(agentReadinessQueryKey)).toBeUndefined();
		finishGet({ data: { agents: [agentReadiness("codex", "Codex")] } });
		await waitFor(() => expect(result.current.data?.agents[0]?.id).toBe("codex"));
		unmount();
	});

	it("leaves freshness policy to the daemon", () => {
		expect(agentReadinessQueryOptions.staleTime).toBe(Number.POSITIVE_INFINITY);
	});

	it("reads the cached daemon snapshot without invoking ensure", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useAgentReadinessQuery(), { wrapper: wrapper(queryClient) });

		await waitFor(() => expect(result.current.data?.agents[0]?.id).toBe("codex"));
		expect(getMock).toHaveBeenCalledWith("/api/v1/agents/readiness");
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
});

it("keeps a newer discovery result when an older missing response arrives", () => {
 const installed = agentReadiness("codex", "Codex", { authentication: "unauthorized" });
 installed.installation.attemptedAt = "2026-09-17T10:00:01Z";
 installed.installation.checkedAt = "2026-09-17T10:00:01Z";
 const missing = agentReadiness("codex", "Codex", { installation: "not_installed", authentication: "unknown" });
 expect(mergeAgentReadiness({agents:[installed]}, {agents:[missing]}).agents[0]?.installation).toEqual(installed.installation);
});

it("updates pending discovery automatically while the consumer is mounted", async () => {
 const pending = agentReadiness("codex", "Codex", {installation:"unknown",authentication:"unknown",freshness:"checking"});
 const found = agentReadiness("codex", "Codex", {authentication:"unauthorized"});
 postMock.mockResolvedValueOnce({data:{agents:[pending]}}).mockResolvedValue({data:{agents:[found]}});
 const queryClient = new QueryClient();
 const {unmount} = renderHook(() => useEnsureAgentReadiness({agentIds:["codex"]}), {wrapper:wrapper(queryClient)});
 await waitFor(() => expect(queryClient.getQueryData(agentReadinessQueryKey)).toEqual({agents:[pending]}));
 await waitFor(() => expect(queryClient.getQueryData(agentReadinessQueryKey)).toEqual({agents:[found]}), {timeout:4000});
 unmount();
});

it("merges a new authentication result independently of older installation data", () => {
	const installed = agentReadiness("codex", "Codex", { authentication: "unknown" });
	installed.installation.attemptedAt = "2026-09-17T10:00:01Z";
	const signedIn = agentReadiness("codex", "Codex", { installation: "unknown" });
	signedIn.authentication.attemptedAt = "2026-09-17T10:00:02Z";
	const result = mergeAgentReadiness({ agents: [installed] }, { agents: [signedIn] }).agents[0];
	expect(result?.installation).toEqual(installed.installation);
	expect(result?.authentication).toEqual(signedIn.authentication);
	expect(result?.effectiveReadiness).toBe("ready");
});

it("preserves completed discovery when the initial GET finishes late", async () => {
	const installed = agentReadiness("codex", "Codex");
	installed.installation.attemptedAt = "2026-09-17T10:00:01Z";
	const missing = agentReadiness("codex", "Codex", { installation: "not_installed" });
	let finishGet!: (value: unknown) => void;
	getMock.mockReturnValue(new Promise((resolve) => { finishGet = resolve; }));
	const queryClient = new QueryClient();
	const { result, unmount } = renderHook(() => useAgentReadinessQuery(), { wrapper: wrapper(queryClient) });
	await waitFor(() => expect(getMock).toHaveBeenCalled());
	queryClient.setQueryData(agentReadinessQueryKey, { agents: [installed] });
	finishGet({ data: { agents: [missing] } });
	await waitFor(() => expect(result.current.isFetching).toBe(false));
	expect(result.current.data?.agents[0]?.installation).toEqual(installed.installation);
	unmount();
});
