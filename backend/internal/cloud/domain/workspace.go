package domain

import "time"

// Workspace is one cloud project coordinator. Agent execution belongs to
// SessionRuntime records and never shares this sandbox.
type Workspace struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"orgId"`
	OwnerUserID   string    `json:"-"`
	RepositoryURL string    `json:"repositoryUrl"`
	RepositoryRef string    `json:"repositoryRef,omitempty"`
	SandboxID     string    `json:"sandboxId,omitempty"`
	State         string    `json:"state"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

const (
	// WorkspacePending means provisioning has been accepted but not claimed.
	WorkspacePending = "pending"
	// WorkspaceProvisioning means the provider is creating and bootstrapping compute.
	WorkspaceProvisioning = "provisioning"
	// WorkspaceReady means the sandbox AO daemon is available.
	WorkspaceReady = "ready"
	// WorkspaceFailed means provisioning ended with a bounded user-visible failure.
	WorkspaceFailed = "failed"
)

// WorkspaceBootstrap contains ephemeral secrets needed only while creating a
// project coordinator. None of these fields are persisted or serialized.
type WorkspaceBootstrap struct {
	ClaudeCredentials []byte
	RuntimeToken      string
	ControlPlaneURL   string
}
