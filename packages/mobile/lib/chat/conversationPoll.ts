import type { ServerConfig } from "../config";

/**
 * How often the chat screen should re-read its conversation, or null to rely
 * on the event stream.
 *
 * The screen normally loads once and lets the stream push updates. That breaks
 * over a Cloudflare quick tunnel: measured, the body is forwarded in ~128 KB
 * chunks, so a few-hundred-byte chat event is never pushed through and the
 * conversation never updates — a reply the agent produced in two seconds went
 * unseen for over a minute.
 *
 * Speeding up the app's main poll did not help, because that one refreshes the
 * session list rather than the conversation.
 *
 * A stopgap for the same reason as the main poll: it costs battery and data,
 * and applies only while the tunnel is in use.
 *
 * See docs/adr/0004-cloudflare-tunnel-for-remote-mobile-access.md: carrying
 * conversation events over the mux WebSocket removes it.
 */
export const CONVERSATION_POLL_MS = 2_000;

export function conversationPollIntervalFor(cfg: ServerConfig | null): number | null {
	return cfg?.endpointKind === "tunnel" ? CONVERSATION_POLL_MS : null;
}
