package domain

import "time"

const (
	// ProjectKindSingleRepo is the existing one-repository project shape.
	ProjectKindSingleRepo ProjectKind = "single_repo"
	// ProjectKindWorkspace is a parent root-as-repo plus child repositories.
	ProjectKindWorkspace ProjectKind = "workspace"
	// ProjectKindScratch is AO-managed plain-directory work for first-run use.
	ProjectKindScratch ProjectKind = "scratch"
	// RootWorkspaceRepoName is the reserved repo_name used for the parent root repo.
	RootWorkspaceRepoName = "__root__"
)

// ProjectKind describes how a registered project materialises session workspaces.
type ProjectKind string

// WithDefault returns ProjectKindSingleRepo when the stored value predates the kind column.
func (k ProjectKind) WithDefault() ProjectKind {
	if k == "" {
		return ProjectKindSingleRepo
	}
	return k
}

// ProjectRecord is the durable project registry row used by storage and services.
type ProjectRecord struct {
	ID            string
	Path          string
	RepoOriginURL string
	DisplayName   string
	RegisteredAt  time.Time
	ArchivedAt    time.Time
	Kind          ProjectKind
	// Config holds the typed per-project configuration AO resolves at spawn. An
	// IsZero value means unset.
	Config ProjectConfig
}

// GitStatus tracks whether a workspace child is a usable git repository
// (ready) or still needs `git init` (needs_init).
type GitStatus string

const (
	// GitStatusReady marks a workspace child as a usable git repository.
	GitStatusReady GitStatus = "ready"
	// GitStatusNeedsInit marks a workspace child that still requires git init.
	GitStatusNeedsInit GitStatus = "needs_init"
)

// WithDefault returns GitStatusReady when the stored value is empty, so callers
// that construct WorkspaceRepoRecord without populating GitStatus (e.g. devimport)
// still satisfy the NOT NULL CHECK constraint on workspace_repos.git_status.
func (g GitStatus) WithDefault() GitStatus {
	if g == "" {
		return GitStatusReady
	}
	return g
}

// WorkspaceRepoRecord is a child repo registered under a workspace project.
// The root repo itself is represented by ProjectRecord and by session_worktrees
// rows using RootWorkspaceRepoName; workspace_repos contains direct children.
type WorkspaceRepoRecord struct {
	ProjectID     ProjectID
	Name          string
	RelativePath  string
	RepoOriginURL string
	DefaultBranch string
	RegisteredAt  time.Time
	GitStatus     GitStatus
}

// SessionWorktreeRecord tracks one repo worktree in a session. Workspace
// projects create one root row plus one child row per WorkspaceRepoRecord.
type SessionWorktreeRecord struct {
	SessionID    SessionID
	RepoName     string
	Branch       string
	BaseSHA      string
	BaseRef      string
	WorktreePath string
	PreservedRef string
	// ponytail: State mirrors session_worktrees.state, an enum that is unused
	// multi-repo scaffolding. The save/restore lifecycle reads and writes only
	// PreservedRef and row presence; State is never set by any live code path
	// and always resolves to the column default ('active' on insert). Wire it
	// when multi-repo worktree lifecycle states actually ship.
	State string
}
