package domain

import "time"

// CodexAccountSource identifies an AO-owned Codex credential slot. Device
// credentials are an import source only; once verified, the slot is managed.
type CodexAccountSource string

// CodexAccountSourceManaged identifies an AO-owned credential slot.
const CodexAccountSourceManaged CodexAccountSource = "managed"

// CodexAccountStatus describes whether a discovered account slot is usable.
type CodexAccountStatus string

const (
	// CodexAccountStatusValid means the descriptor and credential home are safe.
	CodexAccountStatusValid CodexAccountStatus = "valid"
	// CodexAccountStatusSignedOut means the descriptor and private home are safe,
	// but the account currently has no saved credential.
	CodexAccountStatusSignedOut CodexAccountStatus = "signed_out"
	// CodexAccountStatusBroken means the slot is visible but cannot be used.
	CodexAccountStatusBroken CodexAccountStatus = "broken"
)

// CodexAuthMethod identifies the provider authentication mechanism.
type CodexAuthMethod string

const (
	// CodexAuthMethodChatGPT represents browser-backed ChatGPT authentication.
	CodexAuthMethodChatGPT CodexAuthMethod = "chatgpt"
	// CodexAuthMethodAPIKey represents Codex API-key authentication.
	CodexAuthMethodAPIKey CodexAuthMethod = "api_key"
	// CodexAuthMethodOther represents a recognized unclassified mechanism.
	CodexAuthMethodOther CodexAuthMethod = "other"
	// CodexAuthMethodUnknown means the mechanism could not be established.
	CodexAuthMethodUnknown CodexAuthMethod = "unknown"
)

// CodexCapabilityState is the support state of one installed Codex capability.
type CodexCapabilityState string

const (
	// CodexCapabilitySupported means the installed protocol exposes the capability.
	CodexCapabilitySupported CodexCapabilityState = "supported"
	// CodexCapabilityUnsupported means a successful probe proved absence.
	CodexCapabilityUnsupported CodexCapabilityState = "unsupported"
	// CodexCapabilityUnknown means the capability probe was inconclusive.
	CodexCapabilityUnknown CodexCapabilityState = "unknown"
)

// CodexCapabilityObservation is a display-safe capability result.
type CodexCapabilityObservation struct {
	State      CodexCapabilityState `json:"state" enum:"supported,unsupported,unknown"`
	ReasonCode string               `json:"reasonCode"`
	Reason     string               `json:"reason"`
}

// CodexAccountCapabilities reports the account-management protocol surface.
type CodexAccountCapabilities struct {
	AccountRead        CodexCapabilityObservation `json:"accountRead"`
	NativeLogin        CodexCapabilityObservation `json:"nativeLogin"`
	CapacityRead       CodexCapabilityObservation `json:"capacityRead"`
	UsageRead          CodexCapabilityObservation `json:"usageRead"`
	ResetCreditConsume CodexCapabilityObservation `json:"resetCreditConsume"`
	ThreadResume       CodexCapabilityObservation `json:"threadResume"`
	AccountManagement  CodexCapabilityObservation `json:"accountManagement"`
	GlobalSwitch       CodexCapabilityObservation `json:"globalSwitch"`
}

// CodexAccountSnapshot is the display-safe cached state for one AO account.
type CodexAccountSnapshot struct {
	ID             string                         `json:"id"`
	Label          string                         `json:"label"`
	Source         CodexAccountSource             `json:"source" enum:"managed"`
	Status         CodexAccountStatus             `json:"status" enum:"valid,signed_out,broken"`
	ReasonCode     string                         `json:"reasonCode"`
	Reason         string                         `json:"reason"`
	Active         bool                           `json:"active"`
	Authentication AgentAuthenticationObservation `json:"authentication"`
	AuthMethod     CodexAuthMethod                `json:"authMethod" enum:"chatgpt,api_key,other,unknown"`
	AccountEmail   *string                        `json:"accountEmail,omitempty"`
	Capacity       CodexCapacitySnapshot          `json:"capacity"`
	UsageSummary   *CodexAccountUsageSummary      `json:"usageSummary,omitempty"`
	CreatedAt      time.Time                      `json:"createdAt"`
}

// CodexAccountUsageSummary contains optional structured token-usage
// aggregates. Field names carry their units so renderers never have to infer
// what the provider values represent.
type CodexAccountUsageSummary struct {
	LatestDayTokens           *int64    `json:"latestDayTokens,omitempty"`
	LatestDayStartDate        *string   `json:"latestDayStartDate,omitempty"`
	LifetimeTokens            *int64    `json:"lifetimeTokens,omitempty"`
	PeakDailyTokens           *int64    `json:"peakDailyTokens,omitempty"`
	LongestRunningTurnSeconds *int64    `json:"longestRunningTurnSeconds,omitempty"`
	CurrentStreakDays         *int64    `json:"currentStreakDays,omitempty"`
	LongestStreakDays         *int64    `json:"longestStreakDays,omitempty"`
	ObservedAt                time.Time `json:"observedAt"`
}

// CodexResetCreditOutcome is the provider's idempotent result for one reset
// attempt. It is kept internal to the daemon boundary; public callers receive
// the refreshed account snapshot or a typed API error.
type CodexResetCreditOutcome string

const (
	// CodexResetCreditReset means the provider applied a new usage reset.
	CodexResetCreditReset CodexResetCreditOutcome = "reset"
	// CodexResetCreditAlreadyRedeemed means this idempotent attempt was applied previously.
	CodexResetCreditAlreadyRedeemed CodexResetCreditOutcome = "already_redeemed"
	// CodexResetCreditNothingToReset means the account currently has no eligible limit to reset.
	CodexResetCreditNothingToReset CodexResetCreditOutcome = "nothing_to_reset"
	// CodexResetCreditNoCredit means the account has no reset credit available.
	CodexResetCreditNoCredit CodexResetCreditOutcome = "no_credit"
)

const (
	// CodexAccountReasonValid identifies a usable account slot.
	CodexAccountReasonValid = "account_valid"
	// CodexAccountReasonSignedOut identifies a retained account without credentials.
	CodexAccountReasonSignedOut = "account_signed_out"
	// CodexAccountReasonDescriptorInvalid identifies a malformed descriptor.
	CodexAccountReasonDescriptorInvalid = "account_descriptor_invalid"
	// CodexAccountReasonHomeMissing identifies a missing credential home.
	CodexAccountReasonHomeMissing = "account_credential_home_missing"
	// CodexAccountReasonUnsafePath identifies unsafe ownership or link state.
	CodexAccountReasonUnsafePath = "account_unsafe_path"
	// CodexCapabilityReasonSupported is the safe supported reason code.
	CodexCapabilityReasonSupported = "supported"
	// CodexCapabilityReasonUnsupported is the safe unsupported reason code.
	CodexCapabilityReasonUnsupported = "unsupported"
	// CodexCapabilityReasonUnknown is the safe inconclusive reason code.
	CodexCapabilityReasonUnknown = "unknown"
)

// CodexAccountLoginStatus is the daemon-owned pending login lifecycle state.
type CodexAccountLoginStatus string

const (
	// CodexAccountLoginPending means the native login terminal is still active.
	CodexAccountLoginPending CodexAccountLoginStatus = "pending"
	// CodexAccountLoginVerifying means structured verification is running.
	CodexAccountLoginVerifying CodexAccountLoginStatus = "verifying"
	// CodexAccountLoginUnauthorized means verification confirmed signed-out state.
	CodexAccountLoginUnauthorized CodexAccountLoginStatus = "unauthorized"
	// CodexAccountLoginUnverified means verification was inconclusive.
	CodexAccountLoginUnverified CodexAccountLoginStatus = "unverified"
	// CodexAccountLoginCompleted means a verified account was committed.
	CodexAccountLoginCompleted CodexAccountLoginStatus = "completed"
	// CodexAccountLoginCancelled means terminal and staging were removed.
	CodexAccountLoginCancelled CodexAccountLoginStatus = "cancelled"
	// CodexAccountLoginFailed means the operation could not complete safely.
	CodexAccountLoginFailed CodexAccountLoginStatus = "failed"
	// CodexAccountLoginExpired means the terminal exceeded its lifetime.
	CodexAccountLoginExpired CodexAccountLoginStatus = "expired"
)

const (
	// CodexAccountLoginReasonPending is the safe pending-login reason code.
	CodexAccountLoginReasonPending = "login_pending"
	// CodexAccountLoginReasonCompleted is the safe completed-login reason code.
	CodexAccountLoginReasonCompleted = "login_completed"
	// CodexAccountLoginReasonCancelled is the safe cancelled-login reason code.
	CodexAccountLoginReasonCancelled = "login_cancelled"
	// CodexAccountLoginReasonFailed is the safe failed-login reason code.
	CodexAccountLoginReasonFailed = "login_failed"
	// CodexAccountLoginReasonUnauthorized is the safe signed-out reason code.
	CodexAccountLoginReasonUnauthorized = "login_unauthorized"
	// CodexAccountLoginReasonUnverified is the safe inconclusive reason code.
	CodexAccountLoginReasonUnverified = "login_unverified"
	// CodexAccountLoginReasonExpired is the safe expiry reason code.
	CodexAccountLoginReasonExpired = "login_expired"
)

// CodexAccountLoginOperation is the safe transient login projection.
type CodexAccountLoginOperation struct {
	OperationID string                  `json:"operationId"`
	AccountID   string                  `json:"accountId,omitempty"`
	Status      CodexAccountLoginStatus `json:"status" enum:"pending,verifying,unauthorized,unverified,completed,cancelled,failed,expired"`
	ReasonCode  string                  `json:"reasonCode"`
	Reason      string                  `json:"reason"`
	Account     *CodexAccountSnapshot   `json:"account,omitempty"`
	ExpiresAt   time.Time               `json:"expiresAt"`
}

// CodexActiveAccount identifies the AO account currently installed in the
// device-global Codex credential store.
type CodexActiveAccount struct {
	AccountID   string    `json:"accountId"`
	Revision    int64     `json:"revision"`
	ActivatedAt time.Time `json:"activatedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CodexUnmanagedGlobalAccount describes a device-global identity that AO cannot
// safely import or switch because its credential is unavailable or ambiguous.
type CodexUnmanagedGlobalAccount struct {
	Label        string          `json:"label"`
	AuthMethod   CodexAuthMethod `json:"authMethod"`
	AccountEmail *string         `json:"accountEmail,omitempty"`
	ReasonCode   string          `json:"reasonCode"`
	Reason       string          `json:"reason"`
}
