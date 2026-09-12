package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ProjectStore is the project lookup surface needed to choose the workspace
// implementation for a session.
type ProjectStore interface {
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
}

// GitWorkspace is the Git-specific capability set required by the router.
type GitWorkspace interface {
	ports.Workspace
	ports.WorkspaceDefaultBranchRefresher
}

// Deps configures a workspace router.
type Deps struct {
	Git      GitWorkspace
	Scratch  ports.Workspace
	Projects ProjectStore
}

// Workspace delegates workspace operations to the adapter that matches the
// session's project kind.
type Workspace struct {
	git      GitWorkspace
	scratch  ports.Workspace
	projects ProjectStore
}

var _ ports.Workspace = (*Workspace)(nil)
var _ ports.WorkspaceDefaultBranchRefresher = (*Workspace)(nil)
var _ ports.WorkspaceProject = (*Workspace)(nil)
var _ ports.WorkspaceObserver = (*Workspace)(nil)
var _ ports.WorkspaceReclaimer = (*Workspace)(nil)

// New returns a router over git and scratch workspace implementations.
func New(deps Deps) *Workspace {
	return &Workspace{
		git:      deps.Git,
		scratch:  deps.Scratch,
		projects: deps.Projects,
	}
}

// ResolveDefaultBranch delegates local default-branch resolution to the git workspace adapter.
func (w *Workspace) ResolveDefaultBranch(ctx context.Context, repoPath, configuredBranch string) (ports.WorkspaceDefaultBranch, error) {
	return w.git.ResolveDefaultBranch(ctx, repoPath, configuredBranch)
}

// FetchDefaultBranch delegates default-branch fetching to the git workspace adapter.
func (w *Workspace) FetchDefaultBranch(ctx context.Context, repoPath string, target ports.WorkspaceDefaultBranch) error {
	return w.git.FetchDefaultBranch(ctx, repoPath, target)
}

// Create delegates session workspace creation to the project-appropriate
// workspace adapter.
func (w *Workspace) Create(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	adapter, err := w.adapterForProject(ctx, cfg.ProjectID)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	return adapter.Create(ctx, cfg)
}

// Restore delegates session workspace restoration to the project-appropriate
// workspace adapter.
func (w *Workspace) Restore(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	adapter, err := w.adapterForProject(ctx, cfg.ProjectID)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	return adapter.Restore(ctx, cfg)
}

// Destroy delegates normal session workspace cleanup to the
// project-appropriate workspace adapter.
func (w *Workspace) Destroy(ctx context.Context, info ports.WorkspaceInfo) error {
	adapter, err := w.adapterForProject(ctx, info.ProjectID)
	if err != nil {
		return err
	}
	return adapter.Destroy(ctx, info)
}

// DestroyReclaim delegates teardown to the project-appropriate adapter and
// reports whether that adapter actually released anything. An adapter that does
// not report reclaim outcomes falls back to Destroy, whose nil error keeps the
// pre-existing meaning of a completed removal.
func (w *Workspace) DestroyReclaim(ctx context.Context, info ports.WorkspaceInfo) (ports.WorkspaceReclaim, error) {
	adapter, err := w.adapterForProject(ctx, info.ProjectID)
	if err != nil {
		return ports.WorkspaceReclaimAlreadyAbsent, err
	}
	if reclaimer, ok := adapter.(ports.WorkspaceReclaimer); ok {
		return reclaimer.DestroyReclaim(ctx, info)
	}
	return ports.WorkspaceReclaimRemoved, adapter.Destroy(ctx, info)
}

// ForceDestroy delegates forced session workspace cleanup to the
// project-appropriate workspace adapter.
func (w *Workspace) ForceDestroy(ctx context.Context, info ports.WorkspaceInfo) error {
	adapter, err := w.adapterForProject(ctx, info.ProjectID)
	if err != nil {
		return err
	}
	return adapter.ForceDestroy(ctx, info)
}

// StashUncommitted delegates preservation of dirty workspace state to the
// project-appropriate workspace adapter.
func (w *Workspace) StashUncommitted(ctx context.Context, info ports.WorkspaceInfo) (string, error) {
	adapter, err := w.adapterForProject(ctx, info.ProjectID)
	if err != nil {
		return "", err
	}
	return adapter.StashUncommitted(ctx, info)
}

// ApplyPreserved delegates restored dirty workspace state application to the
// project-appropriate workspace adapter.
func (w *Workspace) ApplyPreserved(ctx context.Context, info ports.WorkspaceInfo, ref string) error {
	adapter, err := w.adapterForProject(ctx, info.ProjectID)
	if err != nil {
		return err
	}
	return adapter.ApplyPreserved(ctx, info, ref)
}

// AddExclude delegates local workspace ignore updates to the project-appropriate
// workspace adapter.
func (w *Workspace) AddExclude(ctx context.Context, info ports.WorkspaceInfo, patterns ...string) error {
	adapter, err := w.adapterForProject(ctx, info.ProjectID)
	if err != nil {
		return err
	}
	return adapter.AddExclude(ctx, info, patterns...)
}

// ObserveWorkspace delegates the read-only handoff snapshot to the selected
// project adapter. Adapters that cannot observe state return a clear error
// instead of fabricating repository facts.
func (w *Workspace) ObserveWorkspace(ctx context.Context, info ports.WorkspaceInfo) (ports.WorkspaceObservation, error) {
	adapter, err := w.adapterForProject(ctx, info.ProjectID)
	if err != nil {
		return ports.WorkspaceObservation{}, err
	}
	observer, ok := adapter.(ports.WorkspaceObserver)
	if !ok {
		return ports.WorkspaceObservation{}, errors.New("workspace router: selected workspace does not support observation")
	}
	return observer.ObserveWorkspace(ctx, info)
}

// CreateWorkspaceProject delegates root-as-repo workspace project creation to
// the git adapter.
func (w *Workspace) CreateWorkspaceProject(ctx context.Context, cfg ports.WorkspaceProjectConfig) (ports.WorkspaceProjectInfo, error) {
	gitProject, err := w.gitWorkspaceProject()
	if err != nil {
		return ports.WorkspaceProjectInfo{}, err
	}
	return gitProject.CreateWorkspaceProject(ctx, cfg)
}

// DestroyWorkspaceProject delegates root-as-repo workspace project cleanup to
// the git adapter.
func (w *Workspace) DestroyWorkspaceProject(ctx context.Context, info ports.WorkspaceProjectInfo) error {
	gitProject, err := w.gitWorkspaceProject()
	if err != nil {
		return err
	}
	return gitProject.DestroyWorkspaceProject(ctx, info)
}

func (w *Workspace) adapterForProject(ctx context.Context, projectID domain.ProjectID) (ports.Workspace, error) {
	if w == nil {
		return nil, errors.New("workspace router: nil router")
	}
	if w.projects != nil && projectID != "" {
		project, ok, err := w.projects.GetProject(ctx, string(projectID))
		if err != nil {
			return nil, fmt.Errorf("workspace router: project %q: %w", projectID, err)
		}
		if ok && project.Kind.WithDefault() == domain.ProjectKindScratch {
			if w.scratch == nil {
				return nil, errors.New("workspace router: scratch workspace is not configured")
			}
			return w.scratch, nil
		}
	}
	if w.git == nil {
		return nil, errors.New("workspace router: git workspace is not configured")
	}
	return w.git, nil
}

func (w *Workspace) gitWorkspaceProject() (ports.WorkspaceProject, error) {
	if w == nil || w.git == nil {
		return nil, errors.New("workspace router: git workspace is not configured")
	}
	gitProject, ok := w.git.(ports.WorkspaceProject)
	if !ok {
		return nil, errors.New("workspace router: git workspace does not support workspace projects")
	}
	return gitProject, nil
}
