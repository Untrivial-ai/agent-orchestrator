package domain

import (
	"encoding/json"
	"errors"
	"strings"
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
	ID                 string
	OrgID              string
	ProjectID          string
	Kind               string
	Harness            string
	DisplayName        string
	Branch             string
	Mode               string
	Model              string
	DeniedCommands     []string
	Interface          SessionInterface
	ActivityState      contract.ActivityState
	IsTerminated       bool
	RuntimeConnected   bool
	WorkerLastSeenAt   *time.Time
	StartupAttempts    int
	SandboxProvider    string
	DesiredState       string
	ObservedState      string
	RuntimeState       string
	RuntimeError       string
	AutoInjectCI       bool
	AutoInjectReview   bool
	TerminateOnPRMerge bool
	// WorkerEpoch is the highest worker epoch the session has minted for its
	// agent terminal. It advances every time a fresh worker connects (a resume
	// from idle-pause, a restore, or any re-provision), so a client can key its
	// terminal on it and re-attach to the live agent instead of clinging to the
	// dead epoch's exited terminal. 0 when no worker has ever connected.
	WorkerEpoch int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Status derives the session's display status from runtime and pull request facts.
func (s Session) Status(now time.Time, prs []contract.PRFacts) contract.SessionStatus {
	starting := s.DesiredState == "running" && !s.RuntimeConnected && s.WorkerLastSeenAt == nil && s.StartupAttempts == 0 && (s.ObservedState == "requested" || s.ObservedState == "provisioning" ||
		s.ObservedState == "bootstrapping" || s.ObservedState == "restoring" ||
		s.ObservedState == "ready" || s.ObservedState == "running")
	lastSignalAt := s.UpdatedAt
	if s.WorkerLastSeenAt != nil {
		lastSignalAt = *s.WorkerLastSeenAt
	}
	return contract.DeriveStatus(contract.SessionFacts{
		Activity:       s.ActivityState,
		LastActivityAt: lastSignalAt,
		HasSignal:      s.RuntimeConnected,
		SignalExpected: s.DesiredState == "running" && !starting &&
			(s.WorkerLastSeenAt != nil || s.StartupAttempts > 0 || s.ObservedState == "failed"),
		IsTerminated: s.IsTerminated,
	}, prs, now, 2*time.Minute)
}

type CreateSession struct {
	ProjectID      string
	Kind           string
	Harness        string
	DisplayName    string
	Prompt         string
	Mode           string
	Model          string
	DeniedCommands []string
	Interface      SessionInterface
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

// DefaultOrgCoderDurableRoot is the Coder workspace persistent-volume mount
// point assumed when an organization's Coder config omits one. It mirrors the
// mount the AO-maintained Coder templates use, so a bring-your-own org that does
// not override it still provisions against a valid durable root.
const DefaultOrgCoderDurableRoot = "/home/coder"

// OrgCoderConfig is one organization's non-secret bring-your-own Coder
// connection contract. It is stored as the config JSONB of the org's
// ao_provider_connections row (provider="coder", label="default"); the API
// token is deliberately NOT part of this struct — it is that row's
// encrypted_secret. An org with this config points its coder sessions at its own
// Coder deployment (URL/owner/template) instead of the deployment default.
// Modeled on ProjectCoderConfig.
type OrgCoderConfig struct {
	BaseURL     string            `json:"baseUrl"`
	Owner       string            `json:"owner"`
	TemplateID  string            `json:"templateId"`
	AgentName   string            `json:"agentName,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	DurableRoot string            `json:"durableRoot,omitempty"`
	// EndpointServiceName and Region are optional PrivateLink coordinates for a
	// Coder that lives in a private VPC. They are non-secret config AO ops reads to
	// provision the VPC endpoint; the control plane does NOT use them at connection
	// time. Both stay blank for a directly reachable Coder.
	EndpointServiceName string `json:"endpointServiceName,omitempty"`
	Region              string `json:"region,omitempty"`
}

// normalize trims the config's string fields and fills the durable-root default
// so every encode and decode yields the same canonical, provisioning-ready
// value regardless of what the caller supplied.
func (c *OrgCoderConfig) normalize() {
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.Owner = strings.TrimSpace(c.Owner)
	c.TemplateID = strings.TrimSpace(c.TemplateID)
	c.AgentName = strings.TrimSpace(c.AgentName)
	c.DurableRoot = strings.TrimSpace(c.DurableRoot)
	c.EndpointServiceName = strings.TrimSpace(c.EndpointServiceName)
	c.Region = strings.TrimSpace(c.Region)
	if c.DurableRoot == "" {
		c.DurableRoot = DefaultOrgCoderDurableRoot
	}
	if len(c.Parameters) == 0 {
		c.Parameters = nil
	}
}

// DecodeOrgCoderConfig reads an organization's Coder config from the config
// JSONB of its provider connection. Unlike DecodeProjectCoderConfig the config
// is stored directly (not wrapped in a "coder" envelope), because it is the
// whole config column of a dedicated coder connection row.
func DecodeOrgCoderConfig(config json.RawMessage) (OrgCoderConfig, error) {
	if len(config) == 0 {
		return OrgCoderConfig{}, errors.New("organization coder config is empty")
	}
	var cfg OrgCoderConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return OrgCoderConfig{}, err
	}
	cfg.normalize()
	return cfg, nil
}

// EncodeOrgCoderConfig renders an organization's Coder config to the config
// JSONB stored on its provider connection. The token is never part of it.
func EncodeOrgCoderConfig(cfg OrgCoderConfig) (json.RawMessage, error) {
	cfg.normalize()
	return json.Marshal(cfg)
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
	Model             string
	ReasoningEffort   string
	Mode              string
	ApprovalMode      string
	DeniedCommands    []string
	Harness           string
	Attempt           int
	WorkerEpoch       int64
	CancelRequested   bool
	AgentSessionID    string
	UserEventSequence int64
}

type ChatTurnSettings struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Mode            string `json:"mode,omitempty"`
	ApprovalMode    string `json:"approvalMode,omitempty"`
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
	ID                 string
	OrgID              string
	SessionID          string
	WorkerEpoch        int64
	NextOutputSequence int64
	Kind               string
	State              string
	Scopes             []string
	ErrorMessage       string
	ExpiresAt          time.Time
}

type TerminalOutput struct {
	Sequence int64
	Data     []byte
}

type Cursor struct {
	Time time.Time
	ID   string
}
