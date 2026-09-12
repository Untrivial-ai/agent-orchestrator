import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workflowQueryKeys } from "./useWorkflowPlans";

type TaskView = components["schemas"]["TaskView"];

export function useWorkflowTasks(stageId: string | null) {
	return useQuery({
		queryKey: workflowQueryKeys.tasks(stageId ?? ""),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/workflow/stages/{id}/tasks", {
				params: { path: { id: stageId! } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.tasks ?? []) as TaskView[];
		},
		enabled: !!stageId,
		refetchInterval: (query) => {
			const tasks = query.state.data as TaskView[] | undefined;
			if (!tasks?.length) return false;
			const hasActive = tasks.some((t) => t.status === "running");
			return hasActive ? 5_000 : false;
		},
	});
}

export function useWorkflowTask(taskId: string | null) {
	return useQuery({
		queryKey: workflowQueryKeys.task(taskId ?? ""),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/workflow/tasks/{id}", {
				params: { path: { id: taskId! } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.task as TaskView;
		},
		enabled: !!taskId,
	});
}
