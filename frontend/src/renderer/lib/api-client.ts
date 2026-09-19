import createClient from "openapi-fetch";
import type { paths } from "../../api/schema";
import type { DaemonStatus } from "../../shared/daemon-status";
import { daemonFailureMessage } from "./daemon-failure";

function devApiBaseUrl(): string {
	return typeof window === "undefined" ? "http://127.0.0.1:3001" : window.location.origin;
}

const explicitApiBaseUrl = import.meta.env.VITE_AO_API_BASE_URL;
const initialApiBaseUrl = explicitApiBaseUrl ?? (import.meta.env.DEV ? devApiBaseUrl() : "http://127.0.0.1:3001");

let runtimeApiBaseUrl: string | null = explicitApiBaseUrl ?? null;
let daemonStatus: DaemonStatus = { state: "stopped" };

const baseUrlListeners = new Set<() => void>();

export function getApiBaseUrl(): string {
	return runtimeApiBaseUrl ?? "";
}

export function hasTrustedApiBaseUrl(): boolean {
	return runtimeApiBaseUrl !== null;
}

/**
 * Subscribe to base-URL changes (useSyncExternalStore-compatible). Long-lived
 * connections bound to a specific port — the terminal mux WebSocket, the SSE
 * stream — use this to rebind when the daemon comes back on a different port.
 */
export function subscribeApiBaseUrl(listener: () => void): () => void {
	baseUrlListeners.add(listener);
	return () => {
		baseUrlListeners.delete(listener);
	};
}

export function setApiBaseUrl(nextBaseUrl: string | null): void {
	const normalized = (nextBaseUrl ?? explicitApiBaseUrl ?? null)?.replace(/\/+$/, "") ?? null;
	if (normalized === runtimeApiBaseUrl) return;
	runtimeApiBaseUrl = normalized;
	baseUrlListeners.forEach((listener) => listener());
}

// The renderer records every supervisor status here so API requests made while
// no daemon URL is trusted can return the actual startup failure, not a generic
// availability message.
export function setApiDaemonStatus(nextStatus: DaemonStatus): void {
	daemonStatus = nextStatus;
}

async function runtimeFetch(input: Request): Promise<Response> {
	const baseUrl = runtimeApiBaseUrl;
	if (baseUrl === null) {
		return new Response(JSON.stringify({ message: daemonFailureMessage(daemonStatus), code: daemonStatus.code }), {
			status: 503,
			headers: { "Content-Type": "application/json" },
		});
	}

	const send = async (): Promise<Response> => {
		if (!baseUrl) {
			return fetch(input);
		}

		const url = new URL(input.url);
		const target = new URL(url.pathname + url.search + url.hash, baseUrl);
		if (target.href === input.url) {
			return fetch(input);
		}

		// Rebase onto the runtime base URL by copying fields explicitly and
		// buffering the body. `new Request(target, input)` reads the source
		// request's `duplex` getter, which Electron's Chromium lacks — it throws
		// "The duplex member must be specified" for any request with a body, so
		// every POST would fail in the packaged app. API bodies are small JSON;
		// buffering sidesteps streaming-duplex semantics entirely.
		const body = input.method === "GET" || input.method === "HEAD" ? undefined : await input.arrayBuffer();
		return fetch(target, {
			method: input.method,
			headers: input.headers,
			body,
			signal: input.signal,
			credentials: input.credentials,
			cache: input.cache,
			redirect: input.redirect,
			referrerPolicy: input.referrerPolicy,
			integrity: input.integrity,
			keepalive: input.keepalive,
		});
	};

	return send();
}

export const apiClient = createClient<paths>({
	baseUrl: initialApiBaseUrl,
	fetch: runtimeFetch,
});

/**
 * Human-readable message from an openapi-fetch `error` value. The daemon's
 * error body is `{ error, code, message, requestId }` (backend apierr) — a
 * plain object, so `String(error)` renders "[object Object]". Falls back
 * through Error instances and strings.
 */
export function apiErrorCode(error: unknown): string | undefined {
	if (typeof error === "object" && error !== null) {
		const body = error as { code?: unknown };
		if (typeof body.code === "string" && body.code !== "") return body.code;
	}
	return undefined;
}

/** Structured recovery metadata from the daemon's stable error envelope. */
export function apiErrorDetails(error: unknown): Record<string, unknown> | undefined {
	if (typeof error !== "object" || error === null) return undefined;
	const details = (error as { details?: unknown }).details;
	return typeof details === "object" && details !== null && !Array.isArray(details)
		? (details as Record<string, unknown>)
		: undefined;
}

/** Correlation id from the daemon's stable error envelope. */
export function apiErrorRequestId(error: unknown): string | undefined {
	if (typeof error === "object" && error !== null) {
		const body = error as { requestId?: unknown };
		if (typeof body.requestId === "string" && body.requestId !== "") return body.requestId;
	}
	return undefined;
}

export function apiErrorMessage(error: unknown, fallback = "Request failed"): string {
	if (error instanceof Error) return error.message;
	if (typeof error === "string" && error !== "") return error;
	if (typeof error === "object" && error !== null) {
		const body = error as { code?: unknown; message?: unknown; error?: unknown };
		if (typeof body.error === "object" && body.error !== null) {
			return apiErrorMessage(body.error, fallback);
		}
		if (typeof body.message === "string" && body.message !== "") {
			return body.message;
		}
		if (typeof body.error === "string" && body.error !== "") return body.error;
	}
	return fallback;
}
