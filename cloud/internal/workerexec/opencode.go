package workerexec

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

// opencode's launch/restore argv is built by backend/pkg/agentruntime
// (buildOpencodeLaunch/buildOpencodeRestore), so the cloud worker launches it
// through the same BuildLaunchCommand/BuildRestoreCommand path as claude-code,
// codex, and cursor. agentruntime returns argv only, so the two v2 side effects
// it cannot own stay here in the cloud module: the OPENCODE_CONFIG document
// (writeOpenCodeConfig) and the workspace activity plugin
// (installOpenCodeActivityPlugin). Both mirror the desktop opencode adapter
// (backend/internal/adapters/agent/opencode), which cloud cannot import directly
// because it is an internal package of a different module — the plugin source is
// therefore vendored here as an embedded asset.

// openCodeBakedModelsCache is the read-only, image-baked copy of opencode's
// models.dev catalog (see cloud/scripts/bake-coder-azure-image.sh). opencode is
// a multi-provider aggregator that downloads the whole ~5MB catalog on startup,
// so on a fresh sandbox (cold per-session HOME) that adds ~10s before the TUI
// appears — unlike claude/codex, which have no such fetch. Seeding it warms the
// cache so opencode starts fast.
const openCodeBakedModelsCache = "/opt/ao/opencode/models.json"

// seedOpenCodeModelsCache copies the baked models.dev catalog into the launch
// HOME's opencode cache when absent, so opencode reads a warm cache instead of
// fetching at startup. Best effort: any failure just leaves opencode to fetch at
// runtime (the prior behavior), so a missing baked file or old image is safe.
func seedOpenCodeModelsCache(env map[string]string) {
	src, err := os.Open(openCodeBakedModelsCache)
	if err != nil {
		return
	}
	defer func() { _ = src.Close() }()
	home := strings.TrimSpace(env["HOME"])
	if home == "" {
		home = strings.TrimSpace(os.Getenv("HOME"))
	}
	if home == "" {
		return
	}
	cacheDir := strings.TrimSpace(env["XDG_CACHE_HOME"])
	if cacheDir == "" {
		cacheDir = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(cacheDir, "opencode")
	dest := filepath.Join(dir, "models.json")
	if _, err := os.Stat(dest); err == nil {
		return // already warm (a prior launch or opencode itself wrote it)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".ao-models-*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpPath, dest)
}

// openCodeAgentName is the AO agent's name inside the per-session OPENCODE_CONFIG.
// writeOpenCodeConfig sets both the agent map key and `default_agent` from it, so
// the two agree. It is intentionally a short, stable constant: opencode renders
// the agent name in its TUI status bar, and a per-session id ("ao-<uuid>")
// overflowed there, crowding out the mode ("...a1e1auto"). There is exactly one
// AO agent per session config, so the name only has to be unambiguous within that
// file — a constant is, and it renders cleanly.
func openCodeAgentName(string) string { return "ao" }

type openCodeInlineConfig struct {
	Schema string `json:"$schema,omitempty"`
	// Model pins the session's model at the config top level. opencode v2 removed
	// the `--model` launch flag, so the override rides here (matching the desktop
	// opencode adapter). Empty leaves the harness default.
	Model      string                           `json:"model,omitempty"`
	Permission map[string]string                `json:"permission,omitempty"`
	Agent      map[string]openCodeAgentSettings `json:"agent,omitempty"`
	// DefaultAgent selects the AO-generated agent at launch. v2 removed the
	// `--agent` flag, so the generated agent must be chosen here instead.
	DefaultAgent string `json:"default_agent,omitempty"`
}

type openCodeAgentSettings struct {
	Mode   string `json:"mode,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// Either a ruleset or opencode's scalar "allow", and it outranks every config
	// layer — which is what makes bypass a true bypass.
	Permission any `json:"permission,omitempty"`
}

// writeOpenCodeConfig writes the OPENCODE_CONFIG document beside the system-prompt
// file. It defines a primary AO agent that carries the standing instructions (via
// opencode's {file:./...} include), pins the model, applies the mode's permission
// overlay, and selects that agent through `default_agent` (v2 dropped the
// `--agent` flag). Returns the config path to export as OPENCODE_CONFIG, or ""
// when there is no system prompt to inject (opencode then runs on its own config).
func writeOpenCodeConfig(promptFile string, policy agentruntime.PermissionPolicy, sessionID, model string) (string, error) {
	if strings.TrimSpace(promptFile) == "" {
		return "", nil
	}
	// Permission overlay: accept-edits allows edits (opencode has no such native
	// mode); bypass writes the agent-level "allow" rule that outranks every config
	// layer (the one mode that also passes explicit denies). auto and default carry
	// no rule here — auto rides opencode's own --auto flag (built by agentruntime).
	var permission map[string]string
	var agentPermission any
	switch agentruntime.NormalizePermissionPolicy(policy) {
	case agentruntime.PermissionAcceptEdits:
		permission = map[string]string{"edit": "allow"}
	case agentruntime.PermissionBypassPermissions:
		agentPermission = "allow"
	}
	agentName := openCodeAgentName(sessionID)
	dir := filepath.Dir(promptFile)
	configPath := filepath.Join(dir, "opencode.json")
	config := openCodeInlineConfig{
		Schema:     "https://opencode.ai/config.json",
		Model:      strings.TrimSpace(model),
		Permission: permission,
		Agent: map[string]openCodeAgentSettings{
			agentName: {
				Mode:       "primary",
				Prompt:     "{file:./" + filepath.Base(promptFile) + "}",
				Permission: agentPermission,
			},
		},
		DefaultAgent: agentName,
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := atomicWriteWorkspaceFile(configPath, data, ".ao-opencode-config-*"); err != nil {
		return "", fmt.Errorf("write opencode config: %w", err)
	}
	return configPath, nil
}

const (
	// opencode scans .opencode/plugin/ and .opencode/plugins/ for *.ts/*.js and
	// loads any it finds (verified live against opencode v2.0.19). AO writes the
	// plural plugins/ dir, matching the desktop adapter and upstream tooling.
	openCodePluginDirName  = ".opencode"
	openCodePluginSubDir   = "plugins"
	openCodePluginFileName = "ao-activity.ts"

	// openCodePluginSentinel marks the file as AO-managed, so install never
	// clobbers a user-authored plugin that happens to share the path. It must
	// appear verbatim in the embedded plugin source (asserted by a test).
	openCodePluginSentinel = "agent-orchestrator: managed opencode activity plugin"
)

// openCodePluginSource is AO's opencode activity plugin, vendored from the
// desktop adapter (backend/internal/adapters/agent/opencode/assets/ao-activity.ts)
// and embedded so it ships inside the worker binary. It is a plain
// default-exported object with no imports, so it loads even though
// @opencode/plugin is not resolvable in the sandbox. It shells
// `ao hooks opencode <event>` — the same activity bridge the other harnesses use.
//
//go:embed assets/ao-activity.ts
var openCodePluginSource string

// installOpenCodeActivityPlugin writes AO's activity plugin into the workspace's
// .opencode/plugins/ directory. Mirrors the desktop adapter's GetAgentHooks: the
// write is atomic and idempotent, and it refuses to overwrite a same-named file
// that is not AO-managed (no sentinel) rather than silently destroying it.
func installOpenCodeActivityPlugin(workspace string) error {
	if strings.TrimSpace(workspace) == "" {
		return errors.New("install opencode activity plugin: workspace is required")
	}
	pluginPath := filepath.Join(workspace, openCodePluginDirName, openCodePluginSubDir, openCodePluginFileName)
	if existing, err := os.ReadFile(pluginPath); err == nil {
		if !strings.Contains(string(existing), openCodePluginSentinel) {
			return fmt.Errorf(
				"install opencode activity plugin: refusing to overwrite non-AO file at %s",
				pluginPath,
			)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("install opencode activity plugin: stat plugin: %w", err)
	}
	if err := atomicWriteWorkspaceFile(pluginPath, []byte(openCodePluginSource), ".ao-opencode-plugin-*"); err != nil {
		return fmt.Errorf("install opencode activity plugin: %w", err)
	}
	return nil
}

// atomicWriteWorkspaceFile writes data to path via a temp file + rename, creating
// parent directories as needed and giving the file 0o600. The temp pattern keeps
// concurrent writers from colliding.
func atomicWriteWorkspaceFile(path string, data []byte, tmpPattern string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
