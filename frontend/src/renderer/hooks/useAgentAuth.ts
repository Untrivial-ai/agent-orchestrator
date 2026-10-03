import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiErrorMessage } from "../lib/api-client";
import { clientForSessionHost } from "../lib/host-clients";
import { shellTerminalsQueryKeyForHost, type ShellTerminal } from "./useShellTerminals";

export type AgentAuthPlan = components["schemas"]["AgentAuthPlan"];
export type StartAgentAuthResponse = components["schemas"]["StartAgentAuthResponse"];

export const agentAuthPlansQueryKey = ["agent-auth-plans"] as const;
export const agentAuthPlansQueryKeyForHost = (hostId?: string) => hostId ? ["agent-auth-plans", hostId] as const : agentAuthPlansQueryKey;

async function fetchAgentAuthPlans(hostId?: string): Promise<AgentAuthPlan[]> {
	const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/agents/auth-plans");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load agent authentication plans."));
	return data.plans;
}

export function useAgentAuthPlans(hostId?: string) {
	return useQuery({ queryKey: agentAuthPlansQueryKeyForHost(hostId), queryFn: () => fetchAgentAuthPlans(hostId), staleTime: 60_000 });
}

export function useStartAgentAuth(hostId?: string) {
	const queryClient = useQueryClient();
	const shellQueryKey = shellTerminalsQueryKeyForHost(hostId);
	return useMutation({
		mutationFn: async (agentId: string): Promise<StartAgentAuthResponse> => {
			const { data, error } = await clientForSessionHost(hostId).POST("/api/v1/agents/{agent}/auth", {
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
				...(hostId ? { hostId } : {}),
			};
			queryClient.setQueryData<ShellTerminal[]>(shellQueryKey, (current = []) => [
				...current.filter((item) => item.handleId !== terminal.handleId),
				terminal,
			]);
			void queryClient.invalidateQueries({ queryKey: shellQueryKey });
		},
	});
}

export async function probeAgentAuth(agentId: string, hostId?: string) {
	const { data, error } = await clientForSessionHost(hostId).POST("/api/v1/agents/{agent}/probe", {
		params: { path: { agent: agentId } },
	});
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not check agent login."));
	return data;
}
