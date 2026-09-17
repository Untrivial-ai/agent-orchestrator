// SSE subscription over the Electron-main cloud-cp proxy. EventSource/fetch
// cannot be used renderer-side (no CORS on the CP, and only main holds the
// bearer token), so the stream is opened by main and its chunks arrive over
// aoBridge.cloudCp stream events. This module reassembles those chunks with
// the shared SSE frame parser and delivers the same CloudCpClientEvent
// payloads — and the same resolve/onError contract — as the typed client's
// subscribeSessionEvents.

import type { CloudCpProxyRequestInit, CloudCpStreamEvent } from "../../../main/cloud-cp-proxy";
import { aoBridge } from "../bridge";
import { CloudCpAuthError, CloudCpError } from "./errors";
import { createSseFrameParser } from "./sse";
import type { CloudCpClientEvent } from "./types";

/** The slice of the preload cloudCp bridge this adapter needs. */
export interface CloudCpStreamBridge {
	openStream(init: CloudCpProxyRequestInit): Promise<{ streamId: string }>;
	closeStream(streamId: string): void;
	onStreamEvent(streamId: string, listener: (event: CloudCpStreamEvent) => void): () => void;
}

export interface SubscribeSessionEventsBridgedOptions {
	/** Control-plane origin, e.g. "https://cloud.example.com". A trailing slash is tolerated. */
	baseUrl: string;
	orgId: string;
	sessionId: string;
	/** Resume strictly after this sequence; omit to replay from the beginning. */
	after?: number;
	/** Invoked once per parsed event, in stream order. */
	onEvent: (event: CloudCpClientEvent) => void;
	/** Invoked on setup or mid-stream failure. Aborts via `signal` are silent. */
	onError?: (error: CloudCpError) => void;
	/** Aborting closes the stream and resolves the subscription promise. */
	signal?: AbortSignal;
	/** Bridge override for tests; defaults to the preload cloudCp bridge. */
	bridge?: CloudCpStreamBridge;
}

// openStream failures cross IPC as flattened message strings; main tags them
// with this marker (see main/cloud-cp-proxy.ts) so the status survives.
const STREAM_ERROR_MARKER = /CLOUD_CP_STREAM_ERROR (\d+) ([\s\S]*)/;

// Through a helper so TS does not narrow `signal.aborted` to `false` after an
// early-return check — the flag flips concurrently across awaits.
function isAborted(signal: AbortSignal | undefined): boolean {
	return signal?.aborted === true;
}

function errorFromOpenFailure(error: unknown): CloudCpError {
	if (error instanceof CloudCpError) return error;
	const message = error instanceof Error ? error.message : String(error);
	const match = STREAM_ERROR_MARKER.exec(message);
	if (match !== null) {
		const status = Number(match[1]);
		const detail = match[2].trim() || `Cloud control plane stream failed with status ${status}.`;
		return status === 401
			? new CloudCpAuthError(detail, { status, cause: error })
			: new CloudCpError(detail, { status, cause: error });
	}
	return new CloudCpError(message, { status: 0, cause: error });
}

/**
 * Subscribe to a session's live SSE stream through the main-process proxy.
 * Resolves when the stream ends (server close or abort); failures are
 * reported through `onError`, never as a rejection, so fire-and-forget
 * callers cannot leak unhandled rejections.
 */
export async function subscribeSessionEventsBridged(options: SubscribeSessionEventsBridgedOptions): Promise<void> {
	const { baseUrl, orgId, sessionId, after, onEvent, onError, signal } = options;
	const bridge = options.bridge ?? aoBridge.cloudCp;
	if (isAborted(signal)) return;

	const fail = (error: CloudCpError): void => {
		if (isAborted(signal)) return;
		onError?.(error);
	};

	const query = after === undefined ? "" : `?after=${encodeURIComponent(String(after))}`;
	const path = `/api/cloud/v1/orgs/${encodeURIComponent(orgId)}/sessions/${encodeURIComponent(sessionId)}/events${query}`;

	let streamId: string;
	try {
		({ streamId } = await bridge.openStream({
			baseUrl: baseUrl.replace(/\/+$/, ""),
			path,
			method: "GET",
			headers: { Accept: "text/event-stream" },
		}));
	} catch (error) {
		fail(errorFromOpenFailure(error));
		return;
	}

	// Aborted while the stream was being opened: main already holds it open,
	// so close it instead of leaking a slot from the per-window stream cap.
	if (isAborted(signal)) {
		bridge.closeStream(streamId);
		return;
	}

	const parser = createSseFrameParser();
	await new Promise<void>((resolve) => {
		let settled = false;
		let removeAbortListener: () => void = () => undefined;
		let unsubscribe: () => void = () => undefined;

		// Subscribing synchronously in the same microtask as the openStream
		// resolution matters: main defers the first chunk to a later macrotask,
		// so this listener is guaranteed to be attached before any event lands.
		unsubscribe = bridge.onStreamEvent(streamId, (event) => {
			if (settled) return;
			if (event.type === "chunk") {
				for (const frame of parser.push(event.data)) deliver(frame.data);
				return;
			}
			if (event.type === "end") {
				for (const frame of parser.flush()) deliver(frame.data);
				finish();
				return;
			}
			fail(new CloudCpError(event.message, { status: 0 }));
			finish();
		});

		function finish(): void {
			if (settled) return;
			settled = true;
			unsubscribe();
			removeAbortListener();
			resolve();
		}

		function deliver(data: string): void {
			let event: CloudCpClientEvent;
			try {
				event = JSON.parse(data) as CloudCpClientEvent;
			} catch {
				fail(new CloudCpError("The event stream sent a frame with malformed JSON.", { status: 200 }));
				return;
			}
			onEvent(event);
		}

		if (signal !== undefined) {
			const onAbort = (): void => {
				bridge.closeStream(streamId);
				finish();
			};
			signal.addEventListener("abort", onAbort, { once: true });
			removeAbortListener = () => {
				signal.removeEventListener("abort", onAbort);
			};
			// Raced with the subscription setup above.
			if (isAborted(signal)) onAbort();
		}
	});
}
