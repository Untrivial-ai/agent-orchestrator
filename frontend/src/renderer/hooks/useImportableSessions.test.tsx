import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { useImportableSessions } from "./useImportableSessions";
const h = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: h.get },
	apiErrorMessage: (error: Error) => error.message,
}));
beforeEach(() => {
	h.get.mockReset().mockResolvedValue({ data: { sessions: [] } });
});
function setup() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return {
		client,
		wrapper: ({ children }: PropsWithChildren) => (
			<QueryClientProvider client={client}>{children}</QueryClientProvider>
		),
	};
}
it("does not discover until the project's dialog is opened and caches reopening", async () => {
	const { wrapper, client } = setup();
	const view = renderHook(({ open }) => useImportableSessions("a", open), {
		wrapper,
		initialProps: { open: false },
	});
	expect(h.get).not.toHaveBeenCalled();
	view.rerender({ open: true });
	expect(view.result.current.isLoading).toBe(true);
	expect(h.get).toHaveBeenCalledWith(
		"/api/v1/sessions/importable",
		expect.objectContaining({ params: { query: { projectId: "a" } } }),
	);
	await waitFor(() => expect(view.result.current.isSuccess).toBe(true));
	view.rerender({ open: false });
	view.rerender({ open: true });
	expect(h.get).toHaveBeenCalledTimes(1);
	view.unmount();
	client.clear();
});
it("cancels old project discovery on navigation and never reuses its results", async () => {
	h.get.mockImplementation((_path, init) =>
		init.params.query.projectId === "a"
			? new Promise(() => {})
			: Promise.resolve({ data: { sessions: [] } }),
	);
	const { wrapper, client } = setup();
	const view = renderHook(({ id }) => useImportableSessions(id), {
		wrapper,
		initialProps: { id: "a" },
	});
	const signal = h.get.mock.calls[0][1].signal;
	view.rerender({ id: "b" });
	expect(signal.aborted).toBe(true);
	expect(view.result.current.data).toBeUndefined();
	await waitFor(() => expect(view.result.current.isSuccess).toBe(true));
	expect(h.get.mock.calls[1][1].params.query).toEqual({ projectId: "b" });
	view.unmount();
	client.clear();
});
it("does not issue global discovery for an empty project id", () => {
	const { wrapper, client } = setup();
	const view = renderHook(() => useImportableSessions(""), { wrapper });
	expect(h.get).not.toHaveBeenCalled();
	view.unmount();
	client.clear();
});
