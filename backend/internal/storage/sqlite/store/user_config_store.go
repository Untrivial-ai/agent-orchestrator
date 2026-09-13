package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// GetUserConfig returns the user-scope agent config (the singleton row). A
// missing row — the state for every user until they set one — reports as
// (zero, false, nil) so callers resolve to exactly today's behavior. sqlc's
// :one query surfaces a missing row as sql.ErrNoRows; the store absorbs it.
func (s *Store) GetUserConfig(ctx context.Context) (domain.AgentConfig, bool, error) {
	config, err := s.qr.GetUserConfig(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentConfig{}, false, nil
	}
	if err != nil {
		return domain.AgentConfig{}, false, fmt.Errorf("get user config: %w", err)
	}
	return unmarshalAgentConfig(config.String), true, nil
}

// UpsertUserConfig replaces the singleton user-scope config row wholesale.
func (s *Store) UpsertUserConfig(ctx context.Context, cfg domain.AgentConfig) error {
	encoded, err := marshalAgentConfig(cfg)
	if err != nil {
		return err
	}
	// An IsZero config encodes to "" and stores as SQL NULL, so an unset
	// config round-trips back to a zero value rather than an empty object.
	nullStr := sql.NullString{String: encoded, Valid: encoded != ""}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.UpsertUserConfig(ctx, nullStr)
}
