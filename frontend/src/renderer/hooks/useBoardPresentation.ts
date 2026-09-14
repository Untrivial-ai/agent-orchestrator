import { useShellMaybe } from "../lib/shell-context";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { useSystemRequirementsGate } from "./useSystemRequirementsGate";

// Both header owners use the same readiness boundary as the board's center.
export function useBoardPresentation({
	projectId,
	isSuccess,
	isError,
	hasProjects,
	hasWorkerSessions,
}: {
	projectId?: string;
	isSuccess: boolean;
	isError: boolean;
	hasProjects: boolean;
	hasWorkerSessions: boolean;
}) {
	const shell = useShellMaybe();
	const { blocked: requirementsBlocked } = useSystemRequirementsGate();
	const isDaemonReady = usesPreviewWorkspaceData || (shell ? shell.daemonStatus.state === "ready" : true);
	const workspaceStartupState = shell?.workspaceStartupState ?? "ready";
	const isLoaded = isDaemonReady && workspaceStartupState === "ready" && isSuccess && !requirementsBlocked;
	const showStartup = shell !== null && !shell.daemonStatus.code && (
		!isDaemonReady || workspaceStartupState === "loading" || (!isSuccess && !isError) || requirementsBlocked
	);
	// Browser previews fill an otherwise empty project with demo worker cards.
	const hasVisibleWorkers = hasWorkerSessions || (usesPreviewWorkspaceData && hasProjects);
	return {
		isLoaded,
		showStartup,
		workspaceStartupState,
		showWelcome: !projectId && isLoaded && !hasProjects,
		showProjectEmpty: projectId !== undefined && isLoaded && hasProjects && !hasVisibleWorkers,
	};
}
