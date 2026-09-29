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
import type { CloudCpClient } from "../lib/cloud-cp";
import { createRendererCloudCpClient } from "../lib/cloud-cp/renderer-client";
import { useCloudSession } from "../lib/cloud-session";
import { useCloudGate } from "./useCloudGate";
import { useSettings } from "./useSettings";

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
export { cloudCpFetch, createRendererCloudCpClient } from "../lib/cloud-cp/renderer-client";

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
