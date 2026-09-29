import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useAutomations } from "./useAutomations";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	apiErrorMessage: () => "request failed",
}));

function wrapper(queryClient: QueryClient) {
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

describe("useAutomations", () => {
	beforeEach(() => {
		getMock.mockReset();
	});

	it("fetches every automation page", async () => {
		getMock
			.mockResolvedValueOnce({ data: { automations: [{ id: "automation-1" }], nextCursor: "cursor-2" }, error: undefined })
			.mockResolvedValueOnce({ data: { automations: [{ id: "automation-2" }] }, error: undefined });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

		const { result } = renderHook(() => useAutomations(), { wrapper: wrapper(queryClient) });

		await waitFor(() => expect(result.current.data?.map((item) => item.id)).toEqual(["automation-1", "automation-2"]));
		expect(getMock).toHaveBeenNthCalledWith(1, "/api/v1/automations", { params: { query: { limit: 100, cursor: undefined } } });
		expect(getMock).toHaveBeenNthCalledWith(2, "/api/v1/automations", { params: { query: { limit: 100, cursor: "cursor-2" } } });
	});
});
