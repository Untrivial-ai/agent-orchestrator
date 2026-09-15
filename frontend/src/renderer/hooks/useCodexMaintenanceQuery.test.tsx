import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

import { fetchCodexMaintenance, startCodexUpdate, useCodexMaintenanceQuery, type CodexMaintenanceStatus } from "./useCodexMaintenanceQuery";

const status: CodexMaintenanceStatus = {
	ownership: "npm",
	installedVersion: "0.149.1",
	latestVersion: "0.153.4",
	updateAvailable: true,
	updateSupported: true,
	updateCommand: "npm install -g @openai/codex@latest",
	checkedAt: "2026-09-14T00:00:00Z",
};

function wrapper(queryClient: QueryClient) {
	return ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	getMock.mockReset().mockResolvedValue({ data: status });
	postMock.mockReset().mockResolvedValue({ data: { target: "codex", status: "installing", method: "npm" } });
});

describe("Codex maintenance query", () => {
	it("reads the maintenance advisory endpoint", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useCodexMaintenanceQuery(), { wrapper: wrapper(queryClient) });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(getMock).toHaveBeenCalledWith("/api/v1/agents/codex/maintenance");
		expect(result.current.data?.updateAvailable).toBe(true);
	});

	it("throws a readable error when the advisory request fails", async () => {
		getMock.mockResolvedValue({ error: { code: "boom" } });
		await expect(fetchCodexMaintenance()).rejects.toThrow("Could not load the Codex update advisory.");
	});

	it("posts the expected ownership when starting an update", async () => {
		await startCodexUpdate("npm");
		expect(postMock).toHaveBeenCalledWith("/api/v1/agents/codex/maintenance/update", { body: { expectedOwnership: "npm" } });
	});

	it("omits the body when no expected ownership is known", async () => {
		await startCodexUpdate();
		expect(postMock).toHaveBeenCalledWith("/api/v1/agents/codex/maintenance/update", { body: undefined });
	});
});
