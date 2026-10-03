package session

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

const (
	delegatedTaskTitleLimit   = maxDisplayNameLen
	delegatedTaskUntitledName = "Untitled task"
)

// DelegateTaskInput describes a task AO should spawn as a worker session. Brief
// may be empty to open an idle worker that the user can instruct later. Empty
// RequestedAgent means the spawn uses the project's worker-agent default.
type DelegateTaskInput struct {
	ProjectID       domain.ProjectID
	Brief           string
	RequestedAgent  domain.AgentHarness
	Model           string
	Effort          *string
	ApprovalMode    domain.PermissionMode
	RequestedMode   domain.SessionMode
	Attachments     []ports.SpawnAttachment
	TaskPreparation domain.TaskPreparationToken
}

// DelegateTaskOutcome identifies the spawned worker. OrchestratorID remains
// optional for wire compatibility.
type DelegateTaskOutcome struct {
	OrchestratorID domain.SessionID
	WorkerID       domain.SessionID
}

// PrepareTask starts the reversible worktree-only half of task creation.
func (s *Service) PrepareTask(ctx context.Context, projectID domain.ProjectID) (string, error) {
	project, err := s.requireProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	token, err := s.manager.PrepareTaskWorkspace(ctx, project)
	return string(token), err
}

// CancelTaskPreparation releases an unclaimed speculative worktree. Unknown or
// already-claimed tokens are intentionally idempotent.
func (s *Service) CancelTaskPreparation(ctx context.Context, token string) error {
	return s.manager.CancelTaskPreparation(ctx, domain.TaskPreparationToken(token))
}

// DelegateTask spawns the worker directly, matching `ao spawn`, with a
// provisional display name derived from the task brief. A nonblank direct
// delegation also gets a trusted startup instruction to self-rename before
// implementation begins.
func (s *Service) DelegateTask(ctx context.Context, in DelegateTaskInput) (DelegateTaskOutcome, error) {
	if _, err := s.requireProject(ctx, in.ProjectID); err != nil {
		return DelegateTaskOutcome{}, err
	}
	if in.RequestedAgent != "" && !in.RequestedAgent.IsKnown() {
		return DelegateTaskOutcome{}, apierr.Invalid("UNKNOWN_HARNESS", "Unknown requested agent", nil)
	}
	if in.RequestedMode != "" && !in.RequestedMode.Valid() {
		return DelegateTaskOutcome{}, apierr.Invalid("INVALID_SESSION_MODE", "mode must be chat or tui", nil)
	}
	prompt := in.Brief
	if strings.TrimSpace(prompt) == "" {
		prompt = ""
	}
	startupSystemPrompt := ""
	if prompt != "" {
		startupSystemPrompt = sessionmanager.DelegatedTaskTitleStartupPrompt
	}

	effort, effortOverride := optionalTuningValue(in.Effort)
	worker, _, _, err := s.manager.Spawn(ctx, ports.SpawnConfig{
		ProjectID:           in.ProjectID,
		Kind:                domain.KindWorker,
		Harness:             in.RequestedAgent,
		Prompt:              prompt,
		StartupSystemPrompt: startupSystemPrompt,
		DisplayName:         delegatedTaskDisplayName(in.Brief),
		AgentConfig: ports.AgentConfig{
			Model:       strings.TrimSpace(in.Model),
			Effort:      effort,
			Permissions: in.ApprovalMode,
		},
		EffortOverride: effortOverride,
		RequestedMode:  in.RequestedMode,
		Attachments:    in.Attachments,
		// This is the desktop's new-task path: there is a UI waiting to navigate
		// to the session. A Chat worker answers as soon as it is addressable and
		// finishes starting in the background.
		Async:           true,
		TaskPreparation: in.TaskPreparation,
	})
	if err != nil {
		return DelegateTaskOutcome{}, toSpawnAPIError(err)
	}
	return DelegateTaskOutcome{WorkerID: worker.ID}, nil
}

func optionalTuningValue(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	return strings.TrimSpace(*value), true
}

func delegatedTaskDisplayName(brief string) string {
	title := strings.Join(strings.Fields(brief), " ")
	if title == "" {
		return delegatedTaskUntitledName
	}
	if utf8.RuneCountInString(title) <= delegatedTaskTitleLimit {
		return title
	}
	return strings.TrimSpace(string([]rune(title)[:delegatedTaskTitleLimit]))
}
