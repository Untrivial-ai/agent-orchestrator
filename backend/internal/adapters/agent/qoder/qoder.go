// Package qoder implements the Qoder CLI terminal adapter.
package qoder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	adapterID            = "qoder"
	minimumQoderVersion  = "1.1.54"
	systemPromptMaxBytes = 128 * 1024
)

// Plugin is the Qoder agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Qoder adapter.
func New() *Plugin { return &Plugin{} }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.SubmitActivitySignaler = (*Plugin)(nil)
var _ ports.BlockedActivitySignaler = (*Plugin)(nil)
var _ ports.StartupInputReadinessSignaler = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{ID: adapterID, Name: "Qoder", Description: "Run Qoder worker and orchestrator sessions.", Version: "0.0.1", Capabilities: []adapters.Capability{adapters.CapabilityAgent}}
}

// GetConfigSpec reports Qoder's optional model, reasoning-effort, and starting
// permission-mode overrides.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{Fields: []ports.ConfigField{
		{Key: "model", Type: ports.ConfigFieldString, Description: "Model or Qoder mode passed to `qoder --model`."},
		{Key: "effort", Type: ports.ConfigFieldString, Description: "Reasoning effort passed to `qoder --reasoning-effort`."},
		{Key: "permissions", Type: ports.ConfigFieldEnum, Description: "Starting permission mode.", Enum: []string{"default", "accept-edits", "auto", "bypass-permissions"}},
	}}, nil
}

// GetLaunchCommand builds the fresh-launch argv. AO assigns the conversation id
// up front via --session-id, appends the configured model, effort, permission
// mode, and tool allow/deny flags, passes standing instructions through
// --append-system-prompt, and delivers the task prompt with
// --prompt-interactive so the pane stays interactive. It fails when the caller
// supplied no native session id.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	id := strings.TrimSpace(cfg.NativeSessionID)
	if id == "" {
		return nil, errors.New("qoder: native session id is required")
	}
	bin, err := p.ResolveBinary(ctx)
	if err != nil {
		return nil, err
	}
	cmd := []string{bin, "--session-id", id}
	cmd = appendQoderConfig(cmd, cfg.Config, cfg.Permissions, cfg.AllowedTools, cfg.DisallowedTools)
	prompt, err := systemPromptText(ctx, cfg.SystemPrompt, cfg.SystemPromptFile)
	if err != nil {
		return nil, err
	}
	if prompt != "" {
		cmd = append(cmd, "--append-system-prompt", prompt)
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt-interactive", cfg.Prompt)
	}
	return cmd, nil
}

// GetPromptDeliveryStrategy reports that Qoder takes the task prompt on the
// command line, so AO does not type it into the pane after start.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryInCommand, nil
}

// GetRestoreCommand rebuilds the argv for resuming an existing conversation
// with `qoder [config flags] --resume <agentSessionId>`. ok is false when the
// hook-derived native session id has not landed yet, so callers fall back to
// fresh launch behavior.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	id := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if id == "" {
		return nil, false, nil
	}
	bin, err := p.ResolveBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	cmd := appendQoderConfig([]string{bin}, cfg.Config, cfg.Permissions, cfg.AllowedTools, cfg.DisallowedTools)
	prompt, err := systemPromptText(ctx, cfg.SystemPrompt, cfg.SystemPromptFile)
	if err != nil {
		return nil, false, err
	}
	if prompt != "" {
		cmd = append(cmd, "--append-system-prompt", prompt)
	}
	cmd = append(cmd, "--resume", id)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt-interactive", cfg.Prompt)
	}
	return cmd, true, nil
}

func appendQoderConfig(cmd []string, cfg ports.AgentConfig, permission ports.PermissionMode, allowed, denied []string) []string {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	if effort := strings.TrimSpace(cfg.Effort); effort != "" {
		cmd = append(cmd, "--reasoning-effort", effort)
	}
	if permission == "" {
		permission = cfg.Permissions
	}
	switch ports.NormalizePermissionMode(permission) {
	case ports.PermissionModeAcceptEdits:
		cmd = append(cmd, "--permission-mode", "accept_edits")
	case ports.PermissionModeAuto:
		cmd = append(cmd, "--permission-mode", "auto")
	case ports.PermissionModeBypassPermissions:
		cmd = append(cmd, "--permission-mode", "bypass_permissions")
	}
	for _, tool := range allowed {
		if tool = strings.TrimSpace(tool); tool != "" {
			cmd = append(cmd, "--allowed-tools", tool)
		}
	}
	for _, tool := range denied {
		if tool = strings.TrimSpace(tool); tool != "" {
			cmd = append(cmd, "--disallowed-tools", tool)
		}
	}
	return cmd
}

func systemPromptText(ctx context.Context, inline, path string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if path == "" {
		return "", ctx.Err()
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("qoder: read system prompt file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("qoder: system prompt file is not regular")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("qoder: read system prompt file: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, systemPromptMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("qoder: read system prompt file: %w", err)
	}
	if len(data) > systemPromptMaxBytes {
		return "", fmt.Errorf("qoder: system prompt exceeds %d bytes", systemPromptMaxBytes)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(data), nil
}

// SessionInfo surfaces Qoder hook-derived metadata. Metadata is intentionally
// nil for Qoder: callers get the normalized fields directly, matching the Codex
// adapter.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// EmitsSubmitActivity signals Qoder fires a user-prompt-submit hook under AO's
// launch, so Activity.State can flip to active once a prompt is accepted. See
// ports.SubmitActivitySignaler.
func (p *Plugin) EmitsSubmitActivity() bool { return true }

// EmitsBlockedActivity signals Qoder fires a permission-request hook, so AO can
// report a session as blocked on an approval instead of guessing from pane
// output. See ports.BlockedActivitySignaler.
func (p *Plugin) EmitsBlockedActivity() bool { return true }

// FirstSignalProvesInputReady opts Qoder into gating pane writes on its first
// lifecycle hook: Qoder's SessionStart hook cannot fire before its own startup
// dialogs have cleared, so before that signal pane input may be swallowed by a
// dialog instead of reaching the composer. See
// ports.StartupInputReadinessSignaler.
func (p *Plugin) FirstSignalProvesInputReady() bool { return true }

var qoderBinarySpec = binaryutil.BinarySpec{
	Label: "qoder", Names: []string{"qoder"}, WinNames: []string{"qoder.cmd", "qoder.exe", "qoder"},
	UnixPaths:     []string{"/usr/local/bin/qoder", "/opt/homebrew/bin/qoder"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("qoder"), NodeManaged: true,
	WinPaths: []binaryutil.WinPath{{Base: binaryutil.WinAppData, Parts: []string{"npm", "qoder.cmd"}}, {Base: binaryutil.WinAppData, Parts: []string{"npm", "qoder.exe"}}},
}

// ResolveQoderBinary returns the path to the qoder binary on this machine,
// searching PATH then well-known npm and Homebrew install locations. It returns
// a wrapped ports.ErrAgentBinaryNotFound when qoder is absent.
func ResolveQoderBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, qoderBinarySpec)
}
