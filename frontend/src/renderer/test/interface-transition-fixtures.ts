import type {
	SessionInterfaceTransition,
	SessionInterfaceTransitionStatus,
} from "../hooks/useSessionInterfaceTransition";

export function sessionInterfaceTransition(
	sessionId: string,
	phase: SessionInterfaceTransition["phase"] = "source_stopped",
): SessionInterfaceTransition {
	return {
		createdAt: "2026-09-21T00:00:00Z",
		historyPolicy: "strict",
		id: `transition-${sessionId}`,
		phase,
		policy: "drain",
		sessionId,
		sourceMode: "tui",
		targetMode: "chat",
		updatedAt: "2026-09-21T00:00:01Z",
	};
}

export function sessionInterfaceTransitionStatus(
	sessionId: string,
	phase: SessionInterfaceTransition["phase"] = "source_stopped",
): SessionInterfaceTransitionStatus {
	return {
		supported: true,
		targetMode: "chat",
		transition: sessionInterfaceTransition(sessionId, phase),
	};
}
