package websession_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/websession"
)

func TestStoreCreateAndValidate(t *testing.T) {
	dir := t.TempDir()
	store, err := websession.NewStore(dir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Create a session
	id, err := store.Create()
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Session should be valid immediately
	if !store.Validate(id) {
		t.Fatal("newly created session should be valid")
	}

	// Invalid ID should not validate
	if store.Validate("invalid") {
		t.Fatal("invalid session ID should not validate")
	}
}

func TestStoreExpiry(t *testing.T) {
	dir := t.TempDir()
	store, err := websession.NewStore(dir, 1*time.Second, 2*time.Second)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	id, err := store.Create()
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Wait for sliding window to expire
	time.Sleep(1100 * time.Millisecond)
	if store.Validate(id) {
		t.Fatal("session should have expired after sliding window")
	}
}

func TestStoreRevoke(t *testing.T) {
	dir := t.TempDir()
	store, err := websession.NewStore(dir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	id, err := store.Create()
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Revoke the session
	store.Revoke(id)

	// Should no longer be valid
	if store.Validate(id) {
		t.Fatal("revoked session should not be valid")
	}
}

func TestStoreRevokeAll(t *testing.T) {
	dir := t.TempDir()
	store, err := websession.NewStore(dir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Create multiple sessions
	ids := make([]string, 3)
	for i := range ids {
		id, err := store.Create()
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		ids[i] = id
	}

	// All should be valid before revoke
	for _, id := range ids {
		if !store.Validate(id) {
			t.Fatal("session should be valid before RevokeAll")
		}
	}

	// Revoke all
	store.RevokeAll()

	// None should be valid after revoke
	for _, id := range ids {
		if store.Validate(id) {
			t.Fatal("session should not be valid after RevokeAll")
		}
	}
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()

	// Create store and session
	store1, err := websession.NewStore(dir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	id, err := store1.Create()
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Create new store instance pointing at same directory
	store2, err := websession.NewStore(dir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Session should be found in new store
	if !store2.Validate(id) {
		t.Fatal("session should persist across store instances")
	}
}

func TestStoreFilePermissions(t *testing.T) {
	dir := t.TempDir()
	store, err := websession.NewStore(dir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = store.Create()
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Check file permissions
	sessPath := filepath.Join(dir, "sessions.json")
	stat, err := os.Stat(sessPath)
	if err != nil {
		t.Fatalf("stat sessions.json failed: %v", err)
	}

	mode := stat.Mode().Perm()
	if mode != 0o600 {
		t.Fatalf("sessions.json should be 0o600, got %o", mode)
	}
}

func TestStoreAbsoluteExpiry(t *testing.T) {
	dir := t.TempDir()
	// Sliding window is 30 seconds, absolute is 100ms
	store, err := websession.NewStore(dir, 30*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	id, err := store.Create()
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Session is valid immediately
	if !store.Validate(id) {
		t.Fatal("session should be valid immediately")
	}

	// Wait for absolute expiry
	time.Sleep(150 * time.Millisecond)
	if store.Validate(id) {
		t.Fatal("session should have expired after absolute time")
	}
}

func TestStoreSlidingWindow(t *testing.T) {
	dir := t.TempDir()
	slideTime := 100 * time.Millisecond
	absTime := 500 * time.Millisecond
	store, err := websession.NewStore(dir, slideTime, absTime)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	id, err := store.Create()
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Validate at t=0ms
	if !store.Validate(id) {
		t.Fatal("session should be valid at t=0ms")
	}

	// Wait 80ms and validate again (sliding window should reset)
	time.Sleep(80 * time.Millisecond)
	if !store.Validate(id) {
		t.Fatal("session should be valid after sliding validation")
	}

	// Sleep another 80ms (total 160ms from last validate, 240ms from create)
	// Sliding window should not have expired
	time.Sleep(80 * time.Millisecond)
	if !store.Validate(id) {
		t.Fatal("session should still be valid with sliding window")
	}

	// Wait for absolute expiry (500ms from create)
	time.Sleep(400 * time.Millisecond)
	if store.Validate(id) {
		t.Fatalf("session should expire after absolute time")
	}
}

func TestStoreCreateGeneratesUniqueIDs(t *testing.T) {
	dir := t.TempDir()
	store, err := websession.NewStore(dir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := store.Create()
		if err != nil {
			t.Fatalf("Create failed at iteration %d: %v", i, err)
		}
		if ids[id] {
			t.Fatalf("duplicate session ID generated: %s", id)
		}
		ids[id] = true
	}
}

func TestStoreDirectoryCreation(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "a", "b", "c")

	// Directory doesn't exist yet
	if _, err := os.Stat(subdir); !os.IsNotExist(err) {
		t.Fatalf("directory should not exist: %v", err)
	}

	store, err := websession.NewStore(subdir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = store.Create()
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Directory should exist now
	if _, err := os.Stat(subdir); err != nil {
		t.Fatalf("directory should exist after Create: %v", err)
	}
}

func TestStoreParseErrors(t *testing.T) {
	dir := t.TempDir()
	sessPath := filepath.Join(dir, "sessions.json")

	// Write invalid JSON
	if err := os.WriteFile(sessPath, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// NewStore should handle parse error gracefully
	_, err := websession.NewStore(dir, 24*time.Hour, 90*24*time.Hour)
	if err == nil {
		t.Fatal("NewStore should error on invalid JSON")
	}
	t.Logf("parse error: %v", err)
}
