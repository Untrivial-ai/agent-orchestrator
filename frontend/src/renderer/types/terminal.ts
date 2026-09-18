export type TerminalTarget =
	| { kind: "worker" }
	| {
			kind: "reviewer";
			handleId: string;
			harness: string;
			sessionId: string;
			/** Durable Cloud review state for this exact dedicated terminal. */
			reviewStatus?: "running" | "complete" | "delivered" | "failed" | "cancelled";
	  }
	// A standalone shell the user opened by hand — no agent session behind it,
	// so unlike "worker" and "reviewer" it carries its own handle and never
	// reads from the selected session.
	| {
			/** Shell creation identity; prevents a reused handle inheriting old state. */
			generation: string;
			kind: "shell";
			handleId: string;
			/** Undefined only for a standalone shell outside a session route. */
			sessionId?: string;
			title: string;
	  };

export function terminalTargetBelongsToSession(target: TerminalTarget, sessionId: string | undefined): boolean {
	if (target.kind === "worker") return true;
	return target.sessionId === sessionId;
}
