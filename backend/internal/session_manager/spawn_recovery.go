package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const spawnCleanupBudget = 90 * time.Second

type spawnAttempt struct {
	record           domain.SessionRecord
	fact             domain.SessionStartup
	workspace        ports.WorkspaceInfo
	workspaceProject *ports.WorkspaceProjectInfo
	runtime          ports.RuntimeHandle
	launchID         string
	prepared         bool
	complete         bool
}

func (a *spawnAttempt) snapshot() *domain.SessionStartup {
	fact := a.fact
	return &fact
}

// Create and reserve under the input admission lock so reconciliation cannot
// claim a seed between insertion and registration of its in-flight operation.
func (m *Manager) createSpawnSeed(ctx context.Context, cfg ports.SpawnConfig, config domain.ProjectConfig) (domain.SessionRecord, *spawnAttempt, error) {
	seed := seedRecord(cfg, config, m.clock())
	seed.Metadata.Startup = &domain.SessionStartup{ID: uuid.NewString(), Stage: "seed", StartedAt: m.clock()}
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	rec, err := m.store.CreateSession(ctx, seed)
	if err != nil {
		return domain.SessionRecord{}, nil, err
	}
	m.agentOperations[rec.ID] = agentOperationSpawn
	return rec, &spawnAttempt{record: rec, fact: *seed.Metadata.Startup}, nil
}

type startupWriter interface {
	UpdateSessionStartup(context.Context, domain.SessionRecord, string, domain.SessionControllerOwner) (bool, error)
}

func (m *Manager) writeStartup(ctx context.Context, rec domain.SessionRecord, operationID string, expected domain.SessionControllerOwner) error {
	if writer, ok := m.store.(startupWriter); ok {
		updated, err := writer.UpdateSessionStartup(ctx, rec, operationID, expected)
		if err != nil {
			return fmt.Errorf("record startup %s: %w", rec.ID, err)
		}
		if !updated {
			return fmt.Errorf("record startup %s: operation ownership changed", rec.ID)
		}
		return nil
	}
	return m.store.UpdateSession(ctx, rec)
}

func (m *Manager) startupRecord(ctx context.Context, a *spawnAttempt) (domain.SessionRecord, error) {
	if a.fact.ID == "" {
		return domain.SessionRecord{}, errors.New("startup operation identity is missing; ownership cannot be verified")
	}
	rec, ok, err := m.store.GetSession(ctx, a.record.ID)
	if err != nil {
		return domain.SessionRecord{}, err
	}
	if !ok {
		return domain.SessionRecord{}, ErrNotFound
	}
	if rec.Metadata.Startup == nil || rec.Metadata.Startup.ID != a.fact.ID {
		return domain.SessionRecord{}, fmt.Errorf("startup %s: operation ownership changed", rec.ID)
	}
	if rec.Metadata.RuntimeLaunchID != "" && rec.Metadata.RuntimeLaunchID != a.launchID {
		return domain.SessionRecord{}, fmt.Errorf("startup %s: launch ownership changed", rec.ID)
	}
	if a.fact.ControllerGeneration != "" && rec.Metadata.ControllerGeneration != "" && rec.Metadata.ControllerGeneration != a.fact.ControllerGeneration {
		return domain.SessionRecord{}, fmt.Errorf("startup %s: controller ownership changed", rec.ID)
	}
	return rec, nil
}

func (m *Manager) persistStartup(ctx context.Context, a *spawnAttempt) error {
	rec, err := m.startupRecord(ctx, a)
	if err != nil {
		return err
	}
	expected := rec.ControllerOwner()
	if a.workspaceProject != nil {
		a.fact.Worktrees = nil
		for _, wt := range a.workspaceProject.Worktrees {
			a.fact.Worktrees = append(a.fact.Worktrees, domain.StartupWorktree{RepoName: wt.RepoName, RepoPath: wt.RepoPath, Path: wt.Path, Branch: wt.Branch})
		}
	}
	rec.Metadata.Startup = a.snapshot()
	rec.Metadata.Branch = a.workspace.Branch
	rec.Metadata.WorkspacePath = a.workspace.Path
	rec.Metadata.WorkspaceRepoPath = a.workspace.RepoPath
	rec.Metadata.RuntimeHandleID = a.runtime.ID
	rec.Metadata.RuntimeLaunchID = ""
	if a.fact.RuntimePossible {
		rec.Metadata.RuntimeLaunchID = a.launchID
	}
	return m.writeStartup(ctx, rec, a.fact.ID, expected)
}

func (a *spawnAttempt) recordRuntimeFailure(err error) {
	var effect ports.RuntimeEffectError
	if errors.As(err, &effect) {
		a.runtime = effect.PossibleHandle()
		a.fact.RuntimePossible = effect.EffectOutcome() != ports.RuntimeEffectNone && effect.CleanupOutcome() != ports.RuntimeCleanupSucceeded
		return
	}
	// An error without an effect contract cannot prove that no process started.
	a.fact.RuntimePossible = true
}

func (m *Manager) finishStartup(ctx context.Context, a *spawnAttempt) error {
	a.complete = true
	// Launch and prompt delivery already succeeded. Request expiry while
	// recording completion must not turn a lost response into a stopped worker.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return m.clearStartup(ctx, a)
}

func (m *Manager) clearStartup(ctx context.Context, a *spawnAttempt) error {
	rec, err := m.startupRecord(ctx, a)
	if err != nil {
		return err
	}
	expected := rec.ControllerOwner()
	rec.Metadata.Startup = nil
	return m.writeStartup(ctx, rec, a.fact.ID, expected)
}

// The caller creates exactly one detached deadline for the entire rollback.
// Every failure is returned and the durable operation stays retryable until
// runtime shutdown, workspace cleanup and lifecycle persistence are confirmed.
func (m *Manager) rollbackStartup(ctx context.Context, a *spawnAttempt, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spawnCleanupBudget)
	defer cancel()
	return m.cleanupStartup(cleanupCtx, a, cause)
}

func (m *Manager) cleanupStartup(ctx context.Context, a *spawnAttempt, cause error) error {
	if _, err := m.startupRecord(ctx, a); err != nil {
		return fmt.Errorf("startup rollback: %w", err)
	}
	a.fact.Stage = "cleanup_pending"
	a.fact.LastError = boundedStartupError(cause)
	var cleanupErr error
	if err := m.persistStartup(ctx, a); err != nil {
		return errors.Join(cleanupErr, err)
	}
	if a.fact.Committed {
		rec, err := m.startupRecord(ctx, a)
		if err != nil {
			return err
		}
		if m.preview != nil {
			if err := m.preview.StopSession(ctx, rec.ID); err != nil {
				return m.retainStartupFailure(ctx, a, err)
			}
		}
		if m.browser != nil {
			if err := m.browser.DestroySession(ctx, rec.ID); err != nil {
				return m.retainStartupFailure(ctx, a, err)
			}
		}
		if err := m.terminateNativeSession(ctx, rec); err != nil {
			return m.retainStartupFailure(ctx, a, err)
		}
		if err := m.importAttachments(ctx, rec); err != nil {
			return m.retainStartupFailure(ctx, a, err)
		}
		if err := m.terminateReviewer(ctx, rec.ID, "cancelled by worker session termination"); err != nil {
			return m.retainStartupFailure(ctx, a, err)
		}
		if a.workspace.Path != "" {
			release, err := m.beginShellTerminalTeardown(ctx, rec.ID)
			if err != nil {
				return m.retainStartupFailure(ctx, a, err)
			}
			if release != nil {
				defer release()
			}
		}
	}
	if a.fact.RuntimePossible {
		if a.runtime.ID == "" {
			cleanupErr = errors.Join(cleanupErr, errors.New("startup rollback: runtime creation outcome is unknown; workspace retained"))
			return m.retainStartupFailure(ctx, a, cleanupErr)
		}
		if err := m.runtime.Destroy(ctx, a.runtime); err != nil {
			return m.retainStartupFailure(ctx, a, errors.Join(cleanupErr, fmt.Errorf("startup rollback: stop runtime: %w", err)))
		}
		a.fact.RuntimePossible = false
	}
	if a.fact.ControllerPossible {
		if m.chat == nil {
			return m.retainStartupFailure(ctx, a, errors.Join(cleanupErr, errors.New("startup rollback: chat controller cleanup unavailable")))
		}
		stopper, ok := m.chat.(chatStartupStopper)
		if !ok {
			return m.retainStartupFailure(ctx, a, errors.New("startup rollback: verified controller cleanup unavailable"))
		}
		confirmed, err := stopper.StopChatStartup(ctx, a.record.ID, a.fact.ControllerGeneration)
		if err != nil {
			return m.retainStartupFailure(ctx, a, fmt.Errorf("startup rollback: stop controller: %w", err))
		}
		if !confirmed {
			return m.retainStartupFailure(ctx, a, errors.New("startup rollback: controller ownership is unavailable; workspace retained"))
		}
		a.fact.ControllerPossible = false
	}
	a.runtime = ports.RuntimeHandle{}
	// Persist successful shutdown before attempting a potentially dirty worktree.
	if err := m.persistStartup(ctx, a); err != nil {
		return errors.Join(cleanupErr, err)
	}
	workspacePath := a.workspace.Path
	if a.fact.WorkspaceUncertain && workspacePath == "" {
		cleanupErr = errors.Join(cleanupErr, errors.New("startup rollback: workspace creation outcome is unknown; inspect managed worktrees before cleanup"), m.lcm.MarkTerminated(ctx, a.record.ID))
		return m.retainStartupFailure(ctx, a, cleanupErr)
	}
	if workspacePath != "" {
		var err error
		if a.workspaceProject != nil {
			if adapter, ok := m.workspace.(ports.WorkspaceProject); ok {
				err = adapter.DestroyWorkspaceProject(ctx, *a.workspaceProject)
			} else {
				err = errors.New("workspace project cleanup unavailable")
			}
		} else {
			err = m.workspace.Destroy(ctx, a.workspace)
		}
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("startup rollback: workspace: %w", err), m.lcm.MarkTerminated(ctx, a.record.ID))
			return m.retainStartupFailure(ctx, a, cleanupErr)
		}
		if a.prepared {
			if err := m.cleanupAgentWorkspaceError(ctx, a.record, workspacePath); err != nil {
				cleanupErr = errors.Join(cleanupErr, err, m.lcm.MarkTerminated(ctx, a.record.ID))
				return m.retainStartupFailure(ctx, a, cleanupErr)
			}
		}
	}
	cleanupErr = errors.Join(cleanupErr, m.store.DeleteSessionWorktrees(ctx, a.record.ID))
	a.workspace = ports.WorkspaceInfo{}
	a.workspaceProject = nil
	a.fact.Worktrees = nil
	if err := m.persistStartup(ctx, a); err != nil {
		return errors.Join(cleanupErr, err)
	}
	if !a.fact.Committed && cleanupErr == nil {
		deleted, err := m.store.DeleteSession(ctx, a.record.ID)
		if err == nil && deleted {
			m.cleanupSystemPromptDir(a.record.ID)
			m.cleanupAttachments(ctx, a.record.ID)
			return nil
		}
		cleanupErr = errors.Join(cleanupErr, err)
	}
	cleanupErr = errors.Join(cleanupErr, m.lcm.MarkTerminated(ctx, a.record.ID))
	if cleanupErr != nil {
		return m.retainStartupFailure(ctx, a, cleanupErr)
	}
	if err := m.clearStartup(ctx, a); err != nil {
		return err
	}
	m.cleanupSystemPromptDir(a.record.ID)
	m.cleanupAttachments(ctx, a.record.ID)
	return nil
}

func boundedStartupError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	return message
}

func (m *Manager) retainStartupFailure(ctx context.Context, a *spawnAttempt, err error) error {
	a.fact.LastError = boundedStartupError(err)
	return errors.Join(err, m.persistStartup(ctx, a))
}

// Reconciliation selects only explicit unfinished operations. Missing terminal
// handles alone say nothing about healthy Chat sessions or committed launches.
func (m *Manager) reconcileStartups(ctx context.Context) error {
	recs, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.Metadata.Startup == nil {
			continue
		}
		if err := m.beginAgentOperation(ctx, rec.ID, agentOperationSpawn); err != nil {
			if errors.Is(err, errAgentOperationInProgress) {
				continue
			}
			return err
		}
		func() {
			defer m.endAgentOperation(rec.ID, agentOperationSpawn)
			a := startupAttemptFromRecord(rec)
			if a.fact.Committed && a.fact.Stage != "cleanup_pending" {
				// The launch committed before interruption. Prompt delivery may
				// have succeeded without a durable acknowledgement, so destroying
				// the controller or sending it again could undo or duplicate work.
				a.fact.Stage = "completion_unknown"
				a.fact.LastError = "Launch committed, but initial prompt completion was not recorded; inspect the session before retrying."
				if err := m.persistStartup(ctx, a); err != nil {
					m.logger.Warn("reconcile: startup completion uncertain", "sessionID", rec.ID, "error", err)
				}
				return
			}
			if err := m.rollbackStartup(ctx, a, errors.New("startup interrupted before completion")); err != nil {
				m.logger.Warn("reconcile: startup cleanup pending", "sessionID", rec.ID, "error", err)
			}
		}()
	}
	return nil
}

func startupAttemptFromRecord(rec domain.SessionRecord) *spawnAttempt {
	a := &spawnAttempt{record: rec, fact: *rec.Metadata.Startup, workspace: workspaceInfo(rec), runtime: ports.RuntimeHandle{ID: rec.Metadata.RuntimeHandleID}, launchID: rec.Metadata.RuntimeLaunchID, prepared: true}
	if a.fact.Stage == "workspace_creating" && a.workspace.Path == "" {
		a.fact.WorkspaceUncertain = true
	}
	if len(a.fact.Worktrees) > 0 {
		a.workspaceProject = &ports.WorkspaceProjectInfo{Root: a.workspace}
		for _, wt := range a.fact.Worktrees {
			a.workspaceProject.Worktrees = append(a.workspaceProject.Worktrees, ports.WorkspaceRepoInfo{RepoName: wt.RepoName, RepoPath: wt.RepoPath, Path: wt.Path, Branch: wt.Branch})
		}
	}
	return a
}

func workspaceCreateRejected(err error) bool {
	return errors.Is(err, ports.ErrWorkspaceBranchCheckedOutElsewhere) ||
		errors.Is(err, ports.ErrWorkspaceBranchNotFetched) ||
		errors.Is(err, ports.ErrWorkspaceDefaultBranchUnresolved) ||
		errors.Is(err, ports.ErrWorkspaceBranchInvalid)
}
