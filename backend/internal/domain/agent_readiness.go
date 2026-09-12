package domain

import "time"

// AgentInstallationState is the normalized result of locating a harness CLI.
type AgentInstallationState string

const (
	// AgentInstallationInstalled means the harness binary was located.
	AgentInstallationInstalled AgentInstallationState = "installed"
	// AgentInstallationNotInstalled means the harness binary was not located.
	AgentInstallationNotInstalled AgentInstallationState = "not_installed"
	// AgentInstallationUnknown means installation could not be determined.
	AgentInstallationUnknown AgentInstallationState = "unknown"
)

// AgentAuthenticationState is the normalized result of a harness auth check.
type AgentAuthenticationState string

const (
	// AgentAuthenticationAuthorized means the harness appears signed in.
	AgentAuthenticationAuthorized AgentAuthenticationState = "authorized"
	// AgentAuthenticationUnauthorized means the harness appears signed out.
	AgentAuthenticationUnauthorized AgentAuthenticationState = "unauthorized"
	// AgentAuthenticationUnknown means authentication could not be determined.
	AgentAuthenticationUnknown AgentAuthenticationState = "unknown"
	// AgentAuthenticationNotApplicable means the harness requires no auth check.
	AgentAuthenticationNotApplicable AgentAuthenticationState = "not_applicable"
)

// AgentEffectiveReadiness is derived from installation and authentication.
type AgentEffectiveReadiness string

const (
	// AgentReadinessReady means installation and authentication allow use.
	AgentReadinessReady AgentEffectiveReadiness = "ready"
	// AgentReadinessNotReady means a definite observation blocks use.
	AgentReadinessNotReady AgentEffectiveReadiness = "not_ready"
	// AgentReadinessUnknown means readiness cannot be determined safely.
	AgentReadinessUnknown AgentEffectiveReadiness = "unknown"
)

// AgentReadinessFreshness describes whether an observation can satisfy an
// ensure request without native work.
type AgentReadinessFreshness string

const (
	// AgentReadinessFresh means an observation satisfies its freshness policy.
	AgentReadinessFresh AgentReadinessFreshness = "fresh"
	// AgentReadinessStale means an observation should be rechecked.
	AgentReadinessStale AgentReadinessFreshness = "stale"
	// AgentReadinessChecking means a shared native check is in flight.
	AgentReadinessChecking AgentReadinessFreshness = "checking"
)

// AgentReadinessPurpose selects the freshness policy used by Ensure.
type AgentReadinessPurpose string

const (
	// AgentReadinessPurposeDisplay selects the five-minute display policy.
	AgentReadinessPurposeDisplay AgentReadinessPurpose = "display"
	// AgentReadinessPurposeLaunch selects the thirty-second launch policy.
	AgentReadinessPurposeLaunch AgentReadinessPurpose = "launch"
)

// Valid reports whether the purpose selects a supported freshness policy.
func (p AgentReadinessPurpose) Valid() bool {
	return p == AgentReadinessPurposeDisplay || p == AgentReadinessPurposeLaunch
}

// Stable, safe reason codes exposed through the daemon API.
const (
	AgentReadinessReasonNotChecked              = "not_checked"
	AgentReadinessReasonChecking                = "checking"
	AgentReadinessReasonInstalled               = "installed"
	AgentReadinessReasonNotInstalled            = "not_installed"
	AgentReadinessReasonInstallCheckUnsupported = "install_check_unsupported"
	AgentReadinessReasonInstallCheckTimeout     = "install_check_timeout"
	AgentReadinessReasonInstallCheckFailed      = "install_check_failed"
	AgentReadinessReasonAuthorized              = "authorized"
	AgentReadinessReasonUnauthorized            = "unauthorized"
	AgentReadinessReasonAuthNotApplicable       = "auth_not_applicable"
	AgentReadinessReasonAuthCheckUnsupported    = "auth_check_unsupported"
	AgentReadinessReasonAuthCheckInconclusive   = "auth_check_inconclusive"
	AgentReadinessReasonAuthCheckTimeout        = "auth_check_timeout"
	AgentReadinessReasonAuthCheckFailed         = "auth_check_failed"
	AgentReadinessReasonAuthSkippedNotInstalled = "auth_skipped_not_installed"
)

// AgentInstallationObservation records the latest normalized installation check.
type AgentInstallationObservation struct {
	State       AgentInstallationState  `json:"state" enum:"installed,not_installed,unknown"`
	Freshness   AgentReadinessFreshness `json:"freshness" enum:"fresh,stale,checking"`
	CheckedAt   *time.Time              `json:"checkedAt" format:"date-time"`
	AttemptedAt *time.Time              `json:"attemptedAt" format:"date-time"`
	ReasonCode  string                  `json:"reasonCode"`
	Reason      string                  `json:"reason"`
}

// AgentAuthenticationObservation records the latest normalized authentication check.
type AgentAuthenticationObservation struct {
	State       AgentAuthenticationState `json:"state" enum:"authorized,unauthorized,unknown,not_applicable"`
	Freshness   AgentReadinessFreshness  `json:"freshness" enum:"fresh,stale,checking"`
	CheckedAt   *time.Time               `json:"checkedAt" format:"date-time"`
	AttemptedAt *time.Time               `json:"attemptedAt" format:"date-time"`
	ReasonCode  string                   `json:"reasonCode"`
	Reason      string                   `json:"reason"`
}

// AgentReadinessSnapshot is the daemon-owned readiness view for one harness.
// EffectiveReadiness is always derived when a snapshot is read.
type AgentReadinessSnapshot struct {
	ID                 string                         `json:"id"`
	Label              string                         `json:"label"`
	Installation       AgentInstallationObservation   `json:"installation"`
	Authentication     AgentAuthenticationObservation `json:"authentication"`
	EffectiveReadiness AgentEffectiveReadiness        `json:"effectiveReadiness" enum:"ready,not_ready,unknown"`
	UsageCount         int                            `json:"usageCount"`
	LastUsedAt         *time.Time                     `json:"lastUsedAt,omitempty" format:"date-time"`
}

// EffectiveAgentReadiness derives readiness without treating unknown as a negative result.
func EffectiveAgentReadiness(installation AgentInstallationState, authentication AgentAuthenticationState) AgentEffectiveReadiness {
	switch installation {
	case AgentInstallationNotInstalled:
		return AgentReadinessNotReady
	case AgentInstallationUnknown:
		return AgentReadinessUnknown
	case AgentInstallationInstalled:
		switch authentication {
		case AgentAuthenticationAuthorized, AgentAuthenticationNotApplicable:
			return AgentReadinessReady
		case AgentAuthenticationUnauthorized:
			return AgentReadinessNotReady
		default:
			return AgentReadinessUnknown
		}
	default:
		return AgentReadinessUnknown
	}
}
