// Bridged fetch for the cloud control-plane client. The CP has no CORS and
// the WorkOS bearer token lives only in the Electron main process, so the
// renderer cannot fetch the CP directly: this adapter turns a fetch call into
// an aoBridge.cloudCp.request IPC round trip (main attaches the real token —
// any Authorization header set here is replaced) and rebuilds a Response from
// the proxied status/headers/body. Non-stream calls only: SSE must go through
// subscribeSessionEventsBridged in ./stream-bridge instead.
//
// Plug it into the typed client:
//   createCloudCpClient({ baseUrl, getToken, fetchImpl: createBridgedFetch() })

import type { CloudCpProxyRequestInit, CloudCpProxyResponse } from "../../../main/cloud-cp-proxy";
import { aoBridge } from "../bridge";

/** The slice of the preload cloudCp bridge this adapter needs. */
export interface CloudCpRequestBridge {
	request(init: CloudCpProxyRequestInit): Promise<CloudCpProxyResponse>;
}

const API_PREFIX = "/api/cloud/v1";

/** Statuses the Response constructor forbids a body for. */
const NULL_BODY_STATUSES = new Set([101, 204, 205, 304]);

function abortError(reason: unknown): unknown {
	return reason ?? new DOMException("The operation was aborted.", "AbortError");
}

/**
 * The main-process fetch cannot be cancelled through the request channel, so
 * aborting only abandons the proxied call renderer-side — matching fetch's
 * observable contract (reject with the abort reason) without cancellation.
 */
async function raceAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
	return await new Promise<T>((resolve, reject) => {
		const onAbort = (): void => {
			reject(abortError(signal.reason));
		};
		signal.addEventListener("abort", onAbort, { once: true });
		promise.then(
			(value) => {
				signal.removeEventListener("abort", onAbort);
				resolve(value);
			},
			(error: unknown) => {
				signal.removeEventListener("abort", onAbort);
				reject(error instanceof Error ? error : new Error(String(error)));
			},
		);
	});
}

export function createBridgedFetch(bridge?: CloudCpRequestBridge): typeof fetch {
	const cloudCp = bridge ?? aoBridge.cloudCp;

	return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
		const request = input instanceof Request ? input : undefined;
		const url = request !== undefined ? request.url : input instanceof URL ? input.toString() : String(input);
		const method = (init?.method ?? request?.method ?? "GET").toUpperCase();

		const headers: Record<string, string> = {};
		new Headers(init?.headers ?? request?.headers).forEach((value, key) => {
			headers[key] = value;
		});
		if ((headers["accept"] ?? "").toLowerCase().includes("text/event-stream")) {
			throw new Error(
				"createBridgedFetch cannot carry SSE responses; use subscribeSessionEventsBridged from lib/cloud-cp/stream-bridge instead.",
			);
		}

		const rawBody = init?.body;
		if (rawBody !== undefined && rawBody !== null && typeof rawBody !== "string") {
			throw new Error("createBridgedFetch only supports string request bodies.");
		}

		const parsed = new URL(url);
		const prefixIndex = parsed.pathname.indexOf(API_PREFIX);
		if (prefixIndex === -1) {
			throw new Error(`createBridgedFetch only proxies cloud control-plane URLs under ${API_PREFIX}.`);
		}
		const baseUrl = parsed.origin + parsed.pathname.slice(0, prefixIndex);
		const path = parsed.pathname.slice(prefixIndex) + parsed.search;

		const signal = init?.signal ?? request?.signal;
		if (signal?.aborted === true) throw abortError(signal.reason);

		const pending = cloudCp.request({
			baseUrl,
			path,
			method,
			headers,
			body: rawBody ?? undefined,
		});
		const result = signal != null ? await raceAbort(pending, signal) : await pending;

		const body = NULL_BODY_STATUSES.has(result.status) || result.body === "" ? null : result.body;
		return new Response(body, { status: result.status, headers: result.headers });
	};
}
