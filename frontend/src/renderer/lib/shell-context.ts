import { createContext, useContext } from "react";
import type { components } from "../../api/schema";
import type { useDaemonStatus } from "../hooks/useDaemonStatus";

// Shared state the persistent _shell layout owns and route content reads. The
// daemon status effect (IPC poll + event transport) must run exactly once, so
// it lives in the shell and is handed down here rather than re-run per route.
export type ShellContextValue = {
	daemonStatus: ReturnType<typeof useDaemonStatus>;
	workspaceStartupState: "loading" | "ready" | "error";
	createProject: (input: {
		path: string;
		workerAgent: string;
		orchestratorAgent: string;
		trackerIntake?: components["schemas"]["TrackerIntakeConfig"];
		asWorkspace?: boolean;
	}) => Promise<void>;
	cloneProject: (input: {
		remoteUrl: string;
		destinationParent: string;
		workerAgent: string;
		orchestratorAgent: string;
		trackerIntake?: components["schemas"]["TrackerIntakeConfig"];
	}) => Promise<void>;
	initializeProjectRepository: (path: string) => Promise<void>;
	openRemoteProjectSettings: (hostId: string, projectId: string) => void;
	/** Navigate to a project's board, e.g. a cloud project just created. */
	openProject?: (projectId: string) => void;
	validateImport?: (input: {
		path: string;
		importKind: "project" | "workspace";
	}) => Promise<components["schemas"]["ImportValidationResult"]>;
};

const ShellContext = createContext<ShellContextValue | null>(null);

export const ShellProvider = ShellContext.Provider;

export function useShell(): ShellContextValue {
	const ctx = useContext(ShellContext);
	if (!ctx) throw new Error("useShell must be used within the _shell layout route");
	return ctx;
}

// Non-throwing variant for components that also render outside the shell
// (e.g. Sidebar in unit tests): returns null instead of throwing.
export function useShellMaybe(): ShellContextValue | null {
	return useContext(ShellContext);
}
