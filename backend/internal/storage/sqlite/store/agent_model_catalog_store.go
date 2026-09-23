package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// GetAgentModelCatalog loads the cached catalog for one agent/project scope.
func (s *Store) GetAgentModelCatalog(ctx context.Context, agentID, projectID string) (ports.CachedAgentModelCatalog, bool, error) {
	row, err := s.qr.GetAgentModelCatalog(ctx, gen.GetAgentModelCatalogParams{
		AgentID:   agentID,
		ProjectID: projectID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ports.CachedAgentModelCatalog{}, false, nil
	}
	if err != nil {
		return ports.CachedAgentModelCatalog{}, false, fmt.Errorf("get agent model catalog %s: %w", agentID, err)
	}
	return cachedAgentModelCatalog(row), true, nil
}

// ListAgentModelCatalogsByAgent returns every previously discovered project
// scope for one agent in stable project order.
func (s *Store) ListAgentModelCatalogsByAgent(ctx context.Context, agentID string) ([]ports.CachedAgentModelCatalog, error) {
	rows, err := s.qr.ListAgentModelCatalogsByAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent model catalogs %s: %w", agentID, err)
	}
	records := make([]ports.CachedAgentModelCatalog, 0, len(rows))
	for _, row := range rows {
		records = append(records, cachedAgentModelCatalog(row))
	}
	return records, nil
}

// ListAgentModelCatalogs returns every known scope for prioritized startup
// prefetch. It never creates speculative scopes.
func (s *Store) ListAgentModelCatalogs(ctx context.Context) ([]ports.CachedAgentModelCatalog, error) {
	rows, err := s.qr.ListAgentModelCatalogs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agent model catalogs: %w", err)
	}
	records := make([]ports.CachedAgentModelCatalog, 0, len(rows))
	for _, row := range rows {
		records = append(records, cachedAgentModelCatalog(row))
	}
	return records, nil
}

// UpsertAgentModelCatalog stores the latest successfully discovered catalog.
func (s *Store) UpsertAgentModelCatalog(ctx context.Context, record ports.CachedAgentModelCatalog) error {
	legacyRecord := record.RefreshState == "" && record.Generation == 0
	if record.MetadataJSON == "" {
		record.MetadataJSON = "{}"
	}
	if record.InputFingerprint == "" {
		record.InputFingerprint = record.BinaryVersion
	}
	if record.RefreshState == "" {
		record.RefreshState = "idle"
	}
	if legacyRecord && record.LastSuccessAt.IsZero() {
		record.LastSuccessAt = record.FetchedAt
	}
	if err := s.qw.UpsertAgentModelCatalog(ctx, gen.UpsertAgentModelCatalogParams{
		AgentID:          record.AgentID,
		ProjectID:        record.ProjectID,
		BinaryVersion:    record.BinaryVersion,
		CatalogJson:      record.CatalogJSON,
		Source:           record.Source,
		FetchedAt:        record.FetchedAt.UTC(),
		MetadataJson:     record.MetadataJSON,
		InputFingerprint: record.InputFingerprint,
		LastSuccessAt:    nullableTime(record.LastSuccessAt),
		RefreshState:     record.RefreshState,
		RefreshError:     record.RefreshError,
		RetryCount:       record.RetryCount,
		RetryAt:          nullableTime(record.RetryAt),
		Generation:       record.Generation,
	}); err != nil {
		return fmt.Errorf("upsert agent model catalog %s: %w", record.AgentID, err)
	}
	return nil
}

func cachedAgentModelCatalog(row gen.AgentModelCatalog) ports.CachedAgentModelCatalog {
	return ports.CachedAgentModelCatalog{
		AgentID: row.AgentID, ProjectID: row.ProjectID, BinaryVersion: row.BinaryVersion,
		CatalogJSON: row.CatalogJson, Source: row.Source, FetchedAt: row.FetchedAt,
		MetadataJSON: row.MetadataJson, InputFingerprint: row.InputFingerprint,
		LastSuccessAt: row.LastSuccessAt.Time, RefreshState: row.RefreshState,
		RefreshError: row.RefreshError, RetryCount: row.RetryCount,
		RetryAt: row.RetryAt.Time, Generation: row.Generation,
	}
}

func nullableTime(value time.Time) sql.NullTime {
	return sql.NullTime{Time: value.UTC(), Valid: !value.IsZero()}
}
