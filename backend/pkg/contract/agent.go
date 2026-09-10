package contract

// AgentCapability is a stable feature an agent runtime can expose.
type AgentCapability string

// Agent capabilities understood by AO clients.
const (
	CapabilityInterfaceChat  AgentCapability = "interface.chat"
	CapabilityInterfaceTUI   AgentCapability = "interface.tui"
	CapabilityModelCatalog   AgentCapability = "model.catalog"
	CapabilityCustomModel    AgentCapability = "model.custom"
	CapabilityAttachments    AgentCapability = "attachments"
	CapabilityBrowserPreview AgentCapability = "browser.preview"
	CapabilityReviewExecute  AgentCapability = "review.execute"
	CapabilityResume         AgentCapability = "session.resume"
)

// AgentInstallationState is the host's normalized installation result.
type AgentInstallationState string

// Agent installation states reported by a host.
const (
	AgentInstallationInstalled     AgentInstallationState = "installed"
	AgentInstallationNotInstalled  AgentInstallationState = "not_installed"
	AgentInstallationUnknown       AgentInstallationState = "unknown"
	AgentInstallationNotApplicable AgentInstallationState = "not_applicable"
)

// AgentAuthenticationState is the host's normalized authentication result.
type AgentAuthenticationState string

// Agent authentication states reported by a host.
const (
	AgentAuthenticationAuthorized    AgentAuthenticationState = "authorized"
	AgentAuthenticationUnauthorized  AgentAuthenticationState = "unauthorized"
	AgentAuthenticationUnknown       AgentAuthenticationState = "unknown"
	AgentAuthenticationNotApplicable AgentAuthenticationState = "not_applicable"
)

// AgentOrganizationPolicy is the host's organization-level permission for an agent.
type AgentOrganizationPolicy string

// Agent organization-policy decisions reported by a host.
const (
	OrganizationPolicyNotApplicable AgentOrganizationPolicy = "not_applicable"
	OrganizationPolicyAllowed       AgentOrganizationPolicy = "allowed"
	OrganizationPolicyDenied        AgentOrganizationPolicy = "denied"
)

// AgentAvailability reports normalized host checks and their effective result.
type AgentAvailability struct {
	Available          bool                     `json:"available"`
	Installation       AgentInstallationState   `json:"installation"`
	Authentication     AgentAuthenticationState `json:"authentication"`
	OrganizationPolicy AgentOrganizationPolicy  `json:"organizationPolicy"`
	Reason             string                   `json:"reason,omitempty"`
}

// AgentProfile combines agent features with host-supplied availability.
type AgentProfile struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	Capabilities []AgentCapability `json:"capabilities"`
	Availability AgentAvailability `json:"availability"`
}

// HasAgentCapability reports whether a profile declares a capability.
func HasAgentCapability(profile AgentProfile, capability AgentCapability) bool {
	for _, candidate := range profile.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}
