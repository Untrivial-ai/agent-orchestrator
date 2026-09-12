package domain

import (
	"encoding/json"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

type Principal struct {
	UserID        string
	Provider      string
	ExternalID    string
	Email         string
	DisplayName   string
	ExternalOrgID string
	OrgName       string
	OrgRole       string
}

type Membership struct {
	OrgID       string
	OrgSlug     string
	DisplayName string
	Role        string
}

type LocalRegistration struct {
	Email        string
	DisplayName  string
	PasswordHash string
	OrgSlug      string
	OrgName      string
}

type Project struct {
	ID                 string
	OrgID              string
	DisplayName        string
	RepositoryURL      string
	DefaultBranch      string
	GitHubRepositoryID *int64
	Config             json.RawMessage
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type CreateProject struct {
	DisplayName   string
	RepositoryURL string
	DefaultBranch string
	Config        json.RawMessage
}

type UpdateProject struct {
	DisplayName   string
	DefaultBranch string
}

type Session struct {
	ID               string
	OrgID            string
	ProjectID        string
	Kind             string
	Harness          string
	DisplayName      string
	Branch           string
	Mode             string
	DeniedCommands   []string
	ActivityState    contract.ActivityState
	IsTerminated     bool
	RuntimeConnected bool
	RuntimeState     string
	RuntimeError     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Status derives the session's display status from runtime and pull request facts.
func (s Session) Status(now time.Time, prs []contract.PRFacts) contract.SessionStatus {
	return contract.DeriveStatus(contract.SessionFacts{
		Activity:       s.ActivityState,
		LastActivityAt: s.UpdatedAt,
		HasSignal:      s.RuntimeConnected,
		SignalExpected: s.RuntimeState != "",
		IsTerminated:   s.IsTerminated,
	}, prs, now, 2*time.Minute)
}

type CreateSession struct {
	ProjectID      string
	Kind           string
	Harness        string
	DisplayName    string
	Prompt         string
	Mode           string
	DeniedCommands []string
	Provider       string
	// SandboxConnectionID names a bring-your-own provider credential. It is
	// empty for sandboxes that run on the platform's own account.
	SandboxConnectionID string
	// ResourceProfile and BootstrapContext are the provisioning plan the
	// sandbox row is created with. They are stamped at intent time so a later
	// configuration change cannot disturb an in-flight session.
	ResourceProfile  json.RawMessage
	BootstrapContext json.RawMessage
	Release          string
}

type ClientEvent struct {
	SessionID string
	Sequence  int64
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// WorkerTurn is the durable unit of coding-agent work leased to one worker
// epoch. Attempt fences callbacks from an earlier claim even if a request is
// delayed and delivered after the turn has moved on.
type WorkerTurn struct {
	ID                string
	SessionID         string
	Prompt            string
	Mode              string
	DeniedCommands    []string
	Harness           string
	Attempt           int
	WorkerEpoch       int64
	CancelRequested   bool
	AgentSessionID    string
	UserEventSequence int64
}

// WorkerCredential is the encrypted coding-agent credential selected by the
// session harness. Plaintext is produced only at the authenticated HTTP edge.
type WorkerCredential struct {
	Provider        string
	CredentialType  string
	EncryptedSecret []byte
	Nonce           []byte
	OwnerUserID     string
}

// WorkerRequest is a short-lived durable command for the current worker epoch.
// The database owns routing and leasing so any control-plane replica can submit
// or await it without process affinity.
type WorkerRequest struct {
	ID           string
	OrgID        string
	SessionID    string
	WorkerEpoch  int64
	Kind         string
	Payload      json.RawMessage
	Status       string
	Response     json.RawMessage
	ErrorCode    string
	ErrorMessage string
	Attempt      int
	ExpiresAt    time.Time
}

type TerminalSession struct {
	ID           string
	OrgID        string
	SessionID    string
	WorkerEpoch  int64
	Kind         string
	State        string
	Scopes       []string
	ErrorMessage string
	ExpiresAt    time.Time
}

type TerminalOutput struct {
	Sequence int64
	Data     []byte
}

type Cursor struct {
	Time time.Time
	ID   string
}
