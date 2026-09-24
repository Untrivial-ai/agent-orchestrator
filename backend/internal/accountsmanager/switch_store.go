package accountsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// codexAccountSwitchStore persists only the non-secret switch journal. Account
// credentials and provider responses never enter this file.
type codexAccountSwitchStore struct {
	path string
	mu   sync.Mutex
}

type persistedCodexAccountSwitchState struct {
	Switches []persistedCodexAccountSwitch `json:"switches"`
}

// The domain deliberately keeps IdempotencyKey out of API JSON. The private
// journal still needs it to replay requests safely, so it has its own disk
// representation instead of marshaling the domain object directly.
type persistedCodexAccountSwitch struct {
	ID                     string                              `json:"id"`
	SourceKind             domain.CodexAccountSwitchSourceKind `json:"sourceKind"`
	SourceAccountID        string                              `json:"sourceAccountId,omitempty"`
	TargetAccountID        string                              `json:"targetAccountId"`
	Phase                  domain.CodexAccountSwitchPhase      `json:"phase"`
	FailureCode            string                              `json:"failureCode,omitempty"`
	CredentialsCommittedAt *time.Time                          `json:"credentialsCommittedAt,omitempty"`
	CreatedAt              time.Time                           `json:"createdAt"`
	UpdatedAt              time.Time                           `json:"updatedAt"`
	CompletedAt            *time.Time                          `json:"completedAt,omitempty"`
	IdempotencyKey         string                              `json:"idempotencyKey"`
}

func newCodexAccountSwitchStore(path string) (*codexAccountSwitchStore, error) {
	store := &codexAccountSwitchStore{path: path}
	if _, err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *codexAccountSwitchStore) load() ([]domain.CodexAccountSwitch, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Codex account switch state: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return nil, fmt.Errorf("protect Codex account switch state: %w", err)
	}
	var state persistedCodexAccountSwitchState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decode Codex account switch state: %w", err)
	}
	switches := make([]domain.CodexAccountSwitch, 0, len(state.Switches))
	for _, persisted := range state.Switches {
		switches = append(switches, persisted.domain())
	}
	return switches, nil
}

func (s *codexAccountSwitchStore) persist(switches []domain.CodexAccountSwitch) error {
	persisted := make([]persistedCodexAccountSwitch, 0, len(switches))
	for _, record := range switches {
		persisted = append(persisted, newPersistedCodexAccountSwitch(record))
	}
	raw, err := json.MarshalIndent(persistedCodexAccountSwitchState{Switches: persisted}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Codex account switch state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create Codex account switch state directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("protect Codex account switch state directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".switch-state-*.tmp")
	if err != nil {
		return fmt.Errorf("stage Codex account switch state: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect staged Codex account switch state: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write staged Codex account switch state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staged Codex account switch state: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("commit Codex account switch state: %w", err)
	}
	return nil
}

func newPersistedCodexAccountSwitch(record domain.CodexAccountSwitch) persistedCodexAccountSwitch {
	return persistedCodexAccountSwitch{
		ID: record.ID, SourceKind: record.SourceKind, SourceAccountID: record.SourceAccountID,
		TargetAccountID: record.TargetAccountID, Phase: record.Phase, FailureCode: record.FailureCode,
		CredentialsCommittedAt: record.CredentialsCommittedAt, CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt, CompletedAt: record.CompletedAt, IdempotencyKey: record.IdempotencyKey,
	}
}

func (record persistedCodexAccountSwitch) domain() domain.CodexAccountSwitch {
	return domain.CodexAccountSwitch{
		ID: record.ID, SourceKind: record.SourceKind, SourceAccountID: record.SourceAccountID,
		TargetAccountID: record.TargetAccountID, Phase: record.Phase, FailureCode: record.FailureCode,
		CredentialsCommittedAt: record.CredentialsCommittedAt, CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt, CompletedAt: record.CompletedAt, IdempotencyKey: record.IdempotencyKey,
	}
}

func (s *codexAccountSwitchStore) CreateCodexAccountSwitch(ctx context.Context, record domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	if err := contextErr(ctx); err != nil {
		return domain.CodexAccountSwitch{}, false, err
	}
	if record.SourceKind == "" {
		record.SourceKind = domain.CodexAccountSwitchSourceManaged
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switches, err := s.load()
	if err != nil {
		return domain.CodexAccountSwitch{}, false, err
	}
	for _, existing := range switches {
		if existing.IdempotencyKey == record.IdempotencyKey {
			if existing.TargetAccountID == record.TargetAccountID {
				return existing, false, nil
			}
			return existing, false, ports.ErrCodexAccountSwitchIdempotencyConflict
		}
	}
	for _, existing := range switches {
		if !existing.Phase.Terminal() {
			return existing, false, ports.ErrCodexAccountSwitchInProgress
		}
	}
	switches = append(switches, record)
	if err := s.persist(switches); err != nil {
		return domain.CodexAccountSwitch{}, false, err
	}
	return record, true, nil
}

func (s *codexAccountSwitchStore) GetCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, bool, error) {
	return s.find(ctx, func(record domain.CodexAccountSwitch) bool { return record.ID == strings.TrimSpace(id) })
}

func (s *codexAccountSwitchStore) GetCodexAccountSwitchByIdempotency(ctx context.Context, key string) (domain.CodexAccountSwitch, bool, error) {
	return s.find(ctx, func(record domain.CodexAccountSwitch) bool { return record.IdempotencyKey == strings.TrimSpace(key) })
}

func (s *codexAccountSwitchStore) GetActiveCodexAccountSwitch(ctx context.Context) (domain.CodexAccountSwitch, bool, error) {
	return s.find(ctx, func(record domain.CodexAccountSwitch) bool { return !record.Phase.Terminal() })
}

func (s *codexAccountSwitchStore) find(ctx context.Context, matches func(domain.CodexAccountSwitch) bool) (domain.CodexAccountSwitch, bool, error) {
	if err := contextErr(ctx); err != nil {
		return domain.CodexAccountSwitch{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switches, err := s.load()
	if err != nil {
		return domain.CodexAccountSwitch{}, false, err
	}
	for _, record := range switches {
		if matches(record) {
			return record, true, nil
		}
	}
	return domain.CodexAccountSwitch{}, false, nil
}

func (s *codexAccountSwitchStore) UpdateCodexAccountSwitch(ctx context.Context, record domain.CodexAccountSwitch, expected domain.CodexAccountSwitchPhase) (bool, error) {
	if err := contextErr(ctx); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switches, err := s.load()
	if err != nil {
		return false, err
	}
	for i := range switches {
		if switches[i].ID != record.ID {
			continue
		}
		if switches[i].Phase != expected {
			return false, nil
		}
		switches[i] = record
		if err := s.persist(switches); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
