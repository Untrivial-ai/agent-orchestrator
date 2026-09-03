package process

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const workerHomeEnv = "AO_WORKER_HOME"

var workerCredentialEnvironment = map[string]struct{}{
	"CI_JOB_TOKEN": {}, "GH_ENTERPRISE_TOKEN": {}, "GH_TOKEN": {}, "GITHUB_TOKEN": {},
	"AO_GITHUB_TOKEN": {}, "AO_GITLAB_TOKEN": {}, "AO_GITLAB_HOST_TOKENS": {}, "COPILOT_GITHUB_TOKEN": {},
	"GITLAB_TOKEN": {}, "GIT_ASKPASS": {}, "GIT_SSH": {}, "GIT_SSH_COMMAND": {},
	"GLAB_TOKEN": {}, "SSH_AGENT_PID": {}, "SSH_ASKPASS": {}, "SSH_AUTH_SOCK": {},
	"AO_GIT_BROKER_TOKEN": {}, "AO_GIT_BROKER_SECRET": {},
}

// WorkerEnvironment builds the environment for an agent-owned process. A
// runtime without AO_SESSION_ID is a user shell or an AO infrastructure child
// and stays on the ordinary merge path.
//
// Workers receive an isolated profile and Git configuration. They retain normal
// toolchain variables and provider-specific model credentials, but not host SCM
// tokens, SSH agents, askpass programs, gh/glab profiles, Git system config, or
// the host credential-helper chain. Terminal and native Chat workers share this
// choke point.
func WorkerEnvironment(parent []string, overlay map[string]string) []string {
	merged := environmentMap(parent)
	sessionID := environmentValue(merged, "AO_SESSION_ID")
	if value := strings.TrimSpace(overlay["AO_SESSION_ID"]); value != "" {
		sessionID = value
	}
	if sessionID == "" {
		for key, value := range overlay {
			setEnvironment(merged, key, value)
		}
		return sortedEnvironment(merged)
	}
	// Provider variables from the daemon environment are never a Worker
	// fallback. Only the explicit per-session overlay may restore them.
	for key := range merged {
		if isProviderEnvironmentKey(key) {
			delete(merged, key)
		}
	}
	for key, value := range overlay {
		setEnvironment(merged, key, value)
	}

	dataDir := environmentValue(merged, "AO_DATA_DIR")
	sessionID = safePathComponent(environmentValue(merged, "AO_SESSION_ID"))
	workerHome := environmentValue(merged, workerHomeEnv)
	if workerHome == "" {
		workerHome = filepath.Join(dataDir, "workers", sessionID, "home")
	}
	if workerHome == "" || workerHome == "." {
		workerHome = filepath.Join(os.TempDir(), "ao-workers", sessionID, "home")
	}

	for key := range workerCredentialEnvironment {
		deleteEnvironment(merged, key)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "GH_CONFIG_DIR", "GLAB_CONFIG_DIR"} {
		deleteEnvironment(merged, key)
	}

	setEnvironment(merged, workerHomeEnv, workerHome)
	setEnvironment(merged, "HOME", workerHome)
	setEnvironment(merged, "USERPROFILE", workerHome)
	setEnvironment(merged, "XDG_CONFIG_HOME", filepath.Join(workerHome, ".config"))
	setEnvironment(merged, "GH_CONFIG_DIR", filepath.Join(workerHome, ".config", "gh"))
	setEnvironment(merged, "GLAB_CONFIG_DIR", filepath.Join(workerHome, ".config", "glab-cli"))
	// P0-A never projects the host provider profile. Authentication and
	// transcript projection are intentionally deferred to P0-B.
	setEnvironment(merged, "CLAUDE_CONFIG_DIR", filepath.Join(workerHome, ".claude"))
	setEnvironment(merged, "CODEX_HOME", filepath.Join(workerHome, ".codex"))
	if runtime.GOOS == "windows" {
		setEnvironment(merged, "APPDATA", filepath.Join(workerHome, "AppData", "Roaming"))
		setEnvironment(merged, "LOCALAPPDATA", filepath.Join(workerHome, "AppData", "Local"))
	}

	// Ignore host system/global Git configuration and reset the helper list at
	// command scope. Local repository configuration remains available for normal
	// status/diff/add/commit/worktree operations.
	setEnvironment(merged, "GIT_CONFIG_NOSYSTEM", "1")
	setEnvironment(merged, "GIT_CONFIG_GLOBAL", os.DevNull)
	setEnvironment(merged, "GIT_CONFIG_COUNT", "1")
	setEnvironment(merged, "GIT_CONFIG_KEY_0", "credential.helper")
	setEnvironment(merged, "GIT_CONFIG_VALUE_0", "")
	setEnvironment(merged, "GIT_TERMINAL_PROMPT", "0")
	setEnvironment(merged, "GCM_INTERACTIVE", "Never")
	setEnvironment(merged, "GH_PROMPT_DISABLED", "1")
	// A deterministic local-only identity lets workers commit without reading
	// the user's global .gitconfig.
	setEnvironment(merged, "GIT_AUTHOR_NAME", "Agent Orchestrator Worker")
	setEnvironment(merged, "GIT_AUTHOR_EMAIL", "ao-worker@localhost")
	setEnvironment(merged, "GIT_COMMITTER_NAME", "Agent Orchestrator Worker")
	setEnvironment(merged, "GIT_COMMITTER_EMAIL", "ao-worker@localhost")

	return sortedEnvironment(merged)
}

func isProviderEnvironmentKey(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	return strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "OPENAI_") || strings.HasPrefix(key, "CLAUDE_CODE_")
}

type environmentEntry struct{ key, value string }

func environmentMap(entries []string) map[string]environmentEntry {
	result := make(map[string]environmentEntry, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			result[strings.ToUpper(key)] = environmentEntry{key: key, value: value}
		}
	}
	return result
}

func setEnvironment(env map[string]environmentEntry, key, value string) {
	env[strings.ToUpper(key)] = environmentEntry{key: key, value: value}
}
func deleteEnvironment(env map[string]environmentEntry, key string) {
	delete(env, strings.ToUpper(key))
}
func environmentValue(env map[string]environmentEntry, key string) string {
	return env[strings.ToUpper(key)].value
}
func firstEnvironmentValue(env map[string]environmentEntry, keys ...string) string {
	for _, key := range keys {
		if value := environmentValue(env, key); value != "" {
			return value
		}
	}
	return ""
}
func sortedEnvironment(env map[string]environmentEntry) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		out = append(out, entry.key+"="+entry.value)
	}
	sort.Strings(out)
	return out
}
func safePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, value)
}
