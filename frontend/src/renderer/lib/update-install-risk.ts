import type { AgentProvider } from "../types/workspace";

/**
 * TUI runtimes and established persistent Chat hosts outlive daemon replacement.
 * The daemon reports the actual controller lifetime; do not duplicate provider
 * registrations here. Missing/unknown ownership remains conservatively at risk
 * while a turn may be active, including one parked for approval or user input.
 */
const TURN_MAY_BE_IN_FLIGHT: ReadonlySet<string> = new Set(["working", "no_signal", "needs_input"]);

export type UpdateRiskSession = {
	id: string;
	title: string;
	workspaceName: string;
	provider: AgentProvider;
	/** Optional on WorkspaceSession; an unset mode is not a chat session. */
	mode?: "chat" | "tui";
	status: string;
	isTerminated?: boolean;
	chatProviderPreserved?: boolean;
};

/** The sessions a restart-to-update would cost an in-flight turn. */
export function sessionsAtRiskFromInstall<T extends UpdateRiskSession>(sessions: readonly T[]): T[] {
	return sessions.filter(
		(session) =>
			session.isTerminated !== true &&
			session.mode === "chat" &&
			session.chatProviderPreserved !== true &&
			TURN_MAY_BE_IN_FLIGHT.has(session.status),
	);
}
