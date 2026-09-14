import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workflowQueryKeys } from "./useWorkflowPlans";

type RunView = components["schemas"]["ControllersRunView"];
type ControllersCreateRunRequest = components["schemas"]["ControllersCreateRunRequest"];
type CreateRetryRunRequest = components["schemas"]["CreateRetryRunRequest"];

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

export function useCreateRun(taskId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/runs", {
				body: { taskId } as ControllersCreateRunRequest,
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.run as RunView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
	});
}

export function useStartRun(taskId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (runId: string) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/runs/{id}/start", {
				params: { path: { id: runId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.run as RunView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
	});
}

export function useCancelRun(taskId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (runId: string) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/runs/{id}/cancel", {
				params: { path: { id: runId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.run as RunView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
	});
}

export function useCreateRetryRun(previousRunId: string, taskId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (mode: "resume" | "fresh") => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/runs/{id}/retry", {
				params: { path: { id: previousRunId } },
				body: { mode } as CreateRetryRunRequest,
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.run as RunView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
	});
}
