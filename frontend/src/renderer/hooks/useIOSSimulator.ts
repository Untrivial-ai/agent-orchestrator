import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { apiClient, apiErrorMessage, getApiBaseUrl, subscribeApiBaseUrl } from "../lib/api-client";

export type StreamFrame = {
	data?: string;
	mimeType?: string;
	codec?: "png" | "h264";
	encoded?: ArrayBuffer;
	width?: number;
	height?: number;
};

/** Connection state of the live frame WebSocket. */
export type StreamState = "idle" | "connecting" | "live" | "stalled";

type StreamMessage = { data?: string; mimeType?: string; width?: number; height?: number; error?: string };
type StreamHello = { type: "hello"; udid?: string; width?: number; height?: number; scale?: number };

// WebSocket reconnect backoff: 1s, 2s, 4s, capped at 8s.
const WS_RECONNECT_BASE_MS = 1000;
const WS_RECONNECT_MAX_MS = 8000;

export function useIOSSimulator(enabled: boolean, sessionId?: string) {
	const queryClient = useQueryClient();
	const query = sessionId ? { query: { sessionId } } : undefined;
	const [streamFrame, setStreamFrame] = useState<StreamFrame | null>(null);
	const [streamState, setStreamState] = useState<StreamState>("idle");
	const [streamError, setStreamError] = useState<string | null>(null);
	const [streamHello, setStreamHello] = useState<StreamHello | null>(null);
	const helloRef = useRef<StreamHello | null>(null);

	const status = useQuery({
		queryKey: ["ios-device", "status", sessionId],
		queryFn: async () => {
			const response = await apiClient.GET("/api/v1/ios-device/status", { params: query });
			if (response.error) throw new Error(apiErrorMessage(response.error, "Failed to read iOS Simulator status"));
			return response.data;
		},
		enabled,
		refetchInterval: 2000,
	});
	const devices = useQuery({
		queryKey: ["ios-device", "devices"],
		queryFn: async () => {
			const response = await apiClient.GET("/api/v1/ios-device/devices", { params: query });
			if (response.error) throw new Error(apiErrorMessage(response.error, "Failed to list iOS Simulator devices"));
			return response.data;
		}, enabled, staleTime: 10_000,
	});
	// Toolchain (Xcode/runtime) state drives the "load needed dependencies"
	// flow — without Xcode there is nothing to boot.
	const toolchain = useQuery({
		queryKey: ["ios-device", "toolchain"],
		queryFn: async () => {
			const response = await apiClient.GET("/api/v1/ios-device/toolchain/status");
			if (response.error) throw new Error(apiErrorMessage(response.error, "Failed to read iOS toolchain status"));
			return response.data;
		},
		enabled,
		refetchInterval: 10_000,
	});
	const recheck = useMutation({
		mutationFn: async () => {
			const response = await apiClient.POST("/api/v1/ios-device/toolchain/recheck");
			if (response.error) throw new Error(apiErrorMessage(response.error, "Failed to recheck iOS toolchain"));
			return response.data;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: ["ios-device", "toolchain"] });
		},
	});
	const start = useMutation({
		mutationFn: async (deviceId?: string) => {
			const response = await apiClient.POST("/api/v1/ios-device/start", { params: { query: { ...(query?.query ?? {}), ...(deviceId ? { deviceId } : {}) } } });
			if (response.error) throw new Error(apiErrorMessage(response.error, "Failed to start iOS Simulator"));
			return response.data;
		},
		onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["ios-device", "status", sessionId] }),
	});
	const stop = useMutation({
		mutationFn: async () => {
			const response = await apiClient.POST("/api/v1/ios-device/stop", { params: query });
			if (response.error) throw new Error(apiErrorMessage(response.error, "Failed to stop iOS Simulator"));
			return response.data;
		},
		onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["ios-device", "status", sessionId] }),
	});
	// Screenshot poll is only the fallback: live frames arrive over the
	// WebSocket; the 1s REST poll keeps the panel usable where ScreenCaptureKit
	// is unavailable (headless helpers, restricted environments).
	const screenshot = useQuery({
		queryKey: ["ios-device", "screenshot", sessionId],
		queryFn: async () => {
			const response = await apiClient.GET("/api/v1/ios-device/screenshot", { params: query });
			if (response.error) throw new Error(apiErrorMessage(response.error, "Failed to capture iOS Simulator"));
			return response.data;
		},
		enabled: enabled && status.data?.state === "Booted" && streamState !== "live",
		refetchInterval: 1000,
	});
	const permissions = useQuery({
		queryKey: ["ios-device", "permissions"],
		queryFn: async () => {
			const response = await apiClient.GET("/api/v1/ios-device/permissions");
			return response.data;
		},
		enabled,
		refetchInterval: 5000,
	});

	// Live frame stream with reconnect. One socket at a time; on close or error
	// it reconnects with exponential backoff, and when the daemon's base URL
	// changes (daemon restarted on another port) the open socket is dropped so
	// the reconnection logic picks up the new base URL.
	useEffect(() => {
		if (!enabled) {
			setStreamFrame(null);
			setStreamState("idle");
			setStreamError(null);
			setStreamHello(null);
			helloRef.current = null;
			return;
		}
		let disposed = false;
		let socket: WebSocket | null = null;
		let retryCount = 0;
		let reconnectTimer: ReturnType<typeof setTimeout> | undefined;

		const closeSocket = () => {
			if (socket) {
				// Prevent the onclose handler from scheduling another reconnect
				// for an intentionally closed socket.
				socket.onclose = null;
				socket.close();
				socket = null;
			}
		};
		const scheduleReconnect = () => {
			if (disposed) return;
			const delay = Math.min(WS_RECONNECT_BASE_MS * 2 ** retryCount, WS_RECONNECT_MAX_MS);
			retryCount += 1;
			reconnectTimer = setTimeout(() => {
				reconnectTimer = undefined;
				connect();
			}, delay);
		};
		const connect = () => {
			if (disposed) return;
			const base = getApiBaseUrl();
			if (!base) {
				scheduleReconnect();
				return;
			}
			setStreamError(null);
			setStreamState((current) => (current === "live" ? current : "connecting"));
			const streamQuery = sessionId ? `?sessionId=${encodeURIComponent(sessionId)}` : "";
			socket = new WebSocket(`${base.replace(/^http/, "ws")}/api/v1/ios-device/stream${streamQuery}`);
			socket.binaryType = "arraybuffer";
			socket.onopen = () => {
				if (disposed) return;
				retryCount = 0;
			};
			socket.onmessage = (event) => {
				if (event.data instanceof ArrayBuffer) {
					const hello = helloRef.current;
					if (event.data.byteLength < 8) return;
					const view = new DataView(event.data);
					const width = view.getUint32(0);
					const height = view.getUint32(4);
					setStreamState("live");
					setStreamError(null);
					setStreamFrame({ codec: "h264", encoded: event.data.slice(8), width: width || hello?.width, height: height || hello?.height });
					return;
				}
				let message: StreamMessage;
				try {
					message = JSON.parse(event.data) as StreamMessage;
				} catch {
					return; // malformed frames are ignored
				}
				if (message.error) {
					setStreamState("stalled");
					setStreamError(message.error);
					return;
				}
				if ((message as StreamHello).type === "hello") {
					const hello = message as StreamHello;
					setStreamHello(hello);
					helloRef.current = hello;
					return;
				}
				if (message.data && message.mimeType) {
					setStreamError(null);
					setStreamState("live");
					setStreamFrame({ data: message.data, mimeType: message.mimeType, codec: "png", width: message.width, height: message.height });
				}
			};
			socket.onerror = () => {
				// onclose does the reconnect scheduling.
				socket?.close();
			};
			socket.onclose = () => {
				if (disposed) return;
				socket = null;
				setStreamState("stalled");
				scheduleReconnect();
			};
		};

		const unsubscribeBaseUrl = subscribeApiBaseUrl(() => {
			// The daemon moved (or the renderer learned a trusted URL): drop the
			// socket so the next connect uses the new base URL.
			closeSocket();
		});

		connect();
		return () => {
			disposed = true;
			unsubscribeBaseUrl();
			if (reconnectTimer !== undefined) clearTimeout(reconnectTimer);
			closeSocket();
		};
	}, [enabled, sessionId]);

	const input = useMutation({
		mutationFn: async (
			request:
				| { action: "tap" | "swipe"; x: number; y: number; x2?: number; y2?: number }
				| { action: "text"; text: string }
				| { action: "key"; keyCode: number }
				| { action: "home" | "lock" | "rotateLeft" | "rotateRight" },
		) => {
			const response = await apiClient.POST("/api/v1/ios-device/input", { params: query, body: request });
			if (response.error) throw new Error(apiErrorMessage(response.error, "Failed to send iOS Simulator input"));
			return response.data;
		},
	});
	return {
		status,
		devices,
		toolchain,
		recheck,
		start,
		stop,
		screenshot,
		streamFrame,
		streamState,
		streamError,
		streamHello,
		permissions,
		input,
	};
}
