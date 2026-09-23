import type { Endpoint } from "./endpoints";

/**
 * Whether a failure to connect is likely a rotated tunnel hostname.
 *
 * A quick tunnel takes a new hostname every time the connector restarts. The
 * phone heals by re-reading the daemon's endpoint list after a successful
 * connect — which it cannot do when the stored tunnel is the dead one and
 * there is no LAN or tailnet address to reach the machine by instead. Measured:
 * the window between a connector restarting and its replacement becoming
 * advertisable is about 34 seconds, and a phone that is away for any of it
 * keeps a hostname that will never answer again.
 *
 * Nothing on the phone can recover from that: learning the new hostname
 * requires reaching the machine, which requires the hostname. What it can do is
 * stop reporting a bare "can't connect" and say the code needs rescanning,
 * which is the one action that actually works.
 *
 * A stable hostname removes the cause; see the named-tunnel follow-up.
 */
export function tunnelMayHaveRotated(known: Endpoint[], anyReachable: boolean): boolean {
	if (anyReachable) return false;
	return known.some((e) => e.kind === "tunnel");
}
