import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import type { WorkflowMode, WorkspaceSummary } from "../types/workspace";
import { workspaceQueryKey } from "./useWorkspaceQuery";

export type SetWorkflowModeInput = {
	sessionId: string;
	workflowMode: WorkflowMode;
};

function updateSessionWorkflowMode(
	workspaces: WorkspaceSummary[] | undefined,
	sessionId: string,
	workflowMode: WorkflowMode,
): WorkspaceSummary[] | undefined {
	return workspaces?.map((workspace) => ({
		...workspace,
		sessions: workspace.sessions.map((session) =>
			session.id === sessionId ? { ...session, workflowMode } : session,
		),
	}));
}

/**
 * Persist the user-controlled delivery stage for one session. The write is
 * optimistic so the composer border and board lane move on the keystroke rather
 * than on the round trip.
 */
export function useSetWorkflowMode() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ sessionId, workflowMode }: SetWorkflowModeInput) => {
			if (usesPreviewWorkspaceData) return;
			const { error, response } = await apiClient.PATCH("/api/v1/sessions/{sessionId}/workflow-mode", {
				params: { path: { sessionId } },
				body: { workflowMode },
			});
			if (error) {
				throw new Error(apiErrorMessage(error, `Failed to update workflow mode (${response.status})`));
			}
		},
		onMutate: async ({ sessionId, workflowMode }) => {
			await queryClient.cancelQueries({ queryKey: workspaceQueryKey });
			const previous = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
			queryClient.setQueryData<WorkspaceSummary[]>(workspaceQueryKey, (current) =>
				updateSessionWorkflowMode(current, sessionId, workflowMode),
			);
			return { previous };
		},
		onError: (_error, _input, context) => {
			if (context?.previous) queryClient.setQueryData(workspaceQueryKey, context.previous);
		},
		onSettled: () => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});
}
