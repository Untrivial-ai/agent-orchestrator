import { describe, expect, it } from "vitest";
import { isTerminalGoneError, TERMINAL_STATUS_LABEL, terminalErrorCopy, terminalExitedCopy } from "./terminalCopy";

describe("terminal copy", () => {
	it("labels every link state for people, and never as a bare error", () => {
		expect(TERMINAL_STATUS_LABEL.open).toBe("Live");
		expect(TERMINAL_STATUS_LABEL.closed).toBe("Reconnecting…");
		expect(Object.values(TERMINAL_STATUS_LABEL)).not.toContain("error");
	});

	it("reads a missing PTY as a gone session", () => {
		expect(isTerminalGoneError("tmux: session not found")).toBe(true);
		expect(isTerminalGoneError("missing terminal id")).toBe(false);
	});

	it("maps the daemon's Go error text instead of echoing it", () => {
		expect(terminalErrorCopy("session input is disabled while an exclusive session operation is in progress")).toMatch(/Try again in a moment/);
		const copy = terminalErrorCopy("attach: write unix /tmp/ao.sock: broken pipe");
		expect(copy).not.toContain("unix");
		expect(terminalErrorCopy(undefined)).toBe(copy);
	});

	it("reports an exit without the raw exit code", () => {
		expect(terminalExitedCopy(0)).toBe("This session has ended.");
		expect(terminalExitedCopy(137)).not.toContain("137");
	});
});
