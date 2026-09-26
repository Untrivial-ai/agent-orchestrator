import { agentLabel } from "./agent-options";
import type { AgentInfo } from "./agent-select-options";
import type { CloudCpProviderConnection } from "./cloud-cp";

/** The only agents AO cloud supports, matching the set the control plane's
 * validAgentProvider accepts (cloud/internal/httpapi/provider_handlers.go).
 * Unlike local's full AGENT_OPTIONS list, cloud has no "install" step, so any
 * unlisted agent would just be a dead end. */
export const CLOUD_AGENT_PROVIDERS = ["claude-code", "codex", "cursor", "opencode"] as const;

/** A cloud session has no local daemon project, yet its opencode model picker
 * must reflect the models the *pushed* credential can run — opencode lists a
 * provider's catalog only when that provider's key is present. We can't (and
 * shouldn't) send the secret to local discovery, so we send the credential
 * *type* as a scope hint in place of a project id; the daemon marks the matching
 * env var present (a placeholder) to unlock the list. '@'/':' cannot occur in a
 * real project id (backend projectIDPattern), so the two namespaces never
 * collide. Keep this prefix in sync with credentialScopePrefix in
 * backend/internal/service/agent/service.go. */
const CREDENTIAL_SCOPE_PREFIX = "@cred:";

/** Returns the credential-scoped catalog key for a connected cloud credential
 * type (e.g. "anthropic_api_key" -> "@cred:anthropic_api_key"). */
export function credentialModelScope(credentialType: string): string {
	return `${CREDENTIAL_SCOPE_PREFIX}${credentialType}`;
}

/** The credential type stored on a valid cloud connection for one provider
 * (config.credentialType), preferring the "default" label. Empty when the
 * provider has no valid connection or the control plane did not record a type
 * (e.g. an older connection), in which case callers fall back to unscoped
 * discovery. */
export function connectedCredentialType(
	connections: CloudCpProviderConnection[] | undefined,
	provider: string,
): string {
	const forProvider = (connections ?? []).filter(
		(connection) => connection.provider === provider && connection.validationState === "valid",
	);
	const chosen = forProvider.find((connection) => connection.label === "default") ?? forProvider[0];
	const credentialType = chosen?.config?.credentialType;
	return typeof credentialType === "string" ? credentialType : "";
}

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
