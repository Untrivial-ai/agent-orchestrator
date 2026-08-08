package scratch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Options configures the scratch workspace adapter.
type Options struct {
	ManagedRoot string
}

// Workspace creates per-session plain directories under AO's managed root.
type Workspace struct {
	managedRoot string
}

var _ ports.Workspace = (*Workspace)(nil)
var _ ports.WorkspaceObserver = (*Workspace)(nil)

// New validates ManagedRoot and returns a scratch workspace adapter.
func New(opts Options) (*Workspace, error) {
	if strings.TrimSpace(opts.ManagedRoot) == "" {
		return nil, errors.New("scratch workspace: ManagedRoot is required")
	}
	root, err := physicalAbs(opts.ManagedRoot)
	if err != nil {
		return nil, fmt.Errorf("scratch workspace: managed root: %w", err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("scratch workspace: create managed root: %w", err)
	}
	return &Workspace{managedRoot: filepath.Clean(root)}, nil
}

// Create materializes a branchless scratch directory for one session.
func (w *Workspace) Create(_ context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	path, err := w.managedPath(cfg)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	if exists, err := pathExistsNonEmpty(path); err != nil {
		return ports.WorkspaceInfo{}, err
	} else if exists {
		return ports.WorkspaceInfo{}, fmt.Errorf("scratch workspace: path %q already contains files: %w", path, ports.ErrWorkspaceDirty)
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return ports.WorkspaceInfo{}, fmt.Errorf("scratch workspace: create %q: %w", path, err)
	}
	return ports.WorkspaceInfo{Path: path, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, nil
}

// Restore ensures the preserved scratch directory exists and returns it.
func (w *Workspace) Restore(_ context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	path, err := w.restorePath(cfg)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return ports.WorkspaceInfo{}, fmt.Errorf("scratch workspace: restore %q: %w", path, err)
	}
	return ports.WorkspaceInfo{Path: path, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, nil
}

// Destroy removes only an empty scratch directory. Non-empty directories are
// treated as dirty user work and preserved.
func (w *Workspace) Destroy(_ context.Context, info ports.WorkspaceInfo) error {
	path, err := w.validateManagedPath(info.Path)
	if err != nil {
		return err
	}
	nonEmpty, err := pathExistsNonEmpty(path)
	if err != nil {
		return err
	}
	if nonEmpty {
		return fmt.Errorf("scratch workspace: preserve non-empty %q: %w", path, ports.ErrWorkspaceDirty)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("scratch workspace: remove empty %q: %w", path, err)
	}
	return nil
}

// ForceDestroy intentionally follows Destroy semantics for scratch. Normal AO
// paths must never force-delete non-empty scratch work.
func (w *Workspace) ForceDestroy(ctx context.Context, info ports.WorkspaceInfo) error {
	return w.Destroy(ctx, info)
}

// StashUncommitted is a no-op for scratch because there is no git object store.
func (w *Workspace) StashUncommitted(context.Context, ports.WorkspaceInfo) (string, error) {
	return "", nil
}

// ApplyPreserved is a no-op unless a caller incorrectly supplies a git ref.
func (w *Workspace) ApplyPreserved(_ context.Context, _ ports.WorkspaceInfo, ref string) error {
	if strings.TrimSpace(ref) != "" {
		return errors.New("scratch workspace: preserved refs are not supported")
	}
	return nil
}

// AddExclude is a no-op for scratch because scratch workspaces are plain
// directories with no git status or commit surface.
func (w *Workspace) AddExclude(context.Context, ports.WorkspaceInfo, ...string) error {
	return nil
}

// ObserveWorkspace validates the managed path and returns a path-only
// observation. Scratch projects intentionally have no fabricated Git facts.
func (w *Workspace) ObserveWorkspace(_ context.Context, info ports.WorkspaceInfo) (ports.WorkspaceObservation, error) {
	path, err := w.validateManagedPath(info.Path)
	if err != nil {
		return ports.WorkspaceObservation{}, err
	}
	return ports.WorkspaceObservation{Path: path}, nil
}

func (w *Workspace) managedPath(cfg ports.WorkspaceConfig) (string, error) {
	if cfg.ProjectID == "" {
		return "", errors.New("scratch workspace: project id is required")
	}
	if cfg.SessionID == "" {
		return "", errors.New("scratch workspace: session id is required")
	}
	if err := validatePathComponent("project id", string(cfg.ProjectID)); err != nil {
		return "", err
	}
	if err := validatePathComponent("session id", string(cfg.SessionID)); err != nil {
		return "", err
	}
	roleDir := "workers"
	if cfg.Kind == domain.KindOrchestrator {
		roleDir = "orchestrators"
	}
	return w.validateManagedPath(filepath.Join(w.managedRoot, string(cfg.ProjectID), roleDir, string(cfg.SessionID)))
}

func (w *Workspace) restorePath(cfg ports.WorkspaceConfig) (string, error) {
	if strings.TrimSpace(cfg.Path) != "" {
		return w.validateManagedPath(filepath.Clean(cfg.Path))
	}
	return w.managedPath(cfg)
}

func validatePathComponent(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("scratch workspace: %s is required", name)
	}
	if strings.ContainsAny(value, `/\`) || value == "." || value == ".." {
		return fmt.Errorf("scratch workspace: %s %q must not contain path separators or traversal components", name, value)
	}
	return nil
}

func (w *Workspace) validateManagedPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("scratch workspace: empty path")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("scratch workspace: %q is not absolute", path)
	}
	clean := filepath.Clean(path)
	physical, err := physicalAbs(clean)
	if err != nil {
		return "", fmt.Errorf("scratch workspace: resolve path %q: %w", path, err)
	}
	inside, err := pathWithin(w.managedRoot, physical)
	if err != nil {
		return "", err
	}
	if !inside || samePath(physical, w.managedRoot) {
		return "", fmt.Errorf("scratch workspace: path %q is outside managed root %q", physical, w.managedRoot)
	}
	return physical, nil
}

func pathExistsNonEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err == nil {
		return len(entries) > 0, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("scratch workspace: inspect %q: %w", path, err)
}

func physicalAbs(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	parent := filepath.Dir(abs)
	base := filepath.Base(abs)
	for parent != "." && parent != string(os.PathSeparator) {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(resolved, base), nil
		}
		base = filepath.Join(filepath.Base(parent), base)
		parent = filepath.Dir(parent)
	}
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(resolved, base), nil
	}
	return abs, nil
}

func pathWithin(root, path string) (bool, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, fmt.Errorf("scratch workspace: compare paths: %w", err)
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))), nil
}

func samePath(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	return a == b
}
