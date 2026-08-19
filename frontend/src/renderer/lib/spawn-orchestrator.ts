import { apiClient, apiErrorCode, apiErrorKind, apiErrorMessage, apiErrorRequestId } from "./api-client";
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

const SPAWN_ERROR_KINDS = new Set([
	"bad_request",
	"not_found",
	"conflict",
	"forbidden",
	"internal",
	"not_implemented",
]);

export type OrchestratorSpawnFailureReason = {
	error_kind: string;
	error_code?: string;
};

/** A rejected orchestrator spawn without flattening the daemon's error envelope. */
export class OrchestratorSpawnError extends Error {
	constructor(
		message: string,
		readonly code?: string,
		readonly requestId?: string,
		readonly status?: number,
		readonly kind?: string,
	) {
		super(message);
		this.name = "OrchestratorSpawnError";
	}
}

export function orchestratorSpawnFailureReason(error: unknown): OrchestratorSpawnFailureReason {
	if (!(error instanceof OrchestratorSpawnError)) return { error_kind: "network_error" };
	const errorCode = error.code?.trim();
	const withCode = errorCode ? { error_code: errorCode } : {};
	if (error.kind && SPAWN_ERROR_KINDS.has(error.kind)) return { error_kind: error.kind, ...withCode };
	if (error.status === 400) return { error_kind: "bad_request", ...withCode };
	if (error.status === 403) return { error_kind: "forbidden", ...withCode };
	if (error.status === 404) return { error_kind: "not_found", ...withCode };
	if (error.status === 409) return { error_kind: "conflict", ...withCode };
	if (error.status && error.status >= 500) return { error_kind: "internal", ...withCode };
	return { error_kind: "invalid_response", ...withCode };
}

export function isChatPreflightCode(code?: string): boolean {
	return Boolean(code && CHAT_PREFLIGHT_CODES.has(code));
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
): Promise<string> {
	void captureRendererEvent("ao.renderer.orchestrator_spawn_requested", { project_id: projectId, source });
	try {
		const { data, error, response } = await apiClient.POST("/api/v1/orchestrators", {
			body: { projectId, clean, ...(mode ? { mode } : {}) },
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
				apiErrorKind(error),
			);
		}

		void captureRendererEvent("ao.renderer.orchestrator_spawn_succeeded", { project_id: projectId, source });
		return data.orchestrator.id;
	} catch (err) {
		void captureRendererEvent("ao.renderer.orchestrator_spawn_failed", {
			project_id: projectId,
			source,
			...orchestratorSpawnFailureReason(err),
		});
		throw err;
	}
}
