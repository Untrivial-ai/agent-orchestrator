import { useCallback } from "react";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { useUiStore } from "../stores/ui-store";
import { useNavigateToSession } from "./navigate-to-session";
import { parseSessionLink, resolveSessionLink } from "./session-links";

export function useSessionLinkNavigation(): (url: string) => boolean {
	const workspaceQuery = useWorkspaceQuery();
	const navigateToSession = useNavigateToSession();
	const setSessionLinkError = useUiStore((state) => state.setSessionLinkError);
	return useCallback((url: string) => {
		const target = parseSessionLink(url);
		if (!target) {
			setSessionLinkError("This AO session link is malformed or unsupported.");
			return false;
		}
		if (!workspaceQuery.isSuccess || !workspaceQuery.data) {
			setSessionLinkError("AO could not verify that session. Check the daemon connection and try again.");
			return false;
		}
		const resolved = resolveSessionLink(url, workspaceQuery.data);
		if (!resolved) {
			setSessionLinkError("That session is missing or is not accessible in this AO workspace.");
			return false;
		}
		setSessionLinkError(null);
		navigateToSession(resolved.projectId, resolved.sessionId);
		return true;
	}, [navigateToSession, setSessionLinkError, workspaceQuery.data, workspaceQuery.isSuccess]);
}
