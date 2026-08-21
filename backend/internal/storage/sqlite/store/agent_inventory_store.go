package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// GetAgentInventoryCache loads the last successfully observed local agent inventory.
func (s *Store) GetAgentInventoryCache(ctx context.Context) (string, time.Time, bool, error) {
	row, err := s.qr.GetAgentInventoryCache(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("get agent inventory cache: %w", err)
	}
	return row.InventoryJson, row.ObservedAt, true, nil
}

// UpsertAgentInventoryCache atomically replaces the advisory inventory snapshot.
func (s *Store) UpsertAgentInventoryCache(ctx context.Context, inventoryJSON string, observedAt time.Time) error {
	if err := s.qw.UpsertAgentInventoryCache(ctx, gen.UpsertAgentInventoryCacheParams{
		InventoryJson: inventoryJSON,
		ObservedAt:    observedAt.UTC(),
	}); err != nil {
		return fmt.Errorf("upsert agent inventory cache: %w", err)
	}
	return nil
}
