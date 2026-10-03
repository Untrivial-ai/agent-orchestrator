import { refKey } from "./hosts";
import { resumeOrchestrator, spawnOrchestrator, type OrchestratorSpawnSource } from "./spawn-orchestrator";
import { sessionAgentExited, type WorkspaceSession } from "../types/workspace";

const inFlight = new Map<string, Promise<string>>();

/** Open one project's orchestrator on its owning daemon. Shared by the board
 * and sidebar so two rapid clicks cannot launch two copies. */
export function openRemoteOrchestrator(
	hostId: string,
	projectId: string,
	orchestrator?: WorkspaceSession,
	mode?: "tui",
	clean = false,
	source: OrchestratorSpawnSource = "sidebar",
): Promise<string> {
	const key = `${refKey({ host: hostId, id: projectId })}:${clean ? "clean" : "ensure"}`;
	const current = inFlight.get(key);
	if (current) return current;
	if (!clean && orchestrator && !sessionAgentExited(orchestrator)) return Promise.resolve(orchestrator.id);
	const request = (async () => {
		if (!clean && orchestrator) {
			await resumeOrchestrator(orchestrator.id, hostId);
			return orchestrator.id;
		}
		return spawnOrchestrator(projectId, source, clean, mode, undefined, hostId);
	})();
	inFlight.set(key, request);
	void request.finally(() => {
		if (inFlight.get(key) === request) inFlight.delete(key);
	}).catch(() => undefined);
	return request;
}
