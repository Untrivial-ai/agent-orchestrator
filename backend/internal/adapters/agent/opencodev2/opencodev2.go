// Package opencodev2 implements the official OpenCode 2 TUI contract. The
// executable name is still opencode; the registry identity is opencode-v2.
package opencodev2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// Plugin is the OpenCode 2 TUI agent adapter.
type Plugin struct{ agentbase.Base }

// New returns an OpenCode 2 adapter.
func New() *Plugin { return &Plugin{} }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.SemanticMessageAcceptanceSignaler = (*Plugin)(nil)

// Manifest describes the adapter.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{ID: string(domain.HarnessOpenCodeV2), Name: "OpenCode 2", Description: "Run OpenCode 2 worker sessions.", Version: "0.0.1", Capabilities: []adapters.Capability{adapters.CapabilityAgent}}
}

// EmitsSemanticMessageAcceptance is false: OpenCode 2's prompt hook runs before
// durable prompt admission, so no hook can confirm a prompt was accepted.
func (p *Plugin) EmitsSemanticMessageAcceptance() bool { return false }

// ResolveBinary resolves the shared opencode executable and requires the v2
// command contract before catalog, authentication, install verification, or a
// session can use it.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return opencode.ResolveBinaryForMajor(ctx, 2)
}

// GetConfigSpec returns the model override spec.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override for the OpenCode 2 session agent.")
}

// GetLaunchCommand builds the OpenCode 2 launch argv.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	binary, err := opencode.ResolveBinaryForMajor(ctx, 2)
	if err != nil {
		return nil, err
	}
	return command(ctx, binary, cfg, "")
}

// GetRestoreCommand builds the OpenCode 2 restore argv.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	nativeID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if nativeID == "" {
		return nil, false, nil
	}
	binary, err := opencode.ResolveBinaryForMajor(ctx, 2)
	if err != nil {
		return nil, false, err
	}
	cmd, err := command(ctx, binary, ports.LaunchConfig{Config: cfg.Config, SessionID: cfg.Session.ID, Permissions: cfg.Permissions, Prompt: cfg.Prompt, SystemPrompt: cfg.SystemPrompt, SystemPromptFile: cfg.SystemPromptFile}, nativeID)
	return cmd, err == nil, err
}

// The v2 root TUI has no --model/--agent flags. Select the AO agent through
// default_agent in the highest-precedence inline config. --prompt=<value>
// keeps even leading-dash prompts a single string value in the Effect CLI.
func command(ctx context.Context, binary string, cfg ports.LaunchConfig, nativeID string) ([]string, error) {
	prompt := cfg.SystemPrompt
	if prompt == "" && cfg.SystemPromptFile != "" {
		data, err := os.ReadFile(cfg.SystemPromptFile)
		if err != nil {
			return nil, fmt.Errorf("opencode-v2: read system prompt: %w", err)
		}
		prompt = string(data)
	}
	content, err := prepareConfigContent(os.Getenv("OPENCODE_CONFIG_CONTENT"), cfg.SessionID, prompt, cfg.Config.Model, cfg.Permissions)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var envArgs []string
	if dataHome, ok := DataHome(); ok {
		envArgs = append(envArgs, "XDG_DATA_HOME="+dataHome)
	}
	if content != "" {
		envArgs = append(envArgs, "OPENCODE_CONFIG_CONTENT="+content)
	}
	var cmd []string
	if len(envArgs) > 0 {
		cmd = append([]string{"env"}, envArgs...)
	}
	cmd = append(cmd, binary, "--standalone")
	switch ports.NormalizePermissionMode(cfg.Permissions) {
	case ports.PermissionModeAuto:
		cmd = append(cmd, "--auto")
	case ports.PermissionModeBypassPermissions:
		cmd = append(cmd, "--dangerously-skip-permissions")
	}
	if nativeID != "" {
		cmd = append(cmd, "--session", nativeID)
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt="+cfg.Prompt)
	}
	return cmd, nil
}

type permissionRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

// Only AO's selected agent and default_agent are owned here. User config files,
// credentials, other agents, provider settings, and top-level permission rules
// remain intact. Native v2 applies inline config after project config and
// appends agent rules after global rules (last matching permission wins).
func prepareConfigContent(existing, sessionID, prompt, model string, mode ports.PermissionMode) (string, error) {
	model = strings.TrimSpace(model)
	mode = ports.NormalizePermissionMode(mode)
	if prompt == "" && model == "" && mode != ports.PermissionModeAcceptEdits && mode != ports.PermissionModeBypassPermissions {
		return "", nil
	}
	config := map[string]json.RawMessage{}
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &config); err != nil || config == nil {
			return "", fmt.Errorf("opencode-v2: OPENCODE_CONFIG_CONTENT must be a JSON object")
		}
	}
	agents := map[string]json.RawMessage{}
	if raw, ok := config["agents"]; ok {
		if err := json.Unmarshal(raw, &agents); err != nil || agents == nil {
			return "", fmt.Errorf("opencode-v2: OPENCODE_CONFIG_CONTENT agents must be an object")
		}
	}
	name := aoAgentName(sessionID)
	agent := map[string]any{}
	if raw, ok := agents[name]; ok {
		if err := json.Unmarshal(raw, &agent); err != nil || agent == nil {
			return "", fmt.Errorf("opencode-v2: OPENCODE_CONFIG_CONTENT agent %q must be an object", name)
		}
	}
	// agents.build.permissions is left untouched: inline config replaces arrays on
	// merge, so writing it would clear file-defined build-agent rules.
	agent["mode"] = "primary"
	if prompt != "" {
		agent["system"] = prompt
	}
	delete(agent, "model")
	if model != "" {
		agent["model"] = model
	}
	delete(agent, "permissions")
	switch mode {
	case ports.PermissionModeAcceptEdits:
		agent["permissions"] = []permissionRule{{Action: "edit", Resource: "*", Effect: "allow"}}
	case ports.PermissionModeBypassPermissions:
		agent["permissions"] = []permissionRule{{Action: "*", Resource: "*", Effect: "allow"}}
	}
	raw, err := json.Marshal(agent)
	if err != nil {
		return "", err
	}
	agents[name] = raw
	config["agents"], err = json.Marshal(agents)
	if err != nil {
		return "", err
	}
	config["default_agent"], err = json.Marshal(name)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// PrepareACPConfigContent merges AO's standing instructions and permission
// boundary into OpenCode 2's built-in build agent. OpenCode's ACP bridge may
// snapshot its built-in modes before custom agents finish loading, so AO keeps
// the provider-visible mode stable and answers its permission requests itself.
// User-owned providers, top-level permission rules, and unrelated agents remain
// untouched.
func PrepareACPConfigContent(existing, systemPrompt string, _ ports.PermissionMode) (string, error) {
	config := map[string]json.RawMessage{}
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &config); err != nil || config == nil {
			return "", fmt.Errorf("opencode-v2: OPENCODE_CONFIG_CONTENT must be a JSON object")
		}
	}
	agents := map[string]json.RawMessage{}
	if raw, ok := config["agents"]; ok {
		if err := json.Unmarshal(raw, &agents); err != nil || agents == nil {
			return "", fmt.Errorf("opencode-v2: OPENCODE_CONFIG_CONTENT agents must be an object")
		}
	}
	const name = "build"
	agent := map[string]any{}
	if raw, ok := agents[name]; ok {
		if err := json.Unmarshal(raw, &agent); err != nil || agent == nil {
			return "", fmt.Errorf("opencode-v2: OPENCODE_CONFIG_CONTENT agent %q must be an object", name)
		}
	}
	// agents.build.permissions is left untouched: inline config replaces arrays on
	// merge, so writing it would clear file-defined build-agent rules.
	agent["mode"] = "primary"
	if systemPrompt != "" {
		agent["system"] = systemPrompt
	}
	raw, err := json.Marshal(agent)
	if err != nil {
		return "", err
	}
	agents[name] = raw
	config["agents"], err = json.Marshal(agents)
	if err != nil {
		return "", err
	}
	config["default_agent"], err = json.Marshal(name)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("opencode-v2: encode ACP agent config: %w", err)
	}
	return string(data), nil
}

func aoAgentName(sessionID string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.TrimSpace(sessionID))
	name = strings.Trim(name, "-_")
	if name == "" {
		return "ao-system-prompt"
	}
	return "ao-" + name
}

// SessionInfo returns the standard session info.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// AuthStatus reports authenticated integrations, including environment connections.
// Keep this probe private so readiness never starts/replaces a shared service.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := opencode.ResolveBinaryForMajor(ctx, 2)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := aoprocess.CommandContext(probeCtx, binary, "auth", "list", "--standalone", "--format", "json")
	if dataHome, ok := DataHome(); ok {
		cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dataHome)
	}
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	var integrations []struct {
		Connections []json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(out, &integrations); err != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	for _, integration := range integrations {
		if len(integration.Connections) > 0 {
			return ports.AgentAuthStatusAuthorized, nil
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}
