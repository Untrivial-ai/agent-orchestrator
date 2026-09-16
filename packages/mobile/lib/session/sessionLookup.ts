import { ApiError, getSession } from "../api";
import type { ServerConfig } from "../config";
import type { SessionLookup } from "./sessionRoute";

/**
 * Ask the daemon about one session. Never rejects: an ApiError carries the
 * status the daemon answered with, and anything else — a timeout, a refused
 * connection — never got an answer, which `sessionRouteView` must not read as
 * "not found".
 */
export async function lookUpSession(cfg: ServerConfig, id: string): Promise<SessionLookup> {
	try {
		return { state: "found", session: await getSession(cfg, id) };
	} catch (cause) {
		return { state: "failed", status: cause instanceof ApiError ? cause.status : undefined };
	}
}
