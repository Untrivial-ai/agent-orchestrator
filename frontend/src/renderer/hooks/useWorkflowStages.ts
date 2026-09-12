import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workflowQueryKeys } from "./useWorkflowPlans";

type StageView = components["schemas"]["StageView"];

export function useWorkflowStages(planId: string | null) {
	return useQuery({
		queryKey: workflowQueryKeys.stages(planId ?? ""),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/workflow/plans/{id}/stages", {
				params: { path: { id: planId! } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.stages ?? []) as StageView[];
		},
		enabled: !!planId,
		refetchInterval: (query) => {
			const stages = query.state.data as StageView[] | undefined;
			if (!stages?.length) return false;
			const hasActive = stages.some((s) => s.status === "in_progress");
			return hasActive ? 5_000 : false;
		},
	});
}

export function useStartStage() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ stageId }: { stageId: string; planId: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/stages/{id}/start", {
				params: { path: { id: stageId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.stage as StageView;
		},
		onSuccess: (_stage, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
		onError: (_err, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
	});
}

export function useReadyForApprovalStage() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ stageId }: { stageId: string; planId: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/stages/{id}/ready", {
				params: { path: { id: stageId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.stage as StageView;
		},
		onSuccess: (_stage, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
		onError: (_err, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
	});
}

export function usePassStage() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ stageId }: { stageId: string; planId: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/stages/{id}/pass", {
				params: { path: { id: stageId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.stage as StageView;
		},
		onSuccess: (_stage, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
		onError: (_err, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
	});
}

export function useBlockStage() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ stageId }: { stageId: string; planId: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/stages/{id}/block", {
				params: { path: { id: stageId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.stage as StageView;
		},
		onSuccess: (_stage, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
		onError: (_err, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
	});
}

export function useUnblockStage() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ stageId }: { stageId: string; planId: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/stages/{id}/unblock", {
				params: { path: { id: stageId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.stage as StageView;
		},
		onSuccess: (_stage, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
		onError: (_err, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
	});
}

export function useCancelStage() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ stageId }: { stageId: string; planId: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/stages/{id}/cancel", {
				params: { path: { id: stageId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.stage as StageView;
		},
		onSuccess: (_stage, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
		onError: (_err, { planId }) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.stages(planId) });
		},
	});
}
