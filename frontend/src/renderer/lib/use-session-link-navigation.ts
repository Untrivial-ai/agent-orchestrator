import { useCallback } from "react";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { useUiStore } from "../stores/ui-store";
import { useNavigateToSession } from "./navigate-to-session";
import { parseSessionLink, resolveSessionLink } from "./session-links";

export function useSessionLinkNavigation(): (url: string) => boolean {
	const workspaceQuery = useWorkspaceQuery();
	const navigateToSession = useNavigateToSession();
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	return useCallback((url: string) => {
		const target = parseSessionLink(url);
		if (!target) {
			showGlobalToast("This AO session link is malformed or unsupported.", undefined, {
				tone: "error",
				placement: "top-center",
				dismissible: true,
				dedupeKey: "session-link:error",
			});
			return false;
		}
		if (!workspaceQuery.isSuccess || !workspaceQuery.data) {
			showGlobalToast("AO could not verify that session. Check the daemon connection and try again.", undefined, {
				tone: "error",
				placement: "top-center",
				dismissible: true,
				dedupeKey: "session-link:error",
			});
			return false;
		}
		const resolved = resolveSessionLink(url, workspaceQuery.data);
		if (!resolved) {
			showGlobalToast("That session is missing or is not accessible in this AO workspace.", undefined, {
				tone: "error",
				placement: "top-center",
				dismissible: true,
				dedupeKey: "session-link:error",
			});
			return false;
		}
		if (resolved.isTerminated) {
			showGlobalToast(`Session ${resolved.sessionId} is terminated`, undefined, {
				placement: "top-center",
				dismissible: true,
				durationMs: 5_000,
				dedupeKey: `session-link:${resolved.projectId}:${resolved.sessionId}`,
			});
			return false;
		}
		navigateToSession(resolved.projectId, resolved.sessionId);
		return true;
	}, [navigateToSession, showGlobalToast, workspaceQuery.data, workspaceQuery.isSuccess]);
}
