import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { useUiStore } from "../stores/ui-store";
import { workspaceQueryKey } from "./useWorkspaceQuery";

export function useReapplyPreservedEdits() {
	const queryClient = useQueryClient();
	return useCallback(
		async (sessionId: string) => {
			const { data, error, response } = await apiClient.POST("/api/v1/sessions/{sessionId}/reapply-edits", {
				params: { path: { sessionId } },
			});
			if (error) {
				const fallback = response
					? `Could not put saved edits back (${response.status})`
					: "Could not put saved edits back";
				const message = apiErrorMessage(error, fallback);
				useUiStore.getState().showGlobalToast(message, undefined, "error");
				return { ok: false as const, message };
			}
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			const message = data?.conflicts
				? "Some edits conflict. What fits is in the worktree."
				: "Saved edits are back in the worktree.";
			useUiStore.getState().showGlobalToast(message);
			return { ok: true as const, conflicts: data?.conflicts === true, message };
		},
		[queryClient],
	);
}
