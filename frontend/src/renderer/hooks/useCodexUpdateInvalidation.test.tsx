import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { apiClient } from "../lib/api-client";
import { useCodexUpdateInvalidation } from "./useCodexUpdateInvalidation";

afterEach(() => vi.restoreAllMocks());
it.each(["succeeded", "failed", "interrupted"])("invalidates every project catalog after %s with Settings unmounted", async (status) => {
	vi.spyOn(apiClient, "GET").mockResolvedValue({ data: { jobs: [{ target: "codex", method: "update:npm", status, startedAt: "start", finishedAt: "finish" }] } } as never);
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const invalidate = vi.spyOn(client, "invalidateQueries");
	const result = renderHook(useCodexUpdateInvalidation, { wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> });
	await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["agent-models", "codex"] }));
	expect(invalidate).toHaveBeenCalledWith({ queryKey: ["agent-readiness"] });
	expect(invalidate).toHaveBeenCalledWith({ queryKey: ["codex-update"] });
	result.unmount();
});
