// Package agentruntime contains the provider-specific process mechanics shared
// by AO's desktop adapters and remote Linux workers.
package agentruntime

import (
	"errors"
	"fmt"
	"strings"
)

// Harness identifies a supported coding-agent CLI.
type Harness string

// Supported coding-agent harnesses.
const (
	HarnessOpenCode Harness = "opencode"
)

// PermissionPolicy is AO's provider-neutral approval policy.
type PermissionPolicy string

// Provider-neutral permission policies.
const (
	PermissionDefault           PermissionPolicy = "default"
	PermissionAcceptEdits       PermissionPolicy = "accept-edits"
	PermissionAuto              PermissionPolicy = "auto"
	PermissionBypassPermissions PermissionPolicy = "bypass-permissions"
)

// SessionMode is the durable Cloud execution mode.
type SessionMode string

// Durable Cloud execution modes.
const (
	SessionModeReadOnly SessionMode = "read-only"
	SessionModeStandard SessionMode = "standard"
	SessionModeTrusted  SessionMode = "trusted"
)

// MetadataKeyAgentSessionID is the durable metadata key containing the native
// provider conversation identity.
const MetadataKeyAgentSessionID = "agentSessionId"

// LaunchConfig contains the inputs common to a fresh provider process.
type LaunchConfig struct {
	Harness          Harness
	Binary           string
	SessionID        string
	NativeSessionID  string
	WorkspacePath    string
	Model            string
	Prompt           string
	SystemPrompt     string
	SystemPromptFile string
	Permission       PermissionPolicy
	AllowedTools     []string
	DisallowedTools  []string
	// ProviderArgs are trusted host-owned flags inserted before model and
	// prompt arguments.
	ProviderArgs []string
}

// RestoreConfig contains the inputs needed to resume a native conversation.
type RestoreConfig struct {
	Harness          Harness
	Binary           string
	SessionID        string
	Metadata         map[string]string
	WorkspacePath    string
	Model            string
	Prompt           string
	SystemPrompt     string
	SystemPromptFile string
	Permission       PermissionPolicy
	AllowedTools     []string
	DisallowedTools  []string
	ProviderArgs     []string
}

// BuildLaunchCommand returns argv for a fresh interactive agent process.
func BuildLaunchCommand(cfg LaunchConfig) ([]string, error) {
	if strings.TrimSpace(cfg.Binary) == "" {
		return nil, errors.New("agentruntime: binary is required")
	}
	switch cfg.Harness {
	case HarnessOpenCode:
		return buildOpenCodeLaunch(cfg), nil
	default:
		return nil, fmt.Errorf("agentruntime: unsupported harness %q", cfg.Harness)
	}
}

// BuildRestoreCommand returns argv for a resumed native conversation. ok is
// false when the supplied metadata cannot identify a conversation to resume.
func BuildRestoreCommand(cfg RestoreConfig) ([]string, bool, error) {
	if strings.TrimSpace(cfg.Binary) == "" {
		return nil, false, errors.New("agentruntime: binary is required")
	}
	identity, ok := RestoreIdentity(cfg.Harness, cfg.SessionID, cfg.Metadata)
	if !ok {
		return nil, false, nil
	}
	switch cfg.Harness {
	case HarnessOpenCode:
		return buildOpenCodeRestore(cfg, identity), true, nil
	default:
		return nil, false, fmt.Errorf("agentruntime: unsupported harness %q", cfg.Harness)
	}
}

// RestoreIdentity resolves the native provider identity used by a restore.
// opencode resumes only through the plugin-captured session id; there is no
// deterministic derivation from the AO session id.
func RestoreIdentity(harness Harness, sessionID string, metadata map[string]string) (string, bool) {
	if identity := strings.TrimSpace(metadata[MetadataKeyAgentSessionID]); identity != "" {
		return identity, true
	}
	return "", false
}

// NormalizePermissionPolicy makes unknown persisted values defer to the
// provider's established default behavior.
func NormalizePermissionPolicy(policy PermissionPolicy) PermissionPolicy {
	switch policy {
	case PermissionDefault, PermissionAcceptEdits, PermissionAuto, PermissionBypassPermissions:
		return policy
	default:
		return PermissionDefault
	}
}

// PermissionPolicyForMode selects the native CLI approval policy for a durable
// execution mode. Read-only confinement remains the worker sandbox's job; no
// supported CLI flag alone provides filesystem containment.
func PermissionPolicyForMode(mode SessionMode) PermissionPolicy {
	switch mode {
	case SessionModeStandard:
		return PermissionAuto
	case SessionModeTrusted:
		return PermissionBypassPermissions
	default:
		return PermissionDefault
	}
}

// OpenCodePermissionArgs maps AO policy onto opencode's single approval flag.
// opencode exposes only --dangerously-skip-permissions (no graduated
// accept-edits / auto modes), so bypass-permissions requests the flag and every
// other policy defers to opencode's own permission config.
func OpenCodePermissionArgs(policy PermissionPolicy) []string {
	if NormalizePermissionPolicy(policy) == PermissionBypassPermissions {
		return []string{"--dangerously-skip-permissions"}
	}
	return nil
}

// buildOpenCodeLaunch mirrors the opencode desktop adapter's launch argv:
//
//	opencode [--dangerously-skip-permissions] [--model <model>]
//	         [--agent <ao-agent>] [--prompt <prompt>]
//
// opencode has no CLI flag to set a system prompt, so AO writes an opencode
// config that defines the generated AO agent (see OpenCodeAgentName, whose name
// the caller must match in that config) and selects it with --agent. The initial
// task prompt is delivered via --prompt (its argument, so a leading "-" is not
// read as a flag).
func buildOpenCodeLaunch(cfg LaunchConfig) []string {
	cmd := []string{cfg.Binary}
	cmd = append(cmd, OpenCodePermissionArgs(cfg.Permission)...)
	cmd = append(cmd, cfg.ProviderArgs...)
	if model := strings.TrimSpace(cfg.Model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	if cfg.SystemPrompt != "" || cfg.SystemPromptFile != "" {
		cmd = append(cmd, "--agent", OpenCodeAgentName(cfg.SessionID))
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd
}

// buildOpenCodeRestore re-applies the permission flag and AO agent selection
// before resuming an existing opencode session by its plugin-captured id.
func buildOpenCodeRestore(cfg RestoreConfig, identity string) []string {
	cmd := []string{cfg.Binary}
	cmd = append(cmd, OpenCodePermissionArgs(cfg.Permission)...)
	cmd = append(cmd, cfg.ProviderArgs...)
	if model := strings.TrimSpace(cfg.Model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	if cfg.SystemPrompt != "" || cfg.SystemPromptFile != "" {
		cmd = append(cmd, "--agent", OpenCodeAgentName(cfg.SessionID))
	}
	cmd = append(cmd, "--session", identity)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd
}

// OpenCodeAgentName derives the AO-owned agent name written into the generated
// opencode config and selected with --agent. It mirrors the desktop adapter's
// naming so a config written by one side works with a launcher on the other.
func OpenCodeAgentName(sessionID string) string {
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
