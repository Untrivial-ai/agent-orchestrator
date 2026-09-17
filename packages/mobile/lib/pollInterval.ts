import type { ServerConfig } from "./config";

/**
 * How often to poll, given which endpoint won the race.
 *
 * Normally the poll is a backstop: the conversation event stream carries live
 * updates and the poll only reconciles. Over a Cloudflare quick tunnel it is
 * the only thing that works.
 *
 * Measured against a real tunnel: the body is forwarded in ~128 KB chunks, so
 * a few-hundred-byte chat event is never pushed through on its own. A reply
 * that took the agent two seconds sat unseen for over a minute, while the same
 * conversation was instant over the LAN. Until that path carries a stream —
 * a named tunnel, or a relay — polling faster is what keeps it usable.
 *
 * See docs/adr/0004-cloudflare-tunnel-for-remote-mobile-access.md — this is a
 * stopgap, and the exit condition is concrete: conversation events move to the
 * existing /mux WebSocket, which already reaches the same daemon and already
 * round-trips small frames in 11ms over the tunnel. When that lands, both this
 * and conversationPoll.ts are deleted and the poll returns to DIRECT_POLL_MS on
 * every path. Until then the cost is unmeasured battery and cellular data.
 */

/** Direct paths stream fine, so the poll stays cheap on battery and data. */
export const DIRECT_POLL_MS = 8_000;

/**
 * Over the tunnel the poll is the only live signal, so it has to be quick
 * enough to feel immediate. Deliberately a stopgap: it costs battery and data,
 * and it applies only while that path is in use.
 */
export const TUNNEL_POLL_MS = 2_000;

export function pollIntervalFor(cfg: ServerConfig | null): number {
	return cfg?.endpointKind === "tunnel" ? TUNNEL_POLL_MS : DIRECT_POLL_MS;
}
