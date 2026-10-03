import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const remotes = vi.hoisted(() => ({ connect: vi.fn(), disconnect: vi.fn(async () => undefined) }));
vi.mock("../lib/bridge", () => ({ aoBridge: { remotes } }));

import { connectHost, connectedHosts, disconnectHost } from "../lib/host-clients";
import { useUiStore } from "../stores/ui-store";
import { remoteNotificationsQueryKey, useRemoteNotificationHosts } from "./useRemoteNotifications";

function wrapper({ children }: { children: ReactNode }) {
	return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>;
}

async function connect(hostId: string, port: number) {
	const url = `http://${hostId}:3001`;
	remotes.connect.mockResolvedValueOnce({ hostId, label: `Host ${hostId}`, url, base: `http://127.0.0.1:${port}` });
	await connectHost(url);
}

beforeEach(() => useUiStore.setState({ developerMode: true, remoteHosts: true }));

afterEach(async () => {
	useUiStore.setState({ developerMode: false, remoteHosts: false });
	for (const hostId of connectedHosts()) await disconnectHost(hostId);
	remotes.connect.mockReset();
	vi.unstubAllGlobals();
});

it("keeps same-ID notifications and unread counts separate by host, then drops disconnected hosts", async () => {
	const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		const hostId = url.port === "4101" ? "A" : "B";
		return new Response(JSON.stringify({
			notifications: [{ id: "same-id", title: `From ${hostId}`, status: "unread" }],
			unreadCount: hostId === "A" ? 2 : 3,
			unresolvedCount: 0,
		}), { status: 200, headers: { "content-type": "application/json" } });
	});
	vi.stubGlobal("fetch", fetchMock);
	await connect("A", 4101);
	await connect("B", 4102);

	const { result } = renderHook(() => useRemoteNotificationHosts("unread"), { wrapper });
	await waitFor(() => expect(result.current.totalUnreadCount).toBe(5));
	expect(result.current.hosts.map(({ hostId, label, data }) => [hostId, label, data?.notifications[0]?.title])).toEqual([
		["A", "Host A", "From A"],
		["B", "Host B", "From B"],
	]);
	expect(remoteNotificationsQueryKey("A", "unread")).not.toEqual(remoteNotificationsQueryKey("B", "unread"));
	expect(fetchMock.mock.calls.every(([input]) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		return url.searchParams.get("status") === "unread" && url.searchParams.get("limit") === "100";
	})).toBe(true);

	await act(async () => { await disconnectHost("B"); });
	await waitFor(() => expect(result.current.hosts.map((host) => host.hostId)).toEqual(["A"]));
	expect(result.current.totalUnreadCount).toBe(2);
});

it("does not fetch while disabled and reports a failed host without masking healthy hosts", async () => {
	const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		if (url.port === "4102") return new Response('{"message":"offline"}', { status: 503, headers: { "content-type": "application/json" } });
		return new Response(JSON.stringify({ notifications: [], unreadCount: 4, unresolvedCount: 0 }), {
			status: 200, headers: { "content-type": "application/json" },
		});
	});
	vi.stubGlobal("fetch", fetchMock);
	await connect("A", 4101);
	await connect("B", 4102);

	const { result, rerender } = renderHook(({ enabled }) => useRemoteNotificationHosts("all", enabled), {
		initialProps: { enabled: false }, wrapper,
	});
	expect(fetchMock).not.toHaveBeenCalled();
	expect(result.current.totalUnreadCount).toBe(0);
	rerender({ enabled: true });
	await waitFor(() => expect(result.current.hosts[1]?.isError).toBe(true), { timeout: 3_000 });
	expect(result.current.hosts[0]?.data?.unreadCount).toBe(4);
	expect(result.current.hosts[1]?.data).toBeUndefined();
	expect(result.current.totalUnreadCount).toBe(0);
	expect(remoteNotificationsQueryKey("A", "all")).not.toEqual(remoteNotificationsQueryKey("A", "unread"));
});

it("does not count stale unread data after a host goes offline", async () => {
	let offline = false;
	vi.stubGlobal("fetch", vi.fn(async () => new Response(
		offline ? '{"message":"offline"}' : '{"notifications":[],"unreadCount":4,"unresolvedCount":0}',
		{ status: offline ? 503 : 200, headers: { "content-type": "application/json" } },
	)));
	await connect("A", 4101);
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const { result } = renderHook(() => useRemoteNotificationHosts("unread"), {
		wrapper: ({ children }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>,
	});
	await waitFor(() => expect(result.current.totalUnreadCount).toBe(4));

	offline = true;
	await act(async () => { await queryClient.invalidateQueries({ queryKey: remoteNotificationsQueryKey("A", "unread") }); });
	await waitFor(() => expect(result.current.hosts[0]?.isError).toBe(true), { timeout: 3_000 });
	expect(result.current.hosts[0]?.data).toBeUndefined();
	expect(result.current.totalUnreadCount).toBe(0);
});
