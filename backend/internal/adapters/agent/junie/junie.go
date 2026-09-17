package junie

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type Plugin struct {
	agentbase.Base
	builder        RuntimeFileBuilder
	binaryMu       sync.Mutex
	resolvedBinary string
}

func New() *Plugin { return &Plugin{builder: NewRuntimeFileBuilder()} }
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{ID: "junie", Name: "Junie", Description: "Run JetBrains Junie worker sessions.", Version: "0.0.1", Capabilities: []adapters.Capability{adapters.CapabilityAgent}}
}
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{Fields: []ports.ConfigField{{Key: "model", Type: ports.ConfigFieldString, Description: "Junie model override."}, {Key: "effort", Type: ports.ConfigFieldEnum, Enum: []string{"low", "medium", "high"}, Description: "Junie reasoning effort."}}}, nil
}
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	return p.command(ctx, cfg.DataDir, cfg.SessionID, cfg.SystemPrompt, cfg.SystemPromptFile, cfg.Config, cfg.Permissions, "", cfg.Prompt, false)
}
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	id := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if id == "" {
		return nil, false, nil
	}
	if !validNativeID(id) {
		return nil, false, fmt.Errorf("invalid Junie native session ID")
	}
	cmd, err := p.command(ctx, cfg.DataDir, cfg.Session.ID, cfg.SystemPrompt, cfg.SystemPromptFile, cfg.Config, cfg.Permissions, id, cfg.Prompt, true)
	return cmd, err == nil, err
}
func (p *Plugin) command(ctx context.Context, dataDir, sessionID, prompt, promptFile string, cfg ports.AgentConfig, permissions ports.PermissionMode, nativeID, turn string, resume bool) ([]string, error) {
	bin, err := p.junieBinary(ctx)
	if err != nil {
		return nil, err
	}
	files, err := p.builder.Prepare(ctx, RuntimeFileRequest{DataDir: dataDir, SessionID: sessionID, SystemPrompt: prompt, SystemPromptFile: promptFile})
	if err != nil {
		return nil, err
	}
	cmd := []string{bin, "--skip-update-check", "--config-location", files.ConfigPath, "--guidelines-filename", files.GuidelinesPath}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		cmd = append(cmd, "--model", model)
	}
	if effort := strings.TrimSpace(cfg.Effort); effort != "" {
		if effort != "low" && effort != "medium" && effort != "high" {
			return nil, ports.ErrUnsupportedEffort
		}
		cmd = append(cmd, "--effort", effort)
	}
	if ports.NormalizePermissionMode(permissions) == ports.PermissionModeBypassPermissions {
		cmd = append(cmd, "--brave")
	}
	if resume {
		cmd = append(cmd, "--resume", "--session-id", nativeID)
	}
	if turn != "" {
		cmd = append(cmd, "--prompt", turn)
	}
	return cmd, nil
}

var nativeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

func validNativeID(id string) bool { return nativeIDPattern.MatchString(id) }
func (p *Plugin) SessionInfo(ctx context.Context, s ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	i, ok := agentbase.StandardSessionInfo(s)
	return i, ok, nil
}
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	_, err := p.builder.Prepare(ctx, RuntimeFileRequest{DataDir: cfg.DataDir, SessionID: cfg.SessionID, SystemPrompt: cfg.SystemPrompt, SystemPromptFile: cfg.SystemPromptFile})
	return err
}
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) { return p.junieBinary(ctx) }
func ResolveJunieBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, junieBinarySpec())
}
func (p *Plugin) junieBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	b, err := ResolveJunieBinary(ctx)
	if err == nil {
		p.resolvedBinary = b
	}
	return b, err
}
func junieBinarySpec() binaryutil.BinarySpec {
	return binaryutil.BinarySpec{Label: "junie", Names: []string{"junie"}, WinNames: []string{"junie.exe", "junie.cmd", "junie"}, UnixHomePaths: [][]string{{".local", "bin", "junie"}}, WinPaths: []binaryutil.WinPath{{Base: binaryutil.WinLocalAppData, Parts: []string{"Junie", "bin", "junie.exe"}}, {Base: binaryutil.WinAppData, Parts: []string{"npm", "junie.cmd"}}, {Base: binaryutil.WinHome, Parts: []string{".local", "bin", "junie.exe"}}}}
}

var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
