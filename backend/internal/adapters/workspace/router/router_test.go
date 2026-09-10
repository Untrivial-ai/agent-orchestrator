package router_test

import (
	"context"
	"testing"

	workspacerouter "github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/router"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type projectStore struct {
	projects map[string]domain.ProjectRecord
}

func (s projectStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	row, ok := s.projects[id]
	return row, ok, nil
}

type recordingWorkspace struct {
	createCalls        int
	restoreCalls       int
	destroyCalls       int
	forceDestroyCalls  int
	stashCalls         int
	applyCalls         int
	addExcludeCalls    int
	projectCreateCalls int
	lastCreate         ports.WorkspaceConfig
	lastRestore        ports.WorkspaceConfig
	lastInfo           ports.WorkspaceInfo
	lastRef            string
	lastPatterns       []string
	lastProjectCreate  ports.WorkspaceProjectConfig
	path               string
}

func (w *recordingWorkspace) ResolveDefaultBranch(_ context.Context, _ string, _ string) (ports.WorkspaceDefaultBranch, error) {
	return ports.WorkspaceDefaultBranch{Remote: "origin", Branch: "main", BaseRef: "refs/remotes/origin/main"}, nil
}

func (w *recordingWorkspace) FetchDefaultBranch(_ context.Context, _ string, _ ports.WorkspaceDefaultBranch) error {
	return nil
}

func (w *recordingWorkspace) Create(_ context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	w.createCalls++
	w.lastCreate = cfg
	path := w.path
	if path == "" {
		path = "/tmp/" + string(cfg.SessionID)
	}
	return ports.WorkspaceInfo{Path: path, Branch: cfg.Branch, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, nil
}

func (w *recordingWorkspace) Restore(_ context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	w.restoreCalls++
	w.lastRestore = cfg
	return ports.WorkspaceInfo{Path: cfg.Path, Branch: cfg.Branch, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, nil
}

func (w *recordingWorkspace) Destroy(_ context.Context, info ports.WorkspaceInfo) error {
	w.destroyCalls++
	w.lastInfo = info
	return nil
}
func (w *recordingWorkspace) ForceDestroy(_ context.Context, info ports.WorkspaceInfo) error {
	w.forceDestroyCalls++
	w.lastInfo = info
	return nil
}
func (w *recordingWorkspace) StashUncommitted(_ context.Context, info ports.WorkspaceInfo) (string, error) {
	w.stashCalls++
	w.lastInfo = info
	return "ref/" + string(info.SessionID), nil
}
func (w *recordingWorkspace) ApplyPreserved(_ context.Context, info ports.WorkspaceInfo, ref string) error {
	w.applyCalls++
	w.lastInfo = info
	w.lastRef = ref
	return nil
}
func (w *recordingWorkspace) AddExclude(_ context.Context, info ports.WorkspaceInfo, patterns ...string) error {
	w.addExcludeCalls++
	w.lastInfo = info
	w.lastPatterns = append([]string(nil), patterns...)
	return nil
}

func (w *recordingWorkspace) CreateWorkspaceProject(_ context.Context, cfg ports.WorkspaceProjectConfig) (ports.WorkspaceProjectInfo, error) {
	w.projectCreateCalls++
	w.lastProjectCreate = cfg
	root := ports.WorkspaceInfo{Path: "/tmp/workspace", Branch: cfg.Branch, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}
	return ports.WorkspaceProjectInfo{Root: root}, nil
}

func (w *recordingWorkspace) DestroyWorkspaceProject(context.Context, ports.WorkspaceProjectInfo) error {
	return nil
}

func TestRouterDelegatesCreateByProjectKind(t *testing.T) {
	git := &recordingWorkspace{}
	scratch := &recordingWorkspace{}
	r := workspacerouter.New(workspacerouter.Deps{
		Git:     git,
		Scratch: scratch,
		Projects: projectStore{projects: map[string]domain.ProjectRecord{
			"repo":    {ID: "repo", Kind: domain.ProjectKindSingleRepo},
			"scratch": {ID: "scratch", Kind: domain.ProjectKindScratch},
		}},
	})

	if _, err := r.Create(context.Background(), ports.WorkspaceConfig{ProjectID: "scratch", SessionID: "scratch-1", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("Create scratch: %v", err)
	}
	if scratch.createCalls != 1 || git.createCalls != 0 {
		t.Fatalf("create calls scratch/git = %d/%d, want 1/0", scratch.createCalls, git.createCalls)
	}

	if _, err := r.Create(context.Background(), ports.WorkspaceConfig{ProjectID: "repo", SessionID: "repo-1", Kind: domain.KindWorker, Branch: "ao/repo-1/root"}); err != nil {
		t.Fatalf("Create repo: %v", err)
	}
	if scratch.createCalls != 1 || git.createCalls != 1 {
		t.Fatalf("create calls scratch/git = %d/%d, want 1/1", scratch.createCalls, git.createCalls)
	}
}

func TestRouterDelegatesLifecycleMethodsByProjectKind(t *testing.T) {
	git := &recordingWorkspace{}
	scratch := &recordingWorkspace{}
	r := workspacerouter.New(workspacerouter.Deps{
		Git:     git,
		Scratch: scratch,
		Projects: projectStore{projects: map[string]domain.ProjectRecord{
			"repo":    {ID: "repo", Kind: domain.ProjectKindSingleRepo},
			"scratch": {ID: "scratch", Kind: domain.ProjectKindScratch},
		}},
	})

	if _, err := r.Restore(context.Background(), ports.WorkspaceConfig{ProjectID: "scratch", SessionID: "scratch-1", Path: "/tmp/scratch"}); err != nil {
		t.Fatalf("Restore scratch: %v", err)
	}
	if scratch.restoreCalls != 1 || git.restoreCalls != 0 {
		t.Fatalf("restore calls scratch/git = %d/%d, want 1/0", scratch.restoreCalls, git.restoreCalls)
	}

	scratchInfo := ports.WorkspaceInfo{ProjectID: "scratch", SessionID: "scratch-1", Path: "/tmp/scratch"}
	if err := r.Destroy(context.Background(), scratchInfo); err != nil {
		t.Fatalf("Destroy scratch: %v", err)
	}
	if err := r.ForceDestroy(context.Background(), scratchInfo); err != nil {
		t.Fatalf("ForceDestroy scratch: %v", err)
	}
	ref, err := r.StashUncommitted(context.Background(), scratchInfo)
	if err != nil {
		t.Fatalf("StashUncommitted scratch: %v", err)
	}
	if ref != "ref/scratch-1" {
		t.Fatalf("scratch stash ref = %q, want ref/scratch-1", ref)
	}
	if err := r.ApplyPreserved(context.Background(), scratchInfo, "ref/scratch-1"); err != nil {
		t.Fatalf("ApplyPreserved scratch: %v", err)
	}
	if err := r.AddExclude(context.Background(), scratchInfo, "/.ao/attachments/"); err != nil {
		t.Fatalf("AddExclude scratch: %v", err)
	}
	if scratch.destroyCalls != 1 || scratch.forceDestroyCalls != 1 || scratch.stashCalls != 1 || scratch.applyCalls != 1 || scratch.addExcludeCalls != 1 {
		t.Fatalf("scratch lifecycle calls destroy/force/stash/apply/exclude = %d/%d/%d/%d/%d, want all 1", scratch.destroyCalls, scratch.forceDestroyCalls, scratch.stashCalls, scratch.applyCalls, scratch.addExcludeCalls)
	}
	if len(scratch.lastPatterns) != 1 || scratch.lastPatterns[0] != "/.ao/attachments/" {
		t.Fatalf("scratch exclude patterns = %#v, want /.ao/attachments/", scratch.lastPatterns)
	}
	if git.destroyCalls != 0 || git.forceDestroyCalls != 0 || git.stashCalls != 0 || git.applyCalls != 0 || git.addExcludeCalls != 0 {
		t.Fatalf("git lifecycle calls for scratch = %d/%d/%d/%d/%d, want all 0", git.destroyCalls, git.forceDestroyCalls, git.stashCalls, git.applyCalls, git.addExcludeCalls)
	}

	repoInfo := ports.WorkspaceInfo{ProjectID: "repo", SessionID: "repo-1", Path: "/tmp/repo", Branch: "ao/repo-1"}
	if err := r.Destroy(context.Background(), repoInfo); err != nil {
		t.Fatalf("Destroy repo: %v", err)
	}
	if err := r.AddExclude(context.Background(), repoInfo, "/.ao/attachments/"); err != nil {
		t.Fatalf("AddExclude repo: %v", err)
	}
	if git.destroyCalls != 1 || git.addExcludeCalls != 1 {
		t.Fatalf("git destroy/exclude calls = %d/%d, want 1/1", git.destroyCalls, git.addExcludeCalls)
	}
}

func TestRouterPreservesWorkspaceProjectDelegation(t *testing.T) {
	git := &recordingWorkspace{}
	scratch := &recordingWorkspace{}
	r := workspacerouter.New(workspacerouter.Deps{
		Git:     git,
		Scratch: scratch,
		Projects: projectStore{projects: map[string]domain.ProjectRecord{
			"workspace": {ID: "workspace", Kind: domain.ProjectKindWorkspace},
		}},
	})

	if _, err := r.CreateWorkspaceProject(context.Background(), ports.WorkspaceProjectConfig{ProjectID: "workspace", SessionID: "workspace-1", Branch: "ao/workspace-1"}); err != nil {
		t.Fatalf("CreateWorkspaceProject: %v", err)
	}
	if git.projectCreateCalls != 1 {
		t.Fatalf("git project create calls = %d, want 1", git.projectCreateCalls)
	}
	if scratch.createCalls != 0 {
		t.Fatalf("scratch create calls = %d, want 0", scratch.createCalls)
	}
}
