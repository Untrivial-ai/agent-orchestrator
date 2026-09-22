import { createCloudCpClient, type CloudCpClient } from "./index";

type CloudCpBridgeRequest = (init: {
	baseUrl: string;
	path: string;
	method: string;
	headers?: Record<string, string>;
	body?: string;
}) => Promise<{ status: number; headers: Record<string, string>; body: string }>;

const API_PREFIX = "/api/cloud/v1";
const NULL_BODY_STATUSES = new Set([101, 204, 205, 304]);
const MAIN_PROCESS_TOKEN = "delegated-to-main-process";

function bridgeRequest(): CloudCpBridgeRequest | undefined {
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	const w = window as any;
	return (w.aoBridge?.cloudCp?.request ?? w.ao?.cloudCp?.request) as CloudCpBridgeRequest | undefined;
}

export const cloudCpFetch: typeof fetch = async (input, init) => {
	const request = bridgeRequest();
	if (request === undefined) return window.fetch(input, init);
	const url = new URL(input instanceof Request ? input.url : String(input));
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

export function createRendererCloudCpClient(baseUrl: string): CloudCpClient {
	return createCloudCpClient({
		baseUrl,
		getToken: async () => MAIN_PROCESS_TOKEN,
		fetchImpl: cloudCpFetch,
	});
}
