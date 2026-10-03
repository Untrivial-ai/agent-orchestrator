import { apiClient, apiErrorMessage } from "./api-client";
import { clientForSessionHost } from "./host-clients";

/** Update a session's display name via the daemon (PATCH /sessions/{id}). The
 *  daemon enforces the same 100-character limit as the spawn `--name` flag. */
export async function renameSession(sessionId: string, displayName: string, hostId?: string): Promise<void> {
	const { error, response } = await (hostId ? clientForSessionHost(hostId) : apiClient).PATCH("/api/v1/sessions/{sessionId}", {
		params: { path: { sessionId } },
		body: { displayName },
	});

	if (error) {
		throw new Error(apiErrorMessage(error, `Failed to rename session${response ? ` (${response.status})` : ""}`));
	}
}
