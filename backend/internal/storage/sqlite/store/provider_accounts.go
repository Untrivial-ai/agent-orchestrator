package store

import (
	"context"
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// LoadProviderAccounts reads the one document Account Manager stores.
func (s *Store) LoadProviderAccounts(ctx context.Context) (state domain.ProviderAccountState, err error) {
	facts, err := s.qr.LoadProviderAccounts(ctx)
	if err == nil {
		err = json.Unmarshal([]byte(facts), &state)
	}
	return state, err
}

// SaveProviderAccounts replaces that document.
func (s *Store) SaveProviderAccounts(ctx context.Context, state domain.ProviderAccountState) error {
	facts, _ := json.Marshal(state)
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	return s.qw.SaveProviderAccounts(ctx, string(facts))
}
