import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workflowQueryKeys } from "./useWorkflowPlans";

type RunView = components["schemas"]["ControllersRunView"];

export function useWorkflowRuns(taskId: string | null) {
	return useQuery({
		queryKey: workflowQueryKeys.runs(taskId ?? ""),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/workflow/tasks/{id}/runs", {
				params: { path: { id: taskId! } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.runs ?? []) as RunView[];
		},
		enabled: !!taskId,
		refetchInterval: (query) => {
			const runs = query.state.data as RunView[] | undefined;
			if (!runs?.length) return false;
			const hasActive = runs.some((r) => r.status === "pending" || r.status === "running");
			return hasActive ? 3_000 : false;
		},
	});
}
