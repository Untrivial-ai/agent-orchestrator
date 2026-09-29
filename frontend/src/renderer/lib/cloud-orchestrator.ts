import type { QueryClient } from "@tanstack/react-query";
import { createRendererCloudCpClient } from "../hooks/useCloudCp";
import type { CloudCpAgentProvider, CloudCpProviderConnection } from "./cloud-cp";
import { CLOUD_AGENT_PROVIDERS } from "./cloud-agents";
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
// Orchestrator harness preference. Codex stays the historical default; the rest
// follow in the canonical CLOUD_AGENT_PROVIDERS order. Derived from that single
// list so a newly onboarded cloud harness is automatically eligible to run the
// orchestrator with no change here (see cloud-agents.ts).
const ORCHESTRATOR_PREFERRED_HARNESSES: readonly string[] = ["codex"];
const CLOUD_ORCHESTRATOR_HARNESS_PRIORITY: readonly CloudCpAgentProvider[] = [
	...ORCHESTRATOR_PREFERRED_HARNESSES,
	...CLOUD_AGENT_PROVIDERS.filter((provider) => !ORCHESTRATOR_PREFERRED_HARNESSES.includes(provider)),
] as readonly CloudCpAgentProvider[];

function connectedProviders(
	connections: readonly CloudCpProviderConnection[],
): Set<string> {
	return new Set(
		connections
			.filter((connection) => connection.label === "default" && connection.validationState === "valid")
			.map((connection) => connection.provider),
	);
}

export function selectCloudOrchestratorHarness(
	connections: readonly CloudCpProviderConnection[],
): CloudCpAgentProvider | undefined {
	const connected = connectedProviders(connections);
	return CLOUD_ORCHESTRATOR_HARNESS_PRIORITY.find((harness) => connected.has(harness));
}

// The orchestrator agent the user chose for this project (Project Settings or
// the create-project flow, stored at config.orchestrator.agent). Returned only
// when it is set AND its credential is connected; otherwise undefined so the
// caller falls back to the connected-credential priority. Without honoring this,
// the launcher always took the Codex-first default and ignored a user who picked
// Claude Code (or Cursor) for the project.
export function resolveConfiguredOrchestratorHarness(
	project: { config?: Record<string, unknown> } | undefined,
	connections: readonly CloudCpProviderConnection[],
): CloudCpAgentProvider | undefined {
	const orchestrator = (project?.config as { orchestrator?: { agent?: unknown } } | undefined)?.orchestrator;
	const agent = typeof orchestrator?.agent === "string" ? orchestrator.agent.trim() : "";
	if (agent === "") return undefined;
	return connectedProviders(connections).has(agent as CloudCpAgentProvider)
		? (agent as CloudCpAgentProvider)
		: undefined;
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
	// Pick the orchestrator harness. Prefer the agent the user configured for
	// this project (config.orchestrator.agent); only when the project has not
	// chosen one do we fall back to the connected-credential priority
	// (#4960: Codex -> Claude Code -> Cursor). Previously the configured choice
	// was ignored, so a project set to Claude Code still launched Codex whenever
	// a Codex credential happened to be connected.
	const [orgCredentials, personalCredentials] = await Promise.all([
		client.listProviderConnections(orgId),
		client.listUserProviderConnections(),
	]);
	const connections = [...orgCredentials.providerConnections, ...personalCredentials.providerConnections];
	// The project's configured orchestrator agent is an optional preference. Load
	// it separately and tolerate a failure: it must not block a spawn that the
	// connected-credential priority could still satisfy. A fetch error (or the
	// project not being on the first page) just means "no configured agent".
	const projects = await client.listProjects(orgId, { limit: 100 }).catch(() => null);
	const project = projects?.items.find((candidate) => candidate.id === projectId);
	const harness = resolveConfiguredOrchestratorHarness(project, connections) ?? selectCloudOrchestratorHarness(connections);
	if (!harness) throw new Error("Connect a Cloud coding agent before spawning an orchestrator.");
	try {
		const { session } = await client.createSession(orgId, {
			projectId,
			kind: "orchestrator",
			harness,
			displayName: "Orchestrator",
			// Role instructions are standing system configuration assembled by the
			// worker; do not duplicate them as a visible user message.
			prompt: "",
			...(provider ? { provider } : {}),
		});
		void captureRendererEvent("ao.renderer.cloud_orchestrator_spawn_succeeded", { project_id: projectId });
		return session.id;
	} catch (error) {
		void captureRendererEvent("ao.renderer.cloud_orchestrator_spawn_failed", { project_id: projectId });
		throw error;
	}
}
