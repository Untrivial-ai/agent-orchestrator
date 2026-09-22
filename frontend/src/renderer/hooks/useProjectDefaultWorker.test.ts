import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useProjectDefaultWorker } from "./useProjectDefaultWorker";

const get = vi.hoisted(() => vi.fn());
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: get },
	apiErrorMessage: (error: { error?: { message?: string } }) => error?.error?.message ?? "request failed",
}));

function wrapper(client: QueryClient) {
	return ({ children }: { children: ReactNode }) =>
		createElement(QueryClientProvider, { client }, children);
}

describe("useProjectDefaultWorker", () => {
	beforeEach(() => get.mockReset());

	it("resolves the project worker agent ahead of the daemon-wide default", async () => {
		get.mockResolvedValue({
			data: {
				status: "ok",
				project: { id: "demo", agent: "claude-code", config: { worker: { agent: "codex" } } },
			},
		});
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useProjectDefaultWorker("demo"), { wrapper: wrapper(client) });
		await waitFor(() => expect(result.current).toBe("codex"));
		expect(get).toHaveBeenCalledWith("/api/v1/projects/{id}", { params: { path: { id: "demo" } } });
	});

	it("falls back to the project default agent when no worker override exists", async () => {
		get.mockResolvedValue({
			data: { status: "ok", project: { id: "demo", agent: "claude-code", config: {} } },
		});
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useProjectDefaultWorker("demo"), { wrapper: wrapper(client) });
		await waitFor(() => expect(result.current).toBe("claude-code"));
	});

	it("stays empty without a project and does not fetch", () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useProjectDefaultWorker(""), { wrapper: wrapper(client) });
		expect(result.current).toBe("");
		expect(get).not.toHaveBeenCalled();
	});
});
