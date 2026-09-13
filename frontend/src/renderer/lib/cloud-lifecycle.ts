import type { WorkspaceSession } from "../types/workspace";

export type CloudLifecycleStage =
	| "paused_by_coder"
	| "resuming_workspace"
	| "waiting_for_coder_agent"
	| "starting_ao_worker"
	| "restoring_agent"
	| "connected";

/**
 * Derives the desktop's cloud lifecycle stage from the control-plane contract.
 * The backend remains authoritative for intent and observation; this function
 * only translates those provider-neutral facts into concise presentation.
 */
export function cloudLifecycleStage(session?: WorkspaceSession): CloudLifecycleStage | undefined {
	const lifecycle = session?.cloud;
	if (!lifecycle) return undefined;
	const desired = lifecycle.desiredState;
	const observed = lifecycle.observedState;

	if (lifecycle.sandboxProvider === "coder" && desired === "paused" && observed === "stopped") {
		return "paused_by_coder";
	}
	if (desired === "running" && (observed === "stopped" || observed === "restoring")) {
		return "resuming_workspace";
	}
	if (observed === "requested" || observed === "provisioning") {
		return "waiting_for_coder_agent";
	}
	if (observed === "bootstrapping") {
		return session.runtimeConnected ? "restoring_agent" : "starting_ao_worker";
	}
	if (observed === "running") {
		return session.runtimeConnected ? "connected" : "restoring_agent";
	}
	return undefined;
}
