import type { ServerConfig } from "./config";

/**
 * Whether two configs point at the same endpoint with the same credential.
 *
 * Resolution builds a fresh object on every call, and effects across the app
 * key on the config's identity — the live conversation stream, the REST poll
 * loop, the terminal mux. Handing them a new object for an unchanged endpoint
 * tears those down and rebuilds them, which showed up as chat replies arriving
 * only on the next poll instead of streaming in.
 */
export function sameServerConfig(a: ServerConfig | null, b: ServerConfig | null): boolean {
	if (!a || !b) return false;
	return (
		a.host === b.host &&
		a.httpPort === b.httpPort &&
		Boolean(a.secure) === Boolean(b.secure) &&
		a.password === b.password &&
		// Included because it keys per-machine state: a migrated pairing that
		// adopts an identity must rebuild the connections that depend on it, even
		// though the address it answers on has not moved.
		(a.hostId ?? "") === (b.hostId ?? "")
	);
}
