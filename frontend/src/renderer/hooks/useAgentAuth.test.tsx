import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "../lib/api-client";
import { shellTerminalsQueryKey } from "./useShellTerminals";
import { probeAgentAuth, useAgentAuthPlans, useStartAgentAuth } from "./useAgentAuth";

function wrapper(queryClient: QueryClient) {
	return ({ children }: PropsWithChildren) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

describe("agent authentication hooks", () => {
	afterEach(() => vi.restoreAllMocks());

	it("loads display-safe authentication plans", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue({
			data: { plans: [{ agentId: "codex", action: "login", launchMode: "terminal", available: true, documentationUrl: "https://example.test" }] },
		} as never);
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

		const { result } = renderHook(() => useAgentAuthPlans(), { wrapper: wrapper(client) });

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data?.[0]).toMatchObject({ agentId: "codex", action: "login" });
		expect(apiClient.GET).toHaveBeenCalledWith("/api/v1/agents/auth-plans");
	});

	it("starts a fixed agent flow and adds its terminal to an existing cache before navigation", async () => {
		const terminal = { handleId: "shellterm-auth", workingDir: "/tmp/ao", title: "Log in to Codex", createdAt: new Date().toISOString() };
		vi.spyOn(apiClient, "POST").mockResolvedValue({ data: { agentId: "codex", action: "login", terminal } } as never);
		const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
		client.setQueryData(shellTerminalsQueryKey, [{
			handleId: "shellterm-existing", workingDir: "/tmp/ao", title: "Existing", createdAt: new Date(0).toISOString(),
		}]);
		const invalidate = vi.spyOn(client, "invalidateQueries");
		const { result } = renderHook(() => useStartAgentAuth(), { wrapper: wrapper(client) });

		await act(async () => {
			await result.current.mutateAsync("codex");
		});

		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/auth", {
			params: { path: { agent: "codex" } },
		});
		expect(invalidate).toHaveBeenCalledWith({ queryKey: shellTerminalsQueryKey });
		expect(client.getQueryData<Array<{ handleId: string }>>(shellTerminalsQueryKey)?.map((item) => item.handleId)).toEqual([
			"shellterm-existing",
			"shellterm-auth",
		]);
	});

	it("runs the existing fresh probe for Check login", async () => {
		vi.spyOn(apiClient, "POST").mockResolvedValue({ data: { id: "codex", installed: true, authStatus: "authorized" } } as never);

		await probeAgentAuth("codex");

		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/probe", {
			params: { path: { agent: "codex" } },
		});
	});
});
