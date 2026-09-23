// Package auggie implements the Auggie (Augment Code) agent adapter: launching
// new headless Auggie sessions and resuming sessions when a native Auggie
// session id is known.
//
// Auggie is Augment Code's terminal coding agent (binary "auggie", installed via
// `npm install -g @augmentcode/auggie`). It exposes a headless one-shot mode via
// `--print` (alias `-p`) which runs a single instruction and exits -- the mode AO
// uses to drive it unattended.
//
// Launch shape:
//
//	auggie --print [--rules <f>] [-- <prompt>]
//
// The prompt is the print-mode positional, passed after `--` so a prompt
// beginning with "-" is not mistaken for a flag. A system prompt, when supplied
// as an AO-owned file, is injected via Auggie's `--rules` flag, which appends
// guidance to the workspace rules.
//
// Permissions: Auggie has no single "approve everything" flag. It governs
// unattended tool/file approval through granular `--permission <tool>:<allow|deny>`
// rules (and a read-only `--ask` mode), not a 4-mode bypass like Claude Code.
// Because there is no verifiable blanket auto-approve flag, every AO permission
// mode emits no flag and defers to the user's Auggie configuration, rather than
// guessing a flag that does not exist.
//
// Resume: Auggie supports `--resume <sessionId>` (alias `-r`), usable with
// `--print` for headless resume. AO only has a native session id to resume from
// when one was captured into session metadata. AO's managed SessionStart hook
// records Auggie's conversation_id as that native resume handle.
//
// Hooks/activity: AO merges Auggie's native workspace hooks into
// .augment/settings.local.json and reports session/tool/stop lifecycle events.
package auggie

import (
	"context"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "auggie"

// Plugin is the Auggie agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Auggie adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Auggie",
		Description: "Run Auggie (Augment Code) worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Auggie understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `auggie --model`.",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start a new headless Auggie session:
//
//	auggie --print [--rules <f>] [-- <prompt>]
//
// The prompt is passed after `--` so a prompt beginning with "-" is not mistaken
// for a flag. Auggie's `--instruction` flags are the task input, not a rule or
// system-prompt surface; AO standing instructions use `--rules` when the
// manager provides a prompt file.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binary, err := p.auggieBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary, "--print"}
	if cfg.SystemPromptFile != "" {
		cmd = append(cmd, "--rules", cfg.SystemPromptFile)
	}
	appendModelFlag(&cmd, cfg.Config)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}
	return cmd, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Auggie session
// when a native session id is available in metadata:
//
//	auggie --print --resume <sessionId>
//
// Auggie has no hook surface to capture that id automatically yet, so in practice
// the id is empty and ok is false, letting callers fall back to a fresh launch.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.auggieBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	cmd = []string{binary, "--print"}
	if cfg.SystemPromptFile != "" {
		cmd = append(cmd, "--rules", cfg.SystemPromptFile)
	}
	appendModelFlag(&cmd, cfg.Config)
	cmd = append(cmd, "--resume", agentSessionID)
	return cmd, true, nil
}

// SessionInfo surfaces the Auggie conversation id captured from native hook
// payloads, along with any other standard hook-derived metadata.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// appendModelFlag treats the configured model as opaque: it trims and forwards
// non-empty values for Auggie to validate. Invalid models intentionally surface
// Auggie's error; AO does not silently retry with Auggie's default model.
func appendModelFlag(cmd *[]string, cfg ports.AgentConfig) {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		*cmd = append(*cmd, "--model", model)
	}
}

// Auggie has no single blanket auto-approve/bypass flag; unattended tool/file
// approval is governed by granular `--permission <tool>:<allow|deny>` rules, so
// AO emits no approval flag and defers every mode to the user's Auggie config.
// There is therefore no appendApprovalFlags helper for this adapter.

var auggieBinarySpec = binaryutil.BinarySpec{
	Label:         "auggie",
	Names:         []string{"auggie"},
	WinNames:      []string{"auggie.cmd", "auggie.exe", "auggie"},
	UnixPaths:     []string{"/usr/local/bin/auggie", "/opt/homebrew/bin/auggie"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("auggie"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "auggie.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "auggie.exe"}},
	},
}

// ResolveAuggieBinary finds the `auggie` binary, searching PATH then common
// install locations. It returns a wrapped ports.ErrAgentBinaryNotFound when
// Auggie is absent.
func ResolveAuggieBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, auggieBinarySpec)
}

func (p *Plugin) auggieBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveAuggieBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
