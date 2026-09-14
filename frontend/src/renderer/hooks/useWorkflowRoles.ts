import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { workflowQueryKeys } from "./useWorkflowPlans";

type AgentRoleView = components["schemas"]["ControllersAgentRoleView"];
type CreateAgentRoleRequest = components["schemas"]["ControllersCreateAgentRoleRequest"];
type UpdateAgentRoleRequest = components["schemas"]["ControllersUpdateAgentRoleRequest"];

export function useAgentRoles() {
	return useQuery({
		queryKey: workflowQueryKeys.roles(),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/workflow/roles");
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.roles ?? []) as AgentRoleView[];
		},
	});
}

export function useAgentRole(roleId: string | null) {
	return useQuery({
		queryKey: workflowQueryKeys.role(roleId ?? ""),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/workflow/roles/{id}", {
				params: { path: { id: roleId! } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data as AgentRoleView;
		},
		enabled: !!roleId,
	});
}

export function useCreateAgentRole() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (body: CreateAgentRoleRequest) => {
			const { data, error } = await apiClient.POST("/api/v1/workflow/roles", { body });
			if (error) throw new Error(apiErrorMessage(error));
			return data as AgentRoleView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.roles() });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.roles() });
		},
	});
}

export function useUpdateAgentRole(roleId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (body: UpdateAgentRoleRequest) => {
			const { data, error } = await apiClient.PATCH("/api/v1/workflow/roles/{id}", {
				params: { path: { id: roleId } },
				body,
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data as AgentRoleView;
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.roles() });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.role(roleId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.roles() });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.role(roleId) });
		},
	});
}

export function useSetAgentRoleEnabled(roleId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (enabled: boolean) => {
			const { error } = await apiClient.PATCH("/api/v1/workflow/roles/{id}/enabled", {
				params: { path: { id: roleId } },
				body: { enabled },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.roles() });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.role(roleId) });
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.roles() });
			void queryClient.invalidateQueries({ queryKey: workflowQueryKeys.role(roleId) });
		},
	});
}
