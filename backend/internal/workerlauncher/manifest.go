package workerlauncher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const ManifestVersion = 1

const (
	ExecutableAOAgent = "ao-agent"
	ExecutableClaude  = "claude"
	ExecutableCodex   = "codex"
	ExecutableShell   = "shell"
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	noncePattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)
)

// Manifest is the complete, non-secret launch request consumed by the worker
// side of ao-worker-launcher. Environment values are deliberately absent: the
// bootstrap copies only the named keys that also appear in its fixed allowlist.
type Manifest struct {
	Version                int       `json:"version"`
	SessionID              string    `json:"session_id"`
	ProjectID              string    `json:"project_id"`
	CanonicalWorktree      string    `json:"canonical_worktree"`
	WorkerProfileRoot      string    `json:"worker_profile_root"`
	ExecutableID           string    `json:"executable_id"`
	Argv                   []string  `json:"argv"`
	Cwd                    string    `json:"cwd"`
	AllowedEnvironmentKeys []string  `json:"allowed_environment_keys"`
	TerminalRows           uint16    `json:"terminal_rows"`
	TerminalCols           uint16    `json:"terminal_cols"`
	CreatedAt              time.Time `json:"created_at"`
	ExpiresAt              time.Time `json:"expires_at"`
	Nonce                  string    `json:"nonce"`
}

type ValidationConfig struct {
	WorkspaceRoot      string
	SessionProfileRoot string
	Executables        map[string]string
	AllowedEnvironment map[string]struct{}
	Now                time.Time
}

func (m *Manifest) Validate(cfg ValidationConfig) error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("worker manifest: unsupported version %d", m.Version)
	}
	if !identifierPattern.MatchString(m.SessionID) {
		return errors.New("worker manifest: invalid session_id")
	}
	if !identifierPattern.MatchString(m.ProjectID) {
		return errors.New("worker manifest: invalid project_id")
	}
	if !noncePattern.MatchString(m.Nonce) {
		return errors.New("worker manifest: invalid nonce")
	}
	now := cfg.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if m.CreatedAt.IsZero() || m.ExpiresAt.IsZero() || m.ExpiresAt.Before(m.CreatedAt) {
		return errors.New("worker manifest: invalid lifetime")
	}
	if m.CreatedAt.After(now.Add(30*time.Second)) || !now.Before(m.ExpiresAt) {
		return errors.New("worker manifest: expired or not yet valid")
	}
	if m.ExpiresAt.Sub(m.CreatedAt) > 2*time.Minute {
		return errors.New("worker manifest: lifetime exceeds two minutes")
	}

	worktree, err := CanonicalContainedDirectory(m.CanonicalWorktree, cfg.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("worker manifest: worktree: %w", err)
	}
	profile, err := CanonicalContainedDirectory(m.WorkerProfileRoot, cfg.SessionProfileRoot)
	if err != nil {
		return fmt.Errorf("worker manifest: profile: %w", err)
	}
	cwd, err := CanonicalContainedDirectory(m.Cwd, worktree)
	if err != nil {
		return fmt.Errorf("worker manifest: cwd: %w", err)
	}
	if !samePath(worktree, m.CanonicalWorktree) || !samePath(profile, m.WorkerProfileRoot) || !samePath(cwd, m.Cwd) {
		return errors.New("worker manifest: paths must already be canonical")
	}

	executable, ok := cfg.Executables[m.ExecutableID]
	if !ok || strings.TrimSpace(executable) == "" {
		return fmt.Errorf("worker manifest: executable_id %q is not allowed", m.ExecutableID)
	}
	if _, err := CanonicalExecutable(executable); err != nil {
		return fmt.Errorf("worker manifest: executable_id %q: %w", m.ExecutableID, err)
	}
	if len(m.Argv) > 4096 {
		return errors.New("worker manifest: too many arguments")
	}
	for _, arg := range m.Argv {
		if strings.IndexByte(arg, 0) >= 0 {
			return errors.New("worker manifest: argument contains NUL")
		}
	}

	seen := make(map[string]struct{}, len(m.AllowedEnvironmentKeys))
	for _, key := range m.AllowedEnvironmentKeys {
		upper := strings.ToUpper(strings.TrimSpace(key))
		if upper == "" || upper != key {
			return fmt.Errorf("worker manifest: environment key %q is not canonical", key)
		}
		if _, ok := cfg.AllowedEnvironment[upper]; !ok {
			return fmt.Errorf("worker manifest: environment key %q is not allowed", key)
		}
		if _, duplicate := seen[upper]; duplicate {
			return fmt.Errorf("worker manifest: duplicate environment key %q", key)
		}
		seen[upper] = struct{}{}
	}
	if m.TerminalRows == 0 || m.TerminalCols == 0 {
		return errors.New("worker manifest: terminal dimensions must be non-zero")
	}
	return nil
}

func CanonicalExecutable(path string) (string, error) {
	canonical, err := canonicalExistingPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("executable is not a regular file")
	}
	return canonical, nil
}

// CanonicalContainedDirectory resolves both paths through the operating
// system, rejects UNC/ADS and reparse aliases on Windows, and uses filepath.Rel
// rather than a string prefix for containment.
func CanonicalContainedDirectory(path, root string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return "", errors.New("path and root are required")
	}
	canonicalRoot, err := canonicalExistingPath(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize root: %w", err)
	}
	canonicalPath, err := canonicalExistingPath(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize path: %w", err)
	}
	rootInfo, err := os.Stat(canonicalRoot)
	if err != nil || !rootInfo.IsDir() {
		return "", errors.New("root is not a directory")
	}
	pathInfo, err := os.Stat(canonicalPath)
	if err != nil || !pathInfo.IsDir() {
		return "", errors.New("path is not a directory")
	}
	if !sameVolume(canonicalRoot, canonicalPath) {
		return "", errors.New("path crosses volume boundary")
	}
	rel, err := filepath.Rel(canonicalRoot, canonicalPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path is outside configured root")
	}
	return canonicalPath, nil
}

func canonicalExistingPath(path string) (string, error) {
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("path contains NUL")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(abs)
		if strings.HasPrefix(volume, `\\`) {
			return "", errors.New("UNC paths are not allowed")
		}
		rest := strings.TrimPrefix(abs, volume)
		if strings.Contains(rest, ":") {
			return "", errors.New("alternate data streams are not allowed")
		}
	}
	evaluated := abs
	if runtime.GOOS != "windows" {
		evaluated, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
	}
	final, err := platformFinalPath(evaluated)
	if err != nil {
		return "", err
	}
	// A caller must provide the already-resolved path. This rejects junction,
	// symlink, 8.3 and path-prefix aliases instead of silently blessing them.
	if !samePath(abs, final) {
		return "", errors.New("path uses a symlink, junction, short-name, or non-canonical alias")
	}
	return filepath.Clean(final), nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func sameVolume(a, b string) bool {
	av, bv := filepath.VolumeName(a), filepath.VolumeName(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(av, bv)
	}
	return av == bv
}
