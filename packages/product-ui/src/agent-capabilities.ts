export const AGENT_CAPABILITIES = [
	"interface.chat",
	"interface.tui",
	"model.catalog",
	"model.custom",
	"attachments",
	"browser.preview",
	"review.execute",
	"session.resume",
] as const;

export type AgentCapability = (typeof AGENT_CAPABILITIES)[number];

export const AGENT_INSTALLATION_STATES = [
	"installed",
	"not_installed",
	"unknown",
	"not_applicable",
] as const;

export type AgentInstallationState = (typeof AGENT_INSTALLATION_STATES)[number];

export const AGENT_AUTHENTICATION_STATES = [
	"authorized",
	"unauthorized",
	"unknown",
	"not_applicable",
] as const;

export type AgentAuthenticationState = (typeof AGENT_AUTHENTICATION_STATES)[number];

export type AgentOrganizationPolicy = "not_applicable" | "allowed" | "denied";

export type AgentAvailability = {
	available: boolean;
	installation: AgentInstallationState;
	authentication: AgentAuthenticationState;
	organizationPolicy: AgentOrganizationPolicy;
	reason?: string;
};

export type AgentProfile = {
	id: string;
	label: string;
	capabilities: readonly AgentCapability[];
	availability: AgentAvailability;
};

export function hasAgentCapability(
	profile: Pick<AgentProfile, "capabilities">,
	capability: AgentCapability,
): boolean {
	return profile.capabilities.includes(capability);
}
