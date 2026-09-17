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

type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

func New() *Plugin { return &Plugin{} }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.SubmitActivitySignaler = (*Plugin)(nil)
var _ ports.BlockedActivitySignaler = (*Plugin)(nil)
var _ ports.StartupInputReadinessSignaler = (*Plugin)(nil)

func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{ID: adapterID, Name: "Qoder", Description: "Run Qoder worker and orchestrator sessions.", Version: "0.0.1", Capabilities: []adapters.Capability{adapters.CapabilityAgent}}
}

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

func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryInCommand, nil
}

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
	defer f.Close()
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

func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

func (p *Plugin) EmitsSubmitActivity() bool         { return true }
func (p *Plugin) EmitsBlockedActivity() bool        { return true }
func (p *Plugin) FirstSignalProvesInputReady() bool { return true }

var qoderBinarySpec = binaryutil.BinarySpec{
	Label: "qoder", Names: []string{"qoder"}, WinNames: []string{"qoder.cmd", "qoder.exe", "qoder"},
	UnixPaths:     []string{"/usr/local/bin/qoder", "/opt/homebrew/bin/qoder"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("qoder"), NodeManaged: true,
	WinPaths: []binaryutil.WinPath{{Base: binaryutil.WinAppData, Parts: []string{"npm", "qoder.cmd"}}, {Base: binaryutil.WinAppData, Parts: []string{"npm", "qoder.exe"}}},
}

func ResolveQoderBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, qoderBinarySpec)
}
