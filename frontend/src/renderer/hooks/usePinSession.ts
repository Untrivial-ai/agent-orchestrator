import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { WorkspaceSession } from "../types/workspace";
import { workspaceQueryKeyForHost } from "./useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { clientForSessionHost } from "../lib/host-clients";

export const pinSessionMutationKey = ["pin-session"] as const;
export const unpinSessionMutationKey = ["unpin-session"] as const;

export function usePinSession() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationKey: pinSessionMutationKey,
		mutationFn: async (session: WorkspaceSession) => {
			const { error, response } = await (session.hostId ? clientForSessionHost(session.hostId) : apiClient).POST("/api/v1/sessions/{sessionId}/pin", {
				params: { path: { sessionId: session.id } },
			});
			if (error) {
				const fallback = response ? `Failed to pin session (${response.status})` : "Failed to pin session";
				throw new Error(apiErrorMessage(error, fallback));
			}
		},
		onSuccess: async (_data, session) => {
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKeyForHost(session.hostId) });
		},
		onError: (error) => {
			console.error("Failed to pin session:", error);
		},
	});
}

export function useUnpinSession() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationKey: unpinSessionMutationKey,
		mutationFn: async (session: WorkspaceSession) => {
			const { error, response } = await (session.hostId ? clientForSessionHost(session.hostId) : apiClient).DELETE("/api/v1/sessions/{sessionId}/pin", {
				params: { path: { sessionId: session.id } },
			});
			if (error) {
				const fallback = response ? `Failed to unpin session (${response.status})` : "Failed to unpin session";
				throw new Error(apiErrorMessage(error, fallback));
			}
		},
		onSuccess: async (_data, session) => {
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKeyForHost(session.hostId) });
		},
		onError: (error) => {
			console.error("Failed to unpin session:", error);
		},
	});
}
