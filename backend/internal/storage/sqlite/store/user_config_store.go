package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	return unmarshalAgentConfig(config), true, nil
}

// UpsertUserConfig replaces the singleton user-scope config row wholesale.
func (s *Store) UpsertUserConfig(ctx context.Context, cfg domain.AgentConfig) error {
	nullStr, err := marshalAgentConfig(cfg)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.UpsertUserConfig(ctx, nullStr)
}

// marshalAgentConfig encodes the typed user-scope config into the nullable JSON
// column. An IsZero config stores SQL NULL so an unset config round-trips back
// to a zero value rather than an empty object. Mirrors marshalProjectConfig.
func marshalAgentConfig(cfg domain.AgentConfig) (sql.NullString, error) {
	if cfg.IsZero() {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("marshal user config: %w", err)
	}
	return sql.NullString{String: string(data), Valid: true}, nil
}

// unmarshalAgentConfig decodes the nullable JSON column back into the typed
// struct. SQL NULL (an unset config) decodes to a zero value. A damaged config
// (invalid JSON) also degrades to a zero value rather than erroring — a corrupt
// user config must never block reading the row. Mirrors unmarshalProjectConfig.
func unmarshalAgentConfig(s sql.NullString) domain.AgentConfig {
	if !s.Valid || s.String == "" {
		return domain.AgentConfig{}
	}
	var cfg domain.AgentConfig
	if err := json.Unmarshal([]byte(s.String), &cfg); err != nil {
		return domain.AgentConfig{}
	}
	return cfg
}
