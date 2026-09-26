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
	// OrgCapabilities are entitlement flags for the active organization, seeded
	// from WorkOS organization metadata (metadata.capabilities). They gate
	// optional features such as the coder sandbox provider. Empty for personal or
	// local organizations.
	OrgCapabilities []string
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
	SandboxProvider  string
	DesiredState     string
	ObservedState    string
	RuntimeState     string
	RuntimeError     string
	// WorkerEpoch is the highest worker epoch the session has minted for its
	// agent terminal. It advances every time a fresh worker connects (a resume
	// from idle-pause, a restore, or any re-provision), so a client can key its
	// terminal on it and re-attach to the live agent instead of clinging to the
	// dead epoch's exited terminal. 0 when no worker has ever connected.
	WorkerEpoch            int64
	PreparationExpiresAt   *time.Time `json:"-"`
	PreparationGeneration  int64      `json:"-"`
	PreparationDisposition string     `json:"-"`
	CreatedAt              time.Time
	UpdatedAt              time.Time
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
	// ParentSessionID links a top-level worker to a project's active
	// orchestrator so the orchestrator sees, drives, and receives reports from
	// it exactly as it would a worker it spawned itself. Empty for an
	// orchestrator, a standalone worker, or a worker created for a project that
	// has no active orchestrator.
	ParentSessionID string
	// PreparationExpiresAfter marks a hidden, promptless session that starts
	// cold compute while the user fills in the task composer. Commit clears it.
	PreparationExpiresAfter time.Duration `json:"preparationExpiresAfter,omitempty"`
	// PreparationClientInstanceID identifies one composer attachment. It is not
	// part of the server-derived compatibility identity.
	PreparationClientInstanceID string `json:"preparationClientInstanceId,omitempty"`
}

type CommitSessionPreparation struct {
	DisplayName      string
	Prompt           string
	ClientInstanceID string
	Generation       int64
}

type SessionPreparationLease struct {
	ExpiresAt  time.Time
	Generation int64
}

// RepoRef is one additional repository (beyond the project's primary repo) to
// clone into a session's workspace, optionally at a specific branch. Configured
// on the project (its Config carries the coder dev-kit config) and inherited by
// every session of that project.
type RepoRef struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
}

// ProjectCoderConfig is the coder dev-kit configuration chosen when the project
// is set up. It is stored under the project's Config as {"coder": {...}} and
// inherited by every coder session of the project. An absent/empty config keeps
// the deployment default template and single-repo behavior.
type ProjectCoderConfig struct {
	TemplateID    string    `json:"templateId,omitempty"`
	Size          string    `json:"size,omitempty"`
	StartupScript string    `json:"startupScript,omitempty"`
	ExtraRepos    []RepoRef `json:"extraRepos,omitempty"`
}

// DecodeProjectCoderConfig extracts the coder dev-kit config from a project's
// Config json. ok is false when the project has no coder config.
func DecodeProjectCoderConfig(config json.RawMessage) (cfg ProjectCoderConfig, ok bool) {
	if len(config) == 0 {
		return ProjectCoderConfig{}, false
	}
	var envelope struct {
		Coder *ProjectCoderConfig `json:"coder"`
	}
	if err := json.Unmarshal(config, &envelope); err != nil || envelope.Coder == nil {
		return ProjectCoderConfig{}, false
	}
	return *envelope.Coder, true
}

// MergeProjectCoderConfig stores the coder dev-kit config under the "coder" key
// of a project's Config, preserving any other keys the config already carries.
func MergeProjectCoderConfig(config json.RawMessage, coder ProjectCoderConfig) (json.RawMessage, error) {
	merged := map[string]json.RawMessage{}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &merged); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(coder)
	if err != nil {
		return nil, err
	}
	merged["coder"] = encoded
	return json.Marshal(merged)
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
