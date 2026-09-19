package projectsummary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const summaryProjectionVersion = "3"

// Store supplies the durable facts and projection used by the summary service.
type Store interface {
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
	ListSessions(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error)
	GetProjectSummary(ctx context.Context, projectID domain.ProjectID) (domain.ProjectSummary, bool, error)
	PutProjectSummary(ctx context.Context, summary domain.ProjectSummary) error
}

// ReportOutputFact is an opaque output reference included only as narrative context.
type ReportOutputFact struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Label     string `json:"label,omitempty"`
}

// ReportFact is the delivery-independent subset of a persisted worker report.
type ReportFact struct {
	ID          string
	SessionID   domain.SessionID
	ProjectID   domain.ProjectID
	State       string
	Note        string
	Message     string
	Outputs     []ReportOutputFact
	CreatedAt   time.Time
	RepeatCount int64
}

// GenerationReport is the meaningful worker-authored context sent to the narrative generator.
type GenerationReport struct {
	SessionID   domain.SessionID   `json:"sessionId"`
	SessionName string             `json:"sessionName"`
	State       string             `json:"state,omitempty"`
	Note        string             `json:"note,omitempty"`
	Message     string             `json:"message,omitempty"`
	Outputs     []ReportOutputFact `json:"outputs,omitempty"`
	CreatedAt   time.Time          `json:"createdAt"`
	RepeatCount int64              `json:"repeatCount,omitempty"`
}

// ReportReader returns persisted reports in creation and id order without mutating delivery state.
type ReportReader interface {
	ListProject(context.Context, domain.ProjectID) ([]ReportFact, error)
}

// Service builds and persists project summary projections.
type Service struct {
	store     Store
	generator NarrativeGenerator
	reports   ReportReader
	clock     func() time.Time
	locks     sync.Map
}

// New constructs a project summary service.
func New(store Store, generator NarrativeGenerator, reports ...ReportReader) *Service {
	service := &Service{store: store, generator: generator, clock: time.Now}
	if len(reports) > 0 {
		service.reports = reports[0]
	}
	return service
}

// Get reads the current projection and regenerates it when requested or missing.
func (s *Service) Get(ctx context.Context, projectID domain.ProjectID, refresh bool) (domain.ProjectSummary, error) {
	lock, _ := s.locks.LoadOrStore(projectID, &sync.Mutex{})
	projectLock, ok := lock.(*sync.Mutex)
	if !ok {
		return domain.ProjectSummary{}, fmt.Errorf("project %s summary lock has unexpected type", projectID)
	}
	projectLock.Lock()
	defer projectLock.Unlock()

	projectRecord, ok, err := s.store.GetProject(ctx, string(projectID))
	if err != nil {
		return domain.ProjectSummary{}, err
	} else if !ok {
		return domain.ProjectSummary{}, fmt.Errorf("project %s not found", projectID)
	}
	sessions, err := s.store.ListSessions(ctx, projectID)
	if err != nil {
		return domain.ProjectSummary{}, err
	}
	workers := make([]domain.SessionRecord, 0, len(sessions))
	for _, session := range sessions {
		if session.Kind == domain.KindWorker {
			workers = append(workers, session)
		}
	}
	var reports []ReportFact
	if s.reports != nil {
		reports, err = s.reports.ListProject(ctx, projectID)
		if err != nil {
			return domain.ProjectSummary{}, fmt.Errorf("list project reports: %w", err)
		}
	}
	next := project(projectID, workers, reports, s.clock())
	current, ok, err := s.store.GetProjectSummary(ctx, projectID)
	if err != nil {
		return domain.ProjectSummary{}, err
	}
	if ok && current.SourceWatermark == next.SourceWatermark {
		return current, nil
	}
	if !refresh && ok {
		return current, nil
	}
	if ok {
		next.NeedsAttention = preserveAttention(current.NeedsAttention, next.NeedsAttention, workers)
	}
	harness := projectRecord.Config.Orchestrator.Harness
	model := projectRecord.Config.Orchestrator.AgentConfig.Model
	for _, session := range sessions {
		if session.Kind == domain.KindOrchestrator && !session.IsTerminated {
			harness = session.Harness
			break
		}
	}
	if model == "" {
		model = projectRecord.Config.AgentConfig.Model
	}
	if s.generator == nil {
		return failedGeneration(current, ok, "project summary generator is unavailable"), nil
	}
	narrative, err := s.generator.Update(ctx, GenerationRequest{Harness: harness, Model: model, WorkspacePath: projectRecord.Path, Existing: current.Narrative, Facts: next, Reports: generationReports(reports, workers)})
	if err != nil {
		return failedGeneration(current, ok, err.Error()), nil
	}
	next.Narrative = narrative
	if err := s.store.PutProjectSummary(ctx, next); err != nil {
		return domain.ProjectSummary{}, err
	}
	return next, nil
}

func generationReports(reports []ReportFact, workers []domain.SessionRecord) []GenerationReport {
	result := make([]GenerationReport, 0, len(reports))
	for _, report := range reports {
		result = append(result, GenerationReport{
			SessionID: report.SessionID, SessionName: workerName(workers, report.SessionID), State: report.State,
			Note: report.Note, Message: report.Message, Outputs: report.Outputs, CreatedAt: report.CreatedAt, RepeatCount: report.RepeatCount,
		})
	}
	return result
}

func failedGeneration(current domain.ProjectSummary, exists bool, message string) domain.ProjectSummary {
	if !exists {
		current.NeedsAttention = []domain.ProjectAttentionItem{}
	}
	current.GenerationError = message
	return current
}

func preserveAttention(previous, observed []domain.ProjectAttentionItem, workers []domain.SessionRecord) []domain.ProjectAttentionItem {
	bySession := make(map[domain.SessionID]domain.ProjectAttentionItem, len(previous)+len(observed))
	for _, item := range previous {
		if workerIsLive(workers, item.SessionID) {
			bySession[item.SessionID] = item
		}
	}
	for _, item := range observed {
		if workerIsLive(workers, item.SessionID) {
			bySession[item.SessionID] = item
		}
	}
	result := make([]domain.ProjectAttentionItem, 0, len(bySession))
	for _, item := range bySession {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SessionID < result[j].SessionID })
	return result
}

func workerIsLive(workers []domain.SessionRecord, id domain.SessionID) bool {
	for _, worker := range workers {
		if worker.ID == id {
			return !worker.IsTerminated
		}
	}
	return false
}

func project(projectID domain.ProjectID, workers []domain.SessionRecord, reports []ReportFact, at time.Time) domain.ProjectSummary {
	sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "projection:%s;", summaryProjectionVersion)
	result := domain.ProjectSummary{ProjectID: projectID, GeneratedAt: at, NeedsAttention: []domain.ProjectAttentionItem{}}
	for _, worker := range workers {
		_, _ = fmt.Fprintf(h, "%s|%s|%t|%s|%s;", worker.ID, worker.Activity.State, worker.IsTerminated, worker.UpdatedAt.UTC(), worker.DisplayName)
		if worker.IsTerminated {
			result.CompletedWorkers++
		} else {
			result.ActiveWorkers++
		}
		if !worker.IsTerminated && worker.Activity.State == domain.ActivityWaitingInput {
			question := strings.TrimSpace(worker.Metadata.LatestAssistantUpdate)
			if question == "" {
				question = "This worker needs a decision before it can continue."
			}
			result.NeedsAttention = append(result.NeedsAttention, domain.ProjectAttentionItem{SessionID: worker.ID, SessionName: displayName(worker), Question: question})
		}
	}
	for _, report := range reports {
		_, _ = fmt.Fprintf(h, "%s|%s|%s|%s|%s|%d;", report.ID, report.SessionID, report.State, report.Note, report.Message, report.RepeatCount)
		if report.State == "needs_input" && workerIsLive(workers, report.SessionID) {
			question := strings.TrimSpace(report.Note)
			if question == "" {
				question = strings.TrimSpace(report.Message)
			}
			if question != "" {
				result.NeedsAttention = append(result.NeedsAttention, domain.ProjectAttentionItem{SessionID: report.SessionID, SessionName: workerName(workers, report.SessionID), Question: question})
			}
		}
		for _, output := range report.Outputs {
			_, _ = fmt.Fprintf(h, "%s|%s|%s;", output.Kind, output.Reference, output.Label)
		}
	}
	result.SourceWatermark = hex.EncodeToString(h.Sum(nil))
	return result
}

func workerName(workers []domain.SessionRecord, id domain.SessionID) string {
	for _, worker := range workers {
		if worker.ID == id {
			return displayName(worker)
		}
	}
	return string(id)
}

func displayName(session domain.SessionRecord) string {
	if strings.TrimSpace(session.DisplayName) != "" {
		return session.DisplayName
	}
	return string(session.ID)
}
