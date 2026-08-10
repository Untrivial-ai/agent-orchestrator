package sessionmanager

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The chat-mode controller launch.
//
// It is deliberately a separate path rather than conditionals threaded through
// the terminal launch: a chat session starts no agent process inside a runtime,
// has no pane to write into, and has no prompt-delivery strategy. Sharing that
// code would mean every step asking which kind of session it is in.
//
// What the two modes DO share is everything above this point — project
// resolution, prompts, the seed row, the worktree, provisioning, attachments —
// which is why the branch sits where it does and not higher.

// ChatLauncher starts the structured controller for a chat session. Implemented
// by the chat service; nil in a build without chat support.
type ChatLauncher interface {
	// PreflightChat reports whether a harness can start in chat mode right now.
	// Called before any durable state exists so an unsupported request costs
	// nothing.
	PreflightChat(ctx context.Context, harness domain.AgentHarness) error
	// StartChat launches the controller and returns the provider conversation
	// handle to persist for resume. Implementations must call ControllerReady
	// after the provider and generation exist but before consuming live events.
	StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error)
	// StartChatTurn delivers the initial prompt as a normal turn. There is no
	// paste-and-Enter equivalent in chat mode: the provider either accepts the
	// turn or reports why.
	StartChatTurn(ctx context.Context, id domain.SessionID, text string) (string, error)
	// RelayChatTurn delivers a message AO is carrying on someone else's behalf —
	// `ao send`, an orchestrator writing to a worker, an automation — as a turn
	// attributed to automation rather than to the human at the keyboard.
	RelayChatTurn(ctx context.Context, id domain.SessionID, text string) (string, error)
	// RelayChatTurnWithID is the durable-retry form. Implementations must pass
	// the key through to ChatUserMessage so retry after an uncertain outbox
	// acknowledgement cannot create a second provider turn.
	RelayChatTurnWithID(ctx context.Context, id domain.SessionID, text, clientMessageID string) (string, error)
	// HasLiveChatController reports whether the daemon still owns a controller
	// for the session. Resume uses this to distinguish a stale durable activity
	// state left by an older daemon from a genuinely live controller.
	HasLiveChatController(id domain.SessionID) bool
	// StopChat releases a session's controller.
	StopChat(ctx context.Context, id domain.SessionID) error
}

// ChatStart is what the launcher needs. It mirrors the terminal path's
// LaunchConfig in spirit: everything resolved, nothing left to look up.
type ChatStart struct {
	SessionID     domain.SessionID
	ProjectID     domain.ProjectID
	Kind          domain.SessionKind
	Harness       domain.AgentHarness
	DataDir       string
	WorkspacePath string
	// Env carries the HookPATH-pinned PATH, which is how the agent's own shell
	// commands find `ao`. An orchestrator delegates by running `ao spawn`, so
	// without this a chat orchestrator could talk but not work.
	Env                   map[string]string
	Model                 string
	Permissions           ports.PermissionMode
	SystemPrompt          string
	AdditionalDirectories []string
	// ProviderConversationID resumes a stored conversation instead of opening a
	// new one. Empty means start fresh.
	ProviderConversationID string
	// ControllerReady commits the durable controller facts before the provider
	// event stream is consumed. This prevents an immediate exit from racing a
	// later MarkSpawned write back to idle.
	ControllerReady func(ChatStarted) error
}

// ChatStarted is the durable result of a launch.
type ChatStarted struct {
	ProviderConversationID string
	ControllerGeneration   string
}

// chatSpawn bundles the shared state the chat launch needs from Spawn, so the
// signature does not grow to a dozen positional arguments.
type chatSpawn struct {
	cfg              ports.SpawnConfig
	project          domain.ProjectRecord
	projectKind      domain.ProjectKind
	record           domain.SessionRecord
	workspace        ports.WorkspaceInfo
	workspaceProject *ports.WorkspaceProjectInfo
	prompt           string
	systemPrompt     string
}

// launchChatController starts the provider controller for a chat session and
// marks it spawned.
//
// Rollback mirrors the terminal path's: a failure before the controller exists
// tears the workspace down, and a failure after it exists closes the controller
// first so no app-server process is left behind holding the worktree.
func (m *Manager) launchChatController(ctx context.Context, in chatSpawn) (domain.SessionRecord, error) {
	id := in.record.ID
	agentConfig := effectiveAgentConfig(in.cfg.Kind, in.project.Config, "")

	// The same env the terminal path builds, including the HookPATH pin. The
	// provider passes its environment through to the shell commands it runs, so
	// this is what makes `ao` resolvable to the agent.
	env := m.runtimeEnv(id, in.cfg.ProjectID, in.cfg.IssueID, in.project.Config.Env)
	var diffBaseSHA, diffBaseRef string
	if in.projectKind == domain.ProjectKindSingleRepo {
		diffBaseSHA, diffBaseRef = resolveSpawnDiffBase(
			ctx, in.workspace.Path, in.project.Config.WithDefaults().DefaultBranch)
	}

	var (
		controllerCommitted bool
		completionErr       error
	)
	_, err := m.chat.StartChat(ctx, ChatStart{
		SessionID:             id,
		ProjectID:             in.cfg.ProjectID,
		Kind:                  in.cfg.Kind,
		Harness:               in.cfg.Harness,
		DataDir:               m.dataDir,
		WorkspacePath:         in.workspace.Path,
		Env:                   env,
		Model:                 agentConfig.Model,
		Permissions:           agentConfig.Permissions,
		SystemPrompt:          in.systemPrompt,
		AdditionalDirectories: workspaceProjectDirectories(in.workspace.Path, in.workspaceProject),
		ControllerReady: func(started ChatStarted) error {
			metadata := domain.SessionMetadata{
				Branch:            in.workspace.Branch,
				WorkspacePath:     in.workspace.Path,
				WorkspaceRepoPath: in.workspace.RepoPath,
				Prompt:            in.prompt,
				DiffBaseSHA:       diffBaseSHA,
				DiffBaseRef:       diffBaseRef,
				// No RuntimeHandleID or RuntimeLaunchID: a chat session has no
				// agent pane. Leaving them empty keeps the reaper from probing for
				// a terminal that was never created.
				ProviderConversationID: started.ProviderConversationID,
				ControllerGeneration:   started.ControllerGeneration,
			}
			completionErr = m.lcm.MarkSpawned(ctx, id, metadata)
			controllerCommitted = completionErr == nil
			return completionErr
		},
	})
	if err != nil {
		if completionErr != nil || controllerCommitted {
			m.stopChatBestEffort(ctx, id)
			m.rollbackPreparedSpawnWorkspace(ctx, in.record, in.workspace, in.workspaceProject, true)
			m.markSpawnFailedTerminated(ctx, id)
			if completionErr != nil {
				return domain.SessionRecord{}, fmt.Errorf("spawn %s: completed: %w", id, completionErr)
			}
			return domain.SessionRecord{}, fmt.Errorf("spawn %s: chat controller: %w", id, err)
		}
		// No controller exists, so nothing provider-side needs closing. The
		// runtime was never touched, hence runtimeDestroyed=false.
		m.rollbackSeedSpawnWorkspace(ctx, in.record, in.workspace, in.workspaceProject, false)
		return domain.SessionRecord{}, fmt.Errorf("spawn %s: chat controller: %w", id, err)
	}

	// The initial prompt is a normal turn through the controller. There is no
	// paste-and-Enter equivalent here, and no "deliver after start" variant: the
	// provider either accepts the turn or reports why.
	if in.prompt != "" {
		if _, err := m.chat.StartChatTurn(ctx, id, in.prompt); err != nil {
			m.stopChatBestEffort(ctx, id)
			m.rollbackPreparedSpawnWorkspace(ctx, in.record, in.workspace, in.workspaceProject, true)
			m.markSpawnFailedTerminated(ctx, id)
			return domain.SessionRecord{}, fmt.Errorf("spawn %s: deliver prompt: %w", id, err)
		}
	}

	return m.getRecord(ctx, id)
}

// stopChatBestEffort closes a controller during rollback. A failure here is
// logged rather than returned: the spawn is already failing, and the caller's
// error is the one worth surfacing.
func (m *Manager) stopChatBestEffort(ctx context.Context, id domain.SessionID) {
	if m.chat == nil {
		return
	}
	if err := m.chat.StopChat(ctx, id); err != nil {
		m.logger.Warn("spawn rollback: close chat controller", "sessionID", id, "error", err)
	}
}

// sendChat routes an outbound message into a chat session's conversation.
//
// It reports handled=false for anything that is not a live chat session, so the
// terminal path below it is reached unchanged — including for a session this
// build cannot serve, where the runtime guard's refusal is still the right answer.
//
// The refusals here mirror the terminal path's: a terminated session cannot
// receive a message, and one whose controller is gone cannot either. Busy is not
// a refusal — the controller queues a mid-turn message, which is strictly better
// than the terminal path's habit of dropping a nudge it cannot safely deliver.
func (m *Manager) sendChat(ctx context.Context, id domain.SessionID, message, clientMessageID string) (bool, error) {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return false, fmt.Errorf("send %s: session: %w", id, err)
	}
	if !ok || domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeChat {
		return false, nil
	}
	if m.chat == nil {
		return true, fmt.Errorf("send %s: %w: chat mode is not available in this build",
			id, ports.ErrChatUnsupported)
	}
	if rec.IsTerminated {
		return true, fmt.Errorf("send %s: %w", id, ErrTerminated)
	}
	var relayErr error
	if clientMessageID != "" {
		_, relayErr = m.chat.RelayChatTurnWithID(ctx, id, message, clientMessageID)
	} else {
		_, relayErr = m.chat.RelayChatTurn(ctx, id, message)
	}
	if relayErr != nil {
		return true, fmt.Errorf("send %s: %w", id, relayErr)
	}
	return true, nil
}

// SessionModeDefaults supplies the daemon-owned default session interface.
type SessionModeDefaults interface {
	DefaultSessionMode(ctx context.Context) domain.SessionMode
}

// resolveSessionMode applies the precedence for a spawn:
//
//  1. the mode the caller explicitly requested;
//  2. the daemon-owned default;
//  3. the compatibility default, TUI.
//
// The default is read here, at spawn time, so changing the preference affects only
// sessions created afterwards. An existing session changes only through an explicit,
// capability-gated interface transition; it is never re-resolved from the default.
func (m *Manager) resolveSessionMode(ctx context.Context, requested domain.SessionMode) domain.SessionMode {
	if requested.Valid() {
		return requested
	}
	if m.defaults == nil {
		return domain.DefaultSessionMode
	}
	return domain.NormalizeSessionMode(m.defaults.DefaultSessionMode(ctx))
}

// resumeChatController reattaches a chat session to its provider conversation
// after a daemon restart.
//
// It resumes the stored conversation rather than starting a new one. A resume
// that fails is reported, not silently replaced with a fresh conversation:
// presenting unrelated history as continuous is worse than an error the user can
// act on, and it would strand the work the old conversation was holding.
func (m *Manager) resumeChatController(
	ctx context.Context,
	operation string,
	rec domain.SessionRecord,
	project domain.ProjectRecord,
	ws ports.WorkspaceInfo,
) (RestoreResult, error) {
	if m.chat == nil {
		return RestoreResult{}, fmt.Errorf("%s %s: %w: chat mode is not available in this build",
			operation, rec.ID, ports.ErrChatUnsupported)
	}

	// Recomputed rather than persisted, matching the terminal path: a restored
	// session keeps its standing instructions across the relaunch.
	systemPrompt, err := m.buildSystemPrompt(ctx, rec.Kind, rec.ProjectID)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%s %s: system prompt: %w", operation, rec.ID, err)
	}

	agentConfig := effectiveAgentConfig(rec.Kind, project.Config, "")
	additionalDirectories, err := m.restoredWorkspaceProjectDirectories(ctx, rec, project, ws.Path)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%s %s: workspace roots: %w", operation, rec.ID, err)
	}
	var completionErr error
	_, err = m.chat.StartChat(ctx, ChatStart{
		SessionID:             rec.ID,
		ProjectID:             rec.ProjectID,
		Kind:                  rec.Kind,
		Harness:               rec.Harness,
		DataDir:               m.dataDir,
		WorkspacePath:         ws.Path,
		Env:                   m.runtimeEnv(rec.ID, rec.ProjectID, rec.IssueID, project.Config.Env),
		Model:                 agentConfig.Model,
		Permissions:           agentConfig.Permissions,
		SystemPrompt:          systemPrompt,
		AdditionalDirectories: additionalDirectories,
		// The handle that makes this a resume rather than a new conversation.
		ProviderConversationID: rec.Metadata.ProviderConversationID,
		ControllerReady: func(started ChatStarted) error {
			metadata := rec.Metadata
			metadata.WorkspacePath = ws.Path
			metadata.WorkspaceRepoPath = ws.RepoPath
			if ws.Branch != "" {
				metadata.Branch = ws.Branch
			}
			metadata.ProviderConversationID = started.ProviderConversationID
			// A fresh generation per launch: events still arriving from the
			// controller this one replaced carry the old one and are rejected.
			metadata.ControllerGeneration = started.ControllerGeneration

			completionErr = m.lcm.MarkSpawned(ctx, rec.ID, metadata)
			return completionErr
		},
	})
	if err != nil {
		if completionErr != nil {
			m.stopChatBestEffort(ctx, rec.ID)
			return RestoreResult{}, fmt.Errorf("%s %s: completed: %w", operation, rec.ID, completionErr)
		}
		return RestoreResult{}, fmt.Errorf("%s %s: resume chat: %w", operation, rec.ID, err)
	}

	restored, err := m.getRecord(ctx, rec.ID)
	if err != nil {
		return RestoreResult{}, err
	}
	// Native continuity: the provider still holds the conversation, so the agent
	// resumes with its own history rather than a replayed prompt.
	return RestoreResult{Session: restored, Mode: RestoreModeNative}, nil
}

func workspaceProjectDirectories(root string, project *ports.WorkspaceProjectInfo) []string {
	if project == nil {
		return nil
	}
	root = filepath.Clean(root)
	directories := make([]string, 0, len(project.Worktrees))
	for _, worktree := range project.Worktrees {
		if worktree.Path == "" || filepath.Clean(worktree.Path) == root {
			continue
		}
		directories = append(directories, worktree.Path)
	}
	return directories
}

func (m *Manager) restoredWorkspaceProjectDirectories(
	ctx context.Context,
	rec domain.SessionRecord,
	project domain.ProjectRecord,
	root string,
) ([]string, error) {
	if project.Kind.WithDefault() != domain.ProjectKindWorkspace {
		return nil, nil
	}
	rows, err := m.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	directories := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.WorktreePath == "" || filepath.Clean(row.WorktreePath) == root {
			continue
		}
		directories = append(directories, row.WorktreePath)
	}
	return directories, nil
}
