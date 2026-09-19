package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// GetProjectSummary reads the durable project summary projection.
func (s *Store) GetProjectSummary(ctx context.Context, projectID domain.ProjectID) (domain.ProjectSummary, bool, error) {
	var summary domain.ProjectSummary
	var projection string
	err := s.readDB.QueryRowContext(ctx, `SELECT narrative, projection_json, source_watermark, generated_at FROM project_summaries WHERE project_id = ?`, projectID).
		Scan(&summary.Narrative, &projection, &summary.SourceWatermark, &summary.GeneratedAt)
	if err == sql.ErrNoRows {
		return domain.ProjectSummary{}, false, nil
	}
	if err != nil {
		return domain.ProjectSummary{}, false, fmt.Errorf("get project summary %s: %w", projectID, err)
	}
	if err := json.Unmarshal([]byte(projection), &summary); err != nil {
		return domain.ProjectSummary{}, false, fmt.Errorf("decode project summary %s: %w", projectID, err)
	}
	summary.ProjectID = projectID
	return summary, true, nil
}

// PutProjectSummary replaces the durable project summary projection.
func (s *Store) PutProjectSummary(ctx context.Context, summary domain.ProjectSummary) error {
	projection, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode project summary %s: %w", summary.ProjectID, err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.writeDB.ExecContext(ctx, `INSERT INTO project_summaries(project_id, source_watermark, narrative, projection_json, generated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(project_id) DO UPDATE SET source_watermark=excluded.source_watermark, narrative=excluded.narrative, projection_json=excluded.projection_json, generated_at=excluded.generated_at`,
		summary.ProjectID, summary.SourceWatermark, summary.Narrative, string(projection), summary.GeneratedAt)
	if err != nil {
		return fmt.Errorf("put project summary %s: %w", summary.ProjectID, err)
	}
	return nil
}
