import type { ConnectionFailure } from "./connectionError";

// The Settings "Connected desktop" row. It used to say "Paired" whenever an
// address was saved, so a desktop that had gone away still read as fine right
// above a Test connection row reporting it disconnected. The row now reports
// the board poll's live state, so the two can only disagree for one poll.

export type DesktopStatusTone = "neutral" | "ok" | "error";

export type DesktopStatus = { label: string; tone: DesktopStatusTone };

/**
 * `failure` is what the last failed poll was classified as; null when no poll
 * has failed, which while not yet connected means one is still on its way.
 */
export function describeDesktopStatus(input: {
	configured: boolean;
	connection: "closed" | "connecting" | "open";
	failure: ConnectionFailure | null;
}): DesktopStatus {
	if (!input.configured) return { label: "Set up", tone: "neutral" };
	if (input.connection === "open") return { label: "Connected", tone: "ok" };
	// "closed" with no failure is the gap between pairing and the first poll
	// starting — not an offline desktop.
	if (input.connection === "connecting" || input.failure === null) return { label: "Connecting…", tone: "neutral" };
	switch (input.failure) {
		case "incompatible-host":
			return { label: "Update AO", tone: "error" };
		case "auth":
			return { label: "Password rejected", tone: "error" };
		case "rate-limited":
			return { label: "Locked out", tone: "error" };
		case "server-error":
			return { label: "Machine error", tone: "error" };
		case "tunnel-rotated":
			return { label: "Address changed", tone: "error" };
		default:
			// Still paired, just not answering. Kept apart from "Set up" so an
			// offline desktop never reads as an unpaired phone.
			return { label: "Unreachable", tone: "error" };
	}
}
