package workerexec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

// opencode's launch + prompt-config business logic lives in the cloud module
// (not backend/agentruntime) so the cloud worker stays self-contained and can be
// built/deployed without publishing a new backend release. It uses only existing
// agentruntime types (PermissionPolicy), never a new backend API.
//
// opencode has no CLI flag for a system prompt, so AO writes an OPENCODE_CONFIG
// document that defines a primary "AO agent" carrying the prompt and selects it
// with --agent. Mirrors the desktop opencode adapter.

func openCodeLaunchArgs(binary, sessionID string, providerArgs []string, policy agentruntime.PermissionPolicy, prompt string) []string {
	cmd := []string{binary}
	appendOpenCodePermissionFlags(&cmd, policy)
	cmd = append(cmd, providerArgs...)
	cmd = append(cmd, "--agent", openCodeAgentName(sessionID))
	if prompt != "" {
		cmd = append(cmd, "--prompt", prompt)
	}
	return cmd
}

func openCodeRestoreArgs(binary, sessionID string, providerArgs []string, policy agentruntime.PermissionPolicy, prompt, identity string) []string {
	cmd := openCodeLaunchArgs(binary, sessionID, providerArgs, policy, "")
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

// openCodeAgentName derives a deterministic, filesystem-safe opencode agent name
// from the AO session id. The launch argv (--agent) and writeOpenCodeConfig must
// agree, so both derive it here (kept identical to the desktop opencode adapter).
func openCodeAgentName(sessionID string) string {
	const fallback = "ao-system-prompt"
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return fallback
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-',
			r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" {
		return fallback
	}
	return "ao-" + name
}

type openCodeInlineConfig struct {
	Schema     string                           `json:"$schema,omitempty"`
	Permission map[string]string                `json:"permission,omitempty"`
	Agent      map[string]openCodeAgentSettings `json:"agent,omitempty"`
}

type openCodeAgentSettings struct {
	Mode       string `json:"mode,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	Permission any    `json:"permission,omitempty"`
}

// writeOpenCodeConfig writes the OPENCODE_CONFIG document beside the system-prompt
// file: an AO agent that carries the prompt (via opencode's {file:./...} include)
// plus the permission overlay for the mode. Returns the config path to export as
// OPENCODE_CONFIG, or "" when there is no system prompt to inject.
func writeOpenCodeConfig(promptFile string, policy agentruntime.PermissionPolicy, sessionID string) (string, error) {
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
