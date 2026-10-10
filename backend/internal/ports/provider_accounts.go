package ports

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Account errors are API errors already, so a handler returns them as they are.
var (
	ErrProviderAccountUnknown           = apierr.NotFound("PROVIDER_ACCOUNT_NOT_FOUND", "provider account not found")
	ErrProviderAccountConflict          = apierr.Conflict("PROVIDER_ACCOUNT_CONFLICT", "account operation conflicts with current state", nil)
	ErrProviderAccountBusy              = apierr.Conflict("PROVIDER_ACCOUNT_IN_USE", "this account is still in use; try again when its sessions have finished", nil)
	ErrProviderAccountIncompatible      = apierr.Invalid("PROVIDER_ACCOUNT_INCOMPATIBLE", "account does not match the session provider", nil)
	ErrProviderAccountNameInvalid       = apierr.Invalid("PROVIDER_ACCOUNT_NAME_INVALID", "account name must be between 1 and 80 characters", nil)
	ErrProviderAccountActionUnavailable = apierr.Conflict("PROVIDER_ACCOUNT_ACTION_UNAVAILABLE", "this account action is unavailable", nil)
	ErrProviderPrimaryRequired          = apierr.Conflict("PROVIDER_PRIMARY_REQUIRED", "choose a replacement primary account first", nil)
	ErrProviderLoginRequired            = apierr.Conflict("PROVIDER_LOGIN_REQUIRED", "provider account login required", nil)
	ErrProviderLoginUnknown             = apierr.NotFound("PROVIDER_LOGIN_NOT_FOUND", "login attempt not found; sign in again")
	ErrProviderLoginCallbackBusy        = apierr.Conflict("PROVIDER_LOGIN_CALLBACK_BUSY", "login callback port is in use; finish the other login and retry", nil)
)

// ProviderAccountStore keeps the one document Account Manager stores.
type ProviderAccountStore interface {
	LoadProviderAccounts(context.Context) (domain.ProviderAccountState, error)
	SaveProviderAccounts(context.Context, domain.ProviderAccountState) error
}

// ProviderRoute tells the account helper which account answers one session
// ticket. TicketHash is the lowercase hex SHA-256 of the ticket.
type ProviderRoute struct {
	TicketHash string `json:"ticket_hash"`
	Provider   string `json:"provider"`
	AuthID     string `json:"auth_id"`
}

// ProviderCredential is one sign-in the helper holds on disk. Failed is the
// helper's verdict that the provider no longer accepts it.
type ProviderCredential struct {
	AuthID     string
	Name       string
	Provider   string
	ModifiedAt time.Time
	Failed     bool
}

// ProviderLoginRequest starts a sign-in, or signs an existing account in again.
type ProviderLoginRequest struct {
	Provider       string `json:"provider" enum:"codex,claude"`
	AccountID      string `json:"accountId,omitempty"`
	Mode           string `json:"mode,omitempty" enum:"browser,device,import,api_key"`
	APIKey         string `json:"apiKey,omitempty"`
	BaseURL        string `json:"baseUrl,omitempty"`
	Label          string `json:"label,omitempty"`
	CredentialJSON string `json:"credentialJson,omitempty"`
}

// ProviderLogin is one sign-in attempt. State is the private OAuth state and
// never reaches the API.
type ProviderLogin struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Mode      string `json:"mode,omitempty"`
	State     string `json:"state"`
	URL       string `json:"url"`
	Code      string `json:"code,omitempty"`
	Status    string `json:"status"`
	AccountID string `json:"account_id"`
}

// VerifiedProviderLogin is whose sign-in a finished attempt produced.
type VerifiedProviderLogin struct {
	Provider      string `json:"provider"`
	Email         string `json:"email"`
	Kind          string `json:"kind"`
	CredentialRef string `json:"credential_ref"`
	AuthID        string `json:"auth_id"`
}

// Actions an account helper runs on one account.
const (
	AccountActionReset   = "reset"
	AccountActionResume  = "resume"
	AccountActionRefresh = "refresh-sign-in"
)

// AccountHelper is the account helper process as the daemon uses it.
type AccountHelper interface {
	// Endpoint is the address sessions send model requests to; TicketKey signs
	// their tickets.
	Endpoint() string
	TicketKey() ([]byte, error)
	// ApplyRoutes replaces the helper's whole table. authIDs names every
	// signed-in account. ErrProviderAccountBusy means an account the table drops
	// has a request in flight, and nothing changed.
	ApplyRoutes(ctx context.Context, routes []ProviderRoute, authIDs []string) error
	Credentials(context.Context) ([]ProviderCredential, error)
	DeleteCredential(ctx context.Context, credentialRef string) error
	AccountUsage(context.Context, domain.ProviderAccount) (domain.ProviderAccountUsage, error)
	// AccountAction runs one of the AccountAction* actions. requestID makes a
	// repeated reset spend nothing twice; the result is a domain.ProviderReset*
	// outcome for a reset and empty otherwise.
	AccountAction(ctx context.Context, account domain.ProviderAccount, action, requestID string) (string, error)
	AccountModels(context.Context, domain.ProviderAccount) ([]AgentModelInfo, error)
	StartLogin(ctx context.Context, id string, request ProviderLoginRequest) (ProviderLogin, error)
	LoginStatus(context.Context, ProviderLogin) (string, error)
	CancelLogin(context.Context, ProviderLogin) error
	LoginResult(ctx context.Context, id string) (VerifiedProviderLogin, error)
	// ImportNative copies this computer's own login, or its API key, unless known
	// says to leave it alone. known is asked before anything is copied, with the
	// login's identity: "id:" and its email for a sign-in (empty when it cannot
	// be told), a fingerprint for a key. It returns the identity (empty when
	// there is no login) and the sign-in only when it copied one.
	ImportNative(ctx context.Context, provider string, apiKey bool, known func(identity string) bool) (VerifiedProviderLogin, string, error)
}

// ProviderAccountRouting is what launching a session needs from Account Manager.
type ProviderAccountRouting interface {
	ResolveAccount(ctx context.Context, harness domain.AgentHarness, explicit string) (id string, managed bool, err error)
	AssignAccount(ctx context.Context, id domain.SessionID, harness domain.AgentHarness, accountID string) error
	SessionAccount(context.Context, domain.SessionID) (domain.ProviderSessionRoute, bool, error)
	LaunchAccountEnv(context.Context, domain.SessionID) (map[string]string, error)
	ForgetAccount(context.Context, domain.SessionID) error
	// AdoptSession gives a session that predates Account Manager a route.
	AdoptSession(context.Context, domain.SessionID, domain.AgentHarness) (bool, error)
}

// ManagedProvider answers sign-in state and model lists for the providers
// Account Manager owns. The bool is false for every other harness or scope.
type ManagedProvider interface {
	AuthenticationReadiness(context.Context, domain.AgentHarness, domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, bool)
	DiscoverModels(ctx context.Context, harness domain.AgentHarness, scope string) (AgentModelCatalog, bool, error)
	ModelsFingerprint(ctx context.Context, harness domain.AgentHarness, scope string) (string, bool, error)
}

// ProviderAccountAction is one change to an account, as the API receives it.
type ProviderAccountAction struct {
	Action               string `json:"action" enum:"primary,sign-out,remove,rename,resume,refresh-sign-in,reset,assign-session,settings"`
	ReplacementPrimaryID string `json:"replacementPrimaryId,omitempty" description:"For sign-out and remove of the default account."`
	DisplayName          string `json:"displayName,omitempty" description:"For rename."`
	SessionID            string `json:"sessionId,omitempty" description:"For assign-session: the session to move onto this account."`
	// For settings: each one given replaces what is stored.
	Reserved *bool   `json:"reserved,omitempty" description:"Keep the account out of new sessions."`
	OnLimit  *string `json:"onLimit,omitempty" description:"The account to move sessions to when a limit is reached; empty for none."`
	WarnAt   *int    `json:"warnAt,omitempty" description:"Percent left at which to warn; zero for never."`
}

// ProviderAccountAdmin is what the HTTP API needs from Account Manager.
type ProviderAccountAdmin interface {
	// Accounts lists every account. usage adds provider limits; refresh re-reads
	// this computer's own logins and every account's sign-in state first.
	Accounts(ctx context.Context, usage, refresh bool) ([]domain.ProviderAccountView, error)
	// Act applies one action and returns a reset's outcome, empty otherwise.
	Act(ctx context.Context, accountID string, action ProviderAccountAction) (string, error)
	SessionAccount(context.Context, domain.SessionID) (domain.ProviderSessionRoute, bool, error)
	StartLogin(context.Context, ProviderLoginRequest) (ProviderLogin, error)
	LoginStatus(ctx context.Context, id string) (ProviderLogin, error)
	CancelLogin(ctx context.Context, id string) error
}
