import { agentLabel } from "./agent-options";
import type { AgentInfo } from "./agent-select-options";
import type { CloudCpProviderConnection } from "./cloud-cp";

/** The only agents AO cloud supports, matching the set the control plane's
 * validAgentProvider accepts (cloud/internal/httpapi/provider_handlers.go).
 * Unlike local's full AGENT_OPTIONS list, cloud has no "install" step, so any
 * unlisted agent would just be a dead end. */
export const CLOUD_AGENT_PROVIDERS = ["claude-code", "codex", "cursor", "opencode"] as const;

/** Maps the org's cloud provider connections onto the same AgentInfo shape
 * local readiness uses, so the cloud agent picker is the identical component
 * local's agent sheet already ships (RequiredAgentField, AgentSelectMenuItem,
 * buildRankedAgentOptions): a missing or invalid connection reads as "Needs
 * auth" and is unselectable, exactly like a local agent nobody has logged into,
 * never a "Needs install" row. Lives here (not in a component) so both the
 * create-project flow and the task composer can source the cloud picker without
 * importing a heavy component module. */
export function cloudAgentInfos(connections: CloudCpProviderConnection[] | undefined): AgentInfo[] {
	const byProvider = new Map((connections ?? []).map((connection) => [connection.provider, connection]));
	return CLOUD_AGENT_PROVIDERS.map((id) => {
		const authorized = byProvider.get(id)?.validationState === "valid";
		return {
			id,
			label: agentLabel(id),
			installation: { state: "installed", freshness: "fresh" },
			authentication: { state: authorized ? "authorized" : "unauthorized", freshness: "fresh" },
			effectiveReadiness: authorized ? "ready" : "not_ready",
			usageCount: 0,
			lastUsedAt: null,
		};
	});
}
