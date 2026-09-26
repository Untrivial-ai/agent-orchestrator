package automation

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

const (
	maxDisplayNameRunes = 120
	maxPromptBytes      = 4096
)

// Store is the persistence surface needed for definition management.
type Store interface {
	GetProject(context.Context, string) (domain.ProjectRecord, bool, error)
	CreateAutomation(context.Context, domain.Automation) (domain.Automation, error)
	GetAutomation(context.Context, domain.AutomationID) (domain.Automation, bool, error)
	ListAutomations(context.Context, domain.AutomationFilter) (domain.AutomationPage, error)
	UpdateAutomation(context.Context, domain.Automation) (bool, error)
	DeleteAutomation(context.Context, domain.AutomationID) (bool, error)
	ListAutomationRuns(context.Context, domain.AutomationRunFilter) (domain.AutomationRunPage, error)
	ListLatestAutomationRuns(context.Context, []domain.AutomationID) (map[domain.AutomationID]domain.AutomationRun, error)
}

// Deps are the automation service's injectable dependencies.
type Deps struct {
	Store Store
	// Spawner is the existing daemon session service; automations do not add a
	// second launch path.
	Spawner SessionSpawner
	Clock   func() time.Time
	NewID   func() string
}

// Service owns automation validation and scheduling behavior.
type Service struct {
	store   Store
	spawner SessionSpawner
	clock   func() time.Time
	newID   func() string
}

// New constructs the daemon-owned automation service.
func New(deps Deps) *Service {
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.NewID == nil {
		deps.NewID = uuid.NewString
	}
	return &Service{store: deps.Store, spawner: deps.Spawner, clock: deps.Clock, newID: deps.NewID}
}

// CreateInput is the validated user intent for one recurring definition.
type CreateInput struct {
	ProjectID   domain.ProjectID
	DisplayName string
	Prompt      string
	Kind        domain.SessionKind
	Harness     domain.AgentHarness
	RRule       string
	Cron        string
	Timezone    string
	Enabled     *bool
}

// UpdateInput distinguishes omitted fields from explicit false/empty values.
// Supplying either schedule source replaces the schedule atomically.
type UpdateInput struct {
	DisplayName *string
	Prompt      *string
	Kind        *domain.SessionKind
	Harness     *domain.AgentHarness
	RRule       *string
	Cron        *string
	Timezone    *string
	Enabled     *bool
}

// Create validates, canonicalizes, and persists an automation definition.
func (s *Service) Create(ctx context.Context, input CreateInput) (domain.Automation, error) {
	if s.store == nil {
		return domain.Automation{}, apierr.Internal("AUTOMATION_CREATE_FAILED", "Failed to create automation")
	}
	project, ok, err := s.store.GetProject(ctx, string(input.ProjectID))
	if err != nil {
		return domain.Automation{}, apierr.Internal("AUTOMATION_CREATE_FAILED", "Failed to create automation")
	}
	if !ok || !project.ArchivedAt.IsZero() {
		return domain.Automation{}, apierr.NotFound("PROJECT_NOT_FOUND", "Unknown project")
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" || utf8.RuneCountInString(displayName) > maxDisplayNameRunes {
		return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_NAME", "Automation name must be between 1 and 120 characters", nil)
	}
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" || len(prompt) > maxPromptBytes {
		return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_PROMPT", "Automation prompt must be between 1 and 4096 bytes", nil)
	}
	if input.Kind != domain.KindWorker && input.Kind != domain.KindOrchestrator {
		return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_KIND", "Automation kind must be worker or orchestrator", nil)
	}
	if input.Harness != "" && !input.Harness.IsKnown() {
		return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_HARNESS", "Unknown automation harness", nil)
	}
	now := s.clock().UTC()
	schedule, err := CanonicalizeSchedule(ScheduleInput{
		RRule: input.RRule, Cron: input.Cron, Timezone: input.Timezone,
	}, now)
	if err != nil {
		return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_SCHEDULE", err.Error(), nil)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	rec := domain.Automation{
		ID:          domain.AutomationID("automation-" + s.newID()),
		ProjectID:   input.ProjectID,
		DisplayName: displayName,
		Prompt:      prompt,
		Kind:        input.Kind,
		Harness:     input.Harness,
		RRuleText:   schedule.RRuleText,
		Timezone:    schedule.Timezone,
		Enabled:     enabled,
		NextRunAt:   schedule.NextRunAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	created, err := s.store.CreateAutomation(ctx, rec)
	if err != nil {
		return domain.Automation{}, apierr.Internal("AUTOMATION_CREATE_FAILED", "Failed to create automation")
	}
	return created, nil
}

// Get returns one automation definition.
func (s *Service) Get(ctx context.Context, id domain.AutomationID) (domain.Automation, error) {
	rec, ok, err := s.store.GetAutomation(ctx, id)
	if err != nil {
		return domain.Automation{}, apierr.Internal("AUTOMATION_READ_FAILED", "Failed to read automation")
	}
	if !ok {
		return domain.Automation{}, apierr.NotFound("AUTOMATION_NOT_FOUND", "Unknown automation")
	}
	latest, err := s.store.ListLatestAutomationRuns(ctx, []domain.AutomationID{id})
	if err != nil {
		return domain.Automation{}, apierr.Internal("AUTOMATION_READ_FAILED", "Failed to read automation")
	}
	if run, found := latest[id]; found {
		rec.LatestRun = &run
	}
	return rec, nil
}

// List returns a normalized page of definitions.
func (s *Service) List(ctx context.Context, filter domain.AutomationFilter) (domain.AutomationPage, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 0 || filter.Limit > 100 || filter.Offset < 0 {
		return domain.AutomationPage{}, apierr.Invalid("INVALID_PAGINATION", "Limit must be between 1 and 100 and offset cannot be negative", nil)
	}
	page, err := s.store.ListAutomations(ctx, filter)
	if err != nil {
		return domain.AutomationPage{}, apierr.Internal("AUTOMATION_LIST_FAILED", "Failed to list automations")
	}
	ids := make([]domain.AutomationID, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	latest, err := s.store.ListLatestAutomationRuns(ctx, ids)
	if err != nil {
		return domain.AutomationPage{}, apierr.Internal("AUTOMATION_LIST_FAILED", "Failed to list automations")
	}
	for i := range page.Items {
		if run, ok := latest[page.Items[i].ID]; ok {
			latestRun := run
			page.Items[i].LatestRun = &latestRun
		}
	}
	return page, nil
}

// Update validates a partial definition update and recomputes the next future
// occurrence when a schedule changes or a disabled definition is re-enabled.
func (s *Service) Update(ctx context.Context, id domain.AutomationID, input UpdateInput) (domain.Automation, error) {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return domain.Automation{}, err
	}
	if input.DisplayName != nil {
		rec.DisplayName = strings.TrimSpace(*input.DisplayName)
		if rec.DisplayName == "" || utf8.RuneCountInString(rec.DisplayName) > maxDisplayNameRunes {
			return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_NAME", "Automation name must be between 1 and 120 characters", nil)
		}
	}
	if input.Prompt != nil {
		rec.Prompt = strings.TrimSpace(*input.Prompt)
		if rec.Prompt == "" || len(rec.Prompt) > maxPromptBytes {
			return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_PROMPT", "Automation prompt must be between 1 and 4096 bytes", nil)
		}
	}
	if input.Kind != nil {
		if *input.Kind != domain.KindWorker && *input.Kind != domain.KindOrchestrator {
			return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_KIND", "Automation kind must be worker or orchestrator", nil)
		}
		rec.Kind = *input.Kind
	}
	if input.Harness != nil {
		if *input.Harness != "" && !input.Harness.IsKnown() {
			return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_HARNESS", "Unknown automation harness", nil)
		}
		rec.Harness = *input.Harness
	}
	now := s.clock().UTC()
	scheduleChanged := input.RRule != nil || input.Cron != nil || input.Timezone != nil
	if scheduleChanged {
		rruleText, cronText, timezone := "", "", rec.Timezone
		if input.RRule != nil {
			rruleText = *input.RRule
		}
		if input.Cron != nil {
			cronText = *input.Cron
		}
		if input.Timezone != nil {
			timezone = *input.Timezone
		}
		if input.RRule == nil && input.Cron == nil {
			rruleText = rec.RRuleText
		}
		schedule, scheduleErr := CanonicalizeSchedule(ScheduleInput{RRule: rruleText, Cron: cronText, Timezone: timezone}, now)
		if scheduleErr != nil {
			return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_SCHEDULE", scheduleErr.Error(), nil)
		}
		rec.RRuleText, rec.Timezone, rec.NextRunAt = schedule.RRuleText, schedule.Timezone, schedule.NextRunAt
	}
	if input.Enabled != nil {
		wasEnabled := rec.Enabled
		rec.Enabled = *input.Enabled
		if !wasEnabled && rec.Enabled && !scheduleChanged {
			next, nextErr := NextOccurrence(rec.RRuleText, rec.Timezone, now)
			if nextErr != nil {
				return domain.Automation{}, apierr.Invalid("INVALID_AUTOMATION_SCHEDULE", nextErr.Error(), nil)
			}
			rec.NextRunAt = next
		}
	}
	rec.UpdatedAt = now
	ok, err := s.store.UpdateAutomation(ctx, rec)
	if err != nil {
		return domain.Automation{}, apierr.Internal("AUTOMATION_UPDATE_FAILED", "Failed to update automation")
	}
	if !ok {
		return domain.Automation{}, apierr.NotFound("AUTOMATION_NOT_FOUND", "Unknown automation")
	}
	return s.Get(ctx, id)
}

// Delete removes a definition and its run history. Linked sessions survive by
// the database foreign-key contract.
func (s *Service) Delete(ctx context.Context, id domain.AutomationID) error {
	ok, err := s.store.DeleteAutomation(ctx, id)
	if err != nil {
		return apierr.Internal("AUTOMATION_DELETE_FAILED", "Failed to delete automation")
	}
	if !ok {
		return apierr.NotFound("AUTOMATION_NOT_FOUND", "Unknown automation")
	}
	return nil
}

// Runs returns one definition's newest-first durable run history.
func (s *Service) Runs(ctx context.Context, filter domain.AutomationRunFilter) (domain.AutomationRunPage, error) {
	if _, err := s.Get(ctx, filter.AutomationID); err != nil {
		return domain.AutomationRunPage{}, err
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 0 || filter.Limit > 100 || filter.Offset < 0 {
		return domain.AutomationRunPage{}, apierr.Invalid("INVALID_PAGINATION", "Limit must be between 1 and 100 and offset cannot be negative", nil)
	}
	page, err := s.store.ListAutomationRuns(ctx, filter)
	if err != nil {
		return domain.AutomationRunPage{}, apierr.Internal("AUTOMATION_RUN_LIST_FAILED", "Failed to list automation runs")
	}
	return page, nil
}
