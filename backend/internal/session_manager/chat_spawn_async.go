package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Asynchronous Chat spawn.
//
// A synchronous spawn holds the API open for everything a session needs: a
// remote fetch, a worktree checkout, and the provider's own session/new. On a
// real repository that is seconds of staring at a spinner before the session
// the user just described is even visible.
//
// This path answers as soon as the two things the user interacts with exist —
// the session row and its conversation — and finishes the rest in the
// background. The opening prompt is recorded as a queued turn rather than sent,
// which is also what makes the in-between typeable: everything the user writes
// before the controller arrives lands in the same durable queue, in order, and
// the controller drains it when it starts.
//
// Only worker sessions take this path. An orchestrator owns a project-scoped
// narrative whose rebinding must stay ordered with its controller.

// asyncChatSpawn is the resolved spawn state handed to the background half.
type asyncChatSpawn struct {
	cfg               ports.SpawnConfig
	project           domain.ProjectRecord
	projectKind       domain.ProjectKind
	record            domain.SessionRecord
	branch            string
	prompt            string
	systemPrompt      string
	promptBytes       int
	systemPromptBytes int
}

// asyncChatSpawnEligible reports whether a resolved spawn can answer early.
func asyncChatSpawnEligible(cfg ports.SpawnConfig, mode domain.SessionMode) bool {
	return cfg.Async &&
		mode == domain.SessionModeChat &&
		cfg.Kind == domain.KindWorker
}

// beginAsyncChatSpawn publishes the session, records the opening prompt in the
// durable queue, and hands the rest to the background.
func (m *Manager) beginAsyncChatSpawn(ctx context.Context, in asyncChatSpawn) (domain.SessionRecord, int, int, error) {
	id := in.record.ID
	rec, err := m.setProvisionState(ctx, id, domain.SessionProvisionProvisioning, "")
	if err != nil {
		m.rollbackSpawnSeedRowAfterFailure(ctx, id)
		return domain.SessionRecord{}, 0, 0, wrapSpawnStage(id, ErrSpawnCreate, err)
	}
	if in.prompt != "" {
		if _, err := m.chat.QueueChatPrompt(ctx, id, in.prompt); err != nil {
			m.rollbackSpawnSeedRowAfterFailure(ctx, id)
			return domain.SessionRecord{}, 0, 0, wrapSpawnStage(id, ErrSpawnDeliverPrompt, err)
		}
		rec, err = m.getRecord(ctx, id)
		if err != nil {
			return domain.SessionRecord{}, 0, 0, err
		}
	}
	in.record = rec
	// From here the id is the client's: every later failure must leave a session
	// it can still open, never a deleted row.
	m.markSpawnPublished(id)
	m.runInBackground(func() {
		// The HTTP request that started this is already answered; its context is
		// gone. The work continues under the daemon's lifetime instead.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncChatSpawnBudget)
		defer cancel()
		m.completeAsyncChatSpawn(bg, in)
	})
	return rec, in.promptBytes, in.systemPromptBytes, nil
}

// asyncChatSpawnBudget bounds a background start so a hung provider leaves a
// failed session the user can act on rather than a permanent "starting".
const asyncChatSpawnBudget = 10 * time.Minute

// completeAsyncChatSpawn builds everything the early answer skipped. Failures
// mark the session failed instead of deleting it: the user is already looking
// at the session, and their queued messages live in it.
func (m *Manager) completeAsyncChatSpawn(ctx context.Context, in asyncChatSpawn) {
	id := in.record.ID
	baseRefs := m.refreshDefaultBranchesBestEffort(ctx, in.project)
	ws, workspaceProject, err := m.createSessionWorkspace(ctx, in.project, in.cfg, id, in.branch, baseRefs)
	if err != nil {
		m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrWorkspaceCreate, err))
		return
	}
	if err := m.provisionWorkspace(ctx, in.project, ws.Path); err != nil {
		m.destroySpawnWorkspace(ctx, ws, workspaceProject)
		m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrWorkspaceProvision, err))
		return
	}
	if len(in.cfg.Attachments) > 0 {
		// The prompt already references these by name (spawnAttachmentRefs); this
		// is where the bytes land, before the agent can read them.
		if _, err := m.writeSpawnAttachments(ctx, id, ws.Path, in.cfg.Attachments); err != nil {
			m.destroySpawnWorkspace(ctx, ws, workspaceProject)
			m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrSpawnAttachments, err))
			return
		}
		if err := m.workspace.AddExclude(ctx, ws, "/"+attachmentsDir+"/"); err != nil {
			m.logger.Warn("spawn: exclude attachments dir", "sessionID", id, "error", err)
		}
	}
	// Anything the user attached while this was starting was written canonically
	// only, because there was no worktree to put it in. Replay it now, before the
	// controller can read the turn that references those paths.
	if err := m.restoreAttachments(ctx, id, ws); err != nil {
		m.logger.Warn("spawn: materialize attachments staged while provisioning",
			"sessionID", id, "error", err)
	}

	// Publish the worktree now rather than at the controller commit. Until the
	// row carries it, every workspace-scoped read answers
	// SESSION_WORKSPACE_NOT_FOUND, and the provider start that follows is long
	// enough for the desktop's bounded readiness poll to give up on a session
	// that is perfectly fine. It also means an interrupted start leaves a row
	// that knows which worktree to clean up.
	m.publishProvisionedWorkspace(ctx, id, ws)

	record, err := m.getRecord(ctx, id)
	if err != nil {
		m.destroySpawnWorkspace(ctx, ws, workspaceProject)
		m.failAsyncChatSpawn(ctx, id, err)
		return
	}
	if _, err := m.launchChatController(ctx, chatSpawn{
		cfg:              in.cfg,
		project:          in.project,
		projectKind:      in.projectKind,
		record:           record,
		workspace:        ws,
		workspaceProject: workspaceProject,
		prompt:           in.prompt,
		systemPrompt:     in.systemPrompt,
		// The opening prompt is already a queued turn; the drain below delivers
		// it together with anything typed while this was starting.
		promptQueued: true,
	}); err != nil {
		m.failAsyncChatSpawn(ctx, id, err)
		return
	}
	m.clearSpawnPublished(id)
	if _, err := m.setProvisionState(ctx, id, domain.SessionProvisionReady, ""); err != nil {
		m.logger.Error("spawn: publish provisioned session", "sessionID", id, "error", err)
	}
	if err := m.chat.DrainChatQueue(ctx, id); err != nil {
		m.logger.Error("spawn: dispatch queued prompt", "sessionID", id, "error", err)
	}
}

// failAsyncChatSpawn records why a background start stopped. The row, its
// conversation, and its queue survive so the failure is something the user can
// read and retry rather than a session that silently disappeared.
func (m *Manager) failAsyncChatSpawn(ctx context.Context, id domain.SessionID, cause error) {
	m.logger.Error("spawn: asynchronous chat start failed", "sessionID", id, "error", cause)
	defer m.clearSpawnPublished(id)
	cleanupCtx, cancel := spawnRollbackContext(ctx)
	defer cancel()
	m.stopChatBestEffort(cleanupCtx, id)
	if _, err := m.setProvisionState(cleanupCtx, id, domain.SessionProvisionFailed, cause.Error()); err != nil {
		m.logger.Error("spawn: record failed start", "sessionID", id, "error", err)
	}
}

func (m *Manager) setProvisionState(
	ctx context.Context,
	id domain.SessionID,
	state domain.SessionProvisionState,
	message string,
) (domain.SessionRecord, error) {
	writer, ok := m.store.(provisionStateStore)
	if !ok {
		return m.getRecord(ctx, id)
	}
	if _, err := writer.SetSessionProvisionState(ctx, id, state, message, m.clock()); err != nil {
		return domain.SessionRecord{}, err
	}
	return m.getRecord(ctx, id)
}

// provisionStateStore is the narrow optional write boundary for start-up
// progress. An embedder without it runs synchronous spawns unchanged.
type provisionStateStore interface {
	SetSessionProvisionState(ctx context.Context, id domain.SessionID, state domain.SessionProvisionState, message string, now time.Time) (bool, error)
}

// FailInterruptedProvisioning marks sessions whose background start did not
// survive a daemon restart. Without this a row left mid-start reads as
// "starting" forever: nothing is running that could ever finish it.
func (m *Manager) FailInterruptedProvisioning(ctx context.Context) error {
	recs, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("list sessions for interrupted starts: %w", err)
	}
	var failures []error
	for _, rec := range recs {
		if rec.IsTerminated || !rec.ProvisionState.IsProvisioning() {
			continue
		}
		if _, err := m.setProvisionState(ctx, rec.ID, domain.SessionProvisionFailed,
			"AO restarted before this session finished starting"); err != nil {
			failures = append(failures, fmt.Errorf("session %s: %w", rec.ID, err))
		}
	}
	return errors.Join(failures...)
}

// runInBackground runs work outside the caller's request. The seam exists so
// tests can observe a completed spawn without sleeping.
func (m *Manager) runInBackground(work func()) {
	if m.runBackground != nil {
		m.runBackground(work)
		return
	}
	go work()
}

// spawnPublished reports whether the API has already handed this session's id to
// a client, which makes the row something a user can be looking at rather than
// spawn scratch space. It is intentionally in-memory: a daemon that restarted is
// not serving anyone the same in-flight spawn, and FailInterruptedProvisioning
// settles those rows instead.
func (m *Manager) spawnPublished(id domain.SessionID) bool {
	m.publishedSpawnMu.Lock()
	defer m.publishedSpawnMu.Unlock()
	_, ok := m.publishedSpawns[id]
	return ok
}

func (m *Manager) markSpawnPublished(id domain.SessionID) {
	m.publishedSpawnMu.Lock()
	defer m.publishedSpawnMu.Unlock()
	if m.publishedSpawns == nil {
		m.publishedSpawns = make(map[domain.SessionID]struct{})
	}
	m.publishedSpawns[id] = struct{}{}
}

func (m *Manager) clearSpawnPublished(id domain.SessionID) {
	m.publishedSpawnMu.Lock()
	defer m.publishedSpawnMu.Unlock()
	delete(m.publishedSpawns, id)
}

// publishProvisionedWorkspace records the worktree on a still-provisioning row.
// Best effort: the controller commit writes the same facts again, so a failure
// here costs visibility during the start, never correctness after it.
func (m *Manager) publishProvisionedWorkspace(ctx context.Context, id domain.SessionID, ws ports.WorkspaceInfo) {
	writer, ok := m.store.(provisionedWorkspaceStore)
	if !ok {
		return
	}
	if _, err := writer.SetSessionProvisionedWorkspace(
		ctx, id, ws.Branch, ws.Path, ws.RepoPath, m.clock()); err != nil {
		m.logger.Warn("spawn: publish provisioned workspace", "sessionID", id, "error", err)
	}
}

// provisionedWorkspaceStore is the narrow optional write boundary for a
// worktree that exists before its controller does.
type provisionedWorkspaceStore interface {
	SetSessionProvisionedWorkspace(ctx context.Context, id domain.SessionID, branch, workspacePath, workspaceRepoPath string, now time.Time) (bool, error)
}
