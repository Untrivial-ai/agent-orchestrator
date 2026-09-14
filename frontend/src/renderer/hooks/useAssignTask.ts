import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workflowQueryKeys } from "./useWorkflowPlans";

export function useAssignTask(taskId: string, stageId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (body: { agentRoleId: string; providerId: string; providerModelId: string }) => {
			const { data, error } = await apiClient.PATCH("/api/v1/workflow/tasks/{id}/assignment", {
				params: { path: { id: taskId } },
				body,
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.tasks(stageId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.tasks(stageId) });
		},
	});
}
