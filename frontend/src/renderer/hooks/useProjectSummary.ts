import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";

type ProjectSummary = components["schemas"]["ProjectSummaryResponse"]["summary"];

function previewSummary(projectId: string): ProjectSummary {
	return {
		projectId,
		sourceWatermark: "preview",
		generatedAt: new Date().toISOString(),
		narrative: "Three workers are moving the desktop preview forward. One reviewer decision is holding the terminal polish work, while the dashboard and browser preview remain active.",
		activeWorkers: 3,
		completedWorkers: 1,
		needsAttention: [{ sessionId: "demo-needs-input", sessionName: "Resolve reviewer feedback on terminal polish", question: "Should the compact terminal keep the status row visible when space is constrained?" }],
		outputs: [{ sessionId: "demo-needs-input", sessionName: "Resolve reviewer feedback on terminal polish", kind: "pull_request", url: "https://github.com/aoagents/agent-orchestrator/pull/318", number: 318, state: "Changes requested" }],
	};
}

export const projectSummaryQueryKey = (projectId: string) => ["project-summary", projectId] as const;

export function useProjectSummary(projectId: string, enabled: boolean) {
	const queryClient = useQueryClient();
	const query = useQuery({
		queryKey: projectSummaryQueryKey(projectId),
		enabled,
		queryFn: async () => {
			if (usesPreviewWorkspaceData) return previewSummary(projectId);
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}/summary", { params: { path: { id: projectId } } });
			if (error) throw error;
			return data.summary;
		},
	});
	const refresh = useMutation({
		mutationFn: async () => {
			if (usesPreviewWorkspaceData) return previewSummary(projectId);
			const { data, error } = await apiClient.POST("/api/v1/projects/{id}/summary/refresh", { params: { path: { id: projectId } } });
			if (error) throw error;
			return data.summary;
		},
		onSuccess: (summary) => queryClient.setQueryData(projectSummaryQueryKey(projectId), summary),
	});
	return { ...query, refresh };
}
