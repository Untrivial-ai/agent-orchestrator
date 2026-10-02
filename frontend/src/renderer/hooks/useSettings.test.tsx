import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	apiErrorMessage: () => "request failed",
}));

import { useSettings } from "./useSettings";

function wrapper(queryClient: QueryClient) {
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

beforeEach(() => {
	getMock.mockReset();
});

describe("useSettings tracker intake gate", () => {
	it.each([
		[true, true],
		[false, false],
		[undefined, false],
	])("maps trackerIntakeEnabled=%s to %s", async (trackerIntakeEnabled, expected) => {
		getMock.mockResolvedValue({
			data: { trackerIntakeEnabled },
			error: undefined,
		});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useSettings(), { wrapper: wrapper(queryClient) });

		await waitFor(() => expect(result.current.settings?.trackerIntakeEnabled).toBe(expected));
		expect(getMock).toHaveBeenCalledWith("/api/v1/settings");
	});
});
