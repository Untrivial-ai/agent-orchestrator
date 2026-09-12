import { useEffect, useRef } from "react";
import { useMutation, useMutationState, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { CLOUD_PROJECT_KIND, hasConfiguredOrchestratorAgent, type WorkspaceSession } from "../types/workspace";
import { cloudSessionsQueryKey, workspaceQueryKey, type WorkspaceScope } from "./useWorkspaceQuery";
import { spawnCloudOrchestrator } from "../lib/cloud-orchestrator";
import { isChatPreflightError, spawnOrchestrator, type OrchestratorSpawnSource } from "../lib/spawn-orchestrator";
import { formatOrchestratorStartupError } from "../lib/orchestrator-startup-error";
import { addRendererExceptionStep, captureRendererEvent, captureRendererException } from "../lib/telemetry";
import { useUiStore } from "../stores/ui-store";

export function useProjectOrchestratorAction({
	projectId,
	project,
	orchestrator,
	source,
	sessionId,
}: {
	projectId?: string;
	project?: WorkspaceScope["project"];
	orchestrator?: WorkspaceSession;
	source: OrchestratorSpawnSource;
	sessionId?: string;
}) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const mutationKey = ["project-orchestrator-open", projectId] as const;
	const routeKey = `${projectId ?? ""}/${sessionId ?? ""}`;
	const activeRoute = useRef<string | null>(routeKey);
	activeRoute.current = routeKey;
	useEffect(() => {
		activeRoute.current = routeKey;
		return () => { activeRoute.current = null; };
	}, [routeKey]);
	const isProjectRestarting = useUiStore((state) => projectId ? state.restartingProjectIds.has(projectId) : false);
	const isProvisioning = useUiStore((state) => projectId ? state.provisioningProjectIds.has(projectId) : false);
	const startupError = useUiStore((state) => projectId ? state.orchestratorStartupErrors[projectId] : undefined);
	const setStartupError = useUiStore((state) => state.setOrchestratorStartupError);
	const previousProjectId = useRef(projectId);
	useEffect(() => {
		if (previousProjectId.current && previousProjectId.current !== projectId) {
			setStartupError(previousProjectId.current, null);
		}
		previousProjectId.current = projectId;
	}, [projectId, setStartupError]);
	useEffect(() => {
		if (projectId && orchestrator && startupError) setStartupError(projectId, null);
	}, [projectId, orchestrator, startupError, setStartupError]);
	const mutations = useMutationState({
		filters: { mutationKey, exact: true },
		select: (mutation) => ({ status: mutation.state.status, error: mutation.state.error }),
	});
	const isSpawning = mutations.some((mutation) => mutation.status === "pending");
	const latest = mutations.at(-1);
	const error = !orchestrator && !isSpawning && latest?.status === "error" ? latest.error : null;
	const spawnError = formatOrchestratorStartupError(
		error ? (error instanceof Error ? error.message : t("shell.couldNotSpawn")) : startupError ?? "",
	);
	const mutation = useMutation({
		mutationKey,
		mutationFn: async (mode?: "tui") => {
			if (!projectId) return;
			setStartupError(projectId, null);
			const openedSessionId = project?.kind === CLOUD_PROJECT_KIND
				? await spawnCloudOrchestrator(queryClient, projectId)
				: await spawnOrchestrator(projectId, source, false, mode);
			await queryClient.invalidateQueries({
				queryKey: project?.kind === CLOUD_PROJECT_KIND ? cloudSessionsQueryKey : workspaceQueryKey,
			});
			setStartupError(projectId, null);
			// A completed request belongs to its original route, even if this
			// component survived a project or session change while it was pending.
			if (activeRoute.current === routeKey) {
				void navigate({
					to: "/projects/$projectId/sessions/$sessionId",
					params: { projectId, sessionId: openedSessionId },
				});
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
		if (!projectId || isProjectRestarting || isProvisioning) return;
		// Read the cache synchronously as well as disabling both rendered copies.
		// Two clicks in the same render must still produce just one request.
		if (queryClient.isMutating({ mutationKey, exact: true })) return;
		void addRendererExceptionStep("Orchestrator open requested", {
			source: "orchestrator-open", operation: "open_orchestrator",
			surface: sessionId ? "session_detail" : "project_board", project_id: projectId,
		});
		void captureRendererEvent("ao.renderer.orchestrator_open_requested", { project_id: projectId });
		if (orchestrator) {
			void navigate({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId, sessionId: orchestrator.id } });
		} else if (project?.kind !== CLOUD_PROJECT_KIND && !hasConfiguredOrchestratorAgent(project)) {
			if (project) useUiStore.getState().openProjectSettings(projectId);
		} else {
			mutation.mutate(mode);
		}
	};
	const openNewTask = () => {
		if (projectId && !isProjectRestarting && !isProvisioning) useUiStore.getState().requestNewTask(projectId);
	};
	return { orchestrator, isSpawning, isProjectRestarting, isProvisioning, spawnError,
		canCreateAsTui: isChatPreflightError(error), openOrchestrator, openNewTask };
}

export type ProjectOrchestratorAction = ReturnType<typeof useProjectOrchestratorAction>;
