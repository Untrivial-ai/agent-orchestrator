import { useEffect, useRef } from "react";
import { useWorkspaceTraySessions } from "../hooks/useWorkspaceQuery";
import { aoBridge } from "../lib/bridge";
import { useNavigateToSession } from "../lib/navigate-to-session";

export function TrayRuntime() {
	const sessions = useWorkspaceTraySessions().data ?? [];
	const navigateToSession = useNavigateToSession();

	const lastPushed = useRef<string | null>(null);
	const serialized = JSON.stringify(sessions);
	useEffect(() => {
		if (lastPushed.current === serialized) return;
		lastPushed.current = serialized;
		aoBridge.tray.setAttentionState({ sessions });
	}, [serialized, sessions]);

	useEffect(() => {
		return aoBridge.tray.onOpenSession((target) => {
			navigateToSession(target.projectId, target.sessionId);
		});
	}, [navigateToSession]);

	return null;
}
