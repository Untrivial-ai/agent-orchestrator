import { useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { apiClient, getApiBaseUrl } from "../lib/api-client";
import { attachmentURL } from "../components/chat/messageAttachments";
import { isPotentialWorkspaceFileLink, isWebLink, workspaceFilePath } from "../lib/external-link-policy";
import { useUiStore } from "../stores/ui-store";
import { sessionIsActive, type WorkspaceSession } from "../types/workspace";
import { workspaceQueryKey } from "./useWorkspaceQuery";

function workspaceFilePreviewURL(uri: string, sessionId: string, workspacePaths: string[]): string | undefined {
	const workspacePath = workspaceFilePath(uri, workspacePaths);
	return workspacePath ? attachmentURL(getApiBaseUrl(), sessionId, workspacePath) : undefined;
}

/** Open a supported link in the active session's AO Browser panel. */
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
			if (!session?.id || !active) return;
			const workspacePath = workspaceFilePath(uri, workspacePaths);
			const webLink = isWebLink(uri);
			if (!webLink && !workspacePath && !isPotentialWorkspaceFileLink(uri)) return;
			const sessionId = session.id;
			setInspectorView(sessionId, "browser");
			setInspectorOpen(sessionId, true);
			// Local workspace paths must go through the daemon preview resolver first.
			// Passing an absolute worktree path directly to BrowserView opens an empty
			// tab because Chromium cannot navigate to the filesystem path.
			if (openInBrowser && webLink) {
				void openInBrowser(uri).catch((error) => {
					console.warn("Unable to open link in Browser tab", error);
				});
				return;
			}
			if (openInBrowser && workspacePath) {
				const previewURL = workspaceFilePreviewURL(uri, sessionId, workspacePaths);
				if (previewURL) {
					void openInBrowser(previewURL).catch((error) => {
						console.warn("Unable to open workspace file in Browser tab", error);
					});
					return;
				}
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
