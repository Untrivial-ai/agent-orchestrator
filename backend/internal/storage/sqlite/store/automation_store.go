package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CreateAutomation persists a validated automation definition.
func (s *Store) CreateAutomation(ctx context.Context, rec domain.Automation) (domain.Automation, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.InsertAutomation(ctx, automationToInsert(rec)); err != nil {
		return domain.Automation{}, fmt.Errorf("insert automation %s: %w", rec.ID, err)
	}
	return rec, nil
}

// GetAutomation returns one automation definition by id.
func (s *Store) GetAutomation(ctx context.Context, id domain.AutomationID) (domain.Automation, bool, error) {
	row, err := s.qr.GetAutomation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Automation{}, false, nil
	}
	if err != nil {
		return domain.Automation{}, false, fmt.Errorf("get automation %s: %w", id, err)
	}
	return automationFromGen(row), true, nil
}

// ListAutomations returns a stable filtered page and its filtered total.
func (s *Store) ListAutomations(ctx context.Context, filter domain.AutomationFilter) (domain.AutomationPage, error) {
	projectID := domain.ProjectID("")
	if filter.ProjectID != nil {
		projectID = *filter.ProjectID
	}
	enabledFilter := int64(-1)
	if filter.Enabled != nil {
		if *filter.Enabled {
			enabledFilter = 1
		} else {
			enabledFilter = 0
		}
	}
	params := gen.ListAutomationsParams{
		ProjectID: projectID, EnabledFilter: enabledFilter,
		PageLimit: filter.Limit, PageOffset: filter.Offset,
	}
	rows, err := s.qr.ListAutomations(ctx, params)
	if err != nil {
		return domain.AutomationPage{}, fmt.Errorf("list automations: %w", err)
	}
	total, err := s.qr.CountAutomations(ctx, gen.CountAutomationsParams{
		ProjectID: projectID, EnabledFilter: enabledFilter,
	})
	if err != nil {
		return domain.AutomationPage{}, fmt.Errorf("count automations: %w", err)
	}
	items := make([]domain.Automation, 0, len(rows))
	for _, row := range rows {
		items = append(items, automationFromGen(row))
	}
	return domain.AutomationPage{Items: items, Total: total}, nil
}

// ListLatestAutomationRuns loads at most one newest run per requested
// definition in a single query.
func (s *Store) ListLatestAutomationRuns(ctx context.Context, ids []domain.AutomationID) (map[domain.AutomationID]domain.AutomationRun, error) {
	out := make(map[domain.AutomationID]domain.AutomationRun)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.qr.ListLatestAutomationRuns(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list latest automation runs: %w", err)
	}
	for _, row := range rows {
		run := automationRunFromGen(row)
		out[run.AutomationID] = run
	}
	return out, nil
}

// UpdateAutomation persists a complete validated mutable definition.
func (s *Store) UpdateAutomation(ctx context.Context, rec domain.Automation) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.UpdateAutomation(ctx, gen.UpdateAutomationParams{
		DisplayName: rec.DisplayName,
		Prompt:      rec.Prompt,
		Kind:        rec.Kind,
		Harness:     rec.Harness,
		RruleText:   rec.RRuleText,
		Timezone:    rec.Timezone,
		Enabled:     rec.Enabled,
		NextRunAt:   rec.NextRunAt,
		LastRunAt:   timePtrToNullTime(rec.LastRunAt),
		UpdatedAt:   rec.UpdatedAt,
		ID:          rec.ID,
	})
	if err != nil {
		return false, fmt.Errorf("update automation %s: %w", rec.ID, err)
	}
	return rows > 0, nil
}

// DeleteAutomation removes a definition. Database foreign keys cascade its
// run history and clear origin links on surviving sessions.
func (s *Store) DeleteAutomation(ctx context.Context, id domain.AutomationID) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.DeleteAutomation(ctx, id)
	if err != nil {
		return false, fmt.Errorf("delete automation %s: %w", id, err)
	}
	return rows > 0, nil
}

// ListDueAutomations returns enabled definitions whose durable cursor is due.
func (s *Store) ListDueAutomations(ctx context.Context, now time.Time, limit int64) ([]domain.Automation, error) {
	rows, err := s.qr.ListDueAutomations(ctx, gen.ListDueAutomationsParams{NextRunAt: now, Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("list due automations: %w", err)
	}
	items := make([]domain.Automation, 0, len(rows))
	for _, row := range rows {
		items = append(items, automationFromGen(row))
	}
	return items, nil
}

// MaterializeAutomationRuns atomically inserts a bounded occurrence batch and
// advances the definition cursor. expectedNext is a compare-and-swap fence
// against stale or concurrent scheduler passes.
func (s *Store) MaterializeAutomationRuns(
	ctx context.Context,
	id domain.AutomationID,
	expectedNext time.Time,
	runs []domain.AutomationRun,
	lastRunAt time.Time,
	nextRunAt time.Time,
	updatedAt time.Time,
) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	applied := false
	err := s.inTx(ctx, "materialize automation runs", func(q *gen.Queries) error {
		rows, err := q.AdvanceAutomationSchedule(ctx, gen.AdvanceAutomationScheduleParams{
			LastRunAt: timePtrToNullTime(&lastRunAt), NextRunAt: nextRunAt,
			UpdatedAt: updatedAt, ID: id, NextRunAt_2: expectedNext,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return nil
		}
		for _, run := range runs {
			if _, err := q.InsertAutomationRun(ctx, automationRunToInsert(run)); err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("materialize automation %s: %w", id, err)
	}
	return applied, nil
}

// ClaimNextAutomationRun leases the globally oldest eligible pending run.
// The transaction and active-sibling predicate are the durable non-overlap
// boundary for one automation definition.
func (s *Store) ClaimNextAutomationRun(ctx context.Context, now, leaseExpiresAt time.Time) (domain.AutomationRun, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var claimed domain.AutomationRun
	found := false
	err := s.inTx(ctx, "claim automation run", func(q *gen.Queries) error {
		candidate, err := q.GetNextClaimableAutomationRun(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		rows, err := q.ClaimAutomationRun(ctx, gen.ClaimAutomationRunParams{
			ClaimedAt: timePtrToNullTime(&now), LeaseExpiresAt: timePtrToNullTime(&leaseExpiresAt),
			UpdatedAt: now, ID: candidate.ID,
		})
		if err != nil || rows == 0 {
			return err
		}
		row, err := q.GetAutomationRun(ctx, candidate.ID)
		if err != nil {
			return err
		}
		claimed, found = automationRunFromGen(row), true
		return nil
	})
	if err != nil {
		return domain.AutomationRun{}, false, fmt.Errorf("claim automation run: %w", err)
	}
	return claimed, found, nil
}

func automationToInsert(rec domain.Automation) gen.InsertAutomationParams {
	return gen.InsertAutomationParams{
		ID:          rec.ID,
		ProjectID:   rec.ProjectID,
		DisplayName: rec.DisplayName,
		Prompt:      rec.Prompt,
		Kind:        rec.Kind,
		Harness:     rec.Harness,
		RruleText:   rec.RRuleText,
		Timezone:    rec.Timezone,
		Enabled:     rec.Enabled,
		NextRunAt:   rec.NextRunAt,
		LastRunAt:   timePtrToNullTime(rec.LastRunAt),
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
	}
}

func automationFromGen(row gen.Automation) domain.Automation {
	return domain.Automation{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		DisplayName: row.DisplayName,
		Prompt:      row.Prompt,
		Kind:        row.Kind,
		Harness:     row.Harness,
		RRuleText:   row.RruleText,
		Timezone:    row.Timezone,
		Enabled:     row.Enabled,
		NextRunAt:   row.NextRunAt,
		LastRunAt:   nullTimeToTimePtr(row.LastRunAt),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

// CreateAutomationRun inserts one logical occurrence. When the occurrence was
// already materialized it returns the existing row with created=false.
func (s *Store) CreateAutomationRun(ctx context.Context, rec domain.AutomationRun) (domain.AutomationRun, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := s.qw.InsertAutomationRun(ctx, automationRunToInsert(rec))
	if err != nil {
		return domain.AutomationRun{}, false, fmt.Errorf("insert automation run %s: %w", rec.ID, err)
	}
	if rows > 0 {
		return rec, true, nil
	}
	row, err := s.qw.GetAutomationRunByOccurrence(ctx, gen.GetAutomationRunByOccurrenceParams{
		AutomationID: rec.AutomationID,
		ScheduledFor: rec.ScheduledFor,
	})
	if err != nil {
		return domain.AutomationRun{}, false, fmt.Errorf("read existing automation run occurrence: %w", err)
	}
	return automationRunFromGen(row), false, nil
}

// GetAutomationRun returns one durable occurrence by id.
func (s *Store) GetAutomationRun(ctx context.Context, id domain.AutomationRunID) (domain.AutomationRun, bool, error) {
	row, err := s.qr.GetAutomationRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AutomationRun{}, false, nil
	}
	if err != nil {
		return domain.AutomationRun{}, false, fmt.Errorf("get automation run %s: %w", id, err)
	}
	return automationRunFromGen(row), true, nil
}

// ListActiveAutomationRuns returns spawning and running rows in occurrence order.
func (s *Store) ListActiveAutomationRuns(ctx context.Context) ([]domain.AutomationRun, error) {
	rows, err := s.qr.ListActiveAutomationRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active automation runs: %w", err)
	}
	return automationRunsFromGen(rows), nil
}

// ListExpiredSpawningAutomationRuns returns claims eligible for boot recovery.
func (s *Store) ListExpiredSpawningAutomationRuns(ctx context.Context, now time.Time) ([]domain.AutomationRun, error) {
	rows, err := s.qr.ListExpiredSpawningAutomationRuns(ctx, timePtrToNullTime(&now))
	if err != nil {
		return nil, fmt.Errorf("list expired automation claims: %w", err)
	}
	return automationRunsFromGen(rows), nil
}

func automationRunsFromGen(rows []gen.AutomationRun) []domain.AutomationRun {
	items := make([]domain.AutomationRun, 0, len(rows))
	for _, row := range rows {
		items = append(items, automationRunFromGen(row))
	}
	return items
}

// MarkAutomationRunRunning links the unique spawned session.
func (s *Store) MarkAutomationRunRunning(ctx context.Context, id domain.AutomationRunID, sessionID domain.SessionID, now time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.MarkAutomationRunRunning(ctx, gen.MarkAutomationRunRunningParams{
		SessionID: &sessionID, StartedAt: timePtrToNullTime(&now), UpdatedAt: now, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("mark automation run %s running: %w", id, err)
	}
	return rows > 0, nil
}

// CompleteAutomationRun projects durable session termination into run history.
func (s *Store) CompleteAutomationRun(ctx context.Context, id domain.AutomationRunID, now time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.CompleteAutomationRun(ctx, gen.CompleteAutomationRunParams{FinishedAt: timePtrToNullTime(&now), UpdatedAt: now, ID: id})
	if err != nil {
		return false, fmt.Errorf("complete automation run %s: %w", id, err)
	}
	return rows > 0, nil
}

// FailAutomationRun terminally records a bounded permanent dispatch error.
func (s *Store) FailAutomationRun(ctx context.Context, id domain.AutomationRunID, message string, now time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.FailAutomationRun(ctx, gen.FailAutomationRunParams{
		FinishedAt: timePtrToNullTime(&now), ErrorMessage: nullableString(message), UpdatedAt: now, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("fail automation run %s: %w", id, err)
	}
	return rows > 0, nil
}

// ReleaseAutomationRun returns a transiently failed claim to pending.
func (s *Store) ReleaseAutomationRun(ctx context.Context, id domain.AutomationRunID, message string, now time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.ReleaseAutomationRun(ctx, gen.ReleaseAutomationRunParams{ErrorMessage: nullableString(message), UpdatedAt: now, ID: id})
	if err != nil {
		return false, fmt.Errorf("release automation run %s: %w", id, err)
	}
	return rows > 0, nil
}

func automationRunToInsert(rec domain.AutomationRun) gen.InsertAutomationRunParams {
	return gen.InsertAutomationRunParams{
		ID:             rec.ID,
		AutomationID:   rec.AutomationID,
		ScheduledFor:   rec.ScheduledFor,
		SessionID:      rec.SessionID,
		Status:         rec.Status,
		AttemptCount:   rec.AttemptCount,
		ClaimedAt:      timePtrToNullTime(rec.ClaimedAt),
		LeaseExpiresAt: timePtrToNullTime(rec.LeaseExpiresAt),
		StartedAt:      timePtrToNullTime(rec.StartedAt),
		FinishedAt:     timePtrToNullTime(rec.FinishedAt),
		ErrorMessage:   nullableString(rec.ErrorMessage),
		CreatedAt:      rec.CreatedAt,
		UpdatedAt:      rec.UpdatedAt,
	}
}

func automationRunFromGen(row gen.AutomationRun) domain.AutomationRun {
	return domain.AutomationRun{
		ID:             row.ID,
		AutomationID:   row.AutomationID,
		ScheduledFor:   row.ScheduledFor,
		SessionID:      row.SessionID,
		Status:         row.Status,
		AttemptCount:   row.AttemptCount,
		ClaimedAt:      nullTimeToTimePtr(row.ClaimedAt),
		LeaseExpiresAt: nullTimeToTimePtr(row.LeaseExpiresAt),
		StartedAt:      nullTimeToTimePtr(row.StartedAt),
		FinishedAt:     nullTimeToTimePtr(row.FinishedAt),
		ErrorMessage:   row.ErrorMessage.String,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

// ListAutomationRuns returns newest-first history and its filtered total.
func (s *Store) ListAutomationRuns(ctx context.Context, filter domain.AutomationRunFilter) (domain.AutomationRunPage, error) {
	status := domain.AutomationRunStatus("")
	if filter.Status != nil {
		status = *filter.Status
	}
	rows, err := s.qr.ListAutomationRuns(ctx, gen.ListAutomationRunsParams{
		AutomationID: filter.AutomationID,
		StatusFilter: status,
		PageLimit:    filter.Limit,
		PageOffset:   filter.Offset,
	})
	if err != nil {
		return domain.AutomationRunPage{}, fmt.Errorf("list automation runs: %w", err)
	}
	total, err := s.qr.CountAutomationRuns(ctx, gen.CountAutomationRunsParams{
		AutomationID: filter.AutomationID,
		StatusFilter: status,
	})
	if err != nil {
		return domain.AutomationRunPage{}, fmt.Errorf("count automation runs: %w", err)
	}
	items := make([]domain.AutomationRun, 0, len(rows))
	for _, row := range rows {
		items = append(items, automationRunFromGen(row))
	}
	return domain.AutomationRunPage{Items: items, Total: total}, nil
}
