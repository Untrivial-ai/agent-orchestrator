package domain

import "time"

// SessionRuntime is the durable control-plane mapping from one AO session to
// its dedicated provider sandbox. Orchestrators and workers use the same model;
// Kind is metadata only and never changes the isolation boundary.
type SessionRuntime struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	OrgID       string    `json:"orgId"`
	SessionID   string    `json:"sessionId"`
	SandboxID   string    `json:"sandboxId,omitempty"`
	State       string    `json:"state"`
	Error       string    `json:"error,omitempty"`
	Generation  int64     `json:"-"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const (
	// SessionRuntimeProvisioning means provider setup has not completed.
	SessionRuntimeProvisioning = "provisioning"
	// SessionRuntimeRunning means the isolated agent terminal is live.
	SessionRuntimeRunning = "running"
	// SessionRuntimeStopped means the sandbox has been deleted.
	SessionRuntimeStopped = "stopped"
	// SessionRuntimeFailed means provisioning ended with a bounded failure.
	SessionRuntimeFailed = "failed"
)

// RuntimeLaunch is the provider-neutral launch request received from a cloud
// coordinator. WorkspaceArchive is a gzip-compressed tar overlay of the exact
// prepared AO worktree; it is never persisted by the control plane.
type RuntimeLaunch struct {
	SessionID         string            `json:"sessionId"`
	Branch            string            `json:"branch,omitempty"`
	SourceWorkspace   string            `json:"sourceWorkspace"`
	Argv              []string          `json:"argv"`
	Env               map[string]string `json:"env,omitempty"`
	WorkspaceArchive  []byte            `json:"-"`
	ClaudeCredentials []byte            `json:"-"`
	Files             []RuntimeFile     `json:"-"`
}

// RuntimeFile is one regular file referenced by launch argv outside the
// worktree (for example AO's generated system prompt).
type RuntimeFile struct {
	SourcePath string
	Data       []byte
}
