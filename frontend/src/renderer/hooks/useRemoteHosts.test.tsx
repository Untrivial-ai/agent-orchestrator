import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";

const remotes = vi.hoisted(() => ({
	list: vi.fn(),
	connect: vi.fn(),
	disconnect: vi.fn(),
	connected: vi.fn(),
}));

vi.mock("../lib/bridge", () => ({ aoBridge: { remotes } }));

import { baseUrlForHost, connectedHosts } from "../lib/host-clients";
import { useConnectedHosts } from "./useHostConnection";
import { requestRemoteHostsRefresh, useRemoteHosts } from "./useRemoteHosts";

beforeEach(() => {
	remotes.list.mockReset().mockResolvedValue([
		{ hostId: "box-a", label: "Box A", url: "http://box-a:3001" },
		{ hostId: "box-b", label: "Box B", url: "http://box-b:3001" },
	]);
	remotes.connect.mockReset().mockImplementation(async (url: string) => ({
		hostId: url.includes("box-a") ? "box-a" : "box-b",
		label: url.includes("box-a") ? "Box A" : "Box B",
		url,
		base: "http://127.0.0.1:4000",
	}));
	remotes.disconnect.mockReset().mockResolvedValue(undefined);
	useUiStore.setState({ developerMode: true, remoteHosts: false });
});

afterEach(() => {
	vi.useRealTimers();
	useUiStore.setState({ developerMode: false, remoteHosts: false });
});

it("does not connect to saved boxes until Remote hosts is enabled", async () => {
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts).toHaveLength(0));
	expect(remotes.list).not.toHaveBeenCalled();
	expect(remotes.connect).not.toHaveBeenCalled();
});

it("keeps remote hosts disconnected until Developer mode is enabled", async () => {
	useUiStore.setState({ developerMode: false, remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	const connections = renderHook(() => useConnectedHosts());
	expect(result.current.hosts).toEqual([]);
	expect(connections.result.current).toEqual([]);
	expect(remotes.list).not.toHaveBeenCalled();
	act(() => useUiStore.setState({ developerMode: true }));
	await waitFor(() => expect(result.current.hosts).toHaveLength(2));
	expect(connections.result.current).toEqual(["box-a", "box-b"]);
	act(() => useUiStore.setState({ developerMode: false }));
	await waitFor(() => expect(result.current.hosts).toEqual([]));
	expect(connections.result.current).toEqual([]);
	expect(connectedHosts()).toEqual([]);
});

it("connects both saved boxes and exposes their stable IDs", async () => {
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts).toEqual([
		{ hostId: "box-a", label: "Box A", url: "http://box-a:3001", status: "connected" },
		{ hostId: "box-b", label: "Box B", url: "http://box-b:3001", status: "connected" },
	]));
});

it("disconnects the prior proxy when a connected host becomes unreachable", async () => {
	remotes.list.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: "http://box-a:3001" }]);
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts[0]?.status).toBe("connected"));
	remotes.connect.mockRejectedValueOnce(new Error("host offline"));
	await act(async () => result.current.refresh());
	expect(result.current.hosts[0]?.status).toBe("offline");
	expect(remotes.disconnect).toHaveBeenCalledWith("http://box-a:3001");
	expect(connectedHosts()).not.toContain("box-a");
});

it("shows password rejected when a connected host rejects its saved credential", async () => {
	remotes.list.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: "http://box-a:3001" }]);
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts[0]?.status).toBe("connected"));
	remotes.connect.mockRejectedValueOnce(new Error("host http://box-a:3001 is unauthorized"));
	await act(async () => result.current.refresh());
	expect(result.current.hosts[0]).toMatchObject({ status: "offline", failureReason: "unauthorized" });
	expect(connectedHosts()).not.toContain("box-a");
});

it("turns a connected host offline when a remote request asks for a connection recheck", async () => {
	remotes.list.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: "http://box-a:3001" }]);
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts[0]?.status).toBe("connected"));
	remotes.connect.mockRejectedValueOnce(new Error("host http://box-a:3001 is unauthorized"));
	act(() => requestRemoteHostsRefresh());
	await waitFor(() => expect(result.current.hosts[0]).toMatchObject({ status: "offline", failureReason: "unauthorized" }));
	expect(connectedHosts()).not.toContain("box-a");
});

it("shows an incompatible API version after reconnect without retaining the old proxy", async () => {
	remotes.list.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: "http://box-a:3001" }]);
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts[0]?.status).toBe("connected"));
	remotes.connect.mockRejectedValueOnce(new Error("host http://box-a:3001 is incompatible"));
	await act(async () => result.current.refresh());
	expect(result.current.hosts[0]).toMatchObject({ status: "offline", failureReason: "incompatible" });
	expect(connectedHosts()).not.toContain("box-a");
});

it("keeps healthy hosts visible while rechecking a failed one", async () => {
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts.every((host) => host.status === "connected")).toBe(true));
	let failA!: (reason: Error) => void;
	let finishB!: (host: { hostId: string; label: string; url: string; base: string }) => void;
	remotes.connect.mockImplementationOnce(() => new Promise((_, reject) => { failA = reject; }));
	remotes.connect.mockImplementationOnce(() => new Promise((resolve) => { finishB = resolve; }));
	let refresh!: Promise<void>;
	await act(async () => { refresh = result.current.refresh(); await Promise.resolve(); });
	expect(result.current.hosts.find((host) => host.hostId === "box-b")?.status).toBe("connected");
	await act(async () => {
		finishB({ hostId: "box-b", label: "Box B", url: "http://box-b:3001", base: "http://127.0.0.1:4000" });
		failA(new Error("host http://box-a:3001 is offline"));
		await refresh;
	});
	expect(result.current.hosts.map((host) => [host.hostId, host.status])).toEqual([["box-a", "offline"], ["box-b", "connected"]]);
});

it("keeps B connected when A's corrupted saved address points at B", async () => {
	const sharedUrl = "http://box-b:3001";
	remotes.list.mockResolvedValue([
		{ hostId: "box-a", label: "Box A", url: sharedUrl },
		{ hostId: "box-b", label: "Box B", url: sharedUrl },
	]);
	remotes.connect.mockImplementation(async (url: string, hostId?: string) => {
		if (hostId === "box-a") throw new Error("remote host identity changed");
		return { hostId: "box-b", label: "Box B", url, base: "http://127.0.0.1:4000" };
	});
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts.map((host) => [host.hostId, host.status])).toEqual([
		["box-a", "offline"], ["box-b", "connected"],
	]));
	expect(connectedHosts()).toContain("box-b");
	expect(remotes.disconnect).not.toHaveBeenCalledWith(sharedUrl);
});

it("does not disconnect a newer successful connection after an older refresh fails", async () => {
	remotes.list.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: "http://box-a:3001" }]);
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts[0]?.status).toBe("connected"));
	let failOld!: (reason: Error) => void;
	remotes.connect.mockImplementationOnce(() => new Promise((_, reject) => { failOld = reject; }));
	const oldRefresh = result.current.refresh();
	await waitFor(() => expect(remotes.connect).toHaveBeenCalledTimes(2));
	const newRefresh = result.current.refresh();
	await act(async () => { await newRefresh; failOld(new Error("stale failure")); await oldRefresh; });
	expect(result.current.hosts[0]?.status).toBe("connected");
	expect(connectedHosts()).toContain("box-a");
	expect(remotes.disconnect).not.toHaveBeenCalledWith("http://box-a:3001");
});

it("does not replace a newer proxy when an older refresh succeeds late", async () => {
	const oldUrl = "http://box-a:3001";
	const newUrl = "http://box-a-new:3001";
	remotes.list.mockReset().mockResolvedValueOnce([{ hostId: "box-a", label: "Box A", url: oldUrl }])
		.mockResolvedValueOnce([{ hostId: "box-a", label: "Box A", url: oldUrl }])
		.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: newUrl }]);
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(result.current.hosts[0]?.status).toBe("connected"));
	let finishOld!: (host: { hostId: string; label: string; url: string; base: string }) => void;
	remotes.connect.mockImplementationOnce(() => new Promise((resolve) => { finishOld = resolve; }))
		.mockResolvedValueOnce({ hostId: "box-a", label: "Box A", url: newUrl, base: "http://127.0.0.1:4001" });
	let oldRefresh!: Promise<void>;
	await act(async () => { oldRefresh = result.current.refresh(); await Promise.resolve(); });
	await waitFor(() => expect(remotes.connect).toHaveBeenCalledTimes(2));
	await act(async () => { await result.current.refresh(); });
	expect(baseUrlForHost("box-a")).toBe("http://127.0.0.1:4001");
	await act(async () => { finishOld({ hostId: "box-a", label: "Box A", url: oldUrl, base: "http://127.0.0.1:4000" }); await oldRefresh; });
	expect(baseUrlForHost("box-a")).toBe("http://127.0.0.1:4001");
	expect(remotes.disconnect).not.toHaveBeenCalledWith(newUrl);
});

it("closes a connection that finishes after the feature is disabled", async () => {
	let finishConnect!: (value: { hostId: string; label: string; url: string; base: string }) => void;
	remotes.list.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: "http://box-a:3001" }]);
	remotes.connect.mockImplementation(() => new Promise((resolve) => { finishConnect = resolve; }));
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await waitFor(() => expect(remotes.connect).toHaveBeenCalledOnce());
	act(() => useUiStore.setState({ remoteHosts: false }));
	await act(async () => finishConnect({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" }));
	await waitFor(() => expect(remotes.disconnect).toHaveBeenCalledWith("http://box-a:3001"));
	expect(result.current.hosts).toEqual([]);
});

it("reconnects a saved host that was offline at startup without retrying healthy hosts", async () => {
	vi.useFakeTimers();
	let boxAOnline = false;
	const connect = remotes.connect.getMockImplementation()!;
	remotes.connect.mockImplementation((url: string, hostId: string) =>
		url.includes("box-a") && !boxAOnline
			? Promise.reject(new Error(`host ${url} is offline`))
			: connect(url, hostId),
	);
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await act(async () => { await Promise.resolve(); });
	expect(result.current.hosts.map(({ status }) => status)).toEqual(["offline", "connected"]);
	boxAOnline = true;
	await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
	expect(result.current.hosts.map(({ status }) => status)).toEqual(["connected", "connected"]);
	expect(remotes.connect.mock.calls.filter(([url]) => url.includes("box-b"))).toHaveLength(1);
});

it("does not automatically retry a saved host with an invalid password", async () => {
	vi.useFakeTimers();
	remotes.list.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: "http://box-a:3001" }]);
	remotes.connect.mockRejectedValue(new Error("host http://box-a:3001 is unauthorized"));
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await act(async () => { await Promise.resolve(); });
	expect(result.current.hosts[0]?.status).toBe("offline");
	expect(result.current.hosts[0]?.failureReason).toBe("unauthorized");
	await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
	expect(remotes.connect).toHaveBeenCalledTimes(1);
});

it("does not replace a newer proxy when an old offline retry finishes late", async () => {
	vi.useFakeTimers();
	const oldUrl = "http://box-a:3001";
	const newUrl = "http://box-a-new:3001";
	remotes.list.mockResolvedValueOnce([{ hostId: "box-a", label: "Box A", url: oldUrl }]);
	let finishRetry!: (host: { hostId: string; label: string; url: string; base: string }) => void;
	remotes.connect.mockRejectedValueOnce(new Error(`host ${oldUrl} is offline`))
		.mockImplementationOnce(() => new Promise((resolve) => { finishRetry = resolve; }))
		.mockResolvedValueOnce({ hostId: "box-a", label: "Box A", url: newUrl, base: "http://127.0.0.1:4001" });
	useUiStore.setState({ remoteHosts: true });
	const { result } = renderHook(() => useRemoteHosts());
	await act(async () => { await Promise.resolve(); });
	expect(result.current.hosts[0]?.status).toBe("offline");
	await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
	remotes.list.mockResolvedValue([{ hostId: "box-a", label: "Box A", url: newUrl }]);
	await act(async () => { await result.current.refresh(); });
	expect(baseUrlForHost("box-a")).toBe("http://127.0.0.1:4001");
	await act(async () => finishRetry({ hostId: "box-a", label: "Box A", url: oldUrl, base: "http://127.0.0.1:4000" }));
	expect(baseUrlForHost("box-a")).toBe("http://127.0.0.1:4001");
	expect(remotes.disconnect).toHaveBeenCalledWith(oldUrl);
	expect(remotes.disconnect).not.toHaveBeenCalledWith(newUrl);
});
