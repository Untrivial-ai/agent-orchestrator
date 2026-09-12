// Package codex implements the Codex agent adapter: launching new sessions,
// resuming hook-tracked sessions, installing workspace-local hooks, and reading
// hook-derived session info.
//
// AO-managed sessions derive native session identity and display
// metadata from Codex hooks instead of transcript/cache scans.
package codex

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

// Plugin is the Codex agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Codex adapter.
func New() *Plugin {
	return &Plugin{}
}

// EmitsSubmitActivity signals Codex fires a user-prompt-submit hook under AO's
// launch. See ports.SubmitActivitySignaler.
func (p *Plugin) EmitsSubmitActivity() bool { return true }

// EmitsBlockedActivity is false: codex reports permission prompts as
// waiting_input — it installs no post-tool-use hook, so a blocked state could
// never be cleared mid-turn. confirmActive must not nudge it (an Enter could
// answer a pending decision it cannot report as blocked). See
// ports.BlockedActivitySignaler.
func (p *Plugin) EmitsBlockedActivity() bool { return false }

// ExitDetectionMode opts Codex into AO's process supervisor. Codex hooks
// expose turn boundaries but no reliable session-end event.
func (p *Plugin) ExitDetectionMode() ports.AgentExitDetectionMode {
	return ports.AgentExitDetectionSupervisor
}

// SteersActiveTurn is true: submitting input to the codex TUI mid-turn steers
// the running turn rather than being swallowed or queued, so AO may write an
// unsolicited coordination message into an active codex session. See
// ports.ActiveTurnSteerer.
func (p *Plugin) SteersActiveTurn() bool { return true }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.ActiveTurnSteerer = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentInterfaceHandoff = (*Plugin)(nil)
var _ ports.AgentInterfaceHandoffHistoryProbe = (*Plugin)(nil)
var _ ports.TerminalActivityDetector = (*Plugin)(nil)
var _ ports.EmptyComposerDetector = (*Plugin)(nil)
var _ ports.TerminalSurfaceInspector = (*Plugin)(nil)

// ComposerIsEmpty recognizes Codex's blank composer or its dim placeholder.
// Normal text after the prompt marker is treated as a human draft and causes
// optional semantic handoff collection to fail closed.
func (p *Plugin) ComposerIsEmpty(output string) bool {
	return p.InspectTerminalSurface(output).Composer == ports.TerminalComposerEmpty
}

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "codex",
		Name:        "Codex",
		Description: "Run Codex worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Codex understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `codex --model`.",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start a new Codex session, applying the
// no-update-check, hook-trust bypass, and approval flags, AO's session-flag
// activity hooks, the workspace trust override, optional system-prompt
// instructions, and the initial prompt (passed after `--` so a leading "-" is
// not read as a flag).
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.codexBinary(ctx)
	if err != nil {
		return nil, err
	}

	var providerArgs []string
	if err := appendSessionHookFlags(&providerArgs); err != nil {
		return nil, err
	}
	appendTerminalCompatibilityFlags(&providerArgs)
	return agentruntime.BuildLaunchCommand(agentruntime.LaunchConfig{
		Harness:          agentruntime.HarnessCodex,
		Binary:           binary,
		WorkspacePath:    cfg.WorkspacePath,
		Model:            cfg.Config.Model,
		Prompt:           cfg.Prompt,
		SystemPrompt:     cfg.SystemPrompt,
		SystemPromptFile: cfg.SystemPromptFile,
		Permission:       agentruntime.PermissionPolicy(cfg.Permissions),
		ProviderArgs:     providerArgs,
	})
}

// GetRestoreCommand rebuilds the argv that continues an existing Codex
// session: `codex resume <agentSessionId>`. ok is false when the hook-derived
// native session id has not landed yet, so callers can fall back to fresh
// launch behavior.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if _, ok := agentruntime.RestoreIdentity(
		agentruntime.HarnessCodex,
		cfg.Session.ID,
		cfg.Session.Metadata,
	); !ok {
		return nil, false, nil
	}
	binary, err := p.codexBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	var providerArgs []string
	if err := appendSessionHookFlags(&providerArgs); err != nil {
		return nil, false, err
	}
	appendTerminalCompatibilityFlags(&providerArgs)
	return agentruntime.BuildRestoreCommand(agentruntime.RestoreConfig{
		Harness:          agentruntime.HarnessCodex,
		Binary:           binary,
		SessionID:        cfg.Session.ID,
		Metadata:         cfg.Session.Metadata,
		WorkspacePath:    cfg.Session.WorkspacePath,
		Model:            cfg.Config.Model,
		Prompt:           cfg.Prompt,
		SystemPrompt:     cfg.SystemPrompt,
		SystemPromptFile: cfg.SystemPromptFile,
		Permission:       agentruntime.PermissionPolicy(cfg.Permissions),
		ProviderArgs:     providerArgs,
	})
}

// SessionInfo surfaces Codex hook-derived metadata. Metadata is intentionally
// nil for Codex: callers get the normalized fields directly.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// NativeConversationID bridges Codex's terminal resume id and app-server thread
// id. Codex uses the same native thread UUID on both surfaces; a TUI source must
// have reported it through its hook before it can switch without losing context.
func (p *Plugin) NativeConversationID(
	ctx context.Context,
	session ports.SessionRef,
	currentMode domain.SessionMode,
	providerConversationID string,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if currentMode == domain.SessionModeChat {
		id := strings.TrimSpace(providerConversationID)
		return id, id != "", nil
	}
	id := strings.TrimSpace(session.Metadata[ports.MetadataKeyAgentSessionID])
	return id, id != "", nil
}

// NativeConversationExists distinguishes a Codex thread UUID from a thread
// that app-server can actually resume. Codex returns the UUID from thread/start
// before the first user message materializes a rollout; thread/resume rejects
// that reserved-only UUID with "no rollout found for thread id".
//
// Active rollouts live below CODEX_HOME/sessions. Archived rollouts are
// deliberately excluded because Codex also rejects them from thread/resume.
// We only establish that a non-empty rollout exists; Codex remains responsible
// for parsing its own provider state.
func (p *Plugin) NativeConversationExists(
	ctx context.Context,
	_ ports.SessionRef,
	nativeConversationID string,
	env map[string]string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id, valid := canonicalCodexThreadID(nativeConversationID)
	if !valid {
		return false, nil
	}
	codexHome := strings.TrimSpace(env["CODEX_HOME"])
	if codexHome == "" {
		codexHome = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("codex: resolve rollout root: %w", err)
		}
		codexHome = filepath.Join(home, ".codex")
	}

	found := false
	sessionsDir := filepath.Join(codexHome, "sessions")
	err := filepath.WalkDir(sessionsDir, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !codexRolloutNameMatches(entry.Name(), id) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.Size() > 0 {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("codex: inspect rollout root %s: %w", sessionsDir, err)
	}
	return found, nil
}

func canonicalCodexThreadID(value string) (string, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

func codexRolloutNameMatches(name, nativeConversationID string) bool {
	if !strings.HasPrefix(name, "rollout-") {
		return false
	}
	suffix := "-" + nativeConversationID + ".jsonl"
	return strings.HasSuffix(name, suffix) || strings.HasSuffix(name, suffix+".zst")
}

// AuthStatus checks Codex's local login state without making a model call.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.codexBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := aoprocess.CommandContext(probeCtx, binary, "login", "status").CombinedOutput()
	if probeCtx.Err() != nil {
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	if status, ok := codexAuthStatusFromOutput(out); ok {
		return status, nil
	}
	// The probe is advisory. Version skew, transient startup failures, and
	// unfamiliar output are not proof that credentials are invalid; the actual
	// launch remains the authoritative check.
	_ = err
	return ports.AgentAuthStatusUnknown, nil
}

func codexAuthStatusFromOutput(out []byte) (ports.AgentAuthStatus, bool) {
	text := strings.ToLower(string(out))
	if strings.Contains(text, "not logged in") || strings.Contains(text, "logged out") {
		return ports.AgentAuthStatusUnauthorized, true
	}
	if strings.Contains(text, "logged in") {
		return ports.AgentAuthStatusAuthorized, true
	}
	return ports.AgentAuthStatusUnknown, false
}

// ResolveCodexBinary returns the path to the codex binary on this machine,
// searching platform-specific well-known install locations and PATH.
func ResolveCodexBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if runtime.GOOS == "windows" {
		candidates := []string{}
		if appData := os.Getenv("APPDATA"); appData != "" {
			shim := filepath.Join(appData, "npm", "codex.cmd")
			candidates = append(candidates, windowsNativeCodexCandidatesForShim(shim)...)
			candidates = append(candidates,
				filepath.Join(appData, "npm", "codex.exe"),
				shim,
			)
		}
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, ".cargo", "bin", "codex.exe"))
		}
		candidates = append(candidates, binaryutil.WindowsPackageManagerBinCandidates("codex")...)
		for _, candidate := range candidates {
			if fileExists(candidate) {
				return resolveNativeWindowsCodex(candidate), nil
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}

		for _, name := range []string{"codex.cmd", "codex", "codex.exe"} {
			path, err := exec.LookPath(name)
			if err == nil && path != "" {
				if isWindowsAppsCodexExecutable(path) {
					continue
				}
				return resolveNativeWindowsCodex(path), nil
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}

		return "", fmt.Errorf("codex: %w", ports.ErrAgentBinaryNotFound)
	}

	if path, err := exec.LookPath("codex"); err == nil && path != "" {
		return path, nil
	}

	candidates := []string{
		"/usr/local/bin/codex",
		"/opt/homebrew/bin/codex",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".npm-global", "bin", "codex"),
			filepath.Join(home, ".npm", "bin", "codex"),
			filepath.Join(home, ".local", "bin", "codex"),
			filepath.Join(home, ".cargo", "bin", "codex"),
		)
		candidates = append(candidates, binaryutil.UnixPackageManagerBinCandidates(home, "codex")...)
		nodeManagerCandidates, err := binaryutil.UnixNodeManagerBinCandidates(ctx, home, "codex")
		if err != nil {
			return "", err
		}
		candidates = append(candidates, nodeManagerCandidates...)
	}

	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}

	return "", fmt.Errorf("codex: %w", ports.ErrAgentBinaryNotFound)
}

func resolveNativeWindowsCodex(path string) string {
	if runtime.GOOS != "windows" || !strings.EqualFold(filepath.Ext(path), ".cmd") {
		return path
	}
	for _, candidate := range windowsNativeCodexCandidatesForShim(path) {
		if fileExists(candidate) {
			return candidate
		}
	}
	return path
}

func windowsNativeCodexCandidatesForShim(shim string) []string {
	dir := filepath.Dir(shim)
	return []string{
		filepath.Join(dir, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "bin", "codex.exe"),
		filepath.Join(dir, "node_modules", "@openai", "codex", "bin", "codex.exe"),
	}
}

func isWindowsAppsCodexExecutable(path string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	clean := strings.ToLower(filepath.Clean(path))
	base := filepath.Base(clean)
	return (base == "codex.exe" || base == "codex") &&
		strings.Contains(clean, string(filepath.Separator)+"windowsapps"+string(filepath.Separator)+"openai.codex_")
}

func (p *Plugin) codexBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveCodexBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

// DoctorLaunchProbes returns argv tails `ao doctor` runs against the installed
// codex binary to smoke-test the launch surface AO's hook delivery depends on.
// Probe 1 confirms --dangerously-bypass-hook-trust still exists (clap rejects
// unknown flags with a non-zero exit even alongside --version). Probe 2 loads
// codex's config with AO's `-c` session-flag overrides through the offline
// `features list` subcommand, so an override-parse regression surfaces as a
// non-zero exit or warning output. Both are built from the same flag builders
// the launch command uses, so the probes cannot drift from the real spawn argv.
func DoctorLaunchProbes() [][]string {
	flagProbe := make([]string, 0, 2)
	appendHookTrustBypassFlag(&flagProbe)
	flagProbe = append(flagProbe, "--version")

	overrideProbe := []string{"features", "list"}
	appendNoUpdateCheckFlag(&overrideProbe)
	appendHideRateLimitNudgeFlag(&overrideProbe)
	if err := appendSessionHookFlags(&overrideProbe); err != nil {
		// The probe only asks Codex to parse the hook config; a bare fallback
		// keeps that diagnostic available if the current executable cannot be
		// resolved, while real session launches fail closed above.
		appendSessionHookFlagsForExecutable(&overrideProbe, "ao")
	}
	appendWorkspaceTrustFlag(&overrideProbe, os.TempDir())
	return [][]string{flagProbe, overrideProbe}
}

func appendNoUpdateCheckFlag(cmd *[]string) {
	*cmd = append(*cmd, "-c", "check_for_update_on_startup=false")
}

func appendHideRateLimitNudgeFlag(cmd *[]string) {
	// When the account nears its rate limit, the Codex TUI interposes an
	// interactive "switch to a cheaper model?" dialog before the first turn.
	// In a headless AO pane that dialog hangs the session invisibly and
	// swallows the auto-submitted spawn prompt, so suppress it.
	*cmd = append(*cmd, "-c", "notice.hide_rate_limit_model_nudge=true")
}

func appendHookTrustBypassFlag(cmd *[]string) {
	// AO's activity hooks ride the launch command as session-flag config (see
	// appendSessionHookFlags) and carry no persisted trust hash in the user's
	// `[hooks.state]`. Without this flag Codex would hold them for an
	// interactive hooks review, leaving AO without activity signals.
	*cmd = append(*cmd, "--dangerously-bypass-hook-trust")
}

func appendTerminalCompatibilityFlags(cmd *[]string) {
	if runtime.GOOS == "windows" {
		*cmd = append(*cmd, "--no-alt-screen")
	}
}

// fileExists is a package var so tests can stub it to scope candidate probing.
var fileExists = func(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
