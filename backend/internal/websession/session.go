// Package websession manages web UI session authentication for browser clients.
package websession

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Session represents a logged-in web user. The ID is the cookie value; the hash
// is what we store persistently.
type Session struct {
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	AbsoluteExpiry time.Time `json:"absolute_expiry"`
}

// Store manages session persistence: creation, lookup, and revocation.
// All operations are atomic and safe for concurrent use.
type Store struct {
	mu        sync.RWMutex
	baseDir   string
	sessPath  string
	sessions  map[string]*Session
	slideTime time.Duration
	absTime   time.Duration
}

// NewStore creates a session store backed by a JSON file at baseDir/sessions.json.
// slideTime is the sliding-window duration (refreshed on each request).
// absTime is the absolute maximum session lifetime.
func NewStore(baseDir string, slideTime, absTime time.Duration) (*Store, error) {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	s := &Store{
		baseDir:   baseDir,
		sessPath:  filepath.Join(baseDir, "sessions.json"),
		sessions:  make(map[string]*Session),
		slideTime: slideTime,
		absTime:   absTime,
	}

	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load sessions: %w", err)
	}

	return s, nil
}

// load reads the persistent session file. Called on init and after each write.
func (s *Store) load() error {
	data, err := os.ReadFile(s.sessPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var sessions map[string]*Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		return fmt.Errorf("parse sessions.json: %w", err)
	}

	now := time.Now()
	valid := make(map[string]*Session)
	for id, sess := range sessions {
		if now.Before(sess.ExpiresAt) && now.Before(sess.AbsoluteExpiry) {
			valid[id] = sess
		}
	}

	s.mu.Lock()
	s.sessions = valid
	s.mu.Unlock()

	return nil
}

// persist atomically writes the session map to disk. It takes its own read
// lock, so every caller must have released s.mu first (never call this while
// holding s.mu.Lock() — sync.RWMutex is not reentrant and this would deadlock).
// The read lock is required, not optional: without it, marshaling s.sessions
// here races with a concurrent Validate() sliding a *Session's ExpiresAt.
func (s *Store) persist() error {
	s.mu.RLock()
	data, err := json.Marshal(s.sessions)
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal sessions: %w", err)
	}

	tmpPath := s.sessPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write sessions temp: %w", err)
	}

	if err := os.Rename(tmpPath, s.sessPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename sessions: %w", err)
	}

	return nil
}

// Create generates a new session with the given expiry times and returns the
// session ID (used as the cookie value).
func (s *Store) Create() (string, error) {
	id, err := generateSessionID()
	if err != nil {
		return "", err
	}

	now := time.Now()
	sess := &Session{
		ID:             id,
		CreatedAt:      now,
		ExpiresAt:      now.Add(s.slideTime),
		AbsoluteExpiry: now.Add(s.absTime),
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	if err := s.persist(); err != nil {
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
		return "", err
	}

	return id, nil
}

// Validate checks whether a session ID is valid (exists and not expired).
// If valid, it updates the sliding window and returns true. The lock is
// released before persist() runs (see persist's contract): persist takes its
// own read lock and must never be called while s.mu is held.
func (s *Store) Validate(id string) bool {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return false
	}

	now := time.Now()
	if now.After(sess.ExpiresAt) || now.After(sess.AbsoluteExpiry) {
		delete(s.sessions, id)
		s.mu.Unlock()
		// Best-effort: an expired session dropped from disk on the next
		// successful write is not security-relevant (Validate already
		// rejected it), so a failure here is not worth surfacing.
		_ = s.persist()
		return false
	}

	// Slide the expiry window forward.
	sess.ExpiresAt = now.Add(s.slideTime)
	s.mu.Unlock()

	_ = s.persist()
	return true
}

// Revoke deletes a single session.
func (s *Store) Revoke(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()

	_ = s.persist()
}

// RevokeAll deletes all sessions.
func (s *Store) RevokeAll() {
	s.mu.Lock()
	s.sessions = make(map[string]*Session)
	s.mu.Unlock()

	_ = s.persist()
}

// generateSessionID returns a 32-byte random opaque ID as a hex string.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
