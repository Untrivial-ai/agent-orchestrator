// Package zcode implements the ZCode (Z.AI GLM) agent adapter.
//
// ZCode is Z.AI's official coding agent (binary "zcode", official open repo
// github.com/zai-org/ZCode — source for the Agent CLI and runtime, Apache-2.0;
// its interactive CLI currently requires a source build, because desktop
// release bundles omit the TUI). With no arguments it opens an interactive TUI, which is how AO
// launches it; the initial task is delivered through the terminal after the
// TUI is ready because both --prompt and -p/--print run a single headless
// turn and exit, and a bare positional argument is parsed as a subcommand.
//
// Permission handling maps AO's permission modes onto `--mode`
// (build|edit|plan|yolo). The default explicitly selects build so a saved yolo
// configuration cannot broaden AO permissions. Restore resumes a persisted session
// with `--resume <sessionId>`.
//
// AO installs workspace hooks under .zcode/config.json and grants only their
// exact native declaration digests. UserPromptSubmit supplies hidden additive
// standing instructions; lifecycle callbacks capture the native session ID.
// Model selection stays in ZCode's own model.main configuration.
package zcode

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var zcodeBinarySpec = binaryutil.BinarySpec{
	Label:     "zcode",
	Names:     []string{"zcode"},
	WinNames:  []string{"zcode.cmd", "zcode.exe", "zcode"},
	UnixPaths: []string{"/usr/local/bin/zcode", "/opt/homebrew/bin/zcode"},
	// ZCode's interactive CLI is built from the official source repository
	// github.com/zai-org/ZCode; there is no official npm package. It is a
	// Node-based binary, so version-manager shims (nvm/Volta/fnm) and the
	// Node-managed home paths remain valid resolution targets for a manually built CLI.
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("zcode"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "zcode.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "zcode.exe"}},
	},
}

// Plugin is the ZCode agent adapter.
type Plugin struct {
	agentbase.Base
	binaryMu        sync.Mutex
	resolvedBinary  string
	validateRestore func(context.Context, string, string, map[string]string) error
}

// New returns a ready-to-register ZCode adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "zcode",
		Name:        "ZCode",
		Description: "Run Z.AI ZCode (GLM) worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports ZCode's permission modes. ZCode pins its model in
// its own config (model.main); zcode has no --model launch flag, so AO
// exposes the mode instead of a misleading raw-model field.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{Fields: []ports.ConfigField{{
		Key:         "mode",
		Type:        ports.ConfigFieldEnum,
		Description: "ZCode permission mode passed to `zcode --mode`.",
		Enum:        []string{"build", "edit", "plan", "yolo"},
	}}}, nil
}

// GetLaunchCommand builds `zcode [--mode <mode>] [--disallowed-tools <list>]`
// and leaves the prompt for AO's after-start terminal delivery. Zcode's
// --prompt/-p flags are headless single-turn modes, and a bare positional
// argument is parsed as a subcommand, so the TUI must start empty.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.zcodeBinary(ctx)
	if err != nil {
		return nil, err
	}

	if len(cfg.AllowedTools) != 0 {
		return nil, fmt.Errorf("zcode: tool allowlists are unsupported")
	}
	cmd = []string{binary}
	if err := appendModeFlags(&cmd, cfg.Permissions, cfg.Config.Mode); err != nil {
		return nil, err
	}
	if err := appendDisallowedTools(&cmd, cfg.DisallowedTools); err != nil {
		return nil, err
	}

	if err := p.prepareHookTrust(ctx, binary, cfg.WorkspacePath, cfg.Env); err != nil {
		return nil, err
	}
	return cmd, nil
}

// GetPromptDeliveryStrategy reports that AO should inject prompted ZCode tasks
// into the interactive terminal after startup.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// PromptReadinessHints requires the current native composer and rejects the
// model-configuration screen, which renders an identical composer underneath.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{
			RequireReady: true}, err
	}
	return ports.PromptReadinessHints{
		RequireReady:    true,
		InitialDelay:    750 * time.Millisecond,
		Patterns:        []string{"Type a prompt", "输入提示词"},
		BlockedPatterns: []string{"No available models.", "没有可用模型"},
		PollInterval:    200 * time.Millisecond,
		Timeout:         10 * time.Second,
		Lines:           80,
	}, nil
}

// GetRestoreCommand resumes a prior ZCode session by its captured id, building
// `zcode [--mode <mode>] --resume <agentSessionId>` when we have a captured
// native session id. ok=false otherwise, so the restore manager falls back to
// a fresh launch. `-c/--continue` (latest session for the directory) is
// deliberately not used as that fallback: it could resume an unrelated
// session that happens to share the worktree.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}
	if !nativeIDPattern.MatchString(agentSessionID) {
		return nil, false, fmt.Errorf("zcode: invalid native session ID")
	}
	if len(cfg.AllowedTools) != 0 {
		return nil, false, fmt.Errorf("zcode: tool allowlists are unsupported")
	}

	binary, err := p.zcodeBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = make([]string, 0, 4)
	cmd = append(cmd, binary)
	if err := appendModeFlags(&cmd, cfg.Permissions, cfg.Config.Mode); err != nil {
		return nil, false, err
	}
	if err := appendDisallowedTools(&cmd, cfg.DisallowedTools); err != nil {
		return nil, false, err
	}
	validate := p.validateRestore
	if validate == nil {
		validate = validateNativeRestore
	}
	if err := validate(ctx, cfg.Session.WorkspacePath, agentSessionID, cfg.Env); err != nil {
		return nil, false, err
	}
	if err := p.prepareHookTrust(ctx, binary, cfg.Session.WorkspacePath, cfg.Env); err != nil {
		return nil, false, err
	}
	cmd = append(cmd, "--resume", agentSessionID)
	return cmd, true, nil
}

// SessionInfo reads hook-derived metadata under AO's normalized keys ("title",
// "summary", "agentSessionId"). ZCode reports these through the AO-managed workspace hooks.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// ResolveZcodeBinary finds the `zcode` binary (Z.AI ZCode CLI).
func ResolveZcodeBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, zcodeBinarySpec)
}

func (p *Plugin) zcodeBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveZcodeBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

// appendModeFlags maps AO permission modes onto zcode's --mode values. An
// explicit per-session mode config (build|edit|plan|yolo) wins; otherwise the
// permission mode is mapped — acceptEdits→edit, auto→build (zcode's internal
// "auto" mode is reserved but unimplemented), bypassPermissions→yolo. With neither, build is explicit so a saved yolo setting cannot broaden AO permissions.
var nativeIDPattern = regexp.MustCompile(`^sess_[A-Za-z0-9._-]+$`)

func appendModeFlags(cmd *[]string, permissions ports.PermissionMode, configMode string) error {
	if mode := strings.TrimSpace(configMode); mode != "" {
		// ZCode's own vocabulary — a config written by another path must
		// fail as a clean input error, not reach argv and kill the terminal
		// session at launch.
		switch mode {
		case "build", "edit", "plan", "yolo":
		default:
			return fmt.Errorf("invalid zcode mode %q: want one of build, edit, plan, yolo", mode)
		}
		*cmd = append(*cmd, "--mode", mode)
		return nil
	}
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		*cmd = append(*cmd, "--mode", "build") // Do not inherit a saved yolo mode.
	case ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--mode", "edit")
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--mode", "build")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--mode", "yolo")
	}
	return nil
}

// appendDisallowedTools emits the deny list as a single comma-joined
// --disallowed-tools value. Zcode 0.16.5 has no --allowed-tools flag, so
// allow lists are not forwarded. A rule containing a comma or whitespace is
// rejected: the flag's parser splits on BOTH separators ("Comma or
// space-separated list"), so a literal separator inside a rule would
// silently split it and weaken the deny list.
func appendDisallowedTools(cmd *[]string, disallowed []string) error {
	for _, rule := range disallowed {
		if strings.ContainsAny(rule, ", 	") {
			return fmt.Errorf("zcode: disallowed tool rule %q contains a comma or whitespace; tool rules are joined into a single flag value whose parser splits on both, so a literal separator would silently split the rule", rule)
		}
	}
	if len(disallowed) > 0 {
		*cmd = append(*cmd, "--disallowed-tools", strings.Join(disallowed, ","))
	}
	return nil
}

// InterruptInput cancels a running native turn. Ctrl+C only clears the draft
// or arms ZCode's exit confirmation; Escape is its turn-cancellation key.
func (p *Plugin) InterruptInput() string { return "\x1b" }

// UserPromptSubmit precedes other native hook vetoes, so it cannot acknowledge
// coordination delivery. AO leaves semantic acceptance unsupported.

// ExitDetectionMode leaves process exit detection to the runtime supervisor.
func (p *Plugin) ExitDetectionMode() ports.AgentExitDetectionMode {
	return ports.AgentExitDetectionSupervisor
}
