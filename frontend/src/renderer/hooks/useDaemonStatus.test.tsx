import { renderHook, waitFor } from "@testing-library/react";
import { act } from "react";
import type { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DaemonStatus } from "../../shared/daemon-status";

const {
	getStatusMock,
	onStatusMock,
	removeStatusMock,
	connectMock,
	stopTransportMock,
	setApiBaseUrlMock,
	setApiDaemonStatusMock,
	ensureAgentReadinessMock,
	cacheAgentReadinessMock,
} = vi.hoisted(() => ({
	getStatusMock: vi.fn(),
	onStatusMock: vi.fn(),
	removeStatusMock: vi.fn(),
	connectMock: vi.fn(),
	stopTransportMock: vi.fn(),
	setApiBaseUrlMock: vi.fn(),
	setApiDaemonStatusMock: vi.fn(),
	ensureAgentReadinessMock: vi.fn(),
	cacheAgentReadinessMock: vi.fn(),
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: { daemon: { getStatus: getStatusMock, onStatus: onStatusMock } },
}));

vi.mock("../lib/event-transport", () => ({
	createEventTransport: vi.fn(() => ({ connect: connectMock })),
}));

vi.mock("../lib/api-client", () => ({
	setApiBaseUrl: setApiBaseUrlMock,
	setApiDaemonStatus: setApiDaemonStatusMock,
}));

vi.mock("./useAgentReadinessQuery", () => ({
	agentReadinessQueryKey: ["agent-readiness"],
	ensureAgentReadiness: ensureAgentReadinessMock,
	cacheAgentReadiness: cacheAgentReadinessMock,
}));

import { setEventsConnectionState } from "../lib/events-connection";
import { useDaemonStatus } from "./useDaemonStatus";

function fakeQueryClient(): QueryClient {
	return { invalidateQueries: vi.fn(), removeQueries: vi.fn(), setQueryData: vi.fn() } as unknown as QueryClient;
}

beforeEach(() => {
	vi.useRealTimers();
	setEventsConnectionState("idle");
	getStatusMock.mockReset().mockResolvedValue({ state: "stopped" });
	onStatusMock.mockReset().mockReturnValue(removeStatusMock);
	removeStatusMock.mockReset();
	connectMock.mockReset().mockReturnValue(stopTransportMock);
	stopTransportMock.mockReset();
	setApiBaseUrlMock.mockReset();
	setApiDaemonStatusMock.mockReset();
	ensureAgentReadinessMock.mockReset().mockResolvedValue({ agents: [] });
	cacheAgentReadinessMock.mockReset();
});

afterEach(() => {
	vi.useRealTimers();
});

describe("useDaemonStatus", () => {
	it("applies the initial status, points REST at the reported port, and connects the transport", async () => {
		getStatusMock.mockResolvedValue({ state: "ready", port: 3037 });
		const queryClient = fakeQueryClient();

		const { result } = renderHook(() => useDaemonStatus(queryClient));

		await waitFor(() => expect(result.current).toEqual({ state: "ready", port: 3037 }));
		expect(setApiBaseUrlMock).toHaveBeenCalledWith("http://127.0.0.1:3037");
		expect(connectMock).toHaveBeenCalledTimes(1);
		// Refetching is the (debounced) event transport's job — no direct invalidate.
		expect(queryClient.invalidateQueries).not.toHaveBeenCalled();
	});

	it("quarantines the base URL for statuses without a port", async () => {
		getStatusMock.mockResolvedValue({ state: "stopped", message: "daemon not configured" });
		const queryClient = fakeQueryClient();

		const { result } = renderHook(() => useDaemonStatus(queryClient));

		await waitFor(() => expect(result.current.message).toBe("daemon not configured"));
		expect(setApiBaseUrlMock).toHaveBeenCalledWith(null);
	});

	it("quarantines REST for an incompatible daemon even when its port is known", async () => {
		getStatusMock.mockResolvedValue({ state: "error", port: 3001, message: "wrong daemon" });
		const queryClient = fakeQueryClient();

		const { result } = renderHook(() => useDaemonStatus(queryClient));

		await waitFor(() => expect(result.current).toEqual({ state: "error", port: 3001, message: "wrong daemon" }));
		expect(setApiBaseUrlMock).toHaveBeenCalledWith(null);
	});

	it("applies pushed status events from the bridge", async () => {
		const queryClient = fakeQueryClient();
		const { result } = renderHook(() => useDaemonStatus(queryClient));
		await waitFor(() => expect(onStatusMock).toHaveBeenCalled());
		const pushStatus = onStatusMock.mock.calls[0][0] as (status: DaemonStatus) => void;

		act(() => pushStatus({ state: "ready", port: 4555 }));

		expect(result.current).toEqual({ state: "ready", port: 4555 });
		expect(setApiBaseUrlMock).toHaveBeenCalledWith("http://127.0.0.1:4555");
	});

	it("clears readiness when the daemon identity changes or becomes unavailable", async () => {
		getStatusMock.mockResolvedValue({ state: "ready", port: 4555, pid: 101 });
		const queryClient = fakeQueryClient();
		const { result } = renderHook(() => useDaemonStatus(queryClient));
		await waitFor(() => expect(result.current).toEqual({ state: "ready", port: 4555, pid: 101 }));
		const pushStatus = onStatusMock.mock.calls[0][0] as (status: DaemonStatus) => void;

		act(() => pushStatus({ state: "ready", port: 4555, pid: 102 }));
		act(() => pushStatus({ state: "stopped" }));

		expect(queryClient.removeQueries).toHaveBeenCalledWith({
			queryKey: ["agent-readiness"],
			exact: true,
		});
		expect(queryClient.removeQueries).toHaveBeenCalledWith({
			queryKey: ["codex-accounts"],
			exact: true,
		});
		expect(queryClient.removeQueries).toHaveBeenCalledTimes(6);
	});

	it("ensures display readiness when the window regains focus", async () => {
		getStatusMock.mockResolvedValue({ state: "ready", port: 4555, pid: 101 });
		const queryClient = fakeQueryClient();
		renderHook(() => useDaemonStatus(queryClient));
		await waitFor(() => expect(onStatusMock).toHaveBeenCalled());

		act(() => window.dispatchEvent(new Event("focus")));

		await waitFor(() => expect(ensureAgentReadinessMock).toHaveBeenCalledWith([], "display"));
		expect(cacheAgentReadinessMock).toHaveBeenCalledWith(queryClient, { agents: [] });
	});

	it("refreshes non-ready status until the daemon is ready", async () => {
		vi.useFakeTimers();
		getStatusMock.mockResolvedValueOnce({ state: "starting" }).mockResolvedValueOnce({ state: "ready", port: 4777 });
		const queryClient = fakeQueryClient();

		const { result } = renderHook(() => useDaemonStatus(queryClient));

		await act(async () => {
			await Promise.resolve();
		});
		expect(result.current).toEqual({ state: "starting" });
		await act(async () => {
			await vi.advanceTimersByTimeAsync(2_000);
		});

		expect(result.current).toEqual({ state: "ready", port: 4777 });
		expect(getStatusMock).toHaveBeenCalledTimes(2);
		expect(setApiBaseUrlMock).toHaveBeenCalledWith("http://127.0.0.1:4777");
	});

	it("refreshes ready status so adopted daemon liveness is rechecked", async () => {
		vi.useFakeTimers();
		getStatusMock.mockResolvedValueOnce({ state: "ready", port: 4777 }).mockResolvedValueOnce({ state: "stopped" });
		const queryClient = fakeQueryClient();

		const { result } = renderHook(() => useDaemonStatus(queryClient));

		await act(async () => {
			await Promise.resolve();
		});
		expect(result.current).toEqual({ state: "ready", port: 4777 });
		await act(async () => {
			await vi.advanceTimersByTimeAsync(10_000);
		});

		expect(result.current).toEqual({ state: "stopped" });
		expect(getStatusMock).toHaveBeenCalledTimes(2);
		expect(setApiBaseUrlMock).toHaveBeenCalledWith(null);
	});

	it("ignores stale refresh responses that complete after a newer refresh", async () => {
		let resolveFirst: (status: DaemonStatus) => void = () => undefined;
		getStatusMock
			.mockReturnValueOnce(
				new Promise<DaemonStatus>((resolve) => {
					resolveFirst = resolve;
				}),
			)
			.mockResolvedValueOnce({ state: "ready", port: 4777 });
		const queryClient = fakeQueryClient();

		const { result } = renderHook(() => useDaemonStatus(queryClient));

		act(() => window.dispatchEvent(new Event("focus")));
		await act(async () => {
			await Promise.resolve();
		});
		expect(result.current).toEqual({ state: "ready", port: 4777 });

		await act(async () => {
			resolveFirst({ state: "stopped" });
			await Promise.resolve();
		});

		expect(result.current).toEqual({ state: "ready", port: 4777 });
		expect(setApiBaseUrlMock).toHaveBeenLastCalledWith("http://127.0.0.1:4777");
	});

	it("detects an external daemon exit on event disconnect before the next poll", async () => {
		vi.useFakeTimers();
		getStatusMock
			.mockResolvedValueOnce({ state: "ready", port: 4777, pid: 101 })
			.mockResolvedValueOnce({ state: "stopped", code: "daemon_unreachable" });
		const queryClient = fakeQueryClient();
		const { result } = renderHook(() => useDaemonStatus(queryClient));
		await act(async () => { await Promise.resolve(); });
		expect(result.current).toEqual({ state: "ready", port: 4777, pid: 101 });
		act(() => setEventsConnectionState("connected"));
		expect(getStatusMock).toHaveBeenCalledTimes(1);

		await act(async () => setEventsConnectionState("disconnected"));

		expect(getStatusMock).toHaveBeenCalledTimes(2);
		expect(result.current).toEqual({ state: "stopped", code: "daemon_unreachable" });
		expect(setApiDaemonStatusMock).toHaveBeenLastCalledWith(result.current);
		expect(setApiBaseUrlMock).toHaveBeenLastCalledWith(null);
	});

	it("preserves ready status while a disconnect probe confirms a healthy daemon", async () => {
		vi.useFakeTimers();
		const ready: DaemonStatus = { state: "ready", port: 4555, pid: 101 };
		let resolveProbe: (status: DaemonStatus) => void = () => undefined;
		getStatusMock.mockResolvedValueOnce(ready).mockReturnValueOnce(
			new Promise<DaemonStatus>((resolve) => { resolveProbe = resolve; }),
		);
		const queryClient = fakeQueryClient();
		const { result } = renderHook(() => useDaemonStatus(queryClient));
		await act(async () => { await Promise.resolve(); });
		act(() => setEventsConnectionState("connected"));
		setApiBaseUrlMock.mockClear();
		setApiDaemonStatusMock.mockClear();
		vi.mocked(queryClient.removeQueries).mockClear();

		act(() => setEventsConnectionState("disconnected"));
		expect(getStatusMock).toHaveBeenCalledTimes(2);
		expect(result.current).toEqual(ready);
		expect(setApiDaemonStatusMock).not.toHaveBeenCalled();
		expect(setApiBaseUrlMock).not.toHaveBeenCalled();

		await act(async () => resolveProbe(ready));

		expect(result.current).toEqual(ready);
		expect(setApiDaemonStatusMock).toHaveBeenCalledExactlyOnceWith(ready);
		expect(setApiBaseUrlMock).toHaveBeenCalledExactlyOnceWith("http://127.0.0.1:4555");
		expect(queryClient.removeQueries).not.toHaveBeenCalled();
	});

	it.each([4555, 5050])("ignores a late disconnect probe after a pushed restart on port %i", async (port) => {
		vi.useFakeTimers();
		let resolveProbe: (status: DaemonStatus) => void = () => undefined;
		getStatusMock
			.mockResolvedValueOnce({ state: "ready", port: 4555, pid: 101 })
			.mockReturnValueOnce(new Promise<DaemonStatus>((resolve) => { resolveProbe = resolve; }));
		const queryClient = fakeQueryClient();
		const { result } = renderHook(() => useDaemonStatus(queryClient));
		await act(async () => { await Promise.resolve(); });
		const pushStatus = onStatusMock.mock.calls[0][0] as (status: DaemonStatus) => void;
		act(() => setEventsConnectionState("connected"));
		act(() => setEventsConnectionState("disconnected"));
		expect(getStatusMock).toHaveBeenCalledTimes(2);

		const restarted: DaemonStatus = { state: "ready", port, pid: 202 };
		act(() => pushStatus({ state: "starting" }));
		act(() => pushStatus(restarted));
		expect(result.current).toEqual(restarted);
		setApiDaemonStatusMock.mockClear();
		vi.mocked(queryClient.removeQueries).mockClear();

		await act(async () => resolveProbe({ state: "stopped", code: "daemon_unreachable" }));

		expect(result.current).toEqual(restarted);
		expect(setApiDaemonStatusMock).not.toHaveBeenCalled();
		expect(queryClient.removeQueries).not.toHaveBeenCalled();
		expect(setApiBaseUrlMock).toHaveBeenLastCalledWith(`http://127.0.0.1:${port}`);
	});

	it("retains polling after a rejected disconnect probe", async () => {
		vi.useFakeTimers();
		getStatusMock
			.mockResolvedValueOnce({ state: "ready", port: 4777 })
			.mockRejectedValueOnce(new Error("ipc unavailable"))
			.mockResolvedValueOnce({ state: "stopped", code: "daemon_unreachable" })
			.mockResolvedValueOnce({ state: "ready", port: 5050, pid: 202 });
		const queryClient = fakeQueryClient();
		const { result } = renderHook(() => useDaemonStatus(queryClient));
		await act(async () => { await Promise.resolve(); });
		act(() => setEventsConnectionState("connected"));

		await act(async () => setEventsConnectionState("disconnected"));
		expect(getStatusMock).toHaveBeenCalledTimes(2);
		expect(result.current).toEqual({ state: "ready", port: 4777 });

		await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
		expect(getStatusMock).toHaveBeenCalledTimes(3);
		expect(result.current).toEqual({ state: "stopped", code: "daemon_unreachable" });
		expect(setApiBaseUrlMock).toHaveBeenLastCalledWith(null);

		await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
		expect(getStatusMock).toHaveBeenCalledTimes(4);
		expect(result.current).toEqual({ state: "ready", port: 5050, pid: 202 });
		expect(setApiBaseUrlMock).toHaveBeenLastCalledWith("http://127.0.0.1:5050");
	});

	it("preserves an app-owned daemon exit push without a disconnect probe", async () => {
		vi.useFakeTimers();
		getStatusMock.mockResolvedValue({ state: "ready", port: 4555, pid: 101 });
		const queryClient = fakeQueryClient();
		const { result } = renderHook(() => useDaemonStatus(queryClient));
		await act(async () => { await Promise.resolve(); });
		const pushStatus = onStatusMock.mock.calls[0][0] as (status: DaemonStatus) => void;
		act(() => setEventsConnectionState("connected"));

		const exited: DaemonStatus = { state: "stopped", code: "exited", signal: "SIGKILL" };
		act(() => pushStatus(exited));
		act(() => setEventsConnectionState("disconnected"));

		expect(result.current).toEqual(exited);
		expect(setApiDaemonStatusMock).toHaveBeenLastCalledWith(exited);
		expect(setApiBaseUrlMock).toHaveBeenLastCalledWith(null);
		expect(getStatusMock).toHaveBeenCalledTimes(1);
	});

	it("unsubscribes from event disconnects on unmount", async () => {
		vi.useFakeTimers();
		getStatusMock.mockResolvedValue({ state: "ready", port: 4555, pid: 101 });
		const queryClient = fakeQueryClient();
		const { unmount } = renderHook(() => useDaemonStatus(queryClient));
		await act(async () => { await Promise.resolve(); });
		act(() => setEventsConnectionState("connected"));
		expect(getStatusMock).toHaveBeenCalledTimes(1);

		unmount();
		act(() => setEventsConnectionState("disconnected"));
		await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });

		expect(getStatusMock).toHaveBeenCalledTimes(1);
		expect(stopTransportMock).toHaveBeenCalledTimes(1);
		expect(removeStatusMock).toHaveBeenCalledTimes(1);
	});

	it("still connects the transport when the initial IPC status call fails", async () => {
		getStatusMock.mockRejectedValue(new Error("ipc unavailable"));
		const queryClient = fakeQueryClient();

		const { result } = renderHook(() => useDaemonStatus(queryClient));

		await waitFor(() => expect(connectMock).toHaveBeenCalledTimes(1));
		expect(result.current).toEqual({ state: "stopped" });
	});

	it("tears down the transport and the status listener on unmount", async () => {
		const queryClient = fakeQueryClient();
		const { unmount } = renderHook(() => useDaemonStatus(queryClient));
		await waitFor(() => expect(connectMock).toHaveBeenCalledTimes(1));

		unmount();

		expect(stopTransportMock).toHaveBeenCalledTimes(1);
		expect(removeStatusMock).toHaveBeenCalledTimes(1);
	});
});
