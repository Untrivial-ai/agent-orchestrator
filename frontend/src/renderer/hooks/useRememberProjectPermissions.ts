import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { apiErrorMessage } from "../lib/api-client";
import { clientForSessionHost } from "../lib/host-clients";
import type { ApprovalMode } from "../types/conversation";
import { remoteWorkspaceQueryKey, workspaceQueryKey } from "./useWorkspaceQuery";

/** Save only the confirmed permission choice; selecting a session mode is separate. */
export function useRememberProjectPermissions(projectId: string, sourceHarness?: string, hostId?: string) {
	const queryClient = useQueryClient();
	const mutation = useMutation({
		mutationFn: async ({ targetProjectId, permissions, sourceHarness }: { targetProjectId: string; permissions: ApprovalMode; sourceHarness?: string }) => {
			const { error } = await clientForSessionHost(hostId).PATCH("/api/v1/projects/{id}/permissions", {
				params: { path: { id: targetProjectId } },
				body: { permissions, ...(sourceHarness ? { sourceHarness } : {}) },
			});
			if (error) throw error;
		},
		onSuccess: (_data, { targetProjectId }) => {
			void (hostId
				? queryClient.invalidateQueries({ queryKey: remoteWorkspaceQueryKey(hostId) })
				: Promise.all([
					queryClient.invalidateQueries({ queryKey: ["project", targetProjectId] }),
					queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
				])).catch(() => {});
		},
	});
	const remember = useCallback((permissions: ApprovalMode) =>
		mutation.mutateAsync({ targetProjectId: projectId, permissions, sourceHarness }), [mutation.mutateAsync, projectId, sourceHarness]);
	const belongsToProject = mutation.variables?.targetProjectId === projectId;
	return {
		remember,
		pending: belongsToProject && mutation.isPending,
		savedMode: belongsToProject && mutation.isSuccess && mutation.variables?.sourceHarness === sourceHarness
			? mutation.variables.permissions : undefined,
		error: belongsToProject && mutation.error
			? apiErrorMessage(mutation.error, "Could not remember permissions for this project.")
			: undefined,
	};
}
