import type { QueryClient } from "@tanstack/react-query";
import { createRendererCloudCpClient } from "../hooks/useCloudCp";
import { settingsQueryKey, type Settings } from "../hooks/useSettings";

// A cloud project has no locally-configured orchestrator agent (that config
// lives in the local daemon's project settings), so the launchers must not
// fall through to the project-settings page for it. Instead the orchestrator
// is spawned as a control-plane session in its own sandbox, exactly like a
// cloud worker session; the worker swaps in the orchestrator system prompt
// server-side based on the session kind.
//
// Deliberately hook-free: the launchers (sidebar row, board, topbar, command
// palette) render everywhere, and subscribing them to the cloud session/org
// queries just for this click handler would fire cloud requests on every
// mount. The client is built lazily from the settings query cache instead.
const ORCHESTRATOR_KICKOFF_PROMPT =
	"You are the orchestrator for this project. Survey the repository, then wait for tasks and delegate work to worker sessions.";

/** Spawns a cloud orchestrator session for the project and returns its id. */
export async function spawnCloudOrchestrator(queryClient: QueryClient, projectId: string): Promise<string> {
	const settings = queryClient.getQueryData<Settings>(settingsQueryKey);
	const baseUrl = settings?.cloudControlPlaneUrl ?? "";
	if (baseUrl === "") throw new Error("The cloud control plane is not configured.");
	const client = createRendererCloudCpClient(baseUrl);
	// First-org mirrors useCloudOrg's v0 rule. A cloud project can only exist
	// inside an org, so signed-in users spawning from one always have it.
	const me = await client.me();
	const orgId = me.organizations[0]?.id;
	if (orgId === undefined) throw new Error("No cloud organization is available.");
	const { session } = await client.createSession(orgId, {
		projectId,
		kind: "orchestrator",
		harness: "claude-code",
		displayName: "Orchestrator",
		prompt: ORCHESTRATOR_KICKOFF_PROMPT,
	});
	return session.id;
}
