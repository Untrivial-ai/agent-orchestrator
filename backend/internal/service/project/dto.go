package project

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// GetResult is the discriminated result returned by Service.Get.
type GetResult struct {
	Status   string
	Project  *Project
	Degraded *Degraded
}

// AddInput is the body shape for POST /api/v1/projects.
type AddInput struct {
	Path        string                `json:"path"`
	ProjectID   *string               `json:"projectId,omitempty"`
	Name        *string               `json:"name,omitempty"`
	Config      *domain.ProjectConfig `json:"config,omitempty"`
	AsWorkspace bool                  `json:"asWorkspace,omitempty"`
}

// CloneInput is the body shape for POST /api/v1/projects/clone. The daemon
// derives the checkout directory name from RemoteURL and creates it directly
// beneath DestinationParent before registering the resulting project.
type CloneInput struct {
	RemoteURL         string                `json:"remoteUrl" minLength:"1"`
	DestinationParent string                `json:"destinationParent" minLength:"1"`
	ProjectID         *string               `json:"projectId,omitempty"`
	Name              *string               `json:"name,omitempty"`
	Config            *domain.ProjectConfig `json:"config,omitempty"`
}

// ClonePreparationResult is the checkout returned before project registration.
type ClonePreparationResult struct {
	Path      string `json:"path"`
	RemoteURL string `json:"remoteUrl"`
}

// ClonePreparationCleanupInput identifies a checkout created by prepare-clone
// that the user abandoned before project registration.
type ClonePreparationCleanupInput struct {
	Path string `json:"path" minLength:"1"`
}

// InitializeRepositoryInput is the body shape for POST /api/v1/projects/initialize.
type InitializeRepositoryInput struct {
	Path string `json:"path"`
}

// InitializeRepositoryResult reports the repository path initialized for onboarding.
type InitializeRepositoryResult struct {
	Path string `json:"path"`
}

// UpdateSettingsInput is the body shape for PUT /api/v1/projects/{id}. It
// atomically replaces the user-facing display name and per-project config.
type UpdateSettingsInput struct {
	DisplayName string               `json:"displayName" minLength:"1" maxLength:"20"`
	Config      domain.ProjectConfig `json:"config"`
}

// SetConfigInput is the body shape for PUT /api/v1/projects/{id}/config. Config
// replaces the project's stored config wholesale; a zero-value config clears it.
type SetConfigInput struct {
	Config domain.ProjectConfig `json:"config"`
}

// RemoveResult reports what DELETE /api/v1/projects/{id} actually did.
type RemoveResult struct {
	ProjectID         domain.ProjectID `json:"projectId"`
	RemovedStorageDir bool             `json:"removedStorageDir"`
}
