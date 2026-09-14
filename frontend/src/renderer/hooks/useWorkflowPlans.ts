import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

type PlanView = components["schemas"]["PlanView"];
type CreatePlanRequest = components["schemas"]["CreatePlanRequest"];

export const workflowQueryKeys = {
	plans: (projectId: string) => ["workflow", "plans", projectId] as const,
	plan: (planId: string) => ["workflow", "plan", planId] as const,
	stages: (planId: string) => ["workflow", "stages", planId] as const,
	stage: (stageId: string) => ["workflow", "stage", stageId] as const,
	tasks: (stageId: string) => ["workflow", "tasks", stageId] as const,
	task: (taskId: string) => ["workflow", "task", taskId] as const,
	runs: (taskId: string) => ["workflow", "runs", taskId] as const,
	reviews: (runId: string) => ["workflow", "reviews", runId] as const,
	review: (reviewId: string) => ["workflow", "review", reviewId] as const,
};

export function useWorkflowPlans(projectId: string) {
	return useQuery({
		queryKey: workflowQueryKeys.plans(projectId),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}/plans", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.plans ?? []) as PlanView[];
		},
		enabled: !!projectId,
		refetchInterval: (query) => {
			const plans = query.state.data as PlanView[] | undefined;
			if (!plans?.length) return false;
			const hasActive = plans.some((p) => p.status === "in_progress" || p.status === "confirmed");
			return hasActive ? 10_000 : false;
		},
	});
}

export function useWorkflowPlan(planId: string | null) {
	return useQuery({
		queryKey: workflowQueryKeys.plan(planId ?? ""),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/workflow/plans/{id}", {
				params: { path: { id: planId! } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.plan as PlanView;
		},
		enabled: !!planId,
	});
}

export function useCreatePlan() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (body: CreatePlanRequest) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/plans", { body });
			if (error) throw new Error(apiErrorMessage(error));
			return data?.plan as PlanView;
		},
		onSuccess: (_data, variables) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plans(variables.projectId) });
		},
	});
}

export function useConfirmPlan() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (planId: string) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/plans/{id}/confirm", {
				params: { path: { id: planId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.plan as PlanView;
		},
		onSuccess: (plan) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plans(plan.projectId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plan(plan.id) });
		},
		onError: (_err, planId) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plan(planId) });
		},
	});
}

export function useStartPlan() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (planId: string) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/plans/{id}/start", {
				params: { path: { id: planId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.plan as PlanView;
		},
		onSuccess: (plan) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plans(plan.projectId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plan(plan.id) });
		},
		onError: (_err, planId) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plan(planId) });
		},
	});
}

export function useCompletePlan() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (planId: string) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/plans/{id}/complete", {
				params: { path: { id: planId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.plan as PlanView;
		},
		onSuccess: (plan) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plans(plan.projectId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plan(plan.id) });
		},
		onError: (_err, planId) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plan(planId) });
		},
	});
}

export function useCancelPlan() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (planId: string) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/plans/{id}/cancel", {
				params: { path: { id: planId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.plan as PlanView;
		},
		onSuccess: (plan) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plans(plan.projectId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plan(plan.id) });
		},
		onError: (_err, planId) => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.plan(planId) });
		},
	});
}
