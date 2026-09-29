import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type Automation = components["schemas"]["AutomationResponse"];
export type AutomationRun = components["schemas"]["AutomationRunResponse"];
export type CreateAutomationInput = components["schemas"]["CreateAutomationRequest"];
export type UpdateAutomationInput = components["schemas"]["UpdateAutomationRequest"];

export const automationsQueryKey = ["automations"] as const;

async function fetchAutomationPage(cursor?: string) {
	const { data, error } = await apiClient.GET("/api/v1/automations", { params: { query: { limit: 100, cursor } } });
	if (error) throw new Error(apiErrorMessage(error));
	return data;
}

export function useAutomations() {
	return useQuery({
		queryKey: automationsQueryKey,
		queryFn: async () => {
			const automations: Automation[] = [];
			let cursor: string | undefined;
			do {
				const page = await fetchAutomationPage(cursor);
				automations.push(...(page?.automations ?? []));
				cursor = page?.nextCursor;
			} while (cursor);
			return automations;
		},
		refetchInterval: 15_000,
	});
}

export function useCreateAutomation() {
	const client = useQueryClient();
	return useMutation({
		mutationFn: async (body: CreateAutomationInput) => {
			const { data, error } = await apiClient.POST("/api/v1/automations", { body });
			if (error) throw new Error(apiErrorMessage(error));
			if (!data) throw new Error("Automation response was empty");
			return data.automation;
		},
		onSuccess: (automation) => client.setQueryData<Automation[]>(automationsQueryKey, (current = []) => [...current, automation]),
	});
}

export function useUpdateAutomation() {
	const client = useQueryClient();
	return useMutation({
		mutationFn: async ({ id, body }: { id: string; body: UpdateAutomationInput }) => {
			const { data, error } = await apiClient.PATCH("/api/v1/automations/{automationId}", { params: { path: { automationId: id } }, body });
			if (error) throw new Error(apiErrorMessage(error));
			if (!data) throw new Error("Automation response was empty");
			return data.automation;
		},
		onMutate: async ({ id, body }) => {
			await client.cancelQueries({ queryKey: automationsQueryKey });
			const previous = client.getQueryData<Automation[]>(automationsQueryKey);
			client.setQueryData<Automation[]>(automationsQueryKey, (current = []) => current.map((item) => item.id === id ? { ...item, ...body } as Automation : item));
			return { previous };
		},
		onError: (_error, _variables, context) => client.setQueryData(automationsQueryKey, context?.previous),
		onSuccess: (automation) => client.setQueryData<Automation[]>(automationsQueryKey, (current = []) => current.map((item) => item.id === automation.id ? automation : item)),
	});
}

export function useDeleteAutomation() {
	const client = useQueryClient();
	return useMutation({
		mutationFn: async (id: string) => {
			const { error } = await apiClient.DELETE("/api/v1/automations/{automationId}", { params: { path: { automationId: id } } });
			if (error) throw new Error(apiErrorMessage(error));
			return id;
		},
		onSuccess: (id) => client.setQueryData<Automation[]>(automationsQueryKey, (current = []) => current.filter((item) => item.id !== id)),
	});
}

export function useAutomationRuns(id: string | null) {
	return useQuery({
		queryKey: ["automations", id, "runs"],
		queryFn: async () => {
			if (!id) return [];
			const { data, error } = await apiClient.GET("/api/v1/automations/{automationId}/runs", { params: { path: { automationId: id }, query: { limit: 100 } } });
			if (error) throw new Error(apiErrorMessage(error));
			return data?.runs ?? [];
		},
		enabled: Boolean(id),
		refetchInterval: 15_000,
	});
}
