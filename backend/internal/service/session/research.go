package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	researchTimeout   = 30 * time.Minute
	maxResearchPrompt = 16 << 10
	maxResearchResult = 256 << 10
)

type researchStore interface {
	CreateResearchRun(context.Context, domain.ResearchRun) (domain.ResearchRun, error)
	GetResearchRun(context.Context, string) (domain.ResearchRun, bool, error)
	ListResearchRunsByParent(context.Context, domain.SessionID) ([]domain.ResearchRun, error)
	ListUnfinishedResearchRuns(context.Context) ([]domain.ResearchRun, error)
	MarkResearchRunning(context.Context, string, time.Time) (bool, error)
	FinishResearchRun(context.Context, string, string, string, string, time.Time) (bool, error)
	InterruptResearchRun(context.Context, string, string, time.Time) (bool, error)
}

type researchRunner interface {
	SupportsResearch(domain.AgentHarness) bool
	RunResearch(context.Context, domain.SessionID, string, string, domain.ResearcherConfig, func(context.Context, ports.ChatEvent) (ports.ChatDecision, error)) (string, error)
	StopResearchOrphan(context.Context, domain.ResearchRun) error
}

type pendingResearchApproval struct {
	request domain.ResearchApproval
	options map[string]ports.ChatDecision
	answer  chan ports.ChatDecision
}

func (s *Service) awaitResearchApproval(ctx context.Context, id string, event ports.ChatEvent) (ports.ChatDecision, error) {
	if event.RequestID == "" || len(event.Decisions) == 0 {
		return ports.ChatDecision{}, errors.New("provider returned an approval without choices")
	}
	pending := &pendingResearchApproval{
		request: domain.ResearchApproval{RequestID: event.RequestID, Summary: event.Summary},
		options: make(map[string]ports.ChatDecision, len(event.Decisions)),
		answer:  make(chan ports.ChatDecision, 1),
	}
	for _, option := range event.Decisions {
		if option.ID == "" {
			continue
		}
		pending.request.Options = append(pending.request.Options, domain.ResearchApprovalOption{
			ID: option.ID, Label: option.Label, Kind: string(option.Kind),
		})
		pending.options[option.ID] = ports.ChatDecision{ID: option.ID, Raw: option.Raw}
	}
	if len(pending.options) == 0 {
		return ports.ChatDecision{}, errors.New("provider returned an approval without usable choices")
	}
	s.researchMu.Lock()
	if s.researchApprovals == nil {
		s.researchApprovals = make(map[string]*pendingResearchApproval)
	}
	s.researchApprovals[id] = pending
	s.researchMu.Unlock()
	defer func() {
		s.researchMu.Lock()
		if s.researchApprovals[id] == pending {
			delete(s.researchApprovals, id)
		}
		s.researchMu.Unlock()
	}()
	select {
	case decision := <-pending.answer:
		return decision, nil
	case <-ctx.Done():
		return ports.ChatDecision{}, ctx.Err()
	}
}

// ResolveResearchApproval submits a choice for a pending research approval.
func (s *Service) ResolveResearchApproval(ctx context.Context, parentID domain.SessionID, id, requestID, optionID string) (domain.ResearchRun, error) {
	rec, err := s.GetResearch(ctx, parentID, id)
	if err != nil {
		return domain.ResearchRun{}, err
	}
	s.researchMu.Lock()
	pending := s.researchApprovals[id]
	if pending == nil || pending.request.RequestID != requestID {
		s.researchMu.Unlock()
		return domain.ResearchRun{}, apierr.Conflict("RESEARCH_APPROVAL_EXPIRED", "Research approval is no longer pending", nil)
	}
	decision, ok := pending.options[optionID]
	if !ok {
		s.researchMu.Unlock()
		return domain.ResearchRun{}, apierr.Invalid("INVALID_RESEARCH_DECISION", "Choose one of the offered approval options", nil)
	}
	delete(s.researchApprovals, id)
	pending.answer <- decision
	s.researchMu.Unlock()
	rec.Approval = nil
	return rec, nil
}

func (s *Service) researchDeps() (researchStore, researchRunner, error) {
	store, ok := s.store.(researchStore)
	if !ok {
		return nil, nil, apierr.NotImplemented("RESEARCH_UNAVAILABLE", "Research storage is unavailable")
	}
	runner, ok := s.manager.(researchRunner)
	if !ok {
		return nil, nil, apierr.NotImplemented("RESEARCH_UNAVAILABLE", "Research runner is unavailable")
	}
	return store, runner, nil
}

// StartResearch creates one durable job and runs it after the HTTP response.
func (s *Service) StartResearch(ctx context.Context, parentID domain.SessionID, prompt string) (domain.ResearchRun, error) {
	store, runner, err := s.researchDeps()
	if err != nil {
		return domain.ResearchRun{}, err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || len(prompt) > maxResearchPrompt {
		return domain.ResearchRun{}, apierr.Invalid("INVALID_RESEARCH_PROMPT", "Research prompt must be 1–16384 bytes", nil)
	}
	parent, found, err := s.store.GetSession(ctx, parentID)
	if err != nil {
		return domain.ResearchRun{}, err
	}
	if !found {
		return domain.ResearchRun{}, apierr.NotFound("SESSION_NOT_FOUND", "Orchestrator session not found")
	}
	if parent.IsTerminated || parent.Kind != domain.KindOrchestrator || parent.ProjectID == "" {
		return domain.ResearchRun{}, apierr.Invalid("RESEARCH_ORCHESTRATOR_REQUIRED", "Research requires an active project orchestrator", nil)
	}
	project, err := s.requireProject(ctx, parent.ProjectID)
	if err != nil {
		return domain.ResearchRun{}, err
	}
	config := project.Config.Researcher
	if !config.Enabled {
		return domain.ResearchRun{}, apierr.Conflict("RESEARCH_DISABLED", "Enable and configure the researcher in Project Settings", nil)
	}
	if !runner.SupportsResearch(config.Harness) {
		return domain.ResearchRun{}, apierr.Invalid("RESEARCH_AGENT_UNSUPPORTED", "Selected researcher agent cannot run background chat", nil)
	}
	if err := project.Config.Validate(); err != nil {
		return domain.ResearchRun{}, apierr.Invalid("INVALID_RESEARCH_CONFIG", err.Error(), nil)
	}
	if project.Kind.WithDefault() != domain.ProjectKindSingleRepo {
		return domain.ResearchRun{}, apierr.NotImplemented("RESEARCH_PROJECT_UNSUPPORTED", "Research currently supports single-repository projects")
	}
	if config.AgentConfig.Permissions == "" {
		config.AgentConfig.Permissions = project.Config.AgentConfig.Permissions
		if config.AgentConfig.Permissions == "" {
			config.AgentConfig.Permissions = domain.PermissionModeAuto
		}
	}
	rec := domain.ResearchRun{
		ID: uuid.NewString(), ParentSessionID: parentID, ProjectID: parent.ProjectID,
		Prompt: prompt, Harness: config.Harness, AgentConfig: config.AgentConfig,
		Status: "queued", CreatedAt: s.now(),
	}
	rec, err = store.CreateResearchRun(ctx, rec)
	if errors.Is(err, domain.ErrResearchAlreadyRunning) {
		return domain.ResearchRun{}, apierr.Conflict("RESEARCH_ALREADY_RUNNING", err.Error(), nil)
	}
	if err != nil {
		return domain.ResearchRun{}, err
	}
	base := s.backgroundContext
	if base == nil {
		base = context.Background()
	}
	runCtx, cancel := context.WithTimeout(base, researchTimeout)
	s.researchMu.Lock()
	if s.researchCancels == nil {
		s.researchCancels = make(map[string]context.CancelFunc)
	}
	s.researchCancels[rec.ID] = cancel
	s.researchMu.Unlock()
	work := func() {
		defer func() {
			cancel()
			s.researchMu.Lock()
			delete(s.researchCancels, rec.ID)
			s.researchMu.Unlock()
		}()
		s.runResearch(runCtx, store, runner, rec, config)
	}
	if s.runBackground != nil {
		s.runBackground(work)
	} else {
		go work()
	}
	return rec, nil
}

func (s *Service) runResearch(ctx context.Context, store researchStore, runner researchRunner, rec domain.ResearchRun, config domain.ResearcherConfig) {
	writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelWrite()
	started, err := store.MarkResearchRunning(writeCtx, rec.ID, s.now())
	if err != nil || !started {
		if err != nil && s.logger != nil {
			s.logger.Error("research start failed", "id", rec.ID, "error", err)
		}
		return
	}
	var result string
	runErr := ctx.Err()
	if runErr == nil {
		result, runErr = runner.RunResearch(ctx, rec.ParentSessionID, rec.ID, rec.Prompt, config, func(requestCtx context.Context, event ports.ChatEvent) (ports.ChatDecision, error) {
			return s.awaitResearchApproval(requestCtx, rec.ID, event)
		})
	}
	status, message := "completed", ""
	if runErr != nil {
		status, message = "failed", runErr.Error()
		if errors.Is(ctx.Err(), context.Canceled) {
			status, message = "cancelled", "Research was cancelled"
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = fmt.Sprintf("Research exceeded the %d-minute limit", int(researchTimeout/time.Minute))
		}
		result = ""
	}
	if len(result) > maxResearchResult {
		end := maxResearchResult
		for !utf8.RuneStart(result[end]) {
			end--
		}
		result = result[:end] + "\n\n[Research report truncated by AO.]"
	}
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelFinish()
	if _, err := store.FinishResearchRun(finishCtx, rec.ID, status, result, message, s.now()); err != nil && s.logger != nil {
		s.logger.Error("research finish failed", "id", rec.ID, "error", err)
	}
}

// GetResearch returns a research run belonging to the orchestrator.
func (s *Service) GetResearch(ctx context.Context, parentID domain.SessionID, id string) (domain.ResearchRun, error) {
	store, _, err := s.researchDeps()
	if err != nil {
		return domain.ResearchRun{}, err
	}
	rec, found, err := store.GetResearchRun(ctx, id)
	if err != nil {
		return domain.ResearchRun{}, err
	}
	if !found || rec.ParentSessionID != parentID {
		return domain.ResearchRun{}, apierr.NotFound("RESEARCH_NOT_FOUND", "Research run not found")
	}
	s.researchMu.Lock()
	if pending := s.researchApprovals[id]; pending != nil {
		approval := pending.request
		rec.Approval = &approval
	}
	s.researchMu.Unlock()
	return rec, nil
}

// ListResearch returns the orchestrator's research runs.
func (s *Service) ListResearch(ctx context.Context, parentID domain.SessionID) ([]domain.ResearchRun, error) {
	store, _, err := s.researchDeps()
	if err != nil {
		return nil, err
	}
	parent, found, err := s.store.GetSession(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if !found || parent.Kind != domain.KindOrchestrator {
		return nil, apierr.NotFound("SESSION_NOT_FOUND", "Orchestrator session not found")
	}
	runs, err := store.ListResearchRunsByParent(ctx, parentID)
	if err != nil {
		return nil, err
	}
	s.researchMu.Lock()
	for i := range runs {
		if pending := s.researchApprovals[runs[i].ID]; pending != nil {
			approval := pending.request
			runs[i].Approval = &approval
		}
	}
	s.researchMu.Unlock()
	return runs, nil
}

// CancelResearch stops an active run owned by this daemon.
func (s *Service) CancelResearch(ctx context.Context, parentID domain.SessionID, id string) (domain.ResearchRun, error) {
	rec, err := s.GetResearch(ctx, parentID, id)
	if err != nil {
		return domain.ResearchRun{}, err
	}
	if rec.Status == "running" || rec.Status == "queued" {
		s.researchMu.Lock()
		cancel := s.researchCancels[id]
		s.researchMu.Unlock()
		if cancel == nil {
			return domain.ResearchRun{}, apierr.Conflict("RESEARCH_NOT_OWNED", "Research is not running in this daemon", nil)
		}
		cancel()
	}
	return rec, nil
}

func (s *Service) cancelResearchByParent(ctx context.Context, parentID domain.SessionID) {
	store, _, err := s.researchDeps()
	if err != nil {
		return
	}
	runs, err := store.ListResearchRunsByParent(ctx, parentID)
	if err != nil {
		return
	}
	s.researchMu.Lock()
	defer s.researchMu.Unlock()
	for _, run := range runs {
		if cancel := s.researchCancels[run.ID]; cancel != nil {
			cancel()
		}
	}
}

// RecoverResearch stops orphaned provider hosts before marking their jobs
// interrupted. It never resumes a paid model call after daemon restart.
func (s *Service) RecoverResearch(ctx context.Context) error {
	store, runner, err := s.researchDeps()
	if err != nil {
		return err
	}
	runs, err := store.ListUnfinishedResearchRuns(ctx)
	if err != nil {
		return err
	}
	for _, rec := range runs {
		stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		stopErr := runner.StopResearchOrphan(stopCtx, rec)
		cancel()
		if stopErr != nil {
			return fmt.Errorf("stop orphaned research %s: %w", rec.ID, stopErr)
		}
		if _, err := store.InterruptResearchRun(ctx, rec.ID, "Research was interrupted by daemon restart", s.now()); err != nil {
			return fmt.Errorf("interrupt orphaned research %s: %w", rec.ID, err)
		}
	}
	return nil
}
