import { apiClient, apiErrorCode, apiErrorDetails, apiErrorKind, apiErrorMessage, apiErrorRequestId } from "./api-client";
import type { OrchestratorSpawnSource } from "./orchestrator-spawn-sources";
import { captureRendererEvent } from "./telemetry";
import type { SessionMode } from "../types/conversation";

// Every UI entry point that spawns an orchestrator: the board CTA, the topbar
// and sidebar launchers, the restore-unavailable dialog, and the auto-spawn
// right after a project is added. Emitting the triad from inside
// spawnOrchestrator (keyed by source) guarantees each path reports, instead of
// each call site remembering to instrument itself.
export type { OrchestratorSpawnSource };

const CHAT_PREFLIGHT_CODES = new Set([
	"SESSION_MODE_UNSUPPORTED",
	"CHAT_DRIVER_UNAVAILABLE",
	"CHAT_DRIVER_INCOMPATIBLE",
	"CHAT_AUTH_REQUIRED",
]);

/** A rejected orchestrator spawn without flattening the daemon's error envelope. */
export class OrchestratorSpawnError extends Error {
	constructor(
		message: string,
		readonly code?: string,
		readonly requestId?: string,
		readonly status?: number,
		readonly details?: Record<string, unknown>,
		readonly kind?: string,
	) {
		super(message);
		this.name = "OrchestratorSpawnError";
	}
}

export function isChatPreflightCode(code?: string): boolean {
	return Boolean(code && CHAT_PREFLIGHT_CODES.has(code));
}

/** True when the daemon refused Chat only for the missing approvals channel
 *  and allows retrying without approvals. Mirrors the worker fallback. */
export function canBypassOrchestratorApprovals(code?: string, details?: Record<string, unknown>): boolean {
	if (code !== "SESSION_MODE_UNSUPPORTED" || !details) return false;
	const has = (key: string, value: string) => {
		const entry = details[key];
		return entry === value || (Array.isArray(entry) && entry.includes(value));
	};
	return has("missingCapabilities", "approvals") && has("allowedApprovalModes", "bypass-permissions");
}

export function isChatPreflightError(error: unknown): error is OrchestratorSpawnError {
	return error instanceof OrchestratorSpawnError && isChatPreflightCode(error.code);
}

/** Spawn the project's orchestrator session via the daemon API. When clean is
 *  true the daemon first tears down any active orchestrator for the project, then
 *  re-spawns one on the canonical branch (reattaching the existing branch). */
export async function spawnOrchestrator(
	projectId: string,
	source: OrchestratorSpawnSource,
	clean = false,
	mode?: SessionMode,
	approvalMode?: "default" | "accept-edits" | "auto" | "bypass-permissions",
): Promise<string> {
	void captureRendererEvent("ao.renderer.orchestrator_spawn_requested", { project_id: projectId, source });
	try {
		const { data, error, response } = await apiClient.POST("/api/v1/orchestrators", {
			body: { projectId, clean, ...(mode ? { mode } : {}), ...(approvalMode ? { approvalMode } : {}) },
		});

		if (error || !data?.orchestrator?.id) {
			const message = error
				? apiErrorMessage(error, `Failed to spawn orchestrator (${response.status})`)
				: `Failed to spawn orchestrator (${response.status})`;
			throw new OrchestratorSpawnError(
				message,
				apiErrorCode(error),
				apiErrorRequestId(error),
				response.status,
				apiErrorDetails(error),
				apiErrorKind(error),
			);
		}

		void captureRendererEvent("ao.renderer.orchestrator_spawn_succeeded", { project_id: projectId, source });
		return data.orchestrator.id;
	} catch (err) {
		void captureRendererEvent("ao.renderer.orchestrator_spawn_failed", {
			project_id: projectId,
			source,
			...(err instanceof OrchestratorSpawnError ? { error_kind: err.kind, error_code: err.code } : {}),
		});
		throw err;
	}
}
