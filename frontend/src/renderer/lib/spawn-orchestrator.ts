import { apiClient, apiErrorCode, apiErrorMessage, apiErrorRequestId } from "./api-client";
import type { OrchestratorSpawnSource } from "./orchestrator-spawn-sources";
import type { SessionMode } from "../types/conversation";

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
	) {
		super(message);
		this.name = "OrchestratorSpawnError";
	}
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
	_source: OrchestratorSpawnSource,
	clean = false,
	mode?: SessionMode,
): Promise<string> {
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
		);
	}

	return data.orchestrator.id;
}
