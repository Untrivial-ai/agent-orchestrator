import type { QueryClient } from "@tanstack/react-query";
import { getApiBaseUrl, hasTrustedApiBaseUrl, subscribeApiBaseUrl } from "./api-client";

const INVALIDATE_DEBOUNCE_MS = 150;
const SSE_RETRY_MS = 5_000;
const EVENTSOURCE_CLOSED = 2;

type WorkspaceStream = {
	refs: number;
	disposed: boolean;
	source?: EventSource;
	sourceBaseUrl?: string;
	debounce?: ReturnType<typeof setTimeout>;
	retry?: ReturnType<typeof setTimeout>;
	disconnectBaseUrl: () => void;
	connect: () => void;
	dispose: () => void;
};

const streams = new Map<string, WorkspaceStream>();

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
	const scheduleRetry = () => {
		if (stream.disposed || stream.retry) return;
		stream.retry = setTimeout(() => {
			stream.retry = undefined;
			stream.connect();
		}, SSE_RETRY_MS);
	};
	stream.refs = 0;
	stream.disposed = false;
	stream.connect = () => {
		if (stream.disposed || typeof EventSource === "undefined") return;
		if (!hasTrustedApiBaseUrl()) {
			stream.source?.close();
			stream.source = undefined;
			stream.sourceBaseUrl = undefined;
			return;
		}
		const baseUrl = getApiBaseUrl();
		if (stream.source && stream.sourceBaseUrl === baseUrl && stream.source.readyState !== EVENTSOURCE_CLOSED) return;
		stream.source?.close();
		stream.sourceBaseUrl = baseUrl;
		try {
			const source = new EventSource(
				`${baseUrl.replace(/\/+$/, "")}/api/v1/sessions/${encodeURIComponent(sessionId)}/workspace/events`,
			);
			stream.source = source;
			source.onopen = () => {
				if (!stream.disposed && stream.source === source) invalidate();
			};
			source.onerror = () => {
				if (!stream.disposed && stream.source === source && source.readyState === EVENTSOURCE_CLOSED) scheduleRetry();
			};
			source.addEventListener("workspace_changed", () => {
				if (!stream.disposed && stream.source === source) invalidate();
			});
		} catch {
			stream.source = undefined;
			scheduleRetry();
		}
	};
	stream.disconnectBaseUrl = subscribeApiBaseUrl(stream.connect);
	stream.dispose = () => {
		stream.disposed = true;
		if (stream.debounce) clearTimeout(stream.debounce);
		if (stream.retry) clearTimeout(stream.retry);
		stream.disconnectBaseUrl();
		stream.source?.close();
	};
	stream.connect();
	return stream;
}
