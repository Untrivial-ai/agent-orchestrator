package sessionmanager

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// AffectedSession describes a live worker whose pinned launch permission
// differs from the permission a new worker would receive from project settings.
type AffectedSession struct {
	SessionID domain.SessionID
	Title     string
	Kind      domain.SessionKind
	FromMode  domain.PermissionMode
	ToMode    domain.PermissionMode
}

// RelaunchOutcome reports the result of applying a project's new permission
// mode to one running worker.
type RelaunchOutcome struct {
	SessionID domain.SessionID
	OK        bool
	Error     string
}

// AffectedByPermissionChange returns workers that need an explicit relaunch to
// adopt their project's new permission setting. It never includes terminated
// sessions or the orchestrator: neither is a live agent that needs interrupting.
func (m *Manager) AffectedByPermissionChange(ctx context.Context, projectID domain.ProjectID) ([]AffectedSession, error) {
	project, err := m.loadProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	records, err := m.store.ListSessions(ctx, projectID)
	if err != nil {
		return nil, err
	}

	affected := make([]AffectedSession, 0)
	for _, record := range records {
		if record.IsTerminated || record.Kind != domain.KindWorker || !hasRestorableWorkspace(record, project) {
			continue
		}
		from := launchedPermission(record.Metadata.Permissions)
		to := permissionRelaunchTarget(record.Kind, project.Config)
		if from == to {
			continue
		}
		title := record.DisplayName
		if title == "" {
			title = string(record.ID)
		}
		affected = append(affected, AffectedSession{
			SessionID: record.ID,
			Title:     title,
			Kind:      record.Kind,
			FromMode:  from,
			ToMode:    to,
		})
	}
	return affected, nil
}

func hasRestorableWorkspace(record domain.SessionRecord, project domain.ProjectRecord) bool {
	if record.Metadata.WorkspacePath == "" {
		return false
	}
	return record.Metadata.Branch != "" || project.Kind.WithDefault() == domain.ProjectKindScratch
}

func launchedPermission(permission domain.PermissionMode) domain.PermissionMode {
	if permission == "" {
		return ports.PermissionModeAuto
	}
	return permission
}

func permissionRelaunchTarget(kind domain.SessionKind, config domain.ProjectConfig) domain.PermissionMode {
	return applySpawnAgentConfig(effectiveAgentConfig(kind, config), ports.AgentConfig{}).Permissions
}

// RelaunchForPermissionChange stops and restores every worker still affected by
// the project's current permission setting. It deliberately updates the
// session's pinned launch policy after a successful stop and before restore:
// ordinary restores preserve that pin, while this explicitly confirmed action
// is the one path that replaces it. One failure does not prevent later workers
// from being restarted.
func (m *Manager) RelaunchForPermissionChange(ctx context.Context, projectID domain.ProjectID) ([]RelaunchOutcome, error) {
	affected, err := m.AffectedByPermissionChange(ctx, projectID)
	if err != nil {
		return nil, err
	}

	outcomes := make([]RelaunchOutcome, 0, len(affected))
	for _, session := range affected {
		if _, err := m.Kill(ctx, session.SessionID); err != nil {
			outcomes = append(outcomes, RelaunchOutcome{SessionID: session.SessionID, Error: err.Error()})
			continue
		}
		if err := m.replaceLaunchedPermission(ctx, session.SessionID, session.ToMode); err != nil {
			outcomes = append(outcomes, RelaunchOutcome{SessionID: session.SessionID, Error: err.Error()})
			continue
		}
		if _, err := m.RestoreWithMode(ctx, session.SessionID); err != nil {
			outcomes = append(outcomes, RelaunchOutcome{SessionID: session.SessionID, Error: err.Error()})
			continue
		}
		outcomes = append(outcomes, RelaunchOutcome{SessionID: session.SessionID, OK: true})
	}
	return outcomes, nil
}

func (m *Manager) replaceLaunchedPermission(ctx context.Context, id domain.SessionID, permission domain.PermissionMode) error {
	record, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if !record.IsTerminated {
		return ErrNotRestorable
	}
	record.Metadata.Permissions = permission
	return m.store.UpdateSession(ctx, record)
}
