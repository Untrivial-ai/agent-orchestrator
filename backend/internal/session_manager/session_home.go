package sessionmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
)

const (
	// EnvSessionHomeMode is a daemon/process-level escape hatch for users who
	// intentionally want AO sessions to inherit globally installed skills/MCP
	// configuration. The default is isolated because AO-owned operations must not
	// be hijacked by sibling supervisor user config.
	EnvSessionHomeMode = "AO_SESSION_HOME_MODE"

	sessionHomeModeIsolated = "isolated"
	sessionHomeModeInherit  = "inherit"
	sessionHomeModeAuto     = "auto"
)

type sessionHomeSpec struct {
	Root         string
	ConfigHome   string
	DataHome     string
	StateHome    string
	CacheHome    string
	AppData      string
	LocalAppData string
	SkillDir     string
}

func shouldIsolateSessionHome(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(EnvSessionHomeMode))) {
	case "", sessionHomeModeIsolated, sessionHomeModeAuto:
		return true
	case sessionHomeModeInherit:
		return false
	default:
		// Invalid values fail closed. The var is an expert escape hatch; typoing it
		// should not silently re-expose global skills/MCP config.
		return true
	}
}

func sessionHomeDataDirReady(dataDir string) bool {
	if strings.TrimSpace(dataDir) == "" || !filepath.IsAbs(dataDir) {
		return false
	}
	info, err := os.Stat(dataDir)
	return err == nil && info.IsDir()
}

func sessionHomeRoot(dataDir string, id domain.SessionID) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", fmt.Errorf("session home: AO data dir is required")
	}
	if strings.TrimSpace(string(id)) == "" {
		return "", fmt.Errorf("session home: session id is required")
	}
	return filepath.Join(dataDir, "runtime", "session-home", string(id)), nil
}

func prepareSessionHome(dataDir string, id domain.SessionID, goos string) (sessionHomeSpec, error) {
	root, err := sessionHomeRoot(dataDir, id)
	if err != nil {
		return sessionHomeSpec{}, err
	}
	spec := sessionHomeSpec{Root: root}
	if goos == "windows" {
		spec.AppData = filepath.Join(root, "AppData", "Roaming")
		spec.LocalAppData = filepath.Join(root, "AppData", "Local")
		spec.ConfigHome = filepath.Join(spec.AppData, "xdg-config")
		spec.DataHome = filepath.Join(spec.AppData, "xdg-data")
		spec.StateHome = filepath.Join(spec.LocalAppData, "xdg-state")
		spec.CacheHome = filepath.Join(spec.LocalAppData, "xdg-cache")
	} else {
		spec.ConfigHome = filepath.Join(root, ".config")
		spec.DataHome = filepath.Join(root, ".local", "share")
		spec.StateHome = filepath.Join(root, ".local", "state")
		spec.CacheHome = filepath.Join(root, ".cache")
	}
	spec.SkillDir = filepath.Join(root, ".agents", "skills", skillassets.SkillName)
	for _, dir := range []string{
		root, spec.ConfigHome, spec.DataHome, spec.StateHome, spec.CacheHome,
		filepath.Dir(spec.SkillDir), filepath.Join(root, ".codex"),
		filepath.Join(root, ".claude"), filepath.Join(root, ".gemini"),
		filepath.Join(root, ".qwen"), spec.AppData, spec.LocalAppData,
	} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return spec, fmt.Errorf("session home: create %s: %w", dir, err)
		}
	}
	if err := skillassets.Materialize(spec.SkillDir); err != nil {
		return spec, fmt.Errorf("session home: materialize using-ao skill: %w", err)
	}
	return spec, nil
}

func applySessionHomeEnv(env map[string]string, spec sessionHomeSpec, goos string, caseInsensitive bool) {
	setProtectedEnv(env, "HOME", spec.Root, caseInsensitive)
	setProtectedEnv(env, "XDG_CONFIG_HOME", spec.ConfigHome, caseInsensitive)
	setProtectedEnv(env, "XDG_DATA_HOME", spec.DataHome, caseInsensitive)
	setProtectedEnv(env, "XDG_STATE_HOME", spec.StateHome, caseInsensitive)
	setProtectedEnv(env, "XDG_CACHE_HOME", spec.CacheHome, caseInsensitive)
	// These harnesses consult their own home overrides before HOME. Keep those
	// discovery roots inside the same session profile, even when the daemon or
	// project environment points them at a user-global installation.
	for key, dir := range map[string]string{
		"CODEX_HOME":        filepath.Join(spec.Root, ".codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(spec.Root, ".claude"),
		"GEMINI_CLI_HOME":   filepath.Join(spec.Root, ".gemini"),
		"QWEN_HOME":         filepath.Join(spec.Root, ".qwen"),
	} {
		setProtectedEnv(env, key, dir, caseInsensitive)
	}
	if goos == "windows" {
		setProtectedEnv(env, "USERPROFILE", spec.Root, caseInsensitive)
		setProtectedEnv(env, "APPDATA", spec.AppData, caseInsensitive)
		setProtectedEnv(env, "LOCALAPPDATA", spec.LocalAppData, caseInsensitive)
	}
}
