import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { shellTerminalsQueryKey, type ShellTerminal } from "./useShellTerminals";

export type AgentAuthPlan = components["schemas"]["AgentAuthPlan"];
export type StartAgentAuthResponse = components["schemas"]["StartAgentAuthResponse"];

export const agentAuthPlansQueryKey = ["agent-auth-plans"] as const;

async function fetchAgentAuthPlans(): Promise<AgentAuthPlan[]> {
	const { data, error } = await apiClient.GET("/api/v1/agents/auth-plans");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load agent authentication plans."));
	return data.plans;
}

export function useAgentAuthPlans() {
	return useQuery({ queryKey: agentAuthPlansQueryKey, queryFn: fetchAgentAuthPlans, staleTime: 60_000 });
}

export function useStartAgentAuth() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (agentId: string): Promise<StartAgentAuthResponse> => {
			const { data, error } = await apiClient.POST("/api/v1/agents/{agent}/auth", {
				params: { path: { agent: agentId } },
			});
			if (error || !data) throw new Error(apiErrorMessage(error, "Could not start agent authentication."));
			return data;
		},
		onSuccess: (result) => {
			const terminal: ShellTerminal = {
				handleId: result.terminal.handleId,
				projectId: result.terminal.projectId,
				sessionId: result.terminal.sessionId,
				workingDir: result.terminal.workingDir,
				title: result.terminal.title,
				createdAt: result.terminal.createdAt,
			};
			queryClient.setQueryData<ShellTerminal[]>(shellTerminalsQueryKey, (current = []) => [
				...current.filter((item) => item.handleId !== terminal.handleId),
				terminal,
			]);
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		},
	});
}

export async function probeAgentAuth(agentId: string) {
	const { data, error } = await apiClient.POST("/api/v1/agents/{agent}/probe", {
		params: { path: { agent: agentId } },
	});
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not check agent login."));
	return data;
}
