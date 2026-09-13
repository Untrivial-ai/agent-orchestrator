// Package qwen implements the Qwen Code agent adapter: launching new sessions,
// resuming hook-tracked sessions, installing workspace-local native hooks, and
// reading hook-derived session info.
//
// Qwen Code (github.com/QwenLM/qwen-code) is a fork of Google's gemini-cli, so
// it inherits gemini-cli-shaped flags: `-p/--prompt` (or a positional prompt)
// for the headless one-shot prompt, `--approval-mode
// {plan,default,auto-edit,auto,yolo}` for permissions, and `-r/--resume <id>` to
// continue a specific session. AO starts prompted worker sessions through Qwen's
// documented `--input-file` remote-input bridge, which submits the task into the
// interactive TUI after Qwen reports session_start; this avoids Qwen's
// non-interactive approval behavior in `-p` mode and avoids a blind terminal
// Enter race. Qwen also has a native Claude-Code-shaped hook system configured
// in `.qwen/settings.json` (top-level "hooks" key, event arrays of matcher
// groups with command hooks), and emits a `session_id` in every hook payload —
// so AO captures native session identity and activity from those hooks rather
// than from transcript/cache scans.
package qwen

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Plugin is the Qwen Code agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Qwen Code adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// GetConfigSpec reports the per-project agent config keys Qwen Code
// understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `qwen --model`.",
			},
		},
	}, nil
}

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "qwen",
		Name:        "Qwen Code",
		Description: "Run Qwen Code worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetLaunchCommand builds the argv to start a new Qwen Code session: the
// approval-mode flag, optional system-prompt instructions, and the initial
// prompt. Workers use Qwen's remote-input bridge so the task is submitted into
// the interactive TUI after startup; non-workers use `-p` so command-delivered
// prompts keep their previous one-shot behavior. `-p` takes the prompt as an
// argument, so a leading "-" is not read as a flag.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.qwenBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	appendApprovalFlags(&cmd, cfg.Permissions)
	appendModelFlag(&cmd, cfg.Config)

	systemPrompt, err := launchSystemPromptText(cfg)
	if err != nil {
		return nil, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", systemPrompt)
	}

	if cfg.Prompt != "" && cfg.Kind == domain.KindWorker {
		if runtime.GOOS != "windows" {
			return qwenWorkerRemoteInputCommand(cmd, cfg)
		}
		cmd = append(cmd, "-i", cfg.Prompt)
	} else if cfg.Prompt != "" {
		cmd = append(cmd, "-p", cfg.Prompt)
	}

	return cmd, nil
}

// GetPromptDeliveryStrategy reports that Qwen receives prompted worker tasks via
// argv: either the Unix remote-input launcher or the Windows `-i` fallback.
// Promptless worker launches are plain interactive sessions with no task to
// send.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, cfg ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryInCommand, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Qwen Code
// session: `qwen [--approval-mode <mode>] --resume <agentSessionId>`. ok is false when
// the hook-derived native session id has not landed yet, so callers can fall
// back to fresh launch behavior. Note: ports.RestoreConfig carries no Prompt.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.qwenBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = make([]string, 0, 3)
	cmd = append(cmd, binary)
	appendApprovalFlags(&cmd, cfg.Permissions)
	appendModelFlag(&cmd, cfg.Config)
	systemPrompt, err := restoreSystemPromptText(cfg)
	if err != nil {
		return nil, false, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", systemPrompt)
	}
	cmd = append(cmd, "--resume", agentSessionID)
	return cmd, true, nil
}

// Qwen Code's append-system-prompt flag accepts inline text only. The manager
// normally supplies both inline text and an AO-owned file; if only the file is
// present, read it and pass the contents inline.
func launchSystemPromptText(cfg ports.LaunchConfig) (string, error) {
	return systemPromptTextFrom(cfg.SystemPrompt, cfg.SystemPromptFile)
}

func restoreSystemPromptText(cfg ports.RestoreConfig) (string, error) {
	return systemPromptTextFrom(cfg.SystemPrompt, cfg.SystemPromptFile)
}

func systemPromptTextFrom(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", nil
	}
	data, err := os.ReadFile(file) //nolint:gosec // path is AO-owned launch config
	if err != nil {
		return "", fmt.Errorf("qwen: read system prompt file: %w", err)
	}
	return string(data), nil
}

// SessionInfo surfaces Qwen Code hook-derived metadata. Metadata is
// intentionally nil for Qwen: callers get the normalized fields directly.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

var qwenBinarySpec = binaryutil.BinarySpec{
	Label:         "qwen",
	Names:         []string{"qwen"},
	WinNames:      []string{"qwen.cmd", "qwen.exe", "qwen"},
	UnixPaths:     []string{"/usr/local/bin/qwen", "/opt/homebrew/bin/qwen"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("qwen"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "qwen.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "qwen.exe"}},
	},
}

// ResolveQwenBinary returns the path to the qwen binary, or a wrapped
// ports.ErrAgentBinaryNotFound when it is absent.
func ResolveQwenBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, qwenBinarySpec)
}

func (p *Plugin) qwenBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveQwenBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

// appendApprovalFlags maps AO's four permission modes onto Qwen Code's
// `--approval-mode` choices (plan|default|auto-edit|auto|yolo). Default emits no
// flag so Qwen resolves its starting mode from the user's own config.
func appendApprovalFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to the user's Qwen Code config/default behavior.
	case ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--approval-mode", "auto-edit")
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--approval-mode", "auto")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--approval-mode", "yolo")
	}
}

// appendModelFlag appends --model if a non-empty model is configured.
func appendModelFlag(cmd *[]string, cfg ports.AgentConfig) {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		*cmd = append(*cmd, "--model", model)
	}
}

// AppendSessionFlags adds the TUI-equivalent approval and model flags so Chat
// launches the same Qwen process the terminal adapter would, plus ACP.
func AppendSessionFlags(cmd *[]string, permissions ports.PermissionMode, model string) {
	appendApprovalFlags(cmd, permissions)
	appendModelFlag(cmd, ports.AgentConfig{Model: model})
}

type qwenSubmitCommand struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func qwenWorkerRemoteInputCommand(qwenCmd []string, cfg ports.LaunchConfig) ([]string, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("qwen: data dir is required for worker remote input")
	}
	sessionKey := safeQwenSessionKey(cfg.SessionID)
	if sessionKey == "" {
		sessionKey = "worker"
	}
	dir := filepath.Join(cfg.DataDir, "agent-runtime", "qwen", sessionKey)
	inputPath := filepath.Join(dir, sessionKey+".input.jsonl")
	outputPath := filepath.Join(dir, sessionKey+".output.jsonl")

	submit, err := json.Marshal(qwenSubmitCommand{Type: "submit", Text: cfg.Prompt})
	if err != nil {
		submit = []byte(`{"type":"submit","text":""}`)
	}

	args := append([]string{}, qwenCmd...)
	args = append(args, "--json-file", outputPath, "--input-file", inputPath)

	var script strings.Builder
	script.WriteString("umask 077; ")
	script.WriteString("mkdir -p ")
	script.WriteString(qwenShellQuote(dir))
	script.WriteString("; : > ")
	script.WriteString(qwenShellQuote(inputPath))
	script.WriteString("; : > ")
	script.WriteString(qwenShellQuote(outputPath))
	script.WriteString("; ( for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40; do ")
	script.WriteString("if grep -q '\"session_start\"' ")
	script.WriteString(qwenShellQuote(outputPath))
	script.WriteString("; then printf '%s\\n' ")
	script.WriteString(qwenShellQuote(string(submit)))
	script.WriteString(" >> ")
	script.WriteString(qwenShellQuote(inputPath))
	script.WriteString("; exit 0; fi; sleep 0.25; done; printf '%s\\n' ")
	script.WriteString(qwenShellQuote(string(submit)))
	script.WriteString(" >> ")
	script.WriteString(qwenShellQuote(inputPath))
	script.WriteString(" ) & exec")
	for _, arg := range args {
		script.WriteString(" ")
		script.WriteString(qwenShellQuote(arg))
	}
	return []string{"sh", "-lc", script.String()}, nil
}

func safeQwenSessionKey(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_',
			r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	label := strings.Trim(b.String(), "_")
	if label == "" {
		label = "session"
	}
	return label + "-" + hex.EncodeToString([]byte(sessionID))
}

func qwenShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
