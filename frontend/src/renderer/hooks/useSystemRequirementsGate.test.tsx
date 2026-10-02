import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const apiBaseUrlState = vi.hoisted(() => ({
	url: "",
	listeners: new Set<() => void>(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: vi.fn(), POST: vi.fn() },
	apiErrorMessage: (error: unknown) => String(error),
	getApiBaseUrl: () => apiBaseUrlState.url,
	subscribeApiBaseUrl: (listener: () => void) => {
		apiBaseUrlState.listeners.add(listener);
		return () => apiBaseUrlState.listeners.delete(listener);
	},
}));

vi.mock("../lib/preview-mode", () => ({ usesPreviewWorkspaceData: false }));

import type { ShellTerminal } from "./useShellTerminals";
import { apiClient } from "../lib/api-client";
import { githubAuthAutoLoginOfferedQueryKey, githubAuthTerminalQueryKey, systemRequirementsQueryKey, useGitHubAuthAutoLoginOffered, useGitHubAuthRequirement, useGitHubAuthTerminal, useSystemRequirementsGate } from "./useSystemRequirementsGate";

const loginTerminal: ShellTerminal = {
	createdAt: "2026-09-06T00:00:00Z",
	handleId: "ptyhost-v1:shellterm-github-auth",
	title: "Connect GitHub",
	workingDir: "/tmp/auth-workspace",
};

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
	};
}

function setBaseUrl(url: string) {
	act(() => {
		apiBaseUrlState.url = url;
		apiBaseUrlState.listeners.forEach((listener) => listener());
	});
}

afterEach(() => {
	vi.useRealTimers();
	apiBaseUrlState.url = "";
	apiBaseUrlState.listeners.clear();
	vi.mocked(apiClient.GET).mockReset();
});

describe("useSystemRequirementsGate", () => {
	it("waits for a trusted daemon URL and checks fresh requirements after a restart", async () => {
		const getMock = vi.mocked(apiClient.GET);
		getMock.mockResolvedValueOnce({ data: { requirements: [] }, error: undefined } as never);
		const deferred: { resolve: (value: unknown) => void } = { resolve: () => undefined };
		getMock.mockReturnValueOnce(new Promise<unknown>((resolve) => {
			deferred.resolve = resolve;
		}) as never);
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useSystemRequirementsGate(), { wrapper: wrapper(queryClient) });

		await act(async () => Promise.resolve());
		expect(getMock).not.toHaveBeenCalled();
		expect(result.current.blocked).toBe(true);

		setBaseUrl("http://127.0.0.1:3037");
		await waitFor(() => expect(result.current.ready).toBe(true));
		expect(getMock).toHaveBeenCalledTimes(1);

		setBaseUrl("");
		setBaseUrl("http://127.0.0.1:4555");
		await waitFor(() => expect(getMock).toHaveBeenCalledTimes(2));
		expect(result.current.checking).toBe(true);
		expect(result.current.blocked).toBe(true);

		act(() => deferred.resolve({ data: { requirements: [{ required: true, satisfied: false }] }, error: undefined }));
		await waitFor(() => expect(result.current.requirementsBlocked).toBe(true));

		// The supervisor invalidates this cache on a daemon identity change,
		// including a restart that binds the same port again.
		setBaseUrl("");
		act(() => queryClient.removeQueries({ queryKey: systemRequirementsQueryKey }));
		getMock.mockResolvedValueOnce({ data: { requirements: [] }, error: undefined } as never);
		setBaseUrl("http://127.0.0.1:4555");
		await waitFor(() => expect(getMock).toHaveBeenCalledTimes(3));
		await waitFor(() => expect(result.current.ready).toBe(true));
	});
});

describe("useGitHubAuthTerminal", () => {
	// A `gh auth login` device flow routinely outlives React Query's five-minute
	// default gcTime: the user leaves AO, authenticates in a browser, then comes
	// back. The notice renders only on the home page and the empty board, so
	// opening a project unmounts the last observer of this query. If the handle
	// were collected the panel could not reattach, and the PTY would be orphaned
	// with no way to close it from the notice.
	it("retains the login terminal handle while no component observes it", () => {
		vi.useFakeTimers();
		const queryClient = new QueryClient();

		const { unmount } = renderHook(() => useGitHubAuthTerminal(), { wrapper: wrapper(queryClient) });
		queryClient.setQueryData<ShellTerminal | null>(githubAuthTerminalQueryKey, loginTerminal);
		expect(queryClient.getQueryData(githubAuthTerminalQueryKey)).toEqual(loginTerminal);

		unmount();
		vi.advanceTimersByTime(10 * 60 * 1000);

		expect(queryClient.getQueryData(githubAuthTerminalQueryKey)).toEqual(loginTerminal);
	});

	it("exposes an already-cached handle to a mounting notice", async () => {
		const queryClient = new QueryClient();
		queryClient.setQueryData<ShellTerminal | null>(githubAuthTerminalQueryKey, loginTerminal);

		const { result } = renderHook(() => useGitHubAuthTerminal(), { wrapper: wrapper(queryClient) });

		await waitFor(() => expect(result.current.data).toEqual(loginTerminal));
	});

	it("clears the handle once the flow is done", async () => {
		const queryClient = new QueryClient();
		queryClient.setQueryData<ShellTerminal | null>(githubAuthTerminalQueryKey, loginTerminal);

		const { result } = renderHook(() => useGitHubAuthTerminal(), { wrapper: wrapper(queryClient) });
		await waitFor(() => expect(result.current.data).toEqual(loginTerminal));

		result.current.clear();

		await waitFor(() => expect(result.current.data).toBeNull());
		expect(queryClient.getQueryData(githubAuthTerminalQueryKey)).toBeNull();
	});
});

describe("useGitHubAuthAutoLoginOffered", () => {
	it("retains a dismissed offer while the notice is unmounted", async () => {
		vi.useFakeTimers();
		const queryClient = new QueryClient();
		const first = renderHook(() => useGitHubAuthAutoLoginOffered(), { wrapper: wrapper(queryClient) });

		act(() => first.result.current.markOffered());
		expect(queryClient.getQueryData(githubAuthAutoLoginOfferedQueryKey)).toBe(true);
		first.unmount();
		vi.advanceTimersByTime(10 * 60 * 1000);

		expect(queryClient.getQueryData(githubAuthAutoLoginOfferedQueryKey)).toBe(true);
		const second = renderHook(() => useGitHubAuthAutoLoginOffered(), { wrapper: wrapper(queryClient) });
		expect(second.result.current.offered).toBe(true);
	});
});

describe("useGitHubAuthRequirement", () => {
	it("polls only while a GitHub login terminal is active", async () => {
		vi.useFakeTimers();
		const getMock = vi.mocked(apiClient.GET);
		getMock.mockResolvedValue({
			data: { id: "github-auth", label: "GitHub access", satisfied: false, required: false, detail: "Sign in." },
			error: undefined,
		} as never);
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { rerender } = renderHook(
			({ active }) => useGitHubAuthRequirement(active),
			{ initialProps: { active: true }, wrapper: wrapper(queryClient) },
		);

		await act(async () => vi.advanceTimersByTimeAsync(5_100));
		expect(getMock.mock.calls.length).toBeGreaterThanOrEqual(2);

		rerender({ active: false });
		const callsAfterStopping = getMock.mock.calls.length;
		await act(async () => vi.advanceTimersByTimeAsync(5_100));
		expect(getMock).toHaveBeenCalledTimes(callsAfterStopping);
	});
});
