// Package qodercli implements the Qoder CLI agent adapter: launching an
// interactive session inside a session's worktree, resuming it, and reading the
// session metadata its hooks report back into AO's store.
//
// Qoder CLI (binary "qodercli") is a gemini-cli fork whose CLI surface mirrors
// Claude Code: `--session-id` pins a native session UUID, `--resume <id>`
// continues one, `--permission-mode` and `--allowed-tools`/`--disallowed-tools`
// scope approvals, `--append-system-prompt[-file]` extends the system prompt,
// and a Claude-shaped hook system reports lifecycle events whose payload field
// names, notification types, and session-end reasons are Claude's. Activity
// derivation therefore reuses claudecode.DeriveActivityState, as grok does.
//
// Three things differ from the Claude Code adapter:
//
//   - The initial task is delivered with `-i`, not a positional argument: a
//     bare one-word prompt close to a subcommand name is rejected as an unknown
//     command.
//   - Hooks, workspace trust, and the auto-update switch travel in one inline
//     `--settings` JSON argument instead of a file in the worktree (see
//     settings.go).
//   - Tool scoping is one `--allowed-tools <rule>` flag per rule, where Claude
//     takes a single comma-joined value.
package qodercli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/nativeconfig"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// adapterID is the registry id and the value users pass to
	// `ao spawn --agent`.
	adapterID = "qodercli"

	// qoderDirName is Qoder CLI's user-level state directory under $HOME.
	qoderDirName = ".qoder"
)

// qodercliSessionNamespace derives a stable native session UUID from an AO
// session id. It is deliberately not Claude's namespace: one AO session may
// drive both harnesses over its lifetime, and the two ids should not collide.
var qodercliSessionNamespace = uuid.MustParse("6c8b1e70-5c4a-4a1e-9a4f-1b7c2d3e4f50")

// Plugin is the Qoder CLI agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Qoder CLI adapter.
func New() *Plugin { return &Plugin{} }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.AgentInterfaceHandoff = (*Plugin)(nil)
var _ ports.AgentInterfaceHandoffHistoryProbe = (*Plugin)(nil)

// EmitsSubmitActivity signals that Qoder CLI fires a UserPromptSubmit hook
// under AO's launch, so Activity.State can flip to active once a prompt is
// accepted. See ports.SubmitActivitySignaler.
func (p *Plugin) EmitsSubmitActivity() bool { return true }

// EmitsBlockedActivity signals that Qoder CLI fires the pre/post tool-use trio
// alongside PermissionRequest, so a permission dialog can flip Activity.State
// to blocked and the correlated post can clear it before the turn ends.
// PreToolUse carries tool_use_id and fires before the permission check, so
// lifecycle has an inflight entry to match PermissionRequest's tool_name
// against. See ports.BlockedActivitySignaler.
func (p *Plugin) EmitsBlockedActivity() bool { return true }

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Qoder CLI",
		Description: "Run Qoder CLI worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// permissionConfigEnum lists the permission modes the "permissions" config key
// accepts, mirroring the ports.PermissionMode constants.
var permissionConfigEnum = []string{
	string(ports.PermissionModeDefault),
	string(ports.PermissionModeAcceptEdits),
	string(ports.PermissionModeAuto),
	string(ports.PermissionModeBypassPermissions),
}

// GetConfigSpec reports the per-project agent config keys Qoder CLI understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `qodercli --model`.",
			},
			{
				Key:         "permissions",
				Type:        ports.ConfigFieldEnum,
				Description: "Starting permission mode.",
				Enum:        permissionConfigEnum,
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start an interactive Qoder CLI session:
//
//	qodercli --session-id <uuid> [--permission-mode <mode>]
//	         [--allowed-tools <rule>]... [--disallowed-tools <rule>]...
//	         [--model <m>] [--append-system-prompt-file <f> | --append-system-prompt <t>]
//	         --settings <json> [-i <task>]
//
// --session-id pins the native session UUID so GetRestoreCommand can rebuild
// `qodercli --resume <uuid>` later. AO's "default" permission mode emits no
// flag, so Qoder CLI resolves its starting mode from the user's own config.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	// Defense-in-depth: the project service validates on write, but re-check
	// here so a config written by any other path can't launch a bad command.
	if err := cfg.Config.Validate(); err != nil {
		return nil, fmt.Errorf("qodercli: %w", err)
	}

	binary, err := p.qodercliBinary(ctx)
	if err != nil {
		return nil, err
	}

	nativeID, err := launchSessionUUID(cfg)
	if err != nil {
		return nil, err
	}

	// A project's configured permissions drive the starting mode; an explicit
	// LaunchConfig.Permissions wins when set.
	permissions := cfg.Permissions
	if permissions == "" {
		permissions = cfg.Config.Permissions
	}

	cmd = []string{binary}
	if nativeID != "" {
		cmd = append(cmd, "--session-id", nativeID)
	}
	cmd = appendPermissionFlag(cmd, permissions)
	cmd = appendToolFlags(cmd, cfg.AllowedTools, cfg.DisallowedTools)
	cmd = appendModelFlag(cmd, cfg.Config)
	cmd, err = appendSystemPrompt(cmd, cfg.SystemPromptFile, cfg.SystemPrompt)
	if err != nil {
		return nil, err
	}

	settings, err := SettingsArg(cfg.DataDir, cfg.SessionID, cfg.WorkspacePath)
	if err != nil {
		return nil, err
	}
	cmd = append(cmd, "--settings", settings)

	if cfg.Prompt != "" {
		cmd = append(cmd, "-i", cfg.Prompt)
	}
	return cmd, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Qoder CLI
// session: `qodercli [flags] --settings <json> --resume <id> [-i <turn>]`. It
// prefers the hook-captured native session id and falls back to the
// deterministic UUID AO pinned at launch. ok is false when neither is
// available, so the caller fresh-spawns.
//
// --session-id is never combined with --resume: Qoder CLI rejects that pairing
// unless --fork-session is also passed.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" && cfg.Session.ID != "" {
		agentSessionID = SessionUUID(cfg.Session.ID)
	}
	// A non-UUID resume argument is read as an index into the recent-session
	// list ("--resume 1" means "the most recent session"), which would attach
	// this pane to an unrelated conversation. Only ever resume a real UUID.
	if !isUUID(agentSessionID) {
		return nil, false, nil
	}

	binary, err := p.qodercliBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = []string{binary}
	cmd = appendPermissionFlag(cmd, cfg.Permissions)
	cmd = appendToolFlags(cmd, cfg.AllowedTools, cfg.DisallowedTools)
	cmd = appendModelFlag(cmd, cfg.Config)
	cmd, err = appendSystemPrompt(cmd, cfg.SystemPromptFile, cfg.SystemPrompt)
	if err != nil {
		return nil, false, err
	}

	settings, err := SettingsArg(cfg.DataDir, cfg.Session.ID, cfg.Session.WorkspacePath)
	if err != nil {
		return nil, false, err
	}
	cmd = append(cmd, "--settings", settings, "--resume", agentSessionID)

	// RestoreConfig.Prompt is deliberately NOT put on the resume command line.
	// In an interactive session --resume is resolved asynchronously by the TUI
	// while -i auto-submits from an unrelated effect, so the turn can be sent
	// into a session that has not finished loading. AO delivers the turn
	// through the pane after startup instead, which is the documented
	// after-start path.
	return cmd, true, nil
}

// SessionInfo surfaces the normalized session metadata Qoder CLI's hooks
// persisted into AO's store: native session id, title, and summary. It reads
// only from session.Metadata — never from transcript files.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// NativeConversationID bridges Qoder CLI's terminal and ACP surfaces, which
// share one native session UUID: chat persists the id its ACP session reports,
// while terminal sessions use the hook-captured id or the pinned fallback.
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
	if id == "" && session.ID != "" {
		id = SessionUUID(session.ID)
	}
	return id, id != "", nil
}

// NativeConversationExists distinguishes a reserved session UUID from a
// conversation Qoder CLI has actually persisted. It writes
// <configRoot>/projects/<project-key>/<session-id>.jsonl lazily — only once the
// conversation has content — so a reserved-but-empty id cannot be resumed, and
// relaunching with a --session-id whose transcript does exist fails with
// "Session ID ... is already in use".
func (p *Plugin) NativeConversationExists(
	ctx context.Context,
	session ports.SessionRef,
	nativeConversationID string,
	env map[string]string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id := strings.TrimSpace(nativeConversationID)
	if !isUUID(id) {
		return false, nil
	}
	root, err := configRoot(env)
	if err != nil {
		return false, err
	}
	projectsDir := filepath.Join(root, "projects")

	// Qoder CLI files a transcript under a directory named after the project
	// root it was created in, and resolves --resume within the *current*
	// project only. Probing that one directory stops a same-id transcript from
	// another checkout reading as resumable here: that false positive would
	// emit --resume for an id this project cannot see, and Qoder CLI answers an
	// unresolvable id by opening its session-picker modal, which strands an
	// unattended pane.
	if buckets := transcriptBuckets(session.WorkspacePath); len(buckets) > 0 {
		for _, bucket := range buckets {
			exists, err := transcriptExists(filepath.Join(projectsDir, bucket, id+".jsonl"))
			if err != nil {
				return false, err
			}
			if exists {
				return true, nil
			}
		}
		return false, nil
	}

	// No workspace to scope by, or a path long enough that Qoder CLI appends a
	// hash suffix AO does not reproduce: fall back to scanning every bucket.
	projects, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("qodercli: read transcript root %s: %w", projectsDir, err)
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !project.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(projectsDir, project.Name(), id+".jsonl"))
		switch {
		case err == nil && info.Mode().IsRegular() && info.Size() > 0:
			return true, nil
		case err == nil, os.IsNotExist(err):
			continue
		default:
			return false, fmt.Errorf("qodercli: inspect transcript for %s: %w", id, err)
		}
	}
	return false, nil
}

func transcriptExists(path string) (bool, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		return info.Mode().IsRegular() && info.Size() > 0, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, fmt.Errorf("qodercli: inspect transcript %s: %w", path, err)
	}
}

// maxSanitizedProjectLength is where Qoder CLI starts appending a hash suffix
// to a project directory name.
const maxSanitizedProjectLength = 200

// transcriptBuckets names the directories a workspace's transcripts can live
// in. Qoder CLI derives the name from the project root it resolved at runtime,
// which is the symlink-resolved path — on macOS a /tmp workspace files its
// transcripts under "-private-tmp-…". AO's workspace path may be either form,
// so both are probed.
func transcriptBuckets(workspacePath string) []string {
	var buckets []string
	seen := map[string]bool{}
	for _, candidate := range []string{
		strings.TrimSpace(workspacePath),
		resolvedPath(workspacePath),
	} {
		if candidate == "" {
			continue
		}
		bucket, ok := transcriptBucket(candidate)
		if !ok || seen[bucket] {
			continue
		}
		seen[bucket] = true
		buckets = append(buckets, bucket)
	}
	return buckets
}

func resolvedPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(trimmed)
	if err != nil {
		return ""
	}
	return resolved
}

// transcriptBucket mirrors Qoder CLI's project-directory naming: every
// character that is not ASCII alphanumeric becomes a hyphen, counted the way
// JavaScript counts them (UTF-16 code units), so a non-ASCII path maps to the
// same name on both sides. ok is false for a path long enough to get the hash
// suffix AO deliberately does not reproduce.
func transcriptBucket(workspacePath string) (string, bool) {
	workspace := strings.TrimSpace(workspacePath)
	if workspace == "" {
		return "", false
	}
	var b strings.Builder
	for _, r := range workspace {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 0xFFFF:
			// One rune, two UTF-16 code units, two hyphens.
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	sanitized := b.String()
	if len(sanitized) > maxSanitizedProjectLength {
		return "", false
	}
	return sanitized, true
}

// configRoot resolves Qoder CLI's user-level state directory: QODER_CONFIG_DIR
// names the root outright, QODER_CLI_HOME replaces the home directory, and the
// default is ~/.qoder.
func configRoot(env map[string]string) (string, error) {
	if dir := lookupEnv(env, "QODER_CONFIG_DIR"); dir != "" {
		return filepath.Clean(dir), nil
	}
	if home := lookupEnv(env, "QODER_CLI_HOME"); home != "" {
		return filepath.Join(home, qoderDirName), nil
	}
	return nativeconfig.Resolve(env, "QODER_CONFIG_DIR", qoderDirName)
}

func lookupEnv(env map[string]string, key string) string {
	if value, ok := env[key]; ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(os.Getenv(key))
}

// launchSessionUUID resolves the native session UUID a fresh launch pins:
// AO's requested native id when it supplied one, otherwise the deterministic
// id derived from the AO session.
func launchSessionUUID(cfg ports.LaunchConfig) (string, error) {
	if native := strings.TrimSpace(cfg.NativeSessionID); native != "" {
		parsed, err := uuid.Parse(native)
		if err != nil {
			return "", fmt.Errorf("qodercli: invalid native session id: %w", err)
		}
		return parsed.String(), nil
	}
	if cfg.SessionID != "" {
		return SessionUUID(cfg.SessionID), nil
	}
	return "", nil
}

// SessionUUID maps an AO session id onto the native Qoder CLI session UUID used
// by --session-id and --resume. Qoder CLI validates the UUID shape, so a raw AO
// session id cannot be passed through.
func SessionUUID(aoSessionID string) string {
	return uuid.NewSHA1(qodercliSessionNamespace, []byte(aoSessionID)).String()
}

func isUUID(value string) bool {
	_, err := uuid.Parse(strings.TrimSpace(value))
	return err == nil && strings.TrimSpace(value) != ""
}

// appendPermissionFlag maps AO's permission modes onto Qoder CLI's
// --permission-mode choices. Default emits no flag so Qoder CLI resolves its
// starting mode from the user's own config.
func appendPermissionFlag(cmd []string, permissions ports.PermissionMode) []string {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeAcceptEdits:
		return append(cmd, "--permission-mode", "accept_edits")
	case ports.PermissionModeAuto:
		return append(cmd, "--permission-mode", "auto")
	case ports.PermissionModeBypassPermissions:
		return append(cmd, "--permission-mode", "bypass_permissions")
	default:
		return cmd
	}
}

// appendToolFlags scopes the session to a tool allowlist/denylist. Qoder CLI
// takes one rule per flag occurrence, unlike Claude Code's comma-joined value.
func appendToolFlags(cmd []string, allowed, disallowed []string) []string {
	for _, rule := range allowed {
		if rule = strings.TrimSpace(rule); rule != "" {
			cmd = append(cmd, "--allowed-tools", rule)
		}
	}
	for _, rule := range disallowed {
		if rule = strings.TrimSpace(rule); rule != "" {
			cmd = append(cmd, "--disallowed-tools", rule)
		}
	}
	return cmd
}

func appendModelFlag(cmd []string, cfg ports.AgentConfig) []string {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		return append(cmd, "--model", model)
	}
	return cmd
}

// appendSystemPrompt prefers AO's prompt file so standing instructions stay out
// of the terminal stream, falling back to the inline text.
func appendSystemPrompt(cmd []string, promptFile, prompt string) ([]string, error) {
	if promptFile != "" {
		info, err := os.Stat(promptFile)
		if err != nil {
			return nil, fmt.Errorf("qodercli: inspect system prompt file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("qodercli: system prompt file %q is not a regular file", promptFile)
		}
		return append(cmd, "--append-system-prompt-file", promptFile), nil
	}
	if prompt != "" {
		cmd = append(cmd, "--append-system-prompt", prompt)
	}
	return cmd, nil
}

// qodercliBinarySpec locates the qodercli binary: PATH first, then the standalone
// installer's symlink, npm global locations, and Homebrew. The `qoder` binary is
// deliberately not a candidate — it is an IDE dispatcher script that only
// forwards some invocations to qodercli.
var qodercliBinarySpec = binaryutil.BinarySpec{
	Label:         "qodercli",
	Names:         []string{"qodercli"},
	WinNames:      []string{"qodercli.cmd", "qodercli.exe", "qodercli"},
	UnixPaths:     []string{"/usr/local/bin/qodercli", "/opt/homebrew/bin/qodercli"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("qodercli"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "qodercli.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "qodercli.exe"}},
	},
}

// ResolveQodercliBinary returns the path to the qodercli binary, or a wrapped
// ports.ErrAgentBinaryNotFound when it is absent.
func ResolveQodercliBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, qodercliBinarySpec)
}

func (p *Plugin) qodercliBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveQodercliBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
