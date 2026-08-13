package project

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// Summary is the row shape returned by GET /api/v1/projects.
type Summary struct {
	ID                domain.ProjectID    `json:"id"`
	Name              string              `json:"name"`
	Path              string              `json:"path"`
	Kind              domain.ProjectKind  `json:"kind" enum:"single_repo,workspace,scratch"`
	SessionPrefix     string              `json:"sessionPrefix"`
	OrchestratorAgent domain.AgentHarness `json:"orchestratorAgent,omitempty"`
	ResolveError      string              `json:"resolveError,omitempty"`
}

// Project is the full read-model returned by GET /api/v1/projects/{id}.
type Project struct {
	ID             domain.ProjectID      `json:"id"`
	Name           string                `json:"name"`
	Kind           domain.ProjectKind    `json:"kind" enum:"single_repo,workspace,scratch"`
	Path           string                `json:"path"`
	Repo           string                `json:"repo"`
	DefaultBranch  string                `json:"defaultBranch"`
	Agent          string                `json:"agent,omitempty"`
	Config         *domain.ProjectConfig `json:"config,omitempty"`
	WorkspaceRepos []WorkspaceRepo       `json:"workspaceRepos,omitempty"`
}

// Degraded is returned in place of Project when project config failed to load.
type Degraded struct {
	ID           domain.ProjectID   `json:"id"`
	Name         string             `json:"name"`
	Kind         domain.ProjectKind `json:"kind" enum:"single_repo,workspace,scratch"`
	Path         string             `json:"path"`
	ResolveError string             `json:"resolveError"`
}

// WorkspaceRepo is the project-detail read shape for a registered child repo.
type WorkspaceRepo struct {
	Name          string `json:"name"`
	RelativePath  string `json:"relativePath"`
	Repo          string `json:"repo"`
	DefaultBranch string `json:"defaultBranch"`
}
