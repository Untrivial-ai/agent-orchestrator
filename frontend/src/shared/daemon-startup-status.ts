import type { DaemonStatus } from "./daemon-status";

export const SLOW_DAEMON_START_MESSAGE =
	"AO daemon is still starting. Session recovery can take a while.";

type SlowDaemonStartupInput = {
	output: string;
	executablePath: string;
	workingDirectory: string;
	handshakePath: string | null;
};

export function slowDaemonStartupStatus(input: SlowDaemonStartupInput): DaemonStatus {
	return {
		state: "starting",
		message: SLOW_DAEMON_START_MESSAGE,
		details: startupDetails(input),
		executablePath: input.executablePath,
		workingDirectory: input.workingDirectory,
	};
}

export function isSlowDaemonStartupStatus(status: DaemonStatus): boolean {
	return status.state === "starting" && status.message === SLOW_DAEMON_START_MESSAGE;
}

export function refreshSlowDaemonStartupDetails(status: DaemonStatus, output: string): DaemonStatus {
	const details = output.trim();
	if (!isSlowDaemonStartupStatus(status) || !details || details === status.details) {
		return status;
	}
	return { ...status, details };
}

function startupDetails(input: SlowDaemonStartupInput): string {
	const output = input.output.trim();
	if (output) return output;
	return [
		"No startup output was captured yet.",
		`Executable: ${input.executablePath}`,
		`Working directory: ${input.workingDirectory}`,
		`Expected port confirmation from: ${input.handshakePath ?? "running.json"}`,
	].join("\n");
}
