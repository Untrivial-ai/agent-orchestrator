// Package sqlitetest provides isolated, fully migrated SQLite stores for tests.
package sqlitetest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

var databaseTemplate struct {
	sync.Once
	data []byte
	err  error
}

// Open returns a store cloned from a database migrated once for the current
// test binary. dataDir may contain unrelated fixture files, but it must not
// already contain ao.db. Tests that exercise migration behavior should continue
// to call sqlite.Open directly with an empty directory.
func Open(dataDir string) (*sqlite.Store, error) {
	template, err := templateDatabase()
	if err != nil {
		return nil, fmt.Errorf("build migrated template: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "ao.db")
	if err := writeExclusive(dbPath, template); err != nil {
		return nil, fmt.Errorf("clone migrated template: %w", err)
	}

	// Fast path: skip all Goose migration/repair/reconciliation — the cloned
	// database was fully migrated by buildTemplateDatabase and its schema
	// version is verified by OpenPreMigrated before opening the pools.
	store, err := sqlite.OpenPreMigrated(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open cloned store: %w", err)
	}
	return store, nil
}

// MustOpen returns an isolated store and registers its cleanup with t.
func MustOpen(t testing.TB) *sqlite.Store {
	t.Helper()
	return MustOpenAt(t, t.TempDir())
}

// MustOpenAt is MustOpen for callers that also use dataDir for other fixtures.
func MustOpenAt(t testing.TB, dataDir string) *sqlite.Store {
	t.Helper()

	store, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open cloned SQLite test store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close cloned SQLite test store: %v", err)
		}
	})
	return store
}

func templateDatabase() ([]byte, error) {
	databaseTemplate.Do(func() {
		databaseTemplate.data, databaseTemplate.err = buildTemplateDatabase()
	})
	return databaseTemplate.data, databaseTemplate.err
}

func buildTemplateDatabase() ([]byte, error) {
	dataDir, err := os.MkdirTemp("", "ao-sqlite-test-template-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dataDir) }()

	store, err := sqlite.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("migrate template: %w", err)
	}
	if err := store.Close(); err != nil {
		return nil, fmt.Errorf("close template: %w", err)
	}

	data, err := os.ReadFile(filepath.Join(dataDir, "ao.db"))
	if err != nil {
		return nil, fmt.Errorf("read template: %w", err)
	}
	return data, nil
}

func writeExclusive(path string, data []byte) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	return err
}
