import type { QueryClient } from "@tanstack/react-query";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import type { SessionMode } from "../types/conversation";
import { OrchestratorSpawnError, spawnOrchestrator } from "./spawn-orchestrator";
import type { OrchestratorReplacementFailure } from "../stores/ui-store";

type NavigateToSession = (options: {
	to: "/projects/$projectId/sessions/$sessionId";
	params: { projectId: string; sessionId: string };
}) => unknown;

type RestartProjectOrchestratorOptions = {
	projectId: string;
	queryClient: QueryClient;
	navigate: NavigateToSession;
	setProjectRestarting: (projectId: string, restarting: boolean) => void;
	setOrchestratorReplacementError: (projectId: string, failure: OrchestratorReplacementFailure | null) => void;
	onError?: (error: unknown) => void;
	mode?: SessionMode;
};

async function refreshWorkspaceState(queryClient: QueryClient) {
	try {
		await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
	} catch {
		// The restart outcome is more important than cache refresh bookkeeping:
		// callers still need navigation/error state even if refetching fails.
	}
}

export async function restartProjectOrchestrator({
	projectId,
	queryClient,
	navigate,
	setProjectRestarting,
	setOrchestratorReplacementError,
	onError,
	mode,
}: RestartProjectOrchestratorOptions) {
	// Keep the initiating control focused while the restart is pending so
	// keyboard users retain a focus target for the duration of the operation;
	// blur it only once navigation to the replacement session is about to
	// happen. On failure the control stays focused and the error dialog takes
	// focus normally.
	const activeElement = document.activeElement;
	setProjectRestarting(projectId, true);
	// Keep any replacement-error dialog mounted so Retry retains focus while pending.
	try {
		const sessionId = await spawnOrchestrator(projectId, "restart", true, mode);
		await refreshWorkspaceState(queryClient);
		setOrchestratorReplacementError(projectId, null);
		if (activeElement instanceof HTMLElement) activeElement.blur();
		void navigate({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId, sessionId },
		});
	} catch (error) {
		await refreshWorkspaceState(queryClient);
		setOrchestratorReplacementError(projectId, {
			message: error instanceof Error ? error.message : "Could not replace orchestrator",
			...(error instanceof OrchestratorSpawnError
				? { code: error.code, requestId: error.requestId }
				: {}),
		});
		onError?.(error);
	} finally {
		setProjectRestarting(projectId, false);
	}
}
