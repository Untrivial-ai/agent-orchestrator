//go:build windows

package workerlauncher

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/workeridentity"
)

const IdentityConfigEnvironment = "AO_WORKER_IDENTITY_CONFIG"

type PreparedLaunch struct{ LauncherPath, ConfigPath, ManifestPath string }

func PrepareLaunch(sessionID, cwd string, argv []string, environment []string) (PreparedLaunch, error) {
	values := environmentValues(environment)
	configPath := strings.TrimSpace(values[IdentityConfigEnvironment])
	if configPath == "" {
		return PreparedLaunch{}, errors.New("worker launcher: AO_WORKER_IDENTITY_CONFIG is required; run elevated ao-worker-launcher install")
	}
	cfg, err := workeridentity.LoadConfig(configPath)
	if err != nil {
		return PreparedLaunch{}, err
	}
	worktree, err := CanonicalContainedDirectory(cwd, cfg.WorkspaceRoot)
	if err != nil {
		return PreparedLaunch{}, fmt.Errorf("worker launcher: worktree: %w", err)
	}
	if err := ValidateWorkerRepositoryGitConfig(worktree, cfg.Executables["git"]); err != nil {
		return PreparedLaunch{}, err
	}
	if len(argv) == 0 {
		return PreparedLaunch{}, errors.New("worker launcher: executable is required")
	}
	executable, err := CanonicalExecutable(argv[0])
	if err != nil {
		return PreparedLaunch{}, err
	}
	executableID := ""
	for _, id := range []string{ExecutableAOAgent, ExecutableClaude, ExecutableCodex, ExecutableShell} {
		if configured := cfg.Executables[id]; configured != "" && samePath(executable, configured) {
			executableID = id
			break
		}
	}
	if executableID == "" {
		return PreparedLaunch{}, errors.New("worker launcher: arbitrary executable rejected")
	}
	profile := filepath.Join(cfg.SessionProfileRoot, sessionID)
	if err := os.MkdirAll(filepath.Join(profile, "Temp"), 0o700); err != nil {
		return PreparedLaunch{}, err
	}
	if err := workeridentity.SecureHostTree(profile); err != nil {
		return PreparedLaunch{}, fmt.Errorf("worker launcher: secure profile ACL: %w", err)
	}
	if err := workeridentity.SecureHostTree(worktree); err != nil {
		return PreparedLaunch{}, fmt.Errorf("worker launcher: secure worktree ACL: %w", err)
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return PreparedLaunch{}, err
	}
	now := time.Now().UTC()
	allowedKeys := []string{}
	allowed := AllowedEnvironmentKeys()
	for key := range values {
		if _, ok := allowed[key]; ok {
			allowedKeys = append(allowedKeys, key)
		}
	}
	sort.Strings(allowedKeys)
	projectID := values["AO_PROJECT_ID"]
	if projectID == "" {
		projectID = "unknown-project"
	}
	manifest := Manifest{Version: ManifestVersion, SessionID: sessionID, ProjectID: projectID,
		CanonicalWorktree: worktree, WorkerProfileRoot: profile, ExecutableID: executableID,
		Argv: append([]string(nil), argv[1:]...), Cwd: worktree, AllowedEnvironmentKeys: allowedKeys,
		TerminalRows: 24, TerminalCols: 80, CreatedAt: now, ExpiresAt: now.Add(time.Minute),
		Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
	if err := manifest.Validate(ValidationConfig{WorkspaceRoot: cfg.WorkspaceRoot, SessionProfileRoot: cfg.SessionProfileRoot,
		Executables: cfg.Executables, AllowedEnvironment: allowed, Now: now}); err != nil {
		return PreparedLaunch{}, err
	}
	if err := validateAOAgentCommand(manifest, cfg); err != nil {
		return PreparedLaunch{}, err
	}
	manifestPath := filepath.Join(profile, "launch-manifest-"+manifest.Nonce+".json")
	data, err := json.Marshal(manifest)
	if err != nil {
		return PreparedLaunch{}, err
	}
	f, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return PreparedLaunch{}, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(manifestPath)
		return PreparedLaunch{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(manifestPath)
		return PreparedLaunch{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(manifestPath)
		return PreparedLaunch{}, err
	}
	if err := workeridentity.SecureHostTree(manifestPath); err != nil {
		os.Remove(manifestPath)
		return PreparedLaunch{}, err
	}
	return PreparedLaunch{LauncherPath: cfg.LauncherPath, ConfigPath: configPath, ManifestPath: manifestPath}, nil
}

func secureLinkedGitMetadata(worktree string, cfg workeridentity.Config, logonSID string) error {
	dotGit := filepath.Join(worktree, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return nil
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return err
	}
	line := strings.TrimSpace(string(data))
	value, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return errors.New("worker launcher: invalid linked-worktree .git file")
	}
	gitDir := strings.TrimSpace(value)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktree, gitDir)
	}
	gitDir, err = filepath.Abs(filepath.Clean(gitDir))
	if err != nil {
		return err
	}
	commonDir := gitDir
	if commonData, readErr := os.ReadFile(filepath.Join(gitDir, "commondir")); readErr == nil {
		commonDir = strings.TrimSpace(string(commonData))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		commonDir, err = filepath.Abs(filepath.Clean(commonDir))
		if err != nil {
			return err
		}
	}
	trusted := false
	for _, root := range cfg.GitMetadataRoots {
		if _, containErr := CanonicalContainedDirectory(commonDir, root); containErr == nil {
			trusted = true
			break
		}
	}
	if !trusted {
		return errors.New("worker launcher: linked worktree Git metadata is outside configured trusted roots")
	}
	if err := workeridentity.SecureSessionTreeForLogon(gitDir, logonSID, true); err != nil {
		return fmt.Errorf("worker launcher: secure worktree Git metadata: %w", err)
	}
	if !samePath(gitDir, commonDir) {
		if err := workeridentity.SecureSessionTreeForLogon(commonDir, logonSID, true); err != nil {
			return fmt.Errorf("worker launcher: secure common Git metadata: %w", err)
		}
	}
	return nil
}

func environmentValues(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			out[strings.ToUpper(key)] = value
		}
	}
	return out
}
