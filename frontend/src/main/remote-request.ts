import { parseDaemonProbe } from "../shared/daemon-attach";
import type { RemoteEntry } from "./remotes-store";

// Remote HTTP lives in the main process for two reasons: the renderer's origin
// is app://renderer and a remote daemon has no reason to allow it through CORS,
// and saved connection passwords must not be sent back to renderer memory.
// The Add Host form necessarily holds a newly typed password until IPC saves it.
// "not-a-daemon" is its own answer because the honest sentence differs: the
// address replied, so telling someone it is unreachable sends them to debug a
// network that is working.
export type RemoteHealth = "online" | "unauthorized" | "offline" | "not-a-daemon" | "incompatible";

export class IncompatibleRemoteVersionError extends Error {
	constructor() {
		super("AO versions are incompatible. Update AO on this computer and the remote host.");
		this.name = "IncompatibleRemoteVersionError";
	}
}

type FetchImpl = typeof fetch;

/** This is the only remote probe allowed before a saved password is sent. */
export async function readRemoteIdentity(
	entry: Pick<RemoteEntry, "url">,
	fetchImpl: FetchImpl = fetch,
	signal: AbortSignal = AbortSignal.timeout(5_000),
): Promise<string> {
	const base = (entry.url.includes("://") ? entry.url : `http://${entry.url}`).replace(/\/+$/, "");
	const url = new URL(`${base}/api/v1/identity`);
	if (!["http:", "https:"].includes(url.protocol) || url.username || url.password)
		throw new Error("remote host must use an HTTP(S) URL without embedded credentials");
	const response = await fetchImpl(url.href, { method: "GET", redirect: "error", signal });
	if (!response.ok) throw new Error(`remote identity probe returned ${response.status}`);
	const body = (await response.json()) as { hostId?: unknown; apiVersion?: unknown };
	if (typeof body.hostId !== "string" || body.hostId === "") throw new Error("remote identity probe returned no host ID");
	if (body.hostId === "local") throw new Error("remote identity probe returned reserved local host ID");
	if (body.apiVersion !== 1) throw new IncompatibleRemoteVersionError();
	return body.hostId;
}

export async function probeRemote(
	entry: RemoteEntry,
	fetchImpl: FetchImpl = fetch,
	timeoutMs = 5_000,
): Promise<RemoteHealth> {
	try {
		const base = (entry.url.includes("://") ? entry.url : `http://${entry.url}`).replace(/\/+$/, "");
		const url = new URL(`${base}/healthz`);
		if (!["http:", "https:"].includes(url.protocol) || url.username || url.password)
			throw new Error("remote host must use an HTTP(S) URL without embedded credentials");
		const response = await fetchImpl(url.href, {
			method: "GET",
			redirect: "error",
			headers: {
				Authorization: `Bearer ${entry.password}`,
				...(entry.hostId ? { "X-AO-Expected-Host-ID": entry.hostId } : {}),
			},
			signal: AbortSignal.timeout(timeoutMs),
		});
		if (response.status === 401 || response.status === 403) return "unauthorized";
		if (!response.ok) return "offline";
		// A status code proves something replied, not that it is a daemon. An SPA
		// catch-all (an Expo dev server on a mistyped port, say) answers every path
		// with 200 and an HTML page; accepting that as the api base hands the whole
		// renderer bodies it will read fields off and crash on.
		const text = await response.text();
		let body: unknown;
		try { body = JSON.parse(text); }
		catch { return "not-a-daemon"; }
		return parseDaemonProbe("healthz", body) === null ? "not-a-daemon" : "online";
	} catch {
		// A transport failure is indistinguishable from a wrong port here, and
		// both mean the same thing to the user: it is not reachable.
		return "offline";
	}
}
