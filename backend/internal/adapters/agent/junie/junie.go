// Package junie contains an experimental terminal adapter for
// JetBrains Junie. Its isolated config enables lifecycle hooks, and a non-empty
// AO guidelines file exclusively replaces Junie's normal project guidelines.
// Authenticated live conformance remains unverified; ACP Chat is not registered.
package junie

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "junie"

// Plugin is the experimental Junie terminal adapter. Hook and restore behavior
// still require live verification on the user's installed release.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
	runtimeFiles   RuntimeFileBuilder
}

// New returns the experimental Junie terminal adapter.
func New() *Plugin {
	return &Plugin{runtimeFiles: NewRuntimeFileBuilder()}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns Junie's static adapter description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Junie",
		Description: "Run experimental Junie terminal sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec declares Junie's optional model and documented effort flags.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{Fields: []ports.ConfigField{
		{Key: "model", Type: ports.ConfigFieldString, Description: "Model override passed to `junie --model`."},
		{Key: "effort", Type: ports.ConfigFieldEnum, Description: "Junie reasoning effort.", Enum: []string{"low", "medium", "high"}},
	}}, nil
}

// GetLaunchCommand builds a persistent interactive Junie launch. Any explicit
// task uses the documented --prompt flag. Standing orchestrator instructions
// come exclusively from the AO guidelines file and do not become a user turn.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := rejectToolRestrictions(cfg.AllowedTools, cfg.DisallowedTools); err != nil {
		return nil, err
	}
	effort, err := validatedEffort(cfg.Config.Effort)
	if err != nil {
		return nil, err
	}
	binary, err := p.junieBinary(ctx)
	if err != nil {
		return nil, err
	}
	files, err := p.fileBuilder().Prepare(ctx, RuntimeFileRequest{
		DataDir:          cfg.DataDir,
		SessionID:        cfg.SessionID,
		SystemPrompt:     cfg.SystemPrompt,
		SystemPromptFile: cfg.SystemPromptFile,
	})
	if err != nil {
		return nil, err
	}

	cmd := junieBaseCommand(binary, files, cfg.Config.Model, effort, cfg.Permissions)
	if cfg.Prompt != "" {
		// Keep the prompt as a flag/value pair even when its value begins with '-'.
		// Live parser proof for that case remains an admission prerequisite.
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd, nil
}

// GetPromptDeliveryStrategy reports that Junie receives a worker's initial
// prompt in argv and an orchestrator's standing instructions through argv's
// runtime guideline selection.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryInCommand, nil
}

// GetRestoreCommand resumes exactly the native session id captured by Junie's
// SessionStart hook and reapplies the isolated config and launch options.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	nativeID := cfg.Session.Metadata[ports.MetadataKeyAgentSessionID]
	if nativeID == "" {
		return nil, false, nil
	}
	if err := validateNativeSessionID(nativeID); err != nil {
		return nil, false, err
	}
	if err := rejectToolRestrictions(cfg.AllowedTools, cfg.DisallowedTools); err != nil {
		return nil, false, err
	}
	effort, err := validatedEffort(cfg.Config.Effort)
	if err != nil {
		return nil, false, err
	}
	binary, err := p.junieBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	files, err := p.fileBuilder().Prepare(ctx, RuntimeFileRequest{
		DataDir:          cfg.DataDir,
		SessionID:        cfg.Session.ID,
		SystemPrompt:     cfg.SystemPrompt,
		SystemPromptFile: cfg.SystemPromptFile,
	})
	if err != nil {
		return nil, false, err
	}

	cmd := junieBaseCommand(binary, files, cfg.Config.Model, effort, cfg.Permissions)
	cmd = append(cmd, "--resume", "--session-id", nativeID)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd, true, nil
}

// SessionInfo surfaces metadata recorded by Junie's lifecycle hooks.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

func (p *Plugin) fileBuilder() RuntimeFileBuilder {
	if p.runtimeFiles != nil {
		return p.runtimeFiles
	}
	return NewRuntimeFileBuilder()
}

func junieBaseCommand(binary string, files RuntimeFiles, model, effort string, permissions ports.PermissionMode) []string {
	cmd := []string{binary, "--skip-update-check", "--config-location", files.ConfigPath}
	if files.GuidelinesPath != "" {
		cmd = append(cmd, "--guidelines-filename", files.GuidelinesPath)
	}
	if model = strings.TrimSpace(model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	if effort != "" {
		cmd = append(cmd, "--effort", effort)
	}
	if ports.NormalizePermissionMode(permissions) == ports.PermissionModeBypassPermissions {
		cmd = append(cmd, "--brave")
	}
	return cmd
}

func validatedEffort(value string) (string, error) {
	effort := strings.TrimSpace(value)
	switch effort {
	case "", "low", "medium", "high":
		return effort, nil
	default:
		return "", fmt.Errorf("junie: invalid effort %q (want one of low, medium, high): %w", effort, ports.ErrUnsupportedEffort)
	}
}

func rejectToolRestrictions(allowed, disallowed []string) error {
	if len(allowed) != 0 || len(disallowed) != 0 {
		return errors.New("junie: tool restrictions are unsupported")
	}
	return nil
}

func validateNativeSessionID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 || !utf8.ValidString(value) || strings.HasPrefix(value, "-") {
		return errors.New("junie: invalid native session ID")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("junie: invalid native session ID")
		}
	}
	return nil
}
