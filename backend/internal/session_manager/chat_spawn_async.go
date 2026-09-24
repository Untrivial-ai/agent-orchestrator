package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

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
	preparation       *taskPreparation
	releaseHarness    func()
	retry             bool
}

type asyncChatSpawnRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// beginAsyncChatSpawn publishes the session, records the opening prompt in the
// durable queue, and hands the rest to the background.
func (m *Manager) beginAsyncChatSpawn(ctx context.Context, in asyncChatSpawn) (domain.SessionRecord, int, int, error) {
	id := in.record.ID
	rollback := func() {
		if in.retry {
			return // the session and its existing queue already belong to the user
		}
		if in.preparation == nil {
			m.rollbackSpawnSeedRowAfterFailure(ctx, id)
			return
		}
		cleanupCtx, cancel := spawnRollbackContext(ctx)
		m.discardClaimedTaskPreparation(cleanupCtx, in.preparation)
		cancel()
	}
	rec, err := m.setProvisionState(ctx, id, domain.SessionProvisionProvisioning, "")
	if err != nil {
		rollback()
		return domain.SessionRecord{}, 0, 0, wrapSpawnStage(id, ErrSpawnCreate, err)
	}
	if !in.retry && len(in.cfg.Attachments) > 0 {
		stageStarted := time.Now()
		for i, attachment := range in.cfg.Attachments {
			if err := m.attachments.PutCanonical(ctx, id, spawnAttachmentName(i, attachment), attachment.Data); err != nil {
				rollback()
				return domain.SessionRecord{}, 0, 0, wrapSpawnStage(id, ErrSpawnAttachments, err)
			}
		}
		m.logAsyncChatSpawnStage(id, "spawn_attachments", stageStarted)
	}
	if in.prompt != "" {
		if _, err := m.chat.QueueChatPrompt(ctx, id, in.prompt); err != nil {
			rollback()
			return domain.SessionRecord{}, 0, 0, wrapSpawnStage(id, ErrSpawnDeliverPrompt, err)
		}
		rec, err = m.getRecord(ctx, id)
		if err != nil {
			rollback()
			return domain.SessionRecord{}, 0, 0, err
		}
	}
	in.record = rec
	bg, cancel := context.WithTimeout(m.backgroundContext, asyncChatSpawnBudget)
	run := &asyncChatSpawnRun{cancel: cancel, done: make(chan struct{})}
	m.asyncChatSpawnsMu.Lock()
	m.asyncChatSpawns[id] = run
	m.asyncChatSpawnsMu.Unlock()
	m.runInBackground(func() {
		if in.retry {
			defer m.endAgentResume(id)
		}
		defer func() {
			if in.releaseHarness != nil {
				in.releaseHarness()
			}
			cancel()
			m.asyncChatSpawnsMu.Lock()
			if m.asyncChatSpawns[id] == run {
				delete(m.asyncChatSpawns, id)
			}
			close(run.done)
			m.asyncChatSpawnsMu.Unlock()
		}()
		m.completeAsyncChatSpawn(bg, in)
	})
	return rec, in.promptBytes, in.systemPromptBytes, nil
}

// asyncChatSpawnBudget bounds a background start so a hung provider leaves a
// failed session the user can act on rather than a permanent "starting".
const asyncChatSpawnBudget = 10 * time.Minute

// completeAsyncChatSpawn keeps the visible session and queue on failure.
func (m *Manager) completeAsyncChatSpawn(ctx context.Context, in asyncChatSpawn) {
	id := in.record.ID
	totalStarted := time.Now()
	stageStarted := totalStarted
	var ws ports.WorkspaceInfo
	var workspaceProject *ports.WorkspaceProjectInfo
	var err error
	reusePublishedWorkspace := in.retry && in.record.Metadata.WorkspacePath != ""
	if reusePublishedWorkspace {
		ws = workspaceInfo(in.record)
		if in.projectKind == domain.ProjectKindWorkspace {
			var rows []ports.WorkspaceRepoInfo
			rows, _, err = m.workspaceProjectRows(ctx, in.record)
			if err == nil {
				workspaceProject = &ports.WorkspaceProjectInfo{Root: ws, Worktrees: rows}
			}
		}
	}
	if in.preparation != nil {
		ws, workspaceProject, err = m.awaitTaskPreparation(ctx, in.preparation)
		if err != nil && ctx.Err() != nil {
			in.preparation.cancel()
			m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrWorkspaceCreate, err))
			return
		}
	}
	// The foreground response must not wait behind Git, but the background
	// workspace lifecycle still serializes with spawn, restore and cleanup.
	releaseWorkspaceGate := m.acquireWorkspaceGate(in.cfg.ProjectID)
	defer releaseWorkspaceGate()
	if ws.Path == "" {
		baseRefs := m.refreshDefaultBranchesBestEffort(ctx, in.project)
		m.logAsyncChatSpawnStage(id, "default_branch_refresh", stageStarted)
		stageStarted = time.Now()
		ws, workspaceProject, err = m.createSessionWorkspace(ctx, in.project, in.cfg, id, in.branch, baseRefs)
	} else {
		m.logAsyncChatSpawnStage(id, "prepared_workspace_wait", stageStarted)
		stageStarted = time.Now()
	}
	if err != nil {
		m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrWorkspaceCreate, err))
		return
	}
	if err := ctx.Err(); err != nil {
		m.cleanupAsyncChatWorkspace(ctx, id, ws, workspaceProject)
		return
	}
	m.logAsyncChatSpawnStage(id, "workspace_create", stageStarted)
	stageStarted = time.Now()
	if !reusePublishedWorkspace {
		if err := m.provisionWorkspace(ctx, in.project, ws.Path); err != nil {
			m.cleanupAsyncChatWorkspace(ctx, id, ws, workspaceProject)
			m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrWorkspaceProvision, err))
			return
		}
	}
	if err := ctx.Err(); err != nil {
		m.cleanupAsyncChatWorkspace(ctx, id, ws, workspaceProject)
		return
	}
	m.logAsyncChatSpawnStage(id, "workspace_provision", stageStarted)
	// Publish the worktree now rather than at the controller commit. Until the
	// row carries it, every workspace-scoped read answers
	// SESSION_WORKSPACE_NOT_FOUND, and the provider start that follows is long
	// enough for the desktop's bounded readiness poll to give up on a session
	// that is perfectly fine. It also means an interrupted start leaves a row
	// that knows which worktree to clean up.
	stageStarted = time.Now()
	updated, err := m.store.SetSessionProvisionedWorkspace(
		ctx, id, ws.Branch, ws.Path, ws.RepoPath, m.clock())
	if err != nil {
		m.cleanupAsyncChatWorkspace(ctx, id, ws, workspaceProject)
		m.failAsyncChatSpawn(ctx, id, err)
		return
	}
	if !updated || ctx.Err() != nil {
		m.cleanupAsyncChatWorkspace(ctx, id, ws, workspaceProject)
		return
	}
	m.logAsyncChatSpawnStage(id, "workspace_publish", stageStarted)
	// A concurrent StageAttachments call must either see this workspace path and
	// write directly into it, or land in canonical storage before this replay.
	// This also projects opening attachments saved before the early response.
	stageStarted = time.Now()
	if err := m.restoreAttachments(ctx, id, ws); err != nil {
		m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrSpawnAttachments, err))
		return
	}
	m.logAsyncChatSpawnStage(id, "attachment_restore", stageStarted)

	record, err := m.getRecord(ctx, id)
	if err != nil {
		m.cleanupAsyncChatWorkspace(ctx, id, ws, workspaceProject)
		m.failAsyncChatSpawn(ctx, id, err)
		return
	}
	if err := ctx.Err(); err != nil {
		m.cleanupAsyncChatWorkspace(ctx, id, ws, workspaceProject)
		return
	}
	stageStarted = time.Now()
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
	m.logAsyncChatSpawnStage(id, "controller_start", stageStarted)
	stageStarted = time.Now()
	if err := m.chat.DrainChatQueue(ctx, id); err != nil {
		m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrSpawnDeliverPrompt, err))
		return
	}
	m.logAsyncChatSpawnStage(id, "queue_drain", stageStarted)
	stageStarted = time.Now()
	if _, err := m.setProvisionState(ctx, id, domain.SessionProvisionReady, ""); err != nil {
		m.logger.Error("spawn: publish provisioned session", "sessionID", id, "error", err)
	}
	m.logAsyncChatSpawnStage(id, "mark_ready", stageStarted)
	m.logAsyncChatSpawnStage(id, "total", totalStarted)
}

func (m *Manager) logAsyncChatSpawnStage(id domain.SessionID, stage string, started time.Time) {
	m.logger.Info("spawn: asynchronous chat stage",
		"sessionID", id,
		"stage", stage,
		"duration", time.Since(started),
	)
}

// failAsyncChatSpawn preserves the visible row and queue for retry.
func (m *Manager) failAsyncChatSpawn(ctx context.Context, id domain.SessionID, cause error) {
	m.logger.Error("spawn: asynchronous chat start failed", "sessionID", id, "error", cause)
	cleanupCtx, cancel := spawnRollbackContext(ctx)
	defer cancel()
	m.stopChatBestEffort(cleanupCtx, id)
	if _, err := m.setProvisionState(cleanupCtx, id, domain.SessionProvisionFailed, cause.Error()); err != nil {
		m.logger.Error("spawn: record failed start", "sessionID", id, "error", err)
	}
}

// retryFailedChatSpawn reuses the published session and durable turn queue.
// Queueing the opening brief again would send the user's task twice.
func (m *Manager) retryFailedChatSpawn(ctx context.Context, rec domain.SessionRecord, releaseHarness func()) (RestoreResult, bool, error) {
	if rec.Kind != domain.KindWorker || domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeChat || m.chat == nil {
		return RestoreResult{}, false, fmt.Errorf("retry start %s: %w", rec.ID, ports.ErrChatUnsupported)
	}
	if m.chat.HasLiveChatController(rec.ID) {
		if err := m.chat.DrainChatQueue(ctx, rec.ID); err != nil {
			return RestoreResult{}, false, err
		}
		ready, err := m.setProvisionState(ctx, rec.ID, domain.SessionProvisionReady, "")
		return RestoreResult{Session: ready, Mode: RestoreModeNative}, false, err
	}
	if rec.Metadata.ProviderConversationID != "" {
		var err error
		rec, err = m.setProvisionState(ctx, rec.ID, domain.SessionProvisionProvisioning, "")
		if err != nil {
			return RestoreResult{}, false, err
		}
		fail := func(cause error) (RestoreResult, bool, error) {
			cleanupCtx, cancel := spawnRollbackContext(ctx)
			defer cancel()
			if _, err := m.setProvisionState(cleanupCtx, rec.ID, domain.SessionProvisionFailed, cause.Error()); err != nil {
				return RestoreResult{}, false, errors.Join(cause, err)
			}
			return RestoreResult{}, false, cause
		}
		resumed, err := m.resumeAgentRecordWithPolicy(ctx, "retry start", rec, false, false)
		if err != nil {
			return fail(err)
		}
		if err := m.chat.DrainChatQueue(ctx, rec.ID); err != nil {
			return fail(err)
		}
		resumed.Session, err = m.setProvisionState(ctx, rec.ID, domain.SessionProvisionReady, "")
		if err != nil {
			return fail(err)
		}
		return resumed, false, nil
	}
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return RestoreResult{}, false, err
	}
	systemPrompt, err := m.buildSystemPrompt(ctx, rec.Kind, rec.ProjectID)
	if err != nil {
		return RestoreResult{}, false, err
	}
	config := restoredAgentConfig(rec, project.Config)
	if rec.Metadata.Model != "" {
		config.Model = rec.Metadata.Model
	}
	if rec.Metadata.Permissions != "" {
		config.Permissions = rec.Metadata.Permissions
	}
	cfg := ports.SpawnConfig{
		ProjectID: rec.ProjectID, IssueID: rec.IssueID, Kind: rec.Kind,
		Harness: rec.Harness, RequestedMode: domain.SessionModeChat,
		AgentConfig: config, AgentConfigResolved: true, Async: true,
	}
	projectKind := projectKindForSession(project, rec.ProjectID)
	branch := rec.Metadata.Branch
	if branch == "" {
		branch = DefaultSpawnBranch(rec.ID, rec.Kind, sessionPrefix(project), projectKind, m.dataDir)
	}
	retried, _, _, err := m.beginAsyncChatSpawn(ctx, asyncChatSpawn{
		cfg: cfg, project: project, projectKind: projectKind,
		record: rec, branch: branch, systemPrompt: systemPrompt, retry: true, releaseHarness: releaseHarness,
	})
	if err != nil {
		return RestoreResult{}, false, err
	}
	return RestoreResult{Session: retried, Mode: RestoreModeSavedPrompt}, true, nil
}

func (m *Manager) cleanupAsyncChatWorkspace(ctx context.Context, id domain.SessionID, ws ports.WorkspaceInfo, workspaceProject *ports.WorkspaceProjectInfo) {
	cleanupCtx, cancel := spawnRollbackContext(ctx)
	defer cancel()
	if m.destroySpawnWorkspace(cleanupCtx, ws, workspaceProject) {
		m.clearProvisionedWorkspace(cleanupCtx, id, ws.Path)
	}
}

func (m *Manager) setProvisionState(
	ctx context.Context,
	id domain.SessionID,
	state domain.SessionProvisionState,
	message string,
) (domain.SessionRecord, error) {
	if _, err := m.store.SetSessionProvisionState(ctx, id, state, message, m.clock()); err != nil {
		return domain.SessionRecord{}, err
	}
	return m.getRecord(ctx, id)
}

// FailInterruptedProvisioning marks sessions whose background start did not
// survive a daemon restart. Without this a row left mid-start reads as
// "starting" forever: nothing is running that could ever finish it.
func (m *Manager) FailInterruptedProvisioning(ctx context.Context) error {
	recs, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("list sessions for interrupted starts: %w", err)
	}
	_, err = m.failInterruptedProvisioningRecords(ctx, recs)
	return err
}

func (m *Manager) failInterruptedProvisioningRecords(ctx context.Context, recs []domain.SessionRecord) ([]domain.SessionRecord, error) {
	var failures []error
	var retries []domain.SessionRecord
	for _, rec := range recs {
		if rec.IsTerminated || rec.IsTaskPreparation || !rec.ProvisionState.IsProvisioning() {
			continue
		}
		if _, err := m.setProvisionState(ctx, rec.ID, domain.SessionProvisionFailed,
			"AO restarted before this session finished starting"); err != nil {
			failures = append(failures, fmt.Errorf("session %s: %w", rec.ID, err))
			retries = append(retries, rec)
		}
	}
	return retries, errors.Join(failures...)
}

// runInBackground runs work outside the caller's request. The seam exists so
// tests can observe a completed spawn without sleeping.
func (m *Manager) runInBackground(work func()) {
	m.backgroundWorkers.Add(1)
	tracked := func() {
		defer m.backgroundWorkers.Done()
		work()
	}
	if m.runBackground != nil {
		m.runBackground(tracked)
		return
	}
	go tracked()
}

// WaitBackgroundWorkers waits for daemon-owned spawn and task-preparation work
// after the daemon context has been cancelled.
func (m *Manager) WaitBackgroundWorkers(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		m.backgroundWorkers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) cancelAsyncChatSpawn(ctx context.Context, id domain.SessionID) error {
	m.asyncChatSpawnsMu.Lock()
	run := m.asyncChatSpawns[id]
	m.asyncChatSpawnsMu.Unlock()
	if run != nil {
		run.cancel()
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok || !rec.ProvisionState.IsProvisioning() {
		return err
	}
	_, err = m.setProvisionState(ctx, id, domain.SessionProvisionFailed, "Session start was cancelled")
	return err
}

func (m *Manager) clearProvisionedWorkspace(ctx context.Context, id domain.SessionID, workspacePath string) {
	cleanupCtx, cancel := spawnRollbackContext(ctx)
	defer cancel()
	rec, ok, err := m.store.GetSession(cleanupCtx, id)
	if err != nil || !ok || rec.Metadata.WorkspacePath != workspacePath {
		return
	}
	rec.Metadata.Branch = ""
	rec.Metadata.WorkspacePath = ""
	rec.Metadata.WorkspaceRepoPath = ""
	if err := m.store.UpdateSession(cleanupCtx, rec); err != nil {
		m.logger.Warn("spawn: clear removed workspace", "sessionID", id, "error", err)
	}
}
