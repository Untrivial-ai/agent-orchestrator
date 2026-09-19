// Package zcode implements the ZCode (Z.AI GLM) agent adapter.
//
// ZCode is Z.AI's terminal coding agent (binary "zcode"). With no arguments
// it opens an interactive TUI, which is how AO launches it; the initial task
// is delivered through the terminal after the TUI is ready because both
// --prompt and -p/--print run a single headless turn and exit, and a bare
// positional argument is parsed as a subcommand.
//
// Permission handling maps AO's permission modes onto `--mode`
// (build|edit|plan|yolo). The default omits the flag so ZCode uses its own
// config (interactive default: build). Restore resumes a persisted session
// with `--resume <sessionId>`.
//
// ZCode's hook, skill, and plugin configuration lives in the user-global
// ~/.zcode/cli/config.json — including provider credentials. ZCode does
// support workspace-scoped hook files (<workspace>/zcode.json or
// <workspace>/.zcode/config.json), but it gates them behind an interactive
// workspace-hook trust review that an orchestrator cannot complete
// unattended. AO therefore installs no hooks; session metadata is only
// surfaced when it already exists under the normalized keys.
//
// Model selection is pinned in ZCode's own config (model.main); zcode 0.16.5
// exposes no --model launch flag. Users can still pin the permission mode
// per session through the mode config field, which maps onto `--mode`.
package zcode

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var zcodeBinarySpec = binaryutil.BinarySpec{
	Label:     "zcode",
	Names:     []string{"zcode"},
	WinNames:  []string{"zcode.cmd", "zcode.exe", "zcode"},
	UnixPaths: []string{"/usr/local/bin/zcode", "/opt/homebrew/bin/zcode"},
	// zcode is distributed as an npm package (zcode-app-cli), so the
	// Node-managed home paths (.npm-global, .npm, .local) cover its usual
	// install locations.
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
	binaryMu       sync.Mutex
	resolvedBinary string
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
// its own config (model.main); zcode 0.16.5 has no --model launch flag, so
// AO exposes the mode instead of a misleading raw-model field. The enum is
// the domain vocabulary, so it can never diverge from what AgentConfig
// validation accepts.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	v, _ := domain.ModeVocabulary(domain.HarnessZCode)
	return ports.ConfigSpec{Fields: []ports.ConfigField{{
		Key:         "mode",
		Type:        ports.ConfigFieldEnum,
		Description: "ZCode permission mode passed to `zcode --mode`.",
		Enum:        v.Values,
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

	cmd = []string{binary}
	if err := appendModeFlags(&cmd, cfg.Permissions, cfg.Config.Mode); err != nil {
		return nil, err
	}
	if err := appendDisallowedTools(&cmd, cfg.DisallowedTools); err != nil {
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

// PromptReadinessHints waits for ZCode's composer before AO injects the
// worker's first task. Patterns are any-match terminal lines: the empty
// composer's welcome banner from the @zcode/tui bundle and the always-on
// footer, so a localized or content-carrying UI still matches (the banner
// only mounts for an empty session). Timeout falls back to delivery so a
// changed placeholder cannot permanently block spawning.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 750 * time.Millisecond,
		// "Ask a task about this workspace" is a hardcoded (non-localized)
		// English literal in SessionWelcome.render; "/help commands" is the
		// persistent footer. Either matching is sufficient.
		Patterns:     []string{"Ask a task about this workspace", "/help commands"},
		PollInterval: 200 * time.Millisecond,
		Timeout:      10 * time.Second,
		Lines:        80,
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
	cmd = append(cmd, "--resume", agentSessionID)
	return cmd, true, nil
}

// SessionInfo reads hook-derived metadata under AO's normalized keys ("title",
// "summary", "agentSessionId"). Zcode has no AO-managed hook installation (its
// hook config is user-global), so these appear only when something else — for
// example a manually wired `ao hooks zcode` command — persisted them.
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
// "auto" mode is reserved but unimplemented), bypassPermissions→yolo. With
// neither, no flag is emitted so ZCode's own config governs (interactive
// default: build).
func appendModeFlags(cmd *[]string, permissions ports.PermissionMode, configMode string) error {
	if mode := strings.TrimSpace(configMode); mode != "" {
		// Defense-in-depth: the domain table accepts the union of every
		// harness's modes, so re-check against ZCode's own vocabulary here —
		// a config written by another path must fail as a clean input error,
		// not reach argv and kill the terminal session at launch.
		if err := domain.ValidateMode(domain.HarnessZCode, mode); err != nil {
			return err
		}
		*cmd = append(*cmd, "--mode", mode)
		return nil
	}
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to ZCode's own config.
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
