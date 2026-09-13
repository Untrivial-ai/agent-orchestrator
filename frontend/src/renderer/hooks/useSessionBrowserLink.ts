import { useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { apiClient } from "../lib/api-client";
import { isWorkspaceFileLink, openLinkInSystemBrowser, requiresSystemBrowser } from "../lib/external-link-policy";
import { useUiStore } from "../stores/ui-store";
import { sessionIsActive, type WorkspaceSession } from "../types/workspace";
import { workspaceQueryKey } from "./useWorkspaceQuery";

/** Open an HTTP(S) link in the active worker session's AO Browser panel. */
export function useSessionBrowserLink(
	session?: WorkspaceSession,
	openInBrowser?: (uri: string) => Promise<void>,
	workspacePaths: string[] = [],
): (uri: string) => void {
	const queryClient = useQueryClient();
	const setInspectorView = useUiStore((state) => state.setInspectorView);
	const setInspectorOpen = useUiStore((state) => state.setInspectorOpen);
	const active = session ? sessionIsActive(session) : false;

	return useCallback(
		(uri: string) => {
			if (!session?.id || session.kind !== "worker" || !active) return;
			const isLocalWorkspaceFile = isWorkspaceFileLink(uri, workspacePaths);
			try {
				const url = new URL(uri);
				if (url.protocol !== "http:" && url.protocol !== "https:") return;
			} catch {
				if (!isLocalWorkspaceFile) return;
			}
			if (requiresSystemBrowser(uri)) {
				void openLinkInSystemBrowser(uri);
				return;
			}

			const sessionId = session.id;
			setInspectorView(sessionId, "browser");
			setInspectorOpen(sessionId, true);
			// Local workspace paths must go through the daemon preview resolver first.
			// Passing an absolute worktree path directly to BrowserView opens an empty
			// tab because Chromium cannot navigate to the filesystem path.
			if (openInBrowser && !isLocalWorkspaceFile) {
				void openInBrowser(uri).catch((error) => {
					console.warn("Unable to open link in Browser tab", error);
				});
				return;
			}
			void (async () => {
				try {
					const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/preview", {
						params: { path: { sessionId } },
						body: { url: uri },
					});
					if (error) {
						console.warn("Unable to open link in Browser preview", error);
						return;
					}
					await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
				} catch (error) {
					console.warn("Unable to open link in Browser preview", error);
				}
			})();
		},
		[active, openInBrowser, queryClient, session?.id, session?.kind, setInspectorOpen, setInspectorView, workspacePaths],
	);
}
