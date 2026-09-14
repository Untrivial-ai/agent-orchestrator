import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workflowQueryKeys } from "./useWorkflowPlans";

type RunReviewView = components["schemas"]["RunReviewView"];
type CreateRunReviewRequest = components["schemas"]["CreateRunReviewRequest"];
type RejectReviewRequest = components["schemas"]["RejectReviewRequest"];

export function useReviewsByRun(runId: string | null) {
	return useQuery({
		queryKey: workflowQueryKeys.reviews(runId ?? ""),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/workflow/runs/{id}/reviews", {
				params: { path: { id: runId! } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.reviews ?? []) as RunReviewView[];
		},
		enabled: !!runId,
	});
}

export function useCreateRunReview(runId: string, taskId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (input: { summary?: string; issues?: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/runs/{id}/reviews", {
				params: { path: { id: runId } },
				body: { source: "human", ...input } as CreateRunReviewRequest,
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.review as RunReviewView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.reviews(runId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.reviews(runId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
	});
}

export function usePassReview(reviewId: string, runId: string, taskId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/reviews/{id}/pass", {
				params: { path: { id: reviewId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.review as RunReviewView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.reviews(runId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.reviews(runId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
	});
}

export function useRejectReview(reviewId: string, runId: string, taskId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (issues: string) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/reviews/{id}/reject", {
				params: { path: { id: reviewId } },
				body: { issues } as RejectReviewRequest,
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data?.review as RunReviewView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.reviews(runId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.reviews(runId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.runs(taskId) });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.task(taskId) });
		},
	});
}
