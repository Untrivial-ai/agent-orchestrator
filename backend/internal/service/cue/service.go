package cue

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Service owns the project-scoped Cue lifecycle: create, read, list, update,
// and delete. Validation rejects malformed definitions up front; uniqueness and
// project existence are enforced by the store and surfaced as API errors.
type Service struct {
	store    Store
	sessions Sessions
	newID    func() string
	now      func() time.Time
}

// Deps configures a Service.
type Deps struct {
	Store Store
	// Sessions is the session side of cue invocation: messaging an active
	// session or spawning a worker when no session was requested.
	Sessions Sessions
	// NewID overrides the cue id generator in tests.
	NewID func() string
	// Now overrides the clock in tests.
	Now func() time.Time
}

// New constructs the cue service.
func New(d Deps) *Service {
	if d.NewID == nil {
		d.NewID = func() string { return "cue_" + uuid.NewString() }
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: d.Store, sessions: d.Sessions, newID: d.NewID, now: d.Now}
}

// Create persists a new cue for a project. It reports INVALID_PROJECT_ID for a
// missing project id, the definition-level INVALID_CUE_* errors for malformed
// input, CUE_NAME_EXISTS for a duplicate name within the project, and
// PROJECT_NOT_FOUND for an unregistered project.
func (s *Service) Create(ctx context.Context, projectID domain.ProjectID, input Input) (domain.Cue, error) {
	if s == nil || s.store == nil {
		return domain.Cue{}, errors.New("cue: store is required")
	}
	if strings.TrimSpace(string(projectID)) == "" {
		return domain.Cue{}, apierr.Invalid("INVALID_PROJECT_ID", "Project id is required", nil)
	}
	now := s.now().UTC()
	cue := normalizeInput(input)
	cue.ID = domain.CueID(s.newID())
	cue.ProjectID = projectID
	cue.CreatedAt = now
	cue.UpdatedAt = now
	if err := cue.Validate(); err != nil {
		return domain.Cue{}, invalidCueError(err)
	}
	clearInactivePayload(&cue)
	if err := s.store.InsertCue(ctx, cue); err != nil {
		return domain.Cue{}, storeError(err)
	}
	return cue, nil
}

// List returns one project's cues in name order.
func (s *Service) List(ctx context.Context, projectID domain.ProjectID) ([]domain.Cue, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("cue: store is required")
	}
	if strings.TrimSpace(string(projectID)) == "" {
		return nil, apierr.Invalid("INVALID_PROJECT_ID", "Project id is required", nil)
	}
	return s.store.SelectCuesByProject(ctx, projectID)
}

// Update replaces a cue's whole definition, reporting CUE_NOT_FOUND for an
// unknown id and CUE_NAME_EXISTS for a rename that collides within the project.
func (s *Service) Update(ctx context.Context, cueID domain.CueID, input Input) (domain.Cue, error) {
	if s == nil || s.store == nil {
		return domain.Cue{}, errors.New("cue: store is required")
	}
	if strings.TrimSpace(string(cueID)) == "" {
		return domain.Cue{}, apierr.Invalid("INVALID_CUE_ID", "Cue id is required", nil)
	}
	updated := normalizeInput(input)
	updated.ID = cueID
	updated.UpdatedAt = s.now().UTC()
	if err := updated.Validate(); err != nil {
		return domain.Cue{}, invalidCueError(err)
	}
	clearInactivePayload(&updated)
	cue, ok, err := s.store.UpdateCue(ctx, updated)
	if err != nil {
		return domain.Cue{}, storeError(err)
	}
	if !ok {
		return domain.Cue{}, apierr.NotFound("CUE_NOT_FOUND", "Unknown cue")
	}
	return cue, nil
}

// Delete forgets one cue, reporting CUE_NOT_FOUND for an unknown id.
func (s *Service) Delete(ctx context.Context, cueID domain.CueID) error {
	if s == nil || s.store == nil {
		return errors.New("cue: store is required")
	}
	if strings.TrimSpace(string(cueID)) == "" {
		return apierr.Invalid("INVALID_CUE_ID", "Cue id is required", nil)
	}
	ok, err := s.store.DeleteCueByID(ctx, cueID)
	if err != nil {
		return err
	}
	if !ok {
		return apierr.NotFound("CUE_NOT_FOUND", "Unknown cue")
	}
	return nil
}

func normalizeInput(input Input) domain.Cue {
	return domain.Cue{
		Name:        strings.TrimSpace(input.Name),
		Description: input.Description,
		Type:        input.Type,
		Command:     input.Command,
		Prompt:      input.Prompt,
	}
}

func clearInactivePayload(cue *domain.Cue) {
	if cue.Type == domain.CueTypeCommand {
		cue.Prompt = ""
	} else {
		cue.Command = ""
	}
}

func storeError(err error) error {
	switch {
	case errors.Is(err, domain.ErrCueNameExists):
		return apierr.Conflict("CUE_NAME_EXISTS", "A cue with this name already exists in the project", nil)
	case errors.Is(err, domain.ErrProjectUnknown):
		return apierr.NotFound("PROJECT_NOT_FOUND", "Unknown project")
	default:
		return err
	}
}

func invalidCueError(err error) *apierr.Error {
	switch {
	case errors.Is(err, domain.ErrInvalidCueName):
		return apierr.Invalid("INVALID_CUE_NAME", "Cue name is required and must be at most 64 bytes", nil)
	case errors.Is(err, domain.ErrInvalidCueDescription):
		return apierr.Invalid("INVALID_CUE_DESCRIPTION", "Cue description must be at most 240 bytes", nil)
	case errors.Is(err, domain.ErrInvalidCueType):
		return apierr.Invalid("INVALID_CUE_TYPE", "Cue type must be command or agent", nil)
	case errors.Is(err, domain.ErrInvalidCueCommand):
		return apierr.Invalid("INVALID_CUE_COMMAND", "Command cues require a command of at most 4096 bytes", nil)
	case errors.Is(err, domain.ErrInvalidCuePrompt):
		return apierr.Invalid("INVALID_CUE_PROMPT", "Agent cues require a prompt of at most 16384 bytes", nil)
	default:
		return apierr.Invalid("INVALID_CUE", err.Error(), nil)
	}
}
