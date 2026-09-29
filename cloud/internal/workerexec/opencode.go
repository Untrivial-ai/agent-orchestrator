package workerexec

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

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

// opencode's launch + prompt-config business logic lives in the cloud module
// (not backend/agentruntime) so the cloud worker stays self-contained and can be
// built/deployed without publishing a new backend release. It uses only existing
// agentruntime types (PermissionPolicy), never a new backend API.
//
// opencode has no CLI flag for a system prompt, so AO writes an OPENCODE_CONFIG
// document that defines a primary "AO agent" carrying the prompt and selects it
// with --agent. Mirrors the desktop opencode adapter.

func openCodeLaunchArgs(binary, sessionID, model string, providerArgs []string, policy agentruntime.PermissionPolicy, prompt string) []string {
	cmd := []string{binary}
	appendOpenCodePermissionFlags(&cmd, policy)
	cmd = append(cmd, providerArgs...)
	// opencode's -m/--model takes a provider/model id. Empty leaves opencode on
	// its configured default, so a session with no explicit model is unchanged.
	if m := strings.TrimSpace(model); m != "" {
		cmd = append(cmd, "--model", m)
	}
	cmd = append(cmd, "--agent", openCodeAgentName(sessionID))
	if prompt != "" {
		cmd = append(cmd, "--prompt", prompt)
	}
	return cmd
}

func openCodeRestoreArgs(binary, sessionID, model string, providerArgs []string, policy agentruntime.PermissionPolicy, prompt, identity string) []string {
	cmd := openCodeLaunchArgs(binary, sessionID, model, providerArgs, policy, "")
	cmd = append(cmd, "--session", identity)
	if prompt != "" {
		cmd = append(cmd, "--prompt", prompt)
	}
	return cmd
}

// appendOpenCodePermissionFlags maps AO policy onto opencode's own flags: auto ->
// --auto, bypass -> --dangerously-skip-permissions (paired with an agent-level
// "allow" rule in the config); accept-edits and default carry no flag.
func appendOpenCodePermissionFlags(cmd *[]string, policy agentruntime.PermissionPolicy) {
	switch agentruntime.NormalizePermissionPolicy(policy) {
	case agentruntime.PermissionAuto:
		*cmd = append(*cmd, "--auto")
	case agentruntime.PermissionBypassPermissions:
		*cmd = append(*cmd, "--dangerously-skip-permissions")
	}
}

// openCodeAgentName is the AO agent's name inside the per-session OPENCODE_CONFIG.
// The launch argv (--agent) and writeOpenCodeConfig must agree, so both derive it
// here. It is intentionally a short, stable constant: opencode renders the agent
// name in its TUI status bar, and the previous per-session id ("ao-<uuid>")
// overflowed there, crowding out the mode ("...a1e1auto"). There is exactly one
// AO agent per session config, so the name only has to be unambiguous within that
// file — a constant is, and it renders cleanly.
func openCodeAgentName(string) string { return "ao" }

type openCodeInlineConfig struct {
	Schema     string                           `json:"$schema,omitempty"`
	Permission map[string]string                `json:"permission,omitempty"`
	Agent      map[string]openCodeAgentSettings `json:"agent,omitempty"`
}

type openCodeAgentSettings struct {
	Mode       string `json:"mode,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	Permission any    `json:"permission,omitempty"`
	// Model pins the agent's model. The agent config is opencode's authoritative
	// layer, so setting it here makes the TUI adopt the selected model instead of
	// its persisted/default one (which the launch --model flag alone does not
	// override in the interactive TUI). Empty leaves the harness default.
	Model string `json:"model,omitempty"`
}

// writeOpenCodeConfig writes the OPENCODE_CONFIG document beside the system-prompt
// file: an AO agent that carries the prompt (via opencode's {file:./...} include)
// plus the permission overlay for the mode. Returns the config path to export as
// OPENCODE_CONFIG, or "" when there is no system prompt to inject.
func writeOpenCodeConfig(promptFile string, policy agentruntime.PermissionPolicy, sessionID, model string) (string, error) {
	if strings.TrimSpace(promptFile) == "" {
		return "", nil
	}
	var permission map[string]string
	var agentPermission any
	switch agentruntime.NormalizePermissionPolicy(policy) {
	case agentruntime.PermissionAcceptEdits:
		permission = map[string]string{"edit": "allow"}
	case agentruntime.PermissionBypassPermissions:
		agentPermission = "allow"
	}
	dir := filepath.Dir(promptFile)
	configPath := filepath.Join(dir, "opencode.json")
	config := openCodeInlineConfig{
		Schema:     "https://opencode.ai/config.json",
		Permission: permission,
		Agent: map[string]openCodeAgentSettings{
			openCodeAgentName(sessionID): {
				Mode:       "primary",
				Prompt:     "{file:./" + filepath.Base(promptFile) + "}",
				Permission: agentPermission,
				Model:      strings.TrimSpace(model),
			},
		},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create opencode config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".ao-opencode-config-*")
	if err != nil {
		return "", fmt.Errorf("create opencode config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write opencode config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("secure opencode config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close opencode config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return "", fmt.Errorf("replace opencode config: %w", err)
	}
	return configPath, nil
}
