package accountsmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type persistedRouteState struct {
	Sessions map[string]string `json:"sessions"`
}

// routeState is the small durable part of Accounts Manager state. It stores
// only opaque account references and AO session ids, never tokens or account
// credentials.
type routeState struct {
	path string

	mu       sync.RWMutex
	sessions map[string]string
}

func newRouteState(path string) (*routeState, error) {
	state := &routeState{path: path, sessions: make(map[string]string)}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read accounts manager routes: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("protect accounts manager routes: %w", err)
	}
	var persisted persistedRouteState
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil, fmt.Errorf("decode accounts manager routes: %w", err)
	}
	for sessionID, accountID := range persisted.Sessions {
		sessionID = strings.TrimSpace(sessionID)
		accountID = strings.TrimSpace(accountID)
		if sessionID != "" && accountID != "" {
			state.sessions[sessionID] = accountID
		}
	}
	return state, nil
}

func (s *routeState) accountForSession(sessionID string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	accountID, ok := s.sessions[strings.TrimSpace(sessionID)]
	s.mu.RUnlock()
	return accountID, ok
}

func (s *routeState) setAccountForSession(sessionID, accountID string) error {
	if s == nil {
		return ports.ErrCodexProxyUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	accountID = strings.TrimSpace(accountID)
	if sessionID == "" || accountID == "" {
		return errors.New("session and account ids are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := cloneStringMap(s.sessions)
	candidate[sessionID] = accountID
	if err := s.persist(persistedRouteState{Sessions: candidate}); err != nil {
		return err
	}
	s.sessions = candidate
	return nil
}

func (s *routeState) persist(persisted persistedRouteState) error {
	raw, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("encode accounts manager routes: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create accounts manager route directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".routes-*.tmp")
	if err != nil {
		return fmt.Errorf("stage accounts manager routes: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect accounts manager routes: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write accounts manager routes: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close accounts manager routes: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("commit accounts manager routes: %w", err)
	}
	return nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return map[string]string{}
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
