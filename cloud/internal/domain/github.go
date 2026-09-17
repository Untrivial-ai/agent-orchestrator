package domain

import (
	"encoding/json"
	"time"
)

type GitHubUserAuthAttempt struct {
	ID                     string
	UserID                 string
	StateHash              []byte
	CodeVerifierCiphertext []byte
	CodeVerifierNonce      []byte
	ExpiresAt              time.Time
	ConsumedAt             *time.Time
	CreatedAt              time.Time
}

type GitHubUserConnection struct {
	UserID                 string
	GitHubUserID           int64
	GitHubLogin            string
	GitHubAvatarURL        string
	AccessTokenCiphertext  []byte
	AccessTokenNonce       []byte
	AccessTokenExpiresAt   *time.Time
	RefreshTokenCiphertext []byte
	RefreshTokenNonce      []byte
	RefreshTokenExpiresAt  *time.Time
	LastSyncedAt           time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type GitHubUserInstallation struct {
	GitHubInstallationID int64
	AccountLogin         string
	AccountType          string
	RepositorySelection  string
	CanCreateRepository  bool
	UnavailableReason    string
}

type GitHubInstallation struct {
	ID                   string
	OrgID                string
	GitHubInstallationID int64
	GitHubAccountID      int64
	AccountLogin         string
	AccountType          string
	Status               string
	RepositorySelection  string
	Permissions          json.RawMessage
	Events               []string
	SyncStatus           string
	SyncGeneration       int64
	LastSyncedAt         *time.Time
	LastError            string
	InstalledByUserID    string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type GitHubInstallAttempt struct {
	ID                          string
	OrgID                       string
	InitiatingUserID            string
	Phase                       string
	PendingGitHubInstallationID int64
	OAuthVerifierCiphertext     []byte
	OAuthVerifierNonce          []byte
	ExpiresAt                   time.Time
}

type GitHubRepository struct {
	GrantID            string
	GitHubRepositoryID int64
	GitHubOwnerID      int64
	Name               string
	FullName           string
	HTMLURL            string
	CloneURL           string
	SSHURL             string
	DefaultBranch      string
	Visibility         string
	IsPrivate          bool
	IsArchived         bool
	IsDisabled         bool
	GitHubUpdatedAt    *time.Time
	GrantedAt          time.Time
	RevokedAt          *time.Time
}

// GitHubCheckoutContext is the non-secret authorization result for a worker
// checkout. The store only returns it when the worker's session, project,
// repository grant, installation, and recorded repository identity still agree.
type GitHubCheckoutContext struct {
	OrgID                string
	SessionID            string
	ProjectID            string
	GitHubInstallationID int64
	GitHubRepositoryID   int64
	FullName             string
	CloneURL             string
	DefaultBranch        string
}

type GitHubRepositoryCapability struct {
	ID                   string
	OrgID                string
	UserID               string
	UserExternalID       string
	GitHubUserID         int64
	TargetEnvironment    string
	IdempotencyKey       string
	RequestHash          []byte
	Status               string
	GitHubInstallationID int64
	GitHubRepositoryID   int64
	CapabilityHash       []byte
	CapabilityCiphertext []byte
	CapabilityNonce      []byte
	RepositoryOwner      string
	RepositoryName       string
	Repository           GitHubRepository
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type RemoteGitHubCheckoutContext struct {
	OrgID                string
	SessionID            string
	ProjectID            string
	GitHubInstallationID int64
	GitHubRepositoryID   int64
	UserExternalID       string
	TargetEnvironment    string
	RepositoryURL        string
	CapabilityCiphertext []byte
	CapabilityNonce      []byte
}

type CreateGitHubProject struct {
	GitHubRepositoryID int64
	DisplayName        string
	Config             json.RawMessage
}

type CreateGitHubScratchProject struct {
	ProjectID               string
	Repository              GitHubRepository
	GitHubInstallationID    int64
	AuthorityUserExternalID string
	AuthorityEnvironment    string
	CapabilityHash          []byte
	CapabilityCiphertext    []byte
	CapabilityNonce         []byte
	DisplayName             string
	Config                  json.RawMessage
	Session                 CreateSession
}

type GitHubWebhookDelivery struct {
	DeliveryID           string
	Event                string
	Action               string
	GitHubInstallationID int64
	GitHubRepositoryID   int64
	Payload              []byte
	AttemptCount         int
}
