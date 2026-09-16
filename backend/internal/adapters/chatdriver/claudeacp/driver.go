// Package claudeacp binds Claude Code to AO's generic ACP Chat driver.
//
// AO ships the protocol adapter and its Node runtime, not Claude Code. The
// adapter receives CLAUDE_CODE_EXECUTABLE pointing at the same user-installed
// binary used by AO's existing TUI adapter, so login, subscription, settings,
// MCP configuration, hooks, and project instructions remain the user's own.
package claudeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const minimumNodeMajor = 22

type claudePlugin interface {
	ResolveBinary(context.Context) (string, error)
	AuthStatus(context.Context) (ports.AgentAuthStatus, error)
}

// New constructs the Claude Code ACP driver over the existing Claude agent
// plugin. The plugin remains the canonical discovery/auth implementation for
// both Chat and TUI modes.
func New(plugin claudePlugin, log *slog.Logger, onAuthRejected func()) ports.ChatDriver {
	return &checkpointDriver{plugin: plugin, ChatDriver: acpdriver.New(acpdriver.Config{
		Harness: domain.HarnessClaudeCode,
		// A live rejection is the ground truth that outranks any cached
		// verdict, so drop the cache the moment one arrives. This is also the
		// only auth correction that works for credential sources AO cannot
		// read at all — the Bedrock and Vertex chains — because it needs no
		// credential, no network call, and no provider knowledge.
		OnAuthRejected:        claudeAuthRejected(onAuthRejected),
		PromptResponseFailure: claudePromptResponseFailure,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming:    true,
			ports.ChatCapabilityTools:        true,
			ports.ChatCapabilityApprovals:    true,
			ports.ChatCapabilityInterrupt:    true,
			ports.ChatCapabilityResume:       true,
			ports.ChatCapabilityPromptReplay: true,
			ports.ChatCapabilityUsage:        true,
			ports.ChatCapabilityDiffs:        true,
			ports.ChatCapabilityPlans:        true,
			ports.ChatCapabilityCompaction:   true,
		},
		Probe: func(ctx context.Context) error {
			if _, err := resolveRuntime(ctx); err != nil {
				return fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			claudeBinary, err := plugin.ResolveBinary(ctx)
			if err != nil {
				return fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			if err := validateClaudeACPExecutable(claudeBinary, runtime.GOOS); err != nil {
				return fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			status, err := plugin.AuthStatus(ctx)
			if err != nil && log != nil {
				log.Debug("Claude auth probe inconclusive; continuing", "error", err)
			}
			if err == nil && status == ports.AgentAuthStatusUnauthorized {
				return ports.ErrChatAuthRequired
			}
			return nil
		},
		Launch: func(ctx context.Context, cfg acpdriver.LaunchConfig) (acpdriver.Launch, error) {
			runtimeLaunch, err := resolveRuntime(ctx)
			if err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			claudeBinary, err := plugin.ResolveBinary(ctx)
			if err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			if err := validateClaudeACPExecutable(claudeBinary, runtime.GOOS); err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			var models []ports.AgentModelInfo
			if _, preserve := claudeACPModelConfig(cfg.Env); !preserve {
				var modelErr error
				models, modelErr = claudecode.ProviderModels(ctx, claudeBinary, cfg.WorkspacePath, cfg.Env)
				if modelErr != nil && log != nil {
					log.Debug("Claude provider model discovery unavailable; using ACP defaults", "error", modelErr)
				}
			}
			env := claudeACPLaunchEnv(cfg.Env, claudeBinary, cfg.Model, models)
			return acpdriver.Launch{
				Command: runtimeLaunch.command,
				Args:    runtimeLaunch.args,
				Env:     env,
			}, nil
		},
		SessionMeta:    claudeSessionMeta,
		SessionMode:    claudeSessionMode,
		SessionOptions: claudeSessionOptions,
	}, log)}
}

func claudeAuthRejected(notifyDaemon func()) func() {
	return func() {
		claudecode.InvalidateAuthCache()
		if notifyDaemon != nil {
			notifyDaemon()
		}
	}
}

func claudePromptResponseFailure(response acpsdk.PromptResponse) error {
	if response.StopReason != acpsdk.StopReasonEndTurn {
		return nil
	}
	air := claudeNestedMap(claudeNestedMap(response.Meta, "jetbrains"), "air")
	version, versionOK := air["version"].(float64)
	failure := claudeNestedMap(air, "sessionFailure")
	id, _ := failure["id"].(string)
	title, _ := failure["title"].(string)
	if !versionOK || version < 1 || strings.TrimSpace(id) == "" || strings.TrimSpace(title) == "" || failure["severity"] != "error" {
		return nil
	}
	details, _ := failure["details"].(string)
	var cause error
	if actions, ok := failure["actions"].([]any); ok {
		for _, action := range actions {
			if action == "login" {
				cause = ports.ErrChatAuthRequired
				break
			}
		}
	}
	return ports.NewChatProviderFailure(strings.TrimSpace(title), strings.TrimSpace(details), cause)
}

func claudeNestedMap(meta map[string]any, key string) map[string]any {
	if meta == nil {
		return nil
	}
	value, _ := meta[key].(map[string]any)
	return value
}

// claudeACPLaunchEnv gives claude-agent-acp the same provider model IDs AO
// exposes in its pre-launch picker. The adapter turns availableModels into its
// authoritative ACP model choices, so a raw first-party ID selected in AO is
// accepted by session/set_config_option instead of being rejected because the
// adapter started with aliases only.
func claudeACPLaunchEnv(
	input map[string]string,
	binary string,
	selectedModel string,
	models []ports.AgentModelInfo,
) map[string]string {
	env := make(map[string]string, len(input)+2)
	for key, value := range input {
		env[key] = value
	}
	// This prevents the adapter's optional native Claude package from becoming
	// a second installation managed by AO.
	env["CLAUDE_CODE_EXECUTABLE"] = binary
	selected := strings.TrimSpace(selectedModel)
	if _, configured := input["ANTHROPIC_CUSTOM_MODEL_OPTION"]; selected != "" &&
		!isClaudeNativeModelAlias(selected) && !configured &&
		strings.TrimSpace(os.Getenv("ANTHROPIC_CUSTOM_MODEL_OPTION")) == "" {
		// availableModels restricts Claude Code's built-in picker but does not
		// make every provider-discovered API ID a selectable SDK model. The
		// custom option is the supported bridge for the one API model AO is
		// actually starting this session with.
		env["ANTHROPIC_CUSTOM_MODEL_OPTION"] = selected
	}
	config, preserve := claudeACPModelConfig(input)
	if preserve {
		return env
	}

	ids := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if selected != "" {
		if _, exists := seen[selected]; !exists {
			ids = append(ids, selected)
		}
	}
	if len(ids) == 0 {
		return env
	}

	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return env
	}
	config["availableModels"] = encodedIDs
	encodedConfig, err := json.Marshal(config)
	if err != nil {
		return env
	}
	env["CLAUDE_MODEL_CONFIG"] = string(encodedConfig)
	return env
}

func isClaudeNativeModelAlias(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "default", "sonnet", "opus", "haiku", "fable", "opus[1m]":
		return true
	default:
		return false
	}
}

// claudeACPModelConfig returns a mergeable user configuration. preserve is
// true when AO must pass the value through untouched, either because the user
// supplied an authoritative availableModels list or because ACP should report
// malformed configuration itself. Callers can also use preserve to avoid a
// provider lookup whose result would be discarded.
func claudeACPModelConfig(input map[string]string) (map[string]json.RawMessage, bool) {
	raw, configured := input["CLAUDE_MODEL_CONFIG"]
	if !configured {
		raw = os.Getenv("CLAUDE_MODEL_CONFIG")
	}
	config := make(map[string]json.RawMessage)
	if strings.TrimSpace(raw) == "" {
		return config, false
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, true
	}
	if config == nil {
		return nil, true
	}
	_, userRestricted := config["availableModels"]
	return config, userRestricted
}

func validateClaudeACPExecutable(binary, goos string) error {
	if goos != "windows" {
		return nil
	}
	switch strings.ToLower(filepath.Ext(binary)) {
	case ".cmd", ".bat":
		return fmt.Errorf("resolved Claude Code command shim %q, but chat requires the native claude.exe; reinstall or update Claude Code", binary)
	default:
		return nil
	}
}

func claudeSessionMeta(cfg acpdriver.LaunchConfig) map[string]any {
	standing := strings.TrimSpace(cfg.SystemPrompt)
	if standing == "" {
		return nil
	}
	// Append AO's standing instructions to Claude Code's own prompt. Replacing
	// the preset would discard Claude's native coding/tool instructions.
	return map[string]any{
		"systemPrompt": map[string]any{
			"type": "preset", "preset": "claude_code", "append": standing,
		},
	}
}

func claudeSessionMode(permission ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(permission) {
	case ports.PermissionModeAcceptEdits:
		return "acceptEdits"
	case ports.PermissionModeAuto:
		return "auto"
	case ports.PermissionModeBypassPermissions:
		return "bypassPermissions"
	default:
		return ""
	}
}

func claudeSessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	options := make([]acpdriver.SessionOption, 0, 2)
	if settings.Model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: settings.Model})
	}
	if settings.Effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "effort", Value: settings.Effort})
	}
	return options
}

type runtimeLaunch struct {
	command string
	args    []string
}

// resolveRuntime finds AO's packaged ACP runtime. Explicit command and path
// overrides keep headless development/test installs usable without coupling the
// backend to Electron's directory layout.
func resolveRuntime(ctx context.Context) (runtimeLaunch, error) {
	if err := ctx.Err(); err != nil {
		return runtimeLaunch{}, err
	}
	if command := strings.TrimSpace(os.Getenv("AO_CLAUDE_ACP_COMMAND")); command != "" {
		resolved, err := exec.LookPath(command)
		if err != nil {
			return runtimeLaunch{}, fmt.Errorf("resolve AO_CLAUDE_ACP_COMMAND %q: %w", command, err)
		}
		return runtimeLaunch{command: resolved}, nil
	}

	runtimeDir := strings.TrimSpace(os.Getenv("AO_ACP_RUNTIME_DIR"))
	if runtimeDir == "" {
		runtimeDir = runtimeDirectoryBesideExecutable()
	}
	if runtimeDir == "" {
		return runtimeLaunch{}, errors.New("AO ACP runtime is not installed")
	}
	node := filepath.Join(runtimeDir, "node", "bin", "node")
	if runtime.GOOS == "windows" {
		node = filepath.Join(runtimeDir, "node", "node.exe")
	}
	entry := filepath.Join(runtimeDir, "node_modules", "@agentclientprotocol", "claude-agent-acp", "dist", "index.js")
	if err := requireFile(node, "packaged Node runtime"); err != nil {
		return runtimeLaunch{}, err
	}
	if err := requireFile(entry, "claude-agent-acp entrypoint"); err != nil {
		return runtimeLaunch{}, err
	}
	if err := requireNodeVersion(ctx, node); err != nil {
		return runtimeLaunch{}, err
	}
	return runtimeLaunch{command: node, args: []string{entry}}, nil
}

func runtimeDirectoryBesideExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	resources := filepath.Dir(filepath.Dir(executable))
	for _, candidate := range []string{
		filepath.Join(resources, "acp-runtime"),
		filepath.Join(resources, "resources", "acp-runtime"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

func requireFile(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s at %s: %w", label, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s at %s is a directory", label, path)
	}
	return nil
}

func requireNodeVersion(ctx context.Context, node string) error {
	// node is the explicit AO override or the validated executable inside AO's
	// packaged resources, never prompt/provider input.
	out, err := aoprocess.CommandContext(ctx, node, "--version").Output() //nolint:gosec // Resolved local executable, not provider input.
	if err != nil {
		return fmt.Errorf("run packaged Node: %w", err)
	}
	version := strings.TrimSpace(strings.TrimPrefix(string(out), "v"))
	majorText, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || major < minimumNodeMajor {
		return fmt.Errorf("claude-agent-acp requires Node %d+; found %q", minimumNodeMajor, strings.TrimSpace(string(out)))
	}
	return nil
}
