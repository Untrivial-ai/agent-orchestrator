// Package cline implements the Cline CLI agent adapter: launching terminal
// sessions, resuming sessions by native session id, installing workspace-local
// Cline hooks, and reading hook-derived session info.
//
// Cline is an autonomous coding agent that runs in the terminal (binary
// "cline", installed via `npm i -g cline`). AO opens Cline's normal terminal UI
// and delivers prompted worker tasks after startup so dashboard terminal
// attachments stay readable and Cline's startup command parser is bypassed.
//
// AO-managed sessions prefer native session identity from Cline hooks (the
// workspace-local `.clinerules/hooks/` executable scripts AO installs). Cline's
// interactive hub currently omits that identity, so restore falls back to the
// newest Cline history entry scoped to the session's unique worktree.
package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// Plugin is the Cline agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Cline adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.ContinuousTerminalActivityDetector = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "cline",
		Name:        "Cline",
		Description: "Run Cline worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Cline understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `cline --model`.",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start a new interactive Cline session.
// Prompted worker tasks are injected after startup; passing them in argv makes
// Cline's startup parser reject short prompts such as "hi" as unknown commands.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.clineBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	appendApprovalFlags(&cmd, cfg.Permissions)
	appendModelFlag(&cmd, cfg.Config)

	if cfg.SystemPrompt != "" {
		cmd = append(cmd, "-s", cfg.SystemPrompt)
	}

	return cmd, nil
}

// GetPromptDeliveryStrategy reports that AO should inject prompted Cline tasks
// into the interactive terminal after startup.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// PromptReadinessHints waits briefly for Cline's interactive prompt before AO
// injects the worker's first task.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 750 * time.Millisecond,
		Patterns: []string{
			"Type a message",
			"What can I help",
			">",
		},
		PollInterval: 200 * time.Millisecond,
		Timeout:      8 * time.Second,
		Lines:        80,
	}, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Cline session:
// `cline [approval flags] --id <agentSessionId>`. Resumes are interactive
// because no prompt is supplied here. If hooks did not persist the native id,
// the interactive-hub history is searched by this session's unique worktree.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	binary, err := p.clineBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" && strings.TrimSpace(cfg.Session.WorkspacePath) != "" {
		agentSessionID, err = clineHistorySessionID(ctx, binary, cfg.Session.WorkspacePath)
		if err != nil {
			return nil, false, err
		}
	}
	if agentSessionID == "" {
		return nil, false, nil
	}

	cmd = make([]string, 0, 8)
	cmd = append(cmd, binary)
	appendApprovalFlags(&cmd, cfg.Permissions)
	appendModelFlag(&cmd, cfg.Config)
	if cfg.SystemPrompt != "" {
		cmd = append(cmd, "-s", cfg.SystemPrompt)
	}
	cmd = append(cmd, "--id", agentSessionID)
	return cmd, true, nil
}

// clineHistorySessionID recovers the provider-assigned resumable id when an
// interactive Cline hub hook omits sessionContext.rootSessionId. AO gives each
// session its own worktree, so the newest history entry whose cwd exactly
// matches that worktree belongs to this AO session.
func clineHistorySessionID(ctx context.Context, binary, workspacePath string) (string, error) {
	cmd := aoprocess.CommandContext(ctx, binary, "history", "--json", "--limit", "100") //nolint:gosec // binary is adapter-resolved; args are static
	cmd.Dir = workspacePath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cline: read session history: %w", err)
	}

	var history []struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
	}
	if err := json.Unmarshal(out, &history); err != nil {
		return "", fmt.Errorf("cline: parse session history: %w", err)
	}
	want := filepath.Clean(workspacePath)
	for _, entry := range history {
		id := strings.TrimSpace(entry.SessionID)
		if filepath.Clean(strings.TrimSpace(entry.CWD)) == want && id != "" && len(id) <= maxHookSessionIDLen {
			return id, nil
		}
	}
	return "", nil
}

// SessionInfo surfaces Cline hook-derived metadata. Metadata is intentionally
// nil for Cline: callers get the normalized fields directly.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

var clineBinarySpec = binaryutil.BinarySpec{
	Label:         "cline",
	Names:         []string{"cline"},
	WinNames:      []string{"cline.cmd", "cline.exe", "cline"},
	UnixPaths:     []string{"/usr/local/bin/cline", "/opt/homebrew/bin/cline"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("cline"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "cline.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "cline.exe"}},
	},
}

// ResolveClineBinary returns the path to the cline binary on this machine,
// searching PATH then a handful of well-known install locations (Homebrew, npm
// global). It returns a wrapped ports.ErrAgentBinaryNotFound when cline is absent.
func ResolveClineBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, clineBinarySpec)
}

func (p *Plugin) clineBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveClineBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

func appendModelFlag(cmd *[]string, cfg ports.AgentConfig) {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		*cmd = append(*cmd, "--model", model)
	}
}

func appendApprovalFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to the user's Cline config/default behavior.
	case ports.PermissionModeAcceptEdits:
		// Edit-accepting mode: turn on Cline's auto-approval so edits are
		// applied without prompting, matching the AcceptEdits semantics every
		// other adapter uses (the more-permissive, edit-accepting mode).
		*cmd = append(*cmd, "--auto-approve", "true")
	case ports.PermissionModeAuto:
		// Auto-approve every tool for unattended runs.
		*cmd = append(*cmd, "--auto-approve", "true")
	case ports.PermissionModeBypassPermissions:
		// yolo mode: auto-approve tools with the restricted (safer) toolset.
		*cmd = append(*cmd, "--yolo")
	}
}
