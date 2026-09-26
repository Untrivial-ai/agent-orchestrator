import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { agentsQueryKey } from "./useAgentsQuery";
import { agentInstallJobsQueryKey, useHarnessSetup } from "./useHarnessSetup";

const apiMocks = vi.hoisted(() => ({
	GET: vi.fn(),
	POST: vi.fn(),
}));

vi.mock("../lib/api-client", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/api-client")>();
	return {
		...actual,
		apiClient: { ...actual.apiClient, GET: apiMocks.GET, POST: apiMocks.POST },
	};
});

function wrapper({ children }: { children: ReactNode }) {
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

let queryClient: QueryClient;

beforeEach(() => {
	apiMocks.GET.mockReset();
	apiMocks.POST.mockReset();
	queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	apiMocks.GET.mockImplementation(async (path: string) => {
		if (path === "/api/v1/agents/installers") return { data: { agents: [] } };
		if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } };
		if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } };
		return { data: undefined };
	});
});

describe("useHarnessSetup", () => {
	it("marks the agent catalog stale once an install job succeeds", async () => {
		const invalidated: unknown[] = [];
		const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries").mockImplementation(async (filters) => {
			invalidated.push(filters?.queryKey);
			return undefined;
		});
		renderHook(() => useHarnessSetup(), { wrapper });
		await waitFor(() => {
			expect(apiMocks.GET).toHaveBeenCalledWith("/api/v1/agents/install-jobs");
		});

		// The install runner writes the finished job straight into the cache, which
		// is the same path a polled success takes.
		queryClient.setQueryData(agentInstallJobsQueryKey, [
			{ status: "succeeded", target: "cursor", updatedAt: "2026-09-17T00:00:00Z" },
		]);

		await waitFor(() => {
			expect(invalidated).toContainEqual(agentsQueryKey);
		});
		expect(invalidateSpy).toHaveBeenCalled();
	});
});
