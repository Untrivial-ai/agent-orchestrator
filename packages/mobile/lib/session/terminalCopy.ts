// User-facing copy for the terminal screen's live connection. Pure (no React
// Native imports) so the wording is unit-testable.
import type { MuxStatus } from "../mux";

// The socket reconnects on its own after a close or error (see
// MuxClient.scheduleReconnect), so neither state is final and neither should
// read as a bare "error".
export const TERMINAL_STATUS_LABEL: Record<MuxStatus, string> = {
	connecting: "Connecting…",
	open: "Live",
	closed: "Reconnecting…",
	error: "Can't reach your desktop",
};

/** A missing PTY means the session was terminated, not that the link failed. */
export function isTerminalGoneError(message: string): boolean {
	return /not found/i.test(message);
}

/**
 * The daemon forwards terminal failures as its own Go error text
 * (`err.Error()`), which is not written for people. Map the one condition a user
 * can wait out, and give everything else a single actionable line.
 */
export function terminalErrorCopy(message: string | undefined): string {
	if (message && /exclusive session operation/i.test(message)) {
		return "Typing is paused while AO finishes an operation on this session. Try again in a moment.";
	}
	return "The terminal lost its connection to this session. Go back and open it again.";
}

/** Banner shown when the session's process exits. */
export function terminalExitedCopy(code: number): string {
	return code === 0 ? "This session has ended." : "This session has ended unexpectedly.";
}
