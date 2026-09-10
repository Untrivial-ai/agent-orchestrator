import type { QueryClient } from "@tanstack/react-query";
import { getApiBaseUrl, hasTrustedApiBaseUrl, subscribeApiBaseUrl } from "./api-client";
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

export function getWorkspaceFileConnectionState(sessionId: string): WorkspaceFileConnectionState {
	return connectionStates.get(sessionId) ?? "connecting";
}

export function subscribeWorkspaceFileConnectionState(sessionId: string, listener: () => void): () => void {
	let listeners = connectionStateListeners.get(sessionId);
	if (!listeners) {
		listeners = new Set();
		connectionStateListeners.set(sessionId, listeners);
	}
	listeners.add(listener);
	return () => {
		listeners?.delete(listener);
		if (listeners?.size === 0) connectionStateListeners.delete(sessionId);
		if (!streams.has(sessionId) && !connectionStateListeners.has(sessionId)) connectionStates.delete(sessionId);
	};
}

function setWorkspaceFileConnectionState(sessionId: string, next: WorkspaceFileConnectionState): void {
	if (connectionStates.get(sessionId) === next) return;
	connectionStates.set(sessionId, next);
	connectionStateListeners.get(sessionId)?.forEach((listener) => listener());
}

// Shares one daemon watcher between the rail and maximized copies of a Files
// view. The daemon sends only invalidation edges; Git status and visible diffs
// are then refetched through the existing typed queries.
export function subscribeWorkspaceFileChanges(sessionId: string, queryClient: QueryClient): () => void {
	let stream = streams.get(sessionId);
	if (!stream) {
		stream = createWorkspaceStream(sessionId, queryClient);
		streams.set(sessionId, stream);
	}
	stream.refs += 1;

	return () => {
		const current = streams.get(sessionId);
		if (!current) return;
		current.refs -= 1;
		if (current.refs > 0) return;
		current.dispose();
		streams.delete(sessionId);
		if (!connectionStateListeners.has(sessionId)) connectionStates.delete(sessionId);
	};
}

function createWorkspaceStream(sessionId: string, queryClient: QueryClient): WorkspaceStream {
	const stream = {} as WorkspaceStream;
	const invalidate = () => {
		if (stream.debounce) clearTimeout(stream.debounce);
		stream.debounce = setTimeout(() => {
			void queryClient.invalidateQueries({ queryKey: ["session-workspace-files", sessionId] });
			void queryClient.invalidateQueries({ queryKey: ["session-workspace-file", sessionId] });
			void queryClient.invalidateQueries({ queryKey: ["session-workspace-tree", sessionId] });
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
		setWorkspaceFileConnectionState(sessionId, stream.failures >= 3 ? "degraded" : "connecting");
		scheduleRetry(generation);
	};
	stream.refs = 0;
	stream.disposed = false;
	stream.phase = "idle";
	stream.generation = 0;
	stream.failures = 0;
	stream.retries = 0;
	setWorkspaceFileConnectionState(sessionId, "connecting");
	stream.ensureConnected = () => {
		if (stream.disposed) return;
		if (typeof EventSource === "undefined") {
			setWorkspaceFileConnectionState(sessionId, "degraded");
			return;
		}
		if (!hasTrustedApiBaseUrl()) {
			resetConnection();
			setWorkspaceFileConnectionState(sessionId, "connecting");
			return;
		}
		const baseUrl = getApiBaseUrl();
		if (stream.sourceBaseUrl && stream.sourceBaseUrl !== baseUrl) {
			resetConnection();
			stream.failures = 0;
			stream.retries = 0;
			setWorkspaceFileConnectionState(sessionId, "connecting");
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
				setWorkspaceFileConnectionState(sessionId, "connected");
				invalidate();
			};
			source.onerror = () => {
				if (stream.disposed || generation !== stream.generation || stream.source !== source) return;
				if (source.readyState === EVENTSOURCE_CLOSED) {
					handleTerminalFailure(generation);
					return;
				}
				stream.failures += 1;
				setWorkspaceFileConnectionState(sessionId, stream.failures >= 3 ? "degraded" : "connecting");
			};
			source.addEventListener("workspace_changed", () => {
				if (!stream.disposed && generation === stream.generation && stream.source === source) invalidate();
			});
		} catch {
			stream.source = undefined;
			handleTerminalFailure(generation);
		}
	};
	stream.disconnectBaseUrl = subscribeApiBaseUrl(stream.ensureConnected);
	stream.dispose = () => {
		stream.disposed = true;
		if (stream.debounce) clearTimeout(stream.debounce);
		stream.disconnectBaseUrl();
		resetConnection();
	};
	stream.ensureConnected();
	return stream;
}
