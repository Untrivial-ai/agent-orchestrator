import { type QueryClient, useMutation, useMutationState, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import type { WorkspaceSession } from "../types/workspace";
import { agentSwitchesQueryKey, type AgentSwitch } from "./useAgentSwitches";
import {
	conversationConfigOptionsQueryKey,
	conversationModelsQueryKey,
	conversationQueryKey,
} from "./useConversation";
import { workspaceQueryKey } from "./useWorkspaceQuery";

export type SwitchAgentHarness = components["schemas"]["SwitchAgentRequest"]["targetHarness"];

export type SwitchAgentInput = {
	session: WorkspaceSession;
	targetHarness: SwitchAgentHarness;
	model: string;
	idempotencyKey: string;
};

export const switchAgentMutationKey = ["switch-agent"] as const;
export const recoverAgentSwitchMutationKey = ["recover-agent-switch"] as const;

type SwitchAgentMutationState = {
	error: unknown;
	input?: SwitchAgentInput;
	status: "error" | "idle" | "pending" | "success";
	submittedAt: number;
};

function useSwitchAgentMutations() {
	return useMutationState<SwitchAgentMutationState>({
		filters: { mutationKey: switchAgentMutationKey },
		select: (mutation) => ({
			error: mutation.state.error,
			input: mutation.state.variables as SwitchAgentInput | undefined,
			status: mutation.state.status,
			submittedAt: mutation.state.submittedAt,
		}),
	});
}

export function useSwitchAgentState(sessionId: string) {
	const mutations = useSwitchAgentMutations();
	let latest: SwitchAgentMutationState | undefined;
	let pending: SwitchAgentMutationState | undefined;
	for (const mutation of mutations) {
		if (mutation.input?.session.id !== sessionId) continue;
		if (!latest || mutation.submittedAt > latest.submittedAt) latest = mutation;
		if (
			mutation.status === "pending" &&
			(!pending || mutation.submittedAt > pending.submittedAt)
		) {
			pending = mutation;
		}
	}

	return {
		error:
			!pending &&
			latest?.status === "error" &&
			latest.error instanceof Error
				? latest.error.message
				: null,
		input: pending?.input,
		isPending: Boolean(pending),
	};
}

export function clearSwitchAgentState(queryClient: QueryClient, sessionId: string) {
	const mutationCache = queryClient.getMutationCache();
	for (const mutation of mutationCache.findAll({ mutationKey: switchAgentMutationKey })) {
		const input = mutation.state.variables as SwitchAgentInput | undefined;
		if (input?.session.id === sessionId && mutation.state.status !== "pending") {
			mutationCache.remove(mutation);
		}
	}
}

export function createSwitchAgentIdempotencyKey(): string {
	return crypto.randomUUID();
}

export function useSwitchAgent() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationKey: switchAgentMutationKey,
		mutationFn: async ({ session, targetHarness, model, idempotencyKey }: SwitchAgentInput) => {
			const body: {
				targetHarness: SwitchAgentHarness;
				model?: string;
				idempotencyKey: string;
			} = { targetHarness, idempotencyKey };
			const normalizedModel = model.trim();
			if (normalizedModel) body.model = normalizedModel;
			const { data, error, response } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/switch-agent",
				{
					params: { path: { sessionId: session.id } },
					body,
				},
			);
			if (error || response.status !== 202 || !data?.switch) {
				const fallback = response
					? `Failed to switch agent (${response.status})`
					: "Failed to switch agent";
				throw new Error(apiErrorMessage(error, fallback));
			}
			return data.switch;
		},
		onSuccess: (agentSwitch, variables) => {
			if (!agentSwitch) return;
			queryClient.setQueryData<AgentSwitch[]>(
				agentSwitchesQueryKey(variables.session.id),
				(current = []) => [agentSwitch, ...current.filter((entry) => entry.id !== agentSwitch.id)],
			);
			void queryClient.invalidateQueries({ queryKey: conversationQueryKey(variables.session.id) });
			void queryClient.invalidateQueries({ queryKey: conversationModelsQueryKey(variables.session.id) });
			void queryClient.invalidateQueries({
				queryKey: conversationConfigOptionsQueryKey(variables.session.id),
			});
		},
		onSettled: (_data, _error, variables) => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			void queryClient.invalidateQueries({ queryKey: agentSwitchesQueryKey(variables.session.id) });
		},
	});
}

export function useRecoverAgentSwitch() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationKey: recoverAgentSwitchMutationKey,
		mutationFn: async ({ sessionId, switchId }: { sessionId: string; switchId: string }) => {
			const { data, error, response } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/agent-switches/{switchId}/recover",
				{ params: { path: { sessionId, switchId } } },
			);
			if (error || response.status !== 202 || !data?.switch) {
				const fallback = response
					? `Failed to recover agent switch (${response.status})`
					: "Failed to recover agent switch";
				throw new Error(apiErrorMessage(error, fallback));
			}
			return data.switch;
		},
		onSuccess: (agentSwitch, variables) => {
			queryClient.setQueryData<AgentSwitch[]>(
				agentSwitchesQueryKey(variables.sessionId),
				(current = []) => [agentSwitch, ...current.filter((entry) => entry.id !== agentSwitch.id)],
			);
		},
		onSettled: (_data, _error, variables) => {
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			void queryClient.invalidateQueries({ queryKey: agentSwitchesQueryKey(variables.sessionId) });
		},
	});
}
