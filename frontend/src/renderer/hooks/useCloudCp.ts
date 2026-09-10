/**
 * Renderer-side construction of the cloud control-plane client.
 *
 * The control plane has no CORS and the WorkOS bearer token lives only in the
 * Electron main process, so requests are proxied over the preload `cloudCp`
 * bridge when it is present (main attaches the real token; any Authorization
 * header set renderer-side is replaced). The bridge surface is being built in
 * parallel, so it is reached via optional `window` access rather than a
 * compile-time import. Without a bridge (browser dev preview) calls fall back
 * to `window.fetch`, where no credential is available and the control plane
 * answers 401 — the same terminal state as being signed out.
 */

import { useMemo } from "react";
import { createCloudCpClient, type CloudCpClient } from "../lib/cloud-cp";
import { useCloudSession } from "../lib/cloud-session";
import { useCloudGate } from "./useCloudGate";
import { useSettings } from "./useSettings";

type CloudCpBridgeRequest = (init: {
	baseUrl: string;
	path: string;
	method: string;
	headers?: Record<string, string>;
	body?: string;
}) => Promise<{ status: number; headers: Record<string, string>; body: string }>;

const API_PREFIX = "/api/cloud/v1";

/** Statuses the Response constructor forbids a body for. */
const NULL_BODY_STATUSES = new Set([101, 204, 205, 304]);

/**
 * The typed client refuses to send without a bearer token, but the renderer
 * never holds the real one: the main-process proxy strips this placeholder and
 * stamps the WorkOS token itself. On the no-bridge dev fallback the placeholder
 * reaches the control plane and 401s, which is the intended signed-out shape.
 */
const MAIN_PROCESS_TOKEN = "delegated-to-main-process";

function bridgeRequest(): CloudCpBridgeRequest | undefined {
	// Optional any-cast access: the preload contract may not exist in this
	// build, and its typings are owned by the main-process transport work.
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	const w = window as any;
	return (w.aoBridge?.cloudCp?.request ?? w.ao?.cloudCp?.request) as CloudCpBridgeRequest | undefined;
}

/** Fetch shim over the preload cloudCp bridge; plain window.fetch when absent. */
export const cloudCpFetch: typeof fetch = async (input, init) => {
	const request = bridgeRequest();
	if (request === undefined) return window.fetch(input, init);
	const url = new URL(input instanceof Request ? input.url : String(input));
	// Split origin+mount from the API path so main can re-validate both parts.
	const prefixIndex = url.pathname.indexOf(API_PREFIX);
	const mount = prefixIndex > 0 ? url.pathname.slice(0, prefixIndex) : "";
	const result = await request({
		baseUrl: url.origin + mount,
		path: url.pathname.slice(mount.length) + url.search,
		method: init?.method ?? "GET",
		headers: Object.fromEntries(new Headers(init?.headers).entries()),
		body: typeof init?.body === "string" ? init.body : undefined,
	});
	const body = NULL_BODY_STATUSES.has(result.status) || result.body === "" ? null : result.body;
	return new Response(body, { status: result.status, headers: result.headers });
};

export interface UseCloudCpResult {
	/** Typed control-plane client. Only expected to succeed when `ready` is true. */
	client: CloudCpClient;
	/** Cloud offering on, user signed in, and control-plane URL known. */
	ready: boolean;
	/** Control-plane base URL the client is bound to; include it in query keys. */
	baseUrl: string;
}

/**
 * Non-hook client constructor for event handlers that resolve the base URL
 * lazily (e.g. from the settings query cache) instead of subscribing via
 * hooks. Same transport and token delegation as useCloudCp.
 */
export function createRendererCloudCpClient(baseUrl: string): CloudCpClient {
	return createCloudCpClient({
		baseUrl,
		getToken: async () => MAIN_PROCESS_TOKEN,
		fetchImpl: cloudCpFetch,
	});
}

export function useCloudCp(): UseCloudCpResult {
	const { settings } = useSettings();
	const { cloudEnabled } = useCloudGate();
	const { status } = useCloudSession();
	const baseUrl = settings?.cloudControlPlaneUrl ?? "";
	const client = useMemo(() => createRendererCloudCpClient(baseUrl), [baseUrl]);
	return {
		client,
		ready: cloudEnabled && status === "authenticated" && baseUrl !== "",
		baseUrl,
	};
}
