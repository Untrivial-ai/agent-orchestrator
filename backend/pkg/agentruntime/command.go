// Package agentruntime contains the provider-specific process mechanics shared
// by AO's desktop adapters and remote Linux workers.
package agentruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Harness identifies a supported coding-agent CLI.
type Harness string

// Supported coding-agent harnesses.
const (
	HarnessClaudeCode Harness = "claude-code"
	HarnessCodex      Harness = "codex"
	HarnessCursor     Harness = "cursor"
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

var claudeSessionNamespace = uuid.MustParse("a1f0c3d2-7b54-4e96-8a2b-0d9e1f2a3b4c")

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
	// prompt arguments. Desktop AO uses these for its Codex activity hooks;
	// workers normally leave them empty.
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
	case HarnessClaudeCode:
		return buildClaudeLaunch(cfg)
	case HarnessCodex:
		return buildCodexLaunch(cfg), nil
	case HarnessCursor:
		return buildCursorLaunch(cfg), nil
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
	case HarnessClaudeCode:
		cmd, err := buildClaudeRestore(cfg, identity)
		return cmd, err == nil, err
	case HarnessCodex:
		return buildCodexRestore(cfg, identity), true, nil
	case HarnessCursor:
		return buildCursorRestore(cfg, identity), true, nil
	default:
		return nil, false, fmt.Errorf("agentruntime: unsupported harness %q", cfg.Harness)
	}
}

// RestoreIdentity resolves the native provider identity used by a restore.
// Claude Code can deterministically recover pre-hook sessions from the AO
// session id; Codex and Cursor require their hook-captured identity.
func RestoreIdentity(harness Harness, sessionID string, metadata map[string]string) (string, bool) {
	if identity := strings.TrimSpace(metadata[MetadataKeyAgentSessionID]); identity != "" {
		return identity, true
	}
	if harness == HarnessClaudeCode && strings.TrimSpace(sessionID) != "" {
		return ClaudeSessionID(sessionID), true
	}
	return "", false
}

// ClaudeSessionID maps an AO session id to the stable UUID used by Claude
// Code's --session-id and --resume flags.
func ClaudeSessionID(sessionID string) string {
	return uuid.NewSHA1(claudeSessionNamespace, []byte(sessionID)).String()
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

// ClaudePermissionArgs maps AO policy onto Claude Code flags.
func ClaudePermissionArgs(policy PermissionPolicy) []string {
	switch NormalizePermissionPolicy(policy) {
	case PermissionAcceptEdits:
		return []string{"--permission-mode", "acceptEdits"}
	case PermissionAuto:
		return []string{"--permission-mode", "auto"}
	case PermissionBypassPermissions:
		return []string{"--permission-mode", "bypassPermissions"}
	default:
		return nil
	}
}

// CodexPermissionArgs maps AO policy onto Codex approval flags.
func CodexPermissionArgs(policy PermissionPolicy) []string {
	switch NormalizePermissionPolicy(policy) {
	case PermissionAcceptEdits:
		return []string{"--ask-for-approval", "on-request"}
	case PermissionAuto:
		return []string{"--ask-for-approval", "on-request", "-c", `approvals_reviewer="auto_review"`}
	default:
		return []string{"--dangerously-bypass-approvals-and-sandbox"}
	}
}

// CursorPermissionArgs maps AO policy onto Cursor agent flags.
func CursorPermissionArgs(policy PermissionPolicy) []string {
	switch NormalizePermissionPolicy(policy) {
	case PermissionAuto:
		return []string{"--force"}
	case PermissionBypassPermissions:
		return []string{"--yolo"}
	default:
		return nil
	}
}

func buildClaudeLaunch(cfg LaunchConfig) ([]string, error) {
	cmd := []string{cfg.Binary}
	if cfg.NativeSessionID != "" {
		nativeID, err := uuid.Parse(strings.TrimSpace(cfg.NativeSessionID))
		if err != nil {
			return nil, fmt.Errorf("claude-code: invalid native session id: %w", err)
		}
		cmd = append(cmd, "--session-id", nativeID.String())
	} else if cfg.SessionID != "" {
		cmd = append(cmd, "--session-id", ClaudeSessionID(cfg.SessionID))
	}
	cmd = append(cmd, ClaudePermissionArgs(cfg.Permission)...)
	cmd = appendClaudeToolArgs(cmd, cfg.AllowedTools, cfg.DisallowedTools)
	cmd = append(cmd, cfg.ProviderArgs...)
	if model := strings.TrimSpace(cfg.Model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	var err error
	cmd, err = appendClaudeSystemPrompt(cmd, cfg.SystemPromptFile, cfg.SystemPrompt)
	if err != nil {
		return nil, err
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}
	return cmd, nil
}

func buildClaudeRestore(cfg RestoreConfig, identity string) ([]string, error) {
	cmd := []string{cfg.Binary}
	cmd = append(cmd, ClaudePermissionArgs(cfg.Permission)...)
	cmd = appendClaudeToolArgs(cmd, cfg.AllowedTools, cfg.DisallowedTools)
	cmd = append(cmd, cfg.ProviderArgs...)
	var err error
	cmd, err = appendClaudeSystemPrompt(cmd, cfg.SystemPromptFile, cfg.SystemPrompt)
	if err != nil {
		return nil, err
	}
	cmd = append(cmd, "--resume", identity)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}
	return cmd, nil
}

func appendClaudeToolArgs(cmd, allowed, disallowed []string) []string {
	if len(allowed) > 0 {
		cmd = append(cmd, "--allowedTools", strings.Join(allowed, ","))
	}
	if len(disallowed) > 0 {
		cmd = append(cmd, "--disallowedTools", strings.Join(disallowed, ","))
	}
	return cmd
}

func appendClaudeSystemPrompt(cmd []string, promptFile, prompt string) ([]string, error) {
	if promptFile != "" {
		info, err := os.Stat(promptFile)
		if err != nil {
			return nil, fmt.Errorf("claude-code: inspect system prompt file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("claude-code: system prompt file %q is not a regular file", promptFile)
		}
		file, err := os.Open(promptFile) //nolint:gosec // path is host-owned launch config
		if err != nil {
			return nil, fmt.Errorf("claude-code: open system prompt file: %w", err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("claude-code: close system prompt file: %w", err)
		}
		return append(cmd, "--append-system-prompt-file", promptFile), nil
	}
	if prompt != "" {
		cmd = append(cmd, "--append-system-prompt", prompt)
	}
	return cmd, nil
}

func buildCodexLaunch(cfg LaunchConfig) []string {
	cmd := codexBaseCommand(cfg.Binary, cfg.Permission, cfg.ProviderArgs)
	cmd = appendCodexCommon(cmd, cfg.WorkspacePath, cfg.Model, cfg.SystemPromptFile, cfg.SystemPrompt)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}
	return cmd
}

func buildCodexRestore(cfg RestoreConfig, identity string) []string {
	cmd := append([]string{cfg.Binary, "resume"}, codexBaseArgs(cfg.Permission, cfg.ProviderArgs)...)
	cmd = appendCodexCommon(cmd, cfg.WorkspacePath, cfg.Model, cfg.SystemPromptFile, cfg.SystemPrompt)
	cmd = append(cmd, identity)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}
	return cmd
}

func codexBaseCommand(binary string, policy PermissionPolicy, providerArgs []string) []string {
	return append([]string{binary}, codexBaseArgs(policy, providerArgs)...)
}

func codexBaseArgs(policy PermissionPolicy, providerArgs []string) []string {
	args := []string{
		"-c", "check_for_update_on_startup=false",
		"-c", "notice.hide_rate_limit_model_nudge=true",
		"--dangerously-bypass-hook-trust",
	}
	args = append(args, CodexPermissionArgs(policy)...)
	return append(args, providerArgs...)
}

func appendCodexCommon(cmd []string, workspace, model, promptFile, prompt string) []string {
	cmd = append(cmd, CodexWorkspaceTrustArgs(workspace)...)
	if model = strings.TrimSpace(model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	if promptFile != "" {
		return append(cmd, "-c", "model_instructions_file="+promptFile)
	}
	if prompt != "" {
		cmd = append(cmd, "-c", "developer_instructions="+codexTOMLString(prompt))
	}
	return cmd
}

// CodexWorkspaceTrustArgs returns the invocation-scoped project trust override
// for a checkout, including its resolved path when it differs.
func CodexWorkspaceTrustArgs(workspace string) []string {
	path := strings.TrimSpace(workspace)
	if path == "" {
		return nil
	}
	keys := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		keys = append(keys, resolved)
	}
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, codexTOMLString(key)+`={trust_level="trusted"}`)
	}
	return []string{"-c", "projects={" + strings.Join(entries, ",") + "}"}
}

func codexTOMLString(value string) string {
	if !containsTOMLControl(value) && !strings.Contains(value, "'") {
		return "'" + value + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func containsTOMLControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func buildCursorLaunch(cfg LaunchConfig) []string {
	cmd := []string{cfg.Binary}
	cmd = append(cmd, CursorPermissionArgs(cfg.Permission)...)
	cmd = append(cmd, cfg.ProviderArgs...)
	if model := strings.TrimSpace(cfg.Model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}
	return cmd
}

func buildCursorRestore(cfg RestoreConfig, identity string) []string {
	cmd := []string{cfg.Binary}
	cmd = append(cmd, CursorPermissionArgs(cfg.Permission)...)
	cmd = append(cmd, cfg.ProviderArgs...)
	if model := strings.TrimSpace(cfg.Model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	return append(cmd, "--resume", identity)
}
