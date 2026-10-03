// Package deepseek implements the DeepSeek Harness (dsh) agent adapter: running
// one-shot terminal tasks and rebuilding the argv that continues a persisted
// Harness session.
//
// DeepSeek Harness (binary "dsh", installed with `npm install -g @deepseek-ai/dsh`)
// is a profile launcher rather than a single application: `dsh --profile <name>`
// boots an ordered stack of plugin bundles, and the profile decides what actually
// runs. Two shipped profiles matter to AO:
//
//   - "headless" — one-shot task mode. `dsh --profile headless "<task>"` answers
//     one task, prints the answer, and exits. This is the terminal launch AO
//     drives, in the same shape as other one-shot harnesses.
//   - "acp" — an automation-only Agent Client Protocol stdio server. AO's Chat
//     driver binds to it (see internal/adapters/chatdriver/deepseekacp), which is
//     where per-session model and reasoning-effort selection live.
//
// Launch shape:
//
//	dsh --profile headless <task>
//
// The task is a positional argument; DeepSeek Harness joins multiple words with
// spaces. It falls back to reading the task from stdin both when no task is
// given and when the task is exactly "-", and AO never writes to that stdin, so
// a headless launch requires a task and rejects that literal instead of leaving
// the session blocked until the supervisor kills it.
//
// --session-id adopts an already persisted Harness session and is rejected for
// an id Harness has not seen, so it belongs to the restore path only. A fresh
// launch omits it and lets Harness mint its own id; AO's own SessionID is never
// passed, because it is the AO-internal id, not a Harness-native one.
//
// Model selection: the headless profile exposes no model or reasoning-effort
// flag — its model route comes from the profile's own configuration. Both are
// therefore Chat-only surfaces, advertised by the ACP session as the "model" and
// "reasoning_effort" config options, and the model field AO reports is the value
// that Chat path delivers back to the session. A terminal launch uses the
// profile's route, which the field description states rather than implying the
// override applies everywhere.
//
// Hooks/activity: DeepSeek Harness has no workspace hook file for AO to merge
// into, so GetAgentHooks is a no-op and no activity deriver is registered for
// this harness. Session activity is reported by the ACP Chat stream instead.
package deepseekharness

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "deepseek-harness"

// headlessProfile is the shipped DeepSeek Harness profile that answers a single
// task and exits.
const headlessProfile = "headless"

// stdioTaskLiteral is the task text DeepSeek Harness treats as "read the task
// from stdin" rather than as a prompt.
const stdioTaskLiteral = "-"

// Plugin is the DeepSeek Harness agent adapter. It is safe for concurrent use;
// the binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register DeepSeek Harness adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "DeepSeek",
		Description: "Run DeepSeek Harness (dsh) worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the DeepSeek Harness model override. The value is the
// opaque choice the session advertises — Harness encodes each one as a JSON
// array string such as ["deepseek-official","deepseek-v4-flash"] — and AO passes
// it back unchanged when it opens a Chat session. A terminal (headless) launch
// has no model flag, so it uses the model route configured in the Harness
// profile instead; the description says so rather than implying otherwise.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx,
		"Model override for DeepSeek Harness Chat sessions, using the value the session advertises (for example [\"deepseek-official\",\"deepseek-v4-flash\"]). Terminal launches use the model route configured in the Harness profile.")
}

// GetLaunchCommand builds the argv to run one headless task:
//
//	dsh --profile headless <task>
//
// No --session-id is passed: DeepSeek Harness reads that flag as "adopt this
// already persisted session" and rejects an id it has not seen, and cfg.SessionID
// is the AO-internal id rather than a Harness-native one. A fresh launch
// therefore lets Harness mint its own id, which GetRestoreCommand resumes by
// once AO has captured it.
//
// AO standing instructions have no DeepSeek Harness surface to attach to (no
// system-prompt flag and no workspace hook file), so a system prompt file is
// ignored here rather than passed as a task argument that would be read as work.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binary, err := p.deepseekBinary(ctx)
	if err != nil {
		return nil, err
	}
	task, err := headlessTask(cfg.Prompt)
	if err != nil {
		return nil, err
	}
	return []string{binary, "--profile", headlessProfile, task}, nil
}

// GetRestoreCommand rebuilds the argv that continues a persisted DeepSeek
// Harness session when its id is known:
//
//	dsh --profile headless --session-id <id> <task>
//
// A terminal launch only has that id when AO captured one into session metadata,
// which needs a hook surface DeepSeek Harness does not expose; in practice the id
// is empty and ok is false, letting callers fall back to a fresh launch. Chat
// sessions resume through the ACP driver instead.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.deepseekBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	task, err := headlessTask(cfg.Prompt)
	if err != nil {
		return nil, false, err
	}
	return []string{binary, "--profile", headlessProfile, "--session-id", agentSessionID, task}, true, nil
}

// SessionInfo surfaces the standard hook-derived session metadata. DeepSeek
// Harness reports its session id through ACP rather than a workspace callback, so
// terminal sessions normally carry nothing here.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// headlessTask validates the positional task argument. DeepSeek Harness reads
// the task from stdin both when the argument is absent and when it is exactly
// "-", and a terminal launch never writes to that stdin, so both cases are
// rejected with an actionable error instead of blocking until the supervisor
// kills the session. AO's --prompt is optional, which makes the empty case the
// reachable one.
func headlessTask(prompt string) (string, error) {
	switch strings.TrimSpace(prompt) {
	case "":
		return "", fmt.Errorf("%s: a terminal session needs a task; DeepSeek Harness reads stdin when the %s profile is launched without one", adapterID, headlessProfile)
	case stdioTaskLiteral:
		return "", fmt.Errorf("%s: refusing to pass %q as a task; DeepSeek Harness would read stdin instead of running it", adapterID, stdioTaskLiteral)
	}
	return prompt, nil
}

var deepseekBinarySpec = binaryutil.BinarySpec{
	Label:         "dsh",
	Names:         []string{"dsh"},
	WinNames:      []string{"dsh.cmd", "dsh.exe", "dsh"},
	UnixPaths:     []string{"/usr/local/bin/dsh", "/opt/homebrew/bin/dsh"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("dsh"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "dsh.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "dsh.exe"}},
	},
}

// ResolveDeepSeekBinary finds the `dsh` binary, searching PATH then common
// install locations. It returns a wrapped ports.ErrAgentBinaryNotFound when
// DeepSeek Harness is absent.
func ResolveDeepSeekBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, deepseekBinarySpec)
}

func (p *Plugin) deepseekBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveDeepSeekBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
