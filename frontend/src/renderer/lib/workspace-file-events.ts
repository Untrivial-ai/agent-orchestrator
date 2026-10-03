import type { QueryClient } from "@tanstack/react-query";
import { getApiBaseUrl, hasTrustedApiBaseUrl, subscribeApiBaseUrl } from "./api-client";
import { baseUrlForHost, subscribeConnectedHosts } from "./host-clients";
import { sessionUiKey } from "./hosts";
import { probeRemoteSse } from "./remote-sse-probe";
import { computeSseRetryDelayMs } from "./sse-backoff";

const INVALIDATE_DEBOUNCE_MS = 150;
const EVENTSOURCE_CLOSED = 2;

export type WorkspaceFileConnectionState = "connecting" | "connected" | "degraded";
type ConnectionPhase = "idle" | "connecting" | "open" | "waiting";

type WorkspaceStream = {
	refs: number;
	disposed: boolean;
	phase: ConnectionPhase;
	generation: number;
	failures: number;
	/**
	 * Scheduled rebuilds since the last successful open. Distinct from
	 * `failures`, which also counts the browser's own non-terminal retries and
	 * so would inflate the backoff exponent past what we actually retried.
	 */
	retries: number;
	source?: EventSource;
	poll?: ReturnType<typeof setInterval>;
	stopRemote?: () => void;
	sourceBaseUrl?: string;
	debounce?: ReturnType<typeof setTimeout>;
	retry?: ReturnType<typeof setTimeout>;
	disconnectBaseUrl: () => void;
	ensureConnected: () => void;
	dispose: () => void;
};

const streams = new Map<string, WorkspaceStream>();
const connectionStates = new Map<string, WorkspaceFileConnectionState>();
const connectionStateListeners = new Map<string, Set<() => void>>();

export function getWorkspaceFileConnectionState(sessionId: string, hostId?: string): WorkspaceFileConnectionState {
	return connectionStates.get(sessionUiKey(sessionId, hostId)) ?? "connecting";
}

export function subscribeWorkspaceFileConnectionState(sessionId: string, listener: () => void, hostId?: string): () => void {
	const key = sessionUiKey(sessionId, hostId);
	let listeners = connectionStateListeners.get(key);
	if (!listeners) {
		listeners = new Set();
		connectionStateListeners.set(key, listeners);
	}
	listeners.add(listener);
	return () => {
		listeners?.delete(listener);
		if (listeners?.size === 0) connectionStateListeners.delete(key);
		if (!streams.has(key) && !connectionStateListeners.has(key)) connectionStates.delete(key);
	};
}

function setWorkspaceFileConnectionState(key: string, next: WorkspaceFileConnectionState): void {
	if (connectionStates.get(key) === next) return;
	connectionStates.set(key, next);
	connectionStateListeners.get(key)?.forEach((listener) => listener());
}

// Shares one daemon watcher between the rail and maximized copies of a Files
// view. The daemon sends only invalidation edges; Git status and visible diffs
// are then refetched through the existing typed queries.
export function subscribeWorkspaceFileChanges(sessionId: string, queryClient: QueryClient, hostId?: string): () => void {
	const key = sessionUiKey(sessionId, hostId);
	let stream = streams.get(key);
	if (!stream) {
		stream = createWorkspaceStream(sessionId, queryClient, hostId);
		streams.set(key, stream);
	}
	stream.refs += 1;

	return () => {
		const current = streams.get(key);
		if (!current) return;
		current.refs -= 1;
		if (current.refs > 0) return;
		current.dispose();
		streams.delete(key);
		if (!connectionStateListeners.has(key)) connectionStates.delete(key);
	};
}

function createWorkspaceStream(sessionId: string, queryClient: QueryClient, hostId?: string): WorkspaceStream {
	const stream = {} as WorkspaceStream;
	const key = sessionUiKey(sessionId, hostId);
	const queryPrefix = (name: string) => hostId ? [name, hostId, sessionId] : [name, sessionId];
	const invalidate = () => {
		if (stream.debounce) clearTimeout(stream.debounce);
		stream.debounce = setTimeout(() => {
			for (const name of ["workspace-file-paths", "session-workspace-files", "session-workspace-file", "session-workspace-file-revision", "session-workspace-diffs", "session-workspace-search", "session-workspace-tree"]) {
				void queryClient.invalidateQueries({ queryKey: queryPrefix(hostId && name === "workspace-file-paths" ? "remote-workspace-file-paths" : name) });
			}
		}, INVALIDATE_DEBOUNCE_MS);
	};
	const scheduleRetry = (generation: number) => {
		if (stream.disposed || stream.retry) return;
		stream.phase = "waiting";
		stream.retries += 1;
		const delay = computeSseRetryDelayMs(stream.retries);
		stream.retry = setTimeout(() => {
			stream.retry = undefined;
			if (stream.disposed || generation !== stream.generation) return;
			stream.phase = "idle";
			stream.ensureConnected();
		}, delay);
	};
	const resetConnection = () => {
		stream.generation += 1;
		if (stream.retry) clearTimeout(stream.retry);
		stream.retry = undefined;
		if (stream.poll !== undefined) clearInterval(stream.poll);
		stream.poll = undefined;
		stream.stopRemote?.();
		stream.stopRemote = undefined;
		stream.source?.close();
		stream.source = undefined;
		stream.sourceBaseUrl = undefined;
		stream.phase = "idle";
	};
	const handleTerminalFailure = (generation: number) => {
		if (stream.disposed || generation !== stream.generation) return;
		stream.source?.close();
		stream.source = undefined;
		stream.failures += 1;
		setWorkspaceFileConnectionState(key, stream.failures >= 3 ? "degraded" : "connecting");
		scheduleRetry(generation);
	};
	stream.refs = 0;
	stream.disposed = false;
	stream.phase = "idle";
	stream.generation = 0;
	stream.failures = 0;
	stream.retries = 0;
	setWorkspaceFileConnectionState(key, "connecting");
	stream.ensureConnected = () => {
		if (stream.disposed) return;
		if (hostId && !baseUrlForHost(hostId)) {
			resetConnection();
			setWorkspaceFileConnectionState(key, "degraded");
			return;
		}
		if (!hostId && !hasTrustedApiBaseUrl()) {
			resetConnection();
			setWorkspaceFileConnectionState(key, "connecting");
			return;
		}
		const baseUrl = hostId ? baseUrlForHost(hostId)! : getApiBaseUrl();
		if (stream.sourceBaseUrl && stream.sourceBaseUrl !== baseUrl) {
			resetConnection();
			stream.failures = 0;
			stream.retries = 0;
			setWorkspaceFileConnectionState(key, "connecting");
		}
		if (hostId) {
			if (stream.sourceBaseUrl === baseUrl) return;
			stream.sourceBaseUrl = baseUrl;
			invalidate();
			stream.poll = setInterval(invalidate, 2_000);
			setWorkspaceFileConnectionState(key, "connected");
			stream.stopRemote = probeRemoteSse(
				`${baseUrl.replace(/\/+$/, "")}/api/v1/sessions/${encodeURIComponent(sessionId)}/workspace/events`,
				["workspace_changed"],
				invalidate,
				() => {
					if (stream.poll !== undefined) clearInterval(stream.poll);
					stream.poll = undefined;
					invalidate();
				},
				invalidate,
			);
			return;
		}
		if (typeof EventSource === "undefined") {
			setWorkspaceFileConnectionState(key, "degraded");
			return;
		}
		if (stream.phase !== "idle") return;

		stream.sourceBaseUrl = baseUrl;
		stream.phase = "connecting";
		const generation = ++stream.generation;
		try {
			const source = new EventSource(
				`${baseUrl.replace(/\/+$/, "")}/api/v1/sessions/${encodeURIComponent(sessionId)}/workspace/events`,
			);
			stream.source = source;
			source.onopen = () => {
				if (stream.disposed || generation !== stream.generation || stream.source !== source) return;
				stream.phase = "open";
				stream.failures = 0;
				stream.retries = 0;
				setWorkspaceFileConnectionState(key, "connected");
				invalidate();
			};
			source.onerror = () => {
				if (stream.disposed || generation !== stream.generation || stream.source !== source) return;
				if (source.readyState === EVENTSOURCE_CLOSED) {
					handleTerminalFailure(generation);
					return;
				}
				stream.failures += 1;
				setWorkspaceFileConnectionState(key, stream.failures >= 3 ? "degraded" : "connecting");
			};
			source.addEventListener("workspace_changed", () => {
				if (!stream.disposed && generation === stream.generation && stream.source === source) invalidate();
			});
		} catch {
			stream.source = undefined;
			handleTerminalFailure(generation);
		}
	};
	stream.disconnectBaseUrl = hostId ? subscribeConnectedHosts(stream.ensureConnected) : subscribeApiBaseUrl(stream.ensureConnected);
	stream.dispose = () => {
		stream.disposed = true;
		if (stream.debounce) clearTimeout(stream.debounce);
		stream.disconnectBaseUrl();
		resetConnection();
	};
	stream.ensureConnected();
	return stream;
}
