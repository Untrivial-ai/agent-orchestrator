import { useEffect, useRef } from "react";
import { useMutation, useMutationState, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { CLOUD_PROJECT_KIND, hasConfiguredOrchestratorAgent, type WorkspaceSession } from "../types/workspace";
import { cloudSessionsQueryKey, workspaceQueryKeyForHost, type WorkspaceScope } from "./useWorkspaceQuery";
import { spawnCloudOrchestrator } from "../lib/cloud-orchestrator";
import {
	isChatPreflightError,
	resumeOrchestrator,
	spawnOrchestrator,
	type OrchestratorSpawnSource,
} from "../lib/spawn-orchestrator";
import { formatOrchestratorStartupError } from "../lib/orchestrator-startup-error";
import { addRendererExceptionStep, captureRendererEvent, captureRendererException } from "../lib/telemetry";
import { useUiStore } from "../stores/ui-store";
import { useCanResumeAgent } from "./useCanResumeAgent";
import { openRemoteOrchestrator } from "../lib/remote-orchestrator";
import { sessionUiKey } from "../lib/hosts";
import { sessionNavigateTarget } from "../lib/navigate-to-session";
import { useConnectedHosts } from "./useHostConnection";

export function useProjectOrchestratorAction({
	projectId,
	project,
	orchestrator,
	source,
	sessionId,
	hostId,
}: {
	projectId?: string;
	project?: WorkspaceScope["project"];
	orchestrator?: WorkspaceSession;
	source: OrchestratorSpawnSource;
	sessionId?: string;
	hostId?: string;
}) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const connectedHosts = useConnectedHosts();
	const hostConnected = !hostId || connectedHosts.includes(hostId);
	const mutationKey = hostId ? ["project-orchestrator-open", hostId, projectId] : ["project-orchestrator-open", projectId];
	const routeKey = `${hostId ?? "local"}/${projectId ?? ""}/${sessionId ?? ""}`;
	const activeRoute = useRef<string | null>(routeKey);
	activeRoute.current = routeKey;
	useEffect(() => {
		activeRoute.current = routeKey;
		return () => { activeRoute.current = null; };
	}, [routeKey]);
	const projectKey = projectId ? sessionUiKey(projectId, hostId) : undefined;
	const isProjectRestarting = useUiStore((state) => projectKey ? state.restartingProjectIds.has(projectKey) : false);
	const isProvisioning = useUiStore((state) => projectKey ? state.provisioningProjectIds.has(projectKey) : false);
	const canResumeOrchestrator = useCanResumeAgent(orchestrator, hostId);
	const startupError = useUiStore((state) => projectKey ? state.orchestratorStartupErrors[projectKey] : undefined);
	const setStartupError = useUiStore((state) => state.setOrchestratorStartupError);
	const previousProject = useRef({ projectId, hostId });
	useEffect(() => {
		const previous = previousProject.current;
		if (previous.projectId && (previous.projectId !== projectId || previous.hostId !== hostId)) {
			setStartupError(previous.projectId, null, previous.hostId);
		}
		previousProject.current = { projectId, hostId };
	}, [hostId, projectId, setStartupError]);
	useEffect(() => {
		if (projectId && orchestrator && startupError) setStartupError(projectId, null, hostId);
	}, [hostId, projectId, orchestrator, startupError, setStartupError]);
	const mutations = useMutationState({
		filters: { mutationKey, exact: true },
		select: (mutation) => ({ status: mutation.state.status, error: mutation.state.error }),
	});
	const isSpawning = mutations.some((mutation) => mutation.status === "pending");
	const resumableOrchestrator =
		orchestrator && canResumeOrchestrator && project?.kind !== CLOUD_PROJECT_KIND
			? orchestrator
			: undefined;
	const latest = mutations.at(-1);
	const error = (!orchestrator || resumableOrchestrator) && !isSpawning && latest?.status === "error" ? latest.error : null;
	const spawnError = formatOrchestratorStartupError(
		error ? (error instanceof Error ? error.message : t("shell.couldNotSpawn")) : startupError ?? "",
	);
	const mutation = useMutation({
		mutationKey,
		mutationFn: async (mode?: "tui") => {
			if (!projectId) return;
			setStartupError(projectId, null, hostId);
			const openedSessionId = hostId
				? await openRemoteOrchestrator(hostId, projectId, orchestrator, mode, false, source)
				: resumableOrchestrator
				? (await resumeOrchestrator(resumableOrchestrator.id), resumableOrchestrator.id)
				: project?.kind === CLOUD_PROJECT_KIND
					? await spawnCloudOrchestrator(queryClient, projectId)
					: await spawnOrchestrator(projectId, source, false, mode);
			await queryClient.invalidateQueries({
				queryKey: project?.kind === CLOUD_PROJECT_KIND ? cloudSessionsQueryKey : workspaceQueryKeyForHost(hostId),
			});
			setStartupError(projectId, null, hostId);
			// A completed request belongs to its original route, even if this
			// component survived a project or session change while it was pending.
			if (activeRoute.current === routeKey) {
				void navigate(sessionNavigateTarget(projectId, openedSessionId, hostId));
			}
		},
		onError: (cause) => {
			void captureRendererException(cause, {
				source: "orchestrator-open", operation: "open_orchestrator",
				surface: sessionId ? "session_detail" : "project_board", project_id: projectId,
			});
		},
	});
	const openOrchestrator = (mode?: "tui") => {
		if (!projectId || !hostConnected || isProjectRestarting || isProvisioning) return;
		// Read the cache synchronously as well as disabling both rendered copies.
		// Two clicks in the same render must still produce just one request.
		if (queryClient.isMutating({ mutationKey, exact: true })) return;
		void addRendererExceptionStep("Orchestrator open requested", {
			source: "orchestrator-open", operation: "open_orchestrator",
			surface: sessionId ? "session_detail" : "project_board", project_id: projectId,
		});
		void captureRendererEvent("ao.renderer.orchestrator_open_requested", { project_id: projectId });
		if (hostId && projectId && !orchestrator && project && !hasConfiguredOrchestratorAgent(project)) {
			useUiStore.getState().openProjectSettings(projectId, hostId);
		} else if (resumableOrchestrator) {
			mutation.mutate(mode);
		} else if (orchestrator) {
			void navigate(sessionNavigateTarget(projectId, orchestrator.id, hostId));
		} else if (project?.kind !== CLOUD_PROJECT_KIND && !hasConfiguredOrchestratorAgent(project)) {
			if (project) useUiStore.getState().openProjectSettings(projectId, hostId);
		} else {
			mutation.mutate(mode);
		}
	};
	const openNewTask = () => {
		if (projectId && hostConnected && !isProjectRestarting && !isProvisioning) useUiStore.getState().requestNewTask(projectId, hostId);
	};
	return { orchestrator, isSpawning, isProjectRestarting, isProvisioning, spawnError,
		canCreateAsTui: isChatPreflightError(error), openOrchestrator, openNewTask };
}

export type ProjectOrchestratorAction = ReturnType<typeof useProjectOrchestratorAction>;
