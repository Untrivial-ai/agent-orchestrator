import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { AuthWorkflow } from "../components/AuthTerminalPanel";
import { isActiveInstallJob } from "../components/InstallDependencyDialog";
import { apiClient, apiErrorCode, apiErrorMessage } from "../lib/api-client";
import { agentsQueryKey, refreshAgents } from "./useAgentsQuery";
import { useAgentAuthPlans, useStartAgentAuth } from "./useAgentAuth";
import { closeShellTerminal, shellTerminalsQueryKey } from "./useShellTerminals";
import type { TerminalSessionState } from "./useTerminalSession";

type AgentInstallPlan = components["schemas"]["AgentInstallPlan"];
type InstallJob = components["schemas"]["InstallJob"];

export const agentInstallersQueryKey = ["agent-installers"] as const;
export const agentInstallJobsQueryKey = ["agent-install-jobs"] as const;
const POLL_INTERVAL_MS = 1_000;

async function fetchInstallers(): Promise<AgentInstallPlan[]> {
	const { data, error } = await apiClient.GET("/api/v1/agents/installers");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load harness installers."));
	return data.agents;
}

async function fetchInstallJobs(): Promise<InstallJob[]> {
	const { data, error } = await apiClient.GET("/api/v1/agents/install-jobs");
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not load harness installation jobs."));
	return data.jobs;
}

function upsertJob(current: InstallJob[] | undefined, next: InstallJob): InstallJob[] {
	return [...(current ?? []).filter((job) => job.target !== next.target), next];
}

/**
 * Install and sign-in orchestration for a harness, scoped to a surface that is
 * not Harness Settings (currently onboarding). It runs the same daemon-owned
 * flows Settings uses, so a first-run user never has to leave the flow to get a
 * missing harness installed or an installed harness authenticated.
 */
export function useHarnessSetup({ enabled = true }: { enabled?: boolean } = {}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const installers = useQuery({ queryKey: agentInstallersQueryKey, queryFn: fetchInstallers, staleTime: 60_000, enabled });
	const jobs = useQuery({ queryKey: agentInstallJobsQueryKey, queryFn: fetchInstallJobs, retry: false, enabled });
	const authPlans = useAgentAuthPlans();
	const startAgentAuth = useStartAgentAuth();
	const [actionErrors, setActionErrors] = useState<Partial<Record<string, string>>>({});
	const [pendingInstalls, setPendingInstalls] = useState<ReadonlySet<string>>(new Set());
	const [authWorkflow, setAuthWorkflow] = useState<AuthWorkflow | null>(null);
	const authWorkflowRef = useRef<AuthWorkflow | null>(null);
	const refreshedInstalls = useRef(new Set<string>());
	authWorkflowRef.current = authWorkflow;

	// Poll only while a job is actually running, so an idle onboarding screen is
	// not issuing a request every second.
	const activeJobsKey = useMemo(
		() => (jobs.data ?? []).filter((job) => isActiveInstallJob(job)).map((job) => job.target).sort().join(","),
		[jobs.data],
	);
	const succeededJobsKey = useMemo(
		() => (jobs.data ?? []).filter((job) => job.status === "succeeded").map((job) => `${job.target}:${job.updatedAt ?? job.finishedAt ?? "done"}`).sort().join(","),
		[jobs.data],
	);

	useEffect(() => {
		if (!enabled || !activeJobsKey) return;
		const timer = window.setInterval(() => void jobs.refetch(), POLL_INTERVAL_MS);
		return () => window.clearInterval(timer);
	}, [enabled, activeJobsKey, jobs.refetch]);

	// A finished install is only useful if the catalog behind the row notices.
	useEffect(() => {
		for (const token of succeededJobsKey ? succeededJobsKey.split(",") : []) {
			if (!token || refreshedInstalls.current.has(token)) continue;
			refreshedInstalls.current.add(token);
			const agentId = token.split(":", 1)[0];
			setActionErrors((current) => {
				const next = { ...current };
				delete next[agentId];
				return next;
			});
			void queryClient.invalidateQueries({ queryKey: agentsQueryKey });
		}
	}, [queryClient, succeededJobsKey]);

	const jobFor = useCallback(
		(agentId: string): InstallJob | undefined => (jobs.data ?? []).find((job) => job.target === agentId),
		[jobs.data],
	);
	const authPlanFor = useCallback(
		(agentId: string) => (authPlans.data ?? []).find((plan) => plan.agentId === agentId),
		[authPlans.data],
	);

	const startInstall = useCallback(async (agentId: string) => {
		setPendingInstalls((current) => {
			if (current.has(agentId)) return current;
			const next = new Set(current);
			next.add(agentId);
			return next;
		});
		setActionErrors((current) => {
			const next = { ...current };
			delete next[agentId];
			return next;
		});
		try {
			const plan = (installers.data ?? []).find((item) => item.agentId === agentId);
			const { data, error } = await apiClient.POST("/api/v1/agents/{agent}/install", {
				params: { path: { agent: agentId } },
				body: { operation: "install", ...(plan?.method ? { method: plan.method } : {}) },
			});
			if (error || !data) {
				throw new Error(apiErrorMessage(error, t("onboarding.installStartFailed")));
			}
			queryClient.setQueryData<InstallJob[]>(agentInstallJobsQueryKey, (current) => upsertJob(current, data));
		} catch (error) {
			setActionErrors((current) => ({ ...current, [agentId]: error instanceof Error ? error.message : t("onboarding.installStartFailed") }));
		} finally {
			setPendingInstalls((current) => {
				const next = new Set(current);
				next.delete(agentId);
				return next;
			});
		}
	}, [installers.data, queryClient, t]);

	const closeAuth = useCallback(async () => {
		const workflow = authWorkflowRef.current;
		authWorkflowRef.current = null;
		setAuthWorkflow(null);
		if (!workflow) return;
		try {
			await closeShellTerminal(workflow.terminal.handleId);
		} catch (error) {
			// A terminal the daemon already reaped is not a failure worth
			// blocking the user on; anything else is reported on the row.
			if (apiErrorCode(error) !== "SHELL_TERMINAL_NOT_FOUND") {
				setActionErrors((current) => ({ ...current, [workflow.agentId]: t("onboarding.closeTerminalFailed") }));
			}
		} finally {
			await queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		}
	}, [queryClient, t]);

	const finishAuth = useCallback(async (workflow: AuthWorkflow) => {
		setAuthWorkflow((current) => (current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "verifying", reason: undefined } : current));
		let authorized = false;
		try {
			const catalog = await refreshAgents();
			authorized = catalog.authorized.some((agent) => agent.id === workflow.agentId);
		} catch {
			authorized = false;
		}
		await queryClient.invalidateQueries({ queryKey: agentsQueryKey });
		if (authorized) {
			await closeAuth();
			return;
		}
		setAuthWorkflow((current) => (current?.terminal.handleId === workflow.terminal.handleId ? { ...current, phase: "unauthorized", reason: t("onboarding.authNotCompleted") } : current));
	}, [closeAuth, queryClient, t]);

	const startAuth = useCallback(async (agentId: string) => {
		if (authWorkflowRef.current) return;
		setActionErrors((current) => {
			const next = { ...current };
			delete next[agentId];
			return next;
		});
		try {
			const result = await startAgentAuth.mutateAsync(agentId);
			const workflow: AuthWorkflow = {
				agentId,
				action: result.action,
				terminal: result.terminal,
				guidance: result.guidance ?? "",
				terminalInput: result.terminalInput,
				phase: "running",
				startedAt: Date.now(),
			};
			authWorkflowRef.current = workflow;
			setAuthWorkflow(workflow);
		} catch (error) {
			setActionErrors((current) => ({ ...current, [agentId]: error instanceof Error ? error.message : t("onboarding.authStartFailed") }));
		}
	}, [startAgentAuth, t]);

	const retryAuth = useCallback(async () => {
		const workflow = authWorkflowRef.current;
		if (!workflow) return;
		const agentId = workflow.agentId;
		await closeAuth();
		await startAuth(agentId);
	}, [closeAuth, startAuth]);

	const handleTerminalState = useCallback((state: TerminalSessionState) => {
		if (state !== "exited") return;
		const workflow = authWorkflowRef.current;
		if (!workflow || workflow.phase !== "running") return;
		void finishAuth(workflow);
	}, [finishAuth]);

	return {
		isInstalling: (agentId: string) => pendingInstalls.has(agentId) || isActiveInstallJob(jobFor(agentId)),
		actionErrors,
		authWorkflow,
		authPlanFor,
		closeAuth,
		handleTerminalState,
		jobFor,
		retryAuth,
		startAuth,
		startInstall,
	};
}
