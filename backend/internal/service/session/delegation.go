package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	delegatedTaskTitleLimit             = maxDisplayNameLen
	delegatedTaskUntitledName           = "Untitled task"
	delegatedTaskTitleRefinementTimeout = time.Minute
	// ponytail: cosmetic work is dropped at the daemon-wide cap; add a queue only if skipped titles become a product problem.
	delegatedTaskTitleConcurrency  = 4
	delegatedTaskTitleSystemPrompt = "Return only a concise task title of at most 100 characters. Do not use tools, change files, or explain the answer."
)

// DelegateTaskInput describes a task AO should spawn as a worker session. Brief
// may be empty to open an idle worker that the user can instruct later. Empty
// RequestedAgent means the spawn uses the project's worker-agent default.
type DelegateTaskInput struct {
	ProjectID      domain.ProjectID
	Brief          string
	RequestedAgent domain.AgentHarness
	Model          string
	Effort         *string
	ApprovalMode   domain.PermissionMode
	RequestedMode  domain.SessionMode
	Attachments    []ports.SpawnAttachment
}

// DelegateTaskOutcome identifies the spawned worker. OrchestratorID remains
// optional for wire compatibility; title refinement never requires one.
type DelegateTaskOutcome struct {
	OrchestratorID domain.SessionID
	WorkerID       domain.SessionID
}

// DelegateTask spawns the worker directly, matching `ao spawn`, with a
// provisional display name derived from the task brief. AO then best-effort
// refines that title through a short-lived call to the worker's resolved harness.
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

	effort, effortOverride := optionalTuningValue(in.Effort)
	worker, _, _, err := s.manager.Spawn(ctx, ports.SpawnConfig{
		ProjectID:   in.ProjectID,
		Kind:        domain.KindWorker,
		Harness:     in.RequestedAgent,
		Prompt:      prompt,
		DisplayName: delegatedTaskDisplayName(in.Brief),
		AgentConfig: ports.AgentConfig{
			Model:       strings.TrimSpace(in.Model),
			Effort:      effort,
			Permissions: in.ApprovalMode,
		},
		EffortOverride: effortOverride,
		RequestedMode:  in.RequestedMode,
		Attachments:    in.Attachments,
	})
	if err != nil {
		return DelegateTaskOutcome{}, toSpawnAPIError(err)
	}

	// The worker spawn is the commit point. Background title
	// generation must never hold the new-task response open. A promptless worker
	// stays idle with its provisional title until the user supplies instructions.
	if prompt != "" {
		s.refineDelegatedTaskTitleInBackground(worker.ID, in)
	}
	return DelegateTaskOutcome{WorkerID: worker.ID}, nil
}

func optionalTuningValue(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	return strings.TrimSpace(*value), true
}

func (s *Service) refineDelegatedTaskTitleInBackground(workerID domain.SessionID, in DelegateTaskInput) {
	if s.titleRefinementSlots != nil {
		select {
		case s.titleRefinementSlots <- struct{}{}:
		default:
			if s.logger != nil {
				s.logger.Warn("delegated task title refinement skipped: capacity reached", "workerID", workerID)
			}
			return
		}
	}
	base := s.backgroundContext
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, delegatedTaskTitleRefinementTimeout)
	s.trackTitleRefinement(workerID, cancel)
	work := func() {
		defer func() {
			cancel()
			s.untrackTitleRefinement(workerID)
			if s.titleRefinementSlots != nil {
				<-s.titleRefinementSlots
			}
		}()

		if err := s.refineDelegatedTaskTitle(ctx, workerID, in); err != nil && s.logger != nil {
			s.logger.Warn("delegated task title refinement failed",
				"projectID", in.ProjectID,
				"workerID", workerID,
				"error", err,
			)
		}
	}
	if s.runBackground != nil {
		s.runBackground(work)
		return
	}
	go work()
}

func (s *Service) refineDelegatedTaskTitle(ctx context.Context, workerID domain.SessionID, in DelegateTaskInput) error {
	raw, err := s.manager.RunBackgroundTask(ctx, workerID, delegatedTaskTitleSystemPrompt, in.Brief)
	if err != nil {
		return fmt.Errorf("generate title with worker harness: %w", err)
	}
	title := generatedTaskTitle(raw)
	if title == "" {
		return errors.New("worker harness returned an empty title")
	}
	_, err = s.store.RenameSessionIfDisplayName(
		ctx, workerID, delegatedTaskDisplayName(in.Brief), title, s.now(),
	)
	if err != nil {
		return fmt.Errorf("apply generated title to %s: %w", workerID, err)
	}
	return nil
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

func generatedTaskTitle(raw string) string {
	raw = domain.SanitizeControlChars(raw)
	firstLine, _, _ := strings.Cut(raw, "\n")
	title := strings.TrimLeft(firstLine, "#*->+ \t\r")
	title = strings.Trim(title, " \"'`“”‘’\t\r.,;:!。")
	if strings.IndexFunc(title, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) < 0 {
		return ""
	}
	return delegatedTaskDisplayName(title)
}

func (s *Service) trackTitleRefinement(id domain.SessionID, cancel context.CancelFunc) {
	s.titleRefinementMu.Lock()
	defer s.titleRefinementMu.Unlock()
	if s.titleRefinementCancels == nil {
		s.titleRefinementCancels = map[domain.SessionID]context.CancelFunc{}
	}
	s.titleRefinementCancels[id] = cancel
}

func (s *Service) untrackTitleRefinement(id domain.SessionID) {
	s.titleRefinementMu.Lock()
	defer s.titleRefinementMu.Unlock()
	delete(s.titleRefinementCancels, id)
}

func (s *Service) cancelTitleRefinement(id domain.SessionID) {
	s.titleRefinementMu.Lock()
	cancel := s.titleRefinementCancels[id]
	delete(s.titleRefinementCancels, id)
	s.titleRefinementMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
