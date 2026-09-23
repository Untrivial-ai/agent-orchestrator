import type { EndpointKind } from "./endpoints";

/**
 * How long to keep retrying a tunnel candidate whose hostname does not resolve
 * yet.
 *
 * A brand-new quick tunnel is advertised before every resolver knows it. The
 * daemon holds it back for a settle delay to compensate, but that delay is a
 * fixed guess measured on one network: too short and the phone is handed a name
 * it cannot resolve, too long and every enable pays for it.
 *
 * The daemon also cannot check properly. Probing from the machine that owns the
 * tunnel tests its own resolver, and doing it early caches an NXDOMAIN locally
 * that outlives propagation — measured: curl still failed thirty seconds after
 * dig had begun resolving the same name.
 *
 * The phone's resolver is the one that decides whether the endpoint works, so
 * the phone is where waiting belongs. Retrying here lets the daemon hold the
 * hostname back for less time without turning slow propagation into a dead
 * endpoint.
 */
export const TUNNEL_PROBE_WINDOW_MS = 20_000;

/** Between attempts. Long enough not to hammer a resolver that is negatively
 *  caching, short enough that the race is not left waiting on the gap. */
export const TUNNEL_PROBE_RETRY_DELAY_MS = 2_000;

/**
 * Whether a failed probe is worth another attempt.
 *
 * Only for tunnels: a LAN or tailnet address either answers or is not on this
 * network, and retrying those would hold the race open behind an address that
 * cannot appear.
 */
export function shouldRetryProbe(kind: EndpointKind, elapsedMs: number): boolean {
	return kind === "tunnel" && elapsedMs < TUNNEL_PROBE_WINDOW_MS;
}
