import { useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { apiClient } from "../lib/api-client";
import { useUiStore } from "../stores/ui-store";
import { sessionIsActive, type WorkspaceSession } from "../types/workspace";
import { workspaceQueryKey } from "./useWorkspaceQuery";

/** Route an HTTP(S) link to the browser surface available for the active session. */
export function useSessionBrowserLink(session?: WorkspaceSession): (uri: string) => void {
	const queryClient = useQueryClient();
	const setInspectorView = useUiStore((state) => state.setInspectorView);
	const setInspectorOpen = useUiStore((state) => state.setInspectorOpen);
	const active = session ? sessionIsActive(session) : false;

	return useCallback(
		(uri: string) => {
			if (!session?.id || !active) return;
			try {
				const url = new URL(uri);
				if (url.protocol !== "http:" && url.protocol !== "https:") return;
			} catch {
				return;
			}

			const sessionId = session.id;
			setInspectorView(sessionId, "browser");
			setInspectorOpen(sessionId, true);
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
		[active, queryClient, session?.id, session?.kind, setInspectorOpen, setInspectorView],
	);
}
