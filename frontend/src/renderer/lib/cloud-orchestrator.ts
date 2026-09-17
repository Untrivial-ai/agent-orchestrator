import type { QueryClient } from "@tanstack/react-query";
import { createRendererCloudCpClient } from "../hooks/useCloudCp";
import type { CloudCpAgentProvider, CloudCpProviderConnection } from "./cloud-cp";
import { settingsQueryKey, type Settings } from "../hooks/useSettings";
import { readSelectedSandboxProvider } from "../stores/sandbox-provider-store";
import { captureRendererEvent } from "./telemetry";

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

// A Cloud worker image currently ships these three harnesses. This ordering
// preserves the former Codex default whenever it is available, while allowing
// a user's connected Claude Code or Cursor credential to run the orchestrator
// when Codex is not connected.
const CLOUD_ORCHESTRATOR_HARNESS_PRIORITY: readonly CloudCpAgentProvider[] = ["codex", "claude-code", "cursor"];

export function selectCloudOrchestratorHarness(
	connections: readonly CloudCpProviderConnection[],
): CloudCpAgentProvider | undefined {
	const connected = new Set(
		connections
			.filter((connection) => connection.label === "default" && connection.validationState === "valid")
			.map((connection) => connection.provider),
	);
	return CLOUD_ORCHESTRATOR_HARNESS_PRIORITY.find((harness) => connected.has(harness));
}

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
	// The user's client-side provider preference (when the control plane offers
	// more than one); omitted lets the control plane use its default. Read
	// directly from localStorage since this launcher is deliberately hook-free.
	const provider = readSelectedSandboxProvider();
	// #4960: pick the orchestrator harness from the user's connected Cloud
	// coding-agent credentials (Codex -> Claude Code -> Cursor) instead of
	// hardcoding claude-code.
	const [orgCredentials, personalCredentials] = await Promise.all([
		client.listProviderConnections(orgId),
		client.listUserProviderConnections(),
	]);
	const harness = selectCloudOrchestratorHarness([
		...orgCredentials.providerConnections,
		...personalCredentials.providerConnections,
	]);
	if (!harness) throw new Error("Connect a Cloud coding agent before spawning an orchestrator.");
	try {
		const { session } = await client.createSession(orgId, {
			projectId,
			kind: "orchestrator",
			harness,
			displayName: "Orchestrator",
			prompt: ORCHESTRATOR_KICKOFF_PROMPT,
			...(provider ? { provider } : {}),
		});
		void captureRendererEvent("ao.renderer.cloud_orchestrator_spawn_succeeded", { project_id: projectId });
		return session.id;
	} catch (error) {
		void captureRendererEvent("ao.renderer.cloud_orchestrator_spawn_failed", { project_id: projectId });
		throw error;
	}
}
