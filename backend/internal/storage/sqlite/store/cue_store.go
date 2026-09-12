package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	moderncsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// InsertCue persists a user-managed cue. A duplicate (project_id, name) pair
// reports domain.ErrCueNameExists.
func (s *Store) InsertCue(ctx context.Context, cue domain.Cue) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.qw.InsertCue(ctx, gen.InsertCueParams{
		ID:          cue.ID,
		ProjectID:   cue.ProjectID,
		Name:        cue.Name,
		Description: cue.Description,
		Type:        cue.Type,
		Command:     cue.Command,
		Prompt:      cue.Prompt,
		CreatedAt:   cue.CreatedAt,
		UpdatedAt:   cue.UpdatedAt,
	})
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.ErrCueNameExists
		}
		if isSQLiteForeignKey(err) {
			return domain.ErrProjectUnknown
		}
		return fmt.Errorf("insert cue %s: %w", cue.ID, err)
	}
	return nil
}

// SelectCueByID looks up one cue, reporting whether it existed so the caller
// can answer 404 for an unknown cue id.
func (s *Store) SelectCueByID(ctx context.Context, cueID domain.CueID) (domain.Cue, bool, error) {
	row, err := s.qr.SelectCueByID(ctx, cueID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Cue{}, false, nil
	}
	if err != nil {
		return domain.Cue{}, false, fmt.Errorf("select cue %s: %w", cueID, err)
	}
	return cueFromGen(row), true, nil
}

// SelectCuesByProject returns one project's cues in name order.
func (s *Store) SelectCuesByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.Cue, error) {
	rows, err := s.qr.SelectCuesByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("select cues for project %s: %w", projectID, err)
	}
	out := make([]domain.Cue, 0, len(rows))
	for _, row := range rows {
		out = append(out, cueFromGen(row))
	}
	return out, nil
}

// UpdateCue replaces a cue's definition, reporting whether a row actually
// existed and domain.ErrCueNameExists when a renamed cue collides with an
// existing name in the same project.
func (s *Store) UpdateCue(ctx context.Context, cue domain.Cue) (domain.Cue, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.UpdateCue(ctx, gen.UpdateCueParams{
		Name:        cue.Name,
		Description: cue.Description,
		Type:        cue.Type,
		Command:     cue.Command,
		Prompt:      cue.Prompt,
		UpdatedAt:   cue.UpdatedAt,
		ID:          cue.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Cue{}, false, nil
	}
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.Cue{}, false, domain.ErrCueNameExists
		}
		return domain.Cue{}, false, fmt.Errorf("update cue %s: %w", cue.ID, err)
	}
	return cueFromGen(row), true, nil
}

// DeleteCueByID forgets one cue, reporting whether a row actually existed so
// the caller can answer 404 for an unknown cue id.
func (s *Store) DeleteCueByID(ctx context.Context, cueID domain.CueID) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	affected, err := s.qw.DeleteCueByID(ctx, cueID)
	if err != nil {
		return false, fmt.Errorf("delete cue %s: %w", cueID, err)
	}
	return affected > 0, nil
}

func isSQLiteForeignKey(err error) bool {
	var sqliteErr *moderncsqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
}

func cueFromGen(row gen.Cue) domain.Cue {
	return domain.Cue{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		Name:        row.Name,
		Description: row.Description,
		Type:        row.Type,
		Command:     row.Command,
		Prompt:      row.Prompt,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}
