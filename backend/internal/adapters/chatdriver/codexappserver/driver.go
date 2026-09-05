package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
	"github.com/aoagents/agent-orchestrator/backend/internal/agentlaunch"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// clientName identifies AO to the provider. It shows up in the app-server's
// reported user agent, which makes a stray process attributable.
const (
	clientName    = "agent-orchestrator"
	clientTitle   = "Agent Orchestrator"
	clientVersion = "0.1.0"
	// This is the oldest Codex build whose complete Chat surface AO exercised:
	// thread start/resume, turn start/interrupt, approvals, and every advertised
	// extension. initialize plus model/list alone cannot prove those mutating
	// methods without creating provider state during preflight.
	minimumCodexVersion = "0.146.0"
)

// handshakeTimeout bounds initialize and thread open. These are local IPC calls
// that normally settle in well under a second.
const handshakeTimeout = 60 * time.Second

// codexPlugin is the subset of AO's existing Codex agent plugin that the Chat
// driver reuses. Binary resolution and local auth probing already live there and
// must not be reimplemented: a second copy would drift from what TUI sessions do.
type codexPlugin interface {
	ResolveBinary(ctx context.Context) (string, error)
	AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error)
}

// process is a running app-server, abstracted so tests can substitute pipes for
// a child process.
type process struct {
	stdin  io.WriteCloser
	stdout io.Reader
	// reconnected means the provider process and initialized protocol connection
	// survived a prior daemon. The replacement must not initialize/resume it a
	// second time.
	reconnected   bool
	nextRequestID int64
	// stop releases the process. It must be safe to call more than once.
	stop func() error
	// terminate destroys a persistent host for explicit session shutdown.
	terminate func() error
}

// spawnFunc launches an app-server. Injected so tests never exec anything.
type spawnFunc func(ctx context.Context, bin, workdir string, env []string) (*process, error)

type versionProbeFunc func(context.Context, string) (string, error)
type persistentConnectFunc func(context.Context, persistenthost.Config) (*persistenthost.Transport, error)

// Driver opens Codex conversations over `codex app-server`.
type Driver struct {
	plugin       codexPlugin
	log          *slog.Logger
	spawn        spawnFunc
	versionProbe versionProbeFunc
	persistent   bool
	connectHost  persistentConnectFunc
}

// New builds a Chat driver over the existing Codex agent plugin.
func New(plugin codexPlugin, log *slog.Logger) *Driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Driver{
		plugin: plugin, log: log, spawn: spawnAppServer,
		versionProbe: installedCodexVersion, persistent: true, connectHost: persistenthost.ConnectOrStart,
	}
}

var _ ports.ChatDriver = (*Driver)(nil)

// Harness reports which agent this driver serves.
func (d *Driver) Harness() domain.AgentHarness { return domain.HarnessCodex }

// capabilities is what a Codex app-server of a supported version provides. Each
// entry here was exercised against a live app-server rather than read off a doc.
func capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming:   true,
		ports.ChatCapabilityTools:       true,
		ports.ChatCapabilityApprovals:   true,
		ports.ChatCapabilityInterrupt:   true,
		ports.ChatCapabilityResume:      true,
		ports.ChatCapabilityHistory:     true,
		ports.ChatCapabilityUsage:       true,
		ports.ChatCapabilityDiffs:       true,
		ports.ChatCapabilityPlans:       true,
		ports.ChatCapabilityInteractive: true,
		ports.ChatCapabilityModels:      true,
		// The account's quota position is both pushed (account/rateLimits/updated)
		// and readable on demand (account/rateLimits/read), verified against a live
		// account.
		ports.ChatCapabilityRateLimits: true,
		// Without this a long conversation eventually cannot accept another turn at
		// all: every turn re-sends the history, so context fills on its own and the
		// only way back is to summarize what is already there.
		ports.ChatCapabilityCompaction: true,
		// History operations, all three exercised against a live app-server. Rollback
		// is advertised despite thread/rollback carrying a DEPRECATED annotation:
		// what the installed provider does is the only honest answer, and gating the
		// feature off while the call still works would take undo away for no reason.
		ports.ChatCapabilityRollback: true,
		ports.ChatCapabilityFork:     true,
		ports.ChatCapabilityRename:   true,
		ports.ChatCapabilitySkills:   true,
		// config/mcpServer/reload plus the status inventory read after it, both
		// exercised against a live app-server.
		ports.ChatCapabilityMCPReload: true,
		// Guidance into a turn already in flight, over turn/steer. Advertised only
		// after being driven against a live app-server (TestLiveSteerKeepsTheTurnAndItsWork
		// on codex-cli 0.146.0): the steered turn kept its id, emitted one
		// turn/started and one turn/completed, settled `completed` rather than
		// interrupted, and followed the correction. Strictly better than
		// interrupt-and-resend, which throws the turn's context and in-flight work
		// away.
		ports.ChatCapabilitySteer: true,
	}
}

// Probe reports what this install can do without creating a conversation, so an
// unsupported request can be refused before AO commits a session or worktree.
func (d *Driver) Probe(ctx context.Context) (ports.ChatCapabilities, error) {
	bin, err := d.plugin.ResolveBinary(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
	}

	// Authentication is owned by the daemon's active-account readiness check.
	// Probing the ambient device home here would reject a valid AO account (or
	// admit a different device account) before the managed runtime is launched.
	versionProbe := d.versionProbe
	if versionProbe == nil {
		versionProbe = installedCodexVersion
	}
	versionCtx, versionCancel := context.WithTimeout(ctx, 5*time.Second)
	versionOutput, versionErr := versionProbe(versionCtx, bin)
	versionCancel()
	if versionErr != nil {
		return nil, fmt.Errorf("%w: read Codex version: %w", ports.ErrChatDriverIncompatible, versionErr)
	}
	installed, ok := parseCodexVersion(versionOutput)
	if !ok {
		return nil, fmt.Errorf("%w: unrecognized Codex version %q (AO requires %s or newer)",
			ports.ErrChatDriverIncompatible, strings.TrimSpace(versionOutput), minimumCodexVersion)
	}
	minimum, _ := parseCodexVersion(minimumCodexVersion)
	if installed.less(minimum) {
		return nil, fmt.Errorf("%w: Codex %s is older than AO's tested minimum %s",
			ports.ErrChatDriverIncompatible, installed, minimumCodexVersion)
	}

	// Binary presence is not protocol compatibility. Complete the same initialize
	// handshake a real controller uses, then exercise model/list: it is part of
	// the surface AO advertises and a harmless read that catches older app-server
	// builds before a session row or worktree exists.
	workdir, err := os.Getwd()
	if err != nil || !filepath.IsAbs(workdir) {
		workdir = os.TempDir()
	}
	probeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	conv, err := d.connect(probeCtx, workdir, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conv.Close() }()
	var models struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := conv.conn.request(probeCtx, "model/list", map[string]any{}, &models); err != nil {
		return nil, fmt.Errorf("%w: model/list: %w", ports.ErrChatDriverIncompatible, err)
	}

	return capabilities(), nil
}

// DiscoverModels reads the account's current provider catalog without opening
// a Codex thread. The caller supplies the same project directory and environment
// overlay used for a normal launch so project-scoped Codex configuration applies.
func (d *Driver) DiscoverModels(ctx context.Context, workdir string, env map[string]string) ([]ports.ChatModel, error) {
	if !filepath.IsAbs(workdir) {
		var err error
		workdir, err = os.Getwd()
		if err != nil || !filepath.IsAbs(workdir) {
			workdir = os.TempDir()
		}
	}
	conv, err := d.connect(ctx, workdir, env)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conv.Close() }()
	return listModels(ctx, conv.conn)
}

type codexVersion [3]int

var codexVersionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func parseCodexVersion(output string) (codexVersion, bool) {
	match := codexVersionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return codexVersion{}, false
	}
	var version codexVersion
	for i := range version {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return codexVersion{}, false
		}
		version[i] = value
	}
	return version, true
}

func (v codexVersion) less(other codexVersion) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v codexVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

func installedCodexVersion(ctx context.Context, bin string) (string, error) {
	cmd := aoprocess.CommandContext(ctx, bin, "--version")
	cmd.Env = codexProcessEnv(ctx, bin, nil)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// Start opens a new Codex thread in the session worktree.
func (d *Driver) Start(ctx context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	if !filepath.IsAbs(cfg.WorkspacePath) {
		// app-server resolves a relative cwd against its own process directory,
		// which would silently put the agent in the wrong tree.
		return nil, fmt.Errorf("workspace path must be absolute, got %q", cfg.WorkspacePath)
	}

	conv, reconnected, err := d.connectSession(ctx, cfg.SessionID, cfg.DataDir, cfg.WorkspacePath, cfg.Env, cfg.AllowConcurrentHostReplacement)
	if err != nil {
		return nil, err
	}
	if reconnected {
		// A fresh-start request colliding with a surviving host is a durable-state
		// mismatch, not permission to destroy a provider that may still be working.
		_ = conv.Close()
		return nil, errors.New("persistent chat host already owns a provider conversation for a fresh session")
	}

	policy, sandbox := approvalSettings(cfg.Permissions)
	params := map[string]any{
		"cwd":               cfg.WorkspacePath,
		"approvalPolicy":    policy,
		"approvalsReviewer": approvalReviewer(cfg.Permissions),
		"sandbox":           sandbox,
	}
	if cfg.Model != "" {
		params["model"] = cfg.Model
	}
	if cfg.SystemPrompt != "" {
		params["developerInstructions"] = cfg.SystemPrompt
	}

	var resp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	openCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := conv.conn.request(openCtx, "thread/start", params, &resp); err != nil {
		_ = conv.Terminate()
		return nil, fmt.Errorf("thread/start: %w", err)
	}
	if resp.Thread.ID == "" {
		_ = conv.Terminate()
		return nil, errors.New("thread/start returned no thread id")
	}

	conv.start(resp.Thread.ID, resp.Model, resp.ReasoningEffort)
	return conv, nil
}

// Resume reattaches to a stored Codex thread after a daemon or app-server
// restart. A thread that is still running is rejoined rather than restarted.
func (d *Driver) Resume(ctx context.Context, cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
	if cfg.ProviderConversationID == "" {
		return nil, fmt.Errorf("%w: no stored thread id", ports.ErrChatResumeFailed)
	}
	if !filepath.IsAbs(cfg.WorkspacePath) {
		return nil, fmt.Errorf("workspace path must be absolute, got %q", cfg.WorkspacePath)
	}

	conv, reconnected, err := d.connectSession(ctx, cfg.SessionID, cfg.DataDir, cfg.WorkspacePath, cfg.Env, cfg.AllowConcurrentHostReplacement)
	if err != nil {
		return nil, err
	}
	if reconnected {
		// The host preserved the already-initialized app-server connection and its
		// loaded thread. Host replay bridges output and unresolved server requests
		// across the daemon detach without waiting for the active turn to settle.
		conv.start(cfg.ProviderConversationID, cfg.Model, "")
		return conv, nil
	}

	policy, sandbox := approvalSettings(cfg.Permissions)
	params := map[string]any{
		"threadId":          cfg.ProviderConversationID,
		"cwd":               cfg.WorkspacePath,
		"approvalPolicy":    policy,
		"approvalsReviewer": approvalReviewer(cfg.Permissions),
		"sandbox":           sandbox,
	}
	if cfg.Model != "" {
		params["model"] = cfg.Model
	}
	// thread/resume has no top-level effort field. Codex exposes persistent
	// reasoning effort as a config override, so carry the durable AO choice into
	// the resumed thread instead of silently falling back to the provider default.
	if cfg.Effort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": cfg.Effort}
	}
	// Developer instructions are launch context, not durable conversation
	// history. Reapply AO's current standing role when app-server reconstructs a
	// native thread, just as the TUI adapter does with its resume command.
	if cfg.SystemPrompt != "" {
		params["developerInstructions"] = cfg.SystemPrompt
	}
	resumeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var resp struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	err = conv.conn.request(resumeCtx, "thread/resume", params, &resp)
	if err != nil {
		_ = conv.Terminate()
		// Deliberately not falling back to thread/start: silently opening a new
		// conversation would present unrelated history as continuous.
		return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, err)
	}

	conv.start(cfg.ProviderConversationID, resp.Model, resp.ReasoningEffort)
	return conv, nil
}

// connect spawns app-server and completes the initialize handshake.
func (d *Driver) connect(ctx context.Context, workdir string, env map[string]string) (*conversation, error) {
	bin, err := d.plugin.ResolveBinary(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
	}

	proc, err := d.spawn(ctx, bin, workdir, codexProcessEnv(ctx, bin, env))
	if err != nil {
		return nil, fmt.Errorf("%w: launch app-server: %w", ports.ErrChatDriverUnavailable, err)
	}

	conv := newConversation(proc, d.log)
	if err := d.initialize(ctx, conv); err != nil {
		_ = conv.Close()
		return nil, err
	}
	return conv, nil
}

func (d *Driver) connectSession(
	ctx context.Context,
	sessionID domain.SessionID,
	dataDir, workdir string,
	env map[string]string,
	allowConcurrentHostReplacement bool,
) (*conversation, bool, error) {
	// Injected driver tests intentionally retain the direct pipe launcher. The
	// shipped driver uses spawnAppServer and therefore the persistent host.
	if !d.persistent {
		conv, err := d.connect(ctx, workdir, env)
		return conv, false, err
	}
	bin, err := d.plugin.ResolveBinary(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
	}
	transport, err := d.connectHost(ctx, persistenthost.Config{
		SessionID: string(sessionID),
		DataDir:   dataDir,
		Workdir:   workdir,
		Env:       envSlice(env),
		Argv:      []string{bin, "app-server"},
	})
	if err != nil {
		// Branch activation intentionally stages a replacement controller before
		// terminating the source. The persistent host correctly refuses that second
		// owner; retain the established safe handoff by staging this replacement in
		// a direct app-server. On the next daemon reconciliation it is resumed into a
		// persistent host. Other host failures fail closed and never spawn a rival.
		if allowConcurrentHostReplacement && errors.Is(err, persistenthost.ErrAttached) {
			conv, directErr := d.connect(ctx, workdir, env)
			return conv, false, directErr
		}
		if errors.Is(err, persistenthost.ErrOwnershipInconclusive) ||
			errors.Is(err, persistenthost.ErrAttached) ||
			errors.Is(err, persistenthost.ErrIncompatible) ||
			errors.Is(err, persistenthost.ErrUnauthorized) {
			return nil, false, fmt.Errorf("%w: persistent host: %w", ports.ErrChatRecoveryInconclusive, err)
		}
		return nil, false, fmt.Errorf("%w: persistent host: %w", ports.ErrChatDriverUnavailable, err)
	}
	proc := &process{
		stdin:         transport.Stdin,
		stdout:        transport.Stdout,
		reconnected:   transport.Reconnected,
		nextRequestID: transport.NextRequestID,
		stop:          transport.Stdin.Close,
		terminate: func() error {
			_ = transport.Stdin.Close()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return persistenthost.Shutdown(shutdownCtx, dataDir, string(sessionID))
		},
	}
	conv := newConversation(proc, d.log)
	if transport.Reconnected {
		return conv, true, nil
	}
	if err := d.initialize(ctx, conv); err != nil {
		_ = conv.Terminate()
		return nil, false, err
	}
	return conv, false, nil
}

func (d *Driver) initialize(ctx context.Context, conv *conversation) error {
	return initializeConnection(ctx, conv.conn)
}

func initializeConnection(ctx context.Context, connection *conn) error {
	initCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := connection.request(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": clientName, "title": clientTitle, "version": clientVersion},
		"capabilities": map[string]any{"experimentalApi": true, "optOutNotificationMethods": nil},
	}, nil); err != nil {
		return fmt.Errorf("%w: initialize: %w", ports.ErrChatDriverIncompatible, err)
	}
	if err := connection.notify("initialized", nil); err != nil {
		return fmt.Errorf("notify initialized: %w", err)
	}
	return nil
}

// approvalSettings maps AO's existing per-session permission mode onto Codex's
// approval policy and sandbox.
//
// The default matches what AO already passes a Codex TUI session
// (--dangerously-bypass-approvals-and-sandbox): AO sessions run in isolated
// worktrees and are expected to work without prompting. Chat does not quietly
// become stricter than the terminal path for the same setting.
func approvalSettings(mode ports.PermissionMode) (policy, sandbox string) {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits, ports.PermissionModeAuto:
		// on-request lets the provider decide when to ask; workspace-write keeps
		// edits inside the worktree.
		return "on-request", "workspace-write"
	default:
		return "never", "danger-full-access"
	}
}

// approvalReviewer selects whether Codex asks the user directly or first lets
// its built-in reviewer approve routine safe actions. Explicitly sending "user"
// also resets a thread that previously used auto review.
func approvalReviewer(mode ports.PermissionMode) string {
	if ports.NormalizePermissionMode(mode) == ports.PermissionModeAuto {
		return "auto_review"
	}
	return "user"
}

// spawnAppServer is the real launcher.
func spawnAppServer(ctx context.Context, bin, workdir string, env []string) (*process, error) {
	args := []string{"app-server"}
	cmd := aoprocess.Command(bin, args...)
	cmd.Dir = workdir
	if len(env) > 0 {
		cmd.Env = env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s app-server: %w", bin, err)
	}

	// Drain stderr so a chatty provider cannot fill the pipe buffer and wedge
	// its own process.
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	var stopped bool
	return &process{
		stdin:  stdin,
		stdout: stdout,
		stop: func() error {
			if stopped {
				return nil
			}
			stopped = true
			// Closing stdin is the graceful shutdown; kill only if it lingers.
			_ = stdin.Close()
			done := make(chan struct{})
			go func() { _, _ = cmd.Process.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
			}
			return nil
		},
	}, nil
}

// envSlice merges AO's session env OVER the daemon's own, in the KEY=VALUE form
// exec wants. Sorted so a relaunch is byte-identical, which makes process diffs
// readable.
//
// The merge is the point. AO's map is an OVERLAY -- session id, project id, the
// HookPATH-pinned PATH -- not a whole environment; the terminal path gets away
// with treating it as one only because tmux inherits the daemon's env underneath.
// Using it as a replacement launched the provider with eight variables and no
// HOME, USER, TMPDIR, LANG or SSH_AUTH_SOCK. The provider itself survived that
// (its home-directory lookup falls back to the passwd database), which is why it
// went unnoticed, but every shell command the agent runs inherits this env too:
// no SSH agent means `git push` over SSH fails, no HOME means global git config
// and every toolchain cache is missing. Found while writing the Claude driver,
// where the same shape failed outright with "Not logged in".
func envSlice(env map[string]string) []string {
	return processenv.Merge(env)
}

// codexProcessEnv applies the same executable-aware PATH augmentation as the
// TUI runtime before either the compatibility probe or app-server starts. The
// resolved npm launcher can be absolute and still depend on `#!/usr/bin/env
// node`; Finder-launched daemons commonly resolve the launcher from inventory
// while omitting the Node version manager from PATH.
func codexProcessEnv(ctx context.Context, bin string, env map[string]string) []string {
	overlay := make(map[string]string, len(env)+1)
	for key, value := range env {
		overlay[key] = value
	}
	if _, ok := overlay["PATH"]; !ok {
		overlay["PATH"] = os.Getenv("PATH")
	}
	agentlaunch.AugmentRuntimePATHForLaunchBinary(ctx, overlay, []string{bin}, exec.LookPath, agentlaunch.PinnedDir(os.Executable))
	return envSlice(overlay)
}
