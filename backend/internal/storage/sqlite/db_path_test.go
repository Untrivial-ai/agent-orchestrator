package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestOpenPreservesDataDirectoryCharacters(t *testing.T) {
	for _, tc := range []struct{ name, directory string }{
		{"fragment", "state#one"}, {"query", "state?one"}, {"percent", "state%23one"}, {"unicode", "state space 日本語"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.name == "query" {
				t.Skip("question mark is not a Windows filename character")
			}
			dir := filepath.Join(t.TempDir(), tc.directory)
			st, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.UpsertProject(context.Background(), domain.ProjectRecord{ID: "marker", Path: filepath.Join(dir, "repo")}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "ao.db")); err != nil {
				t.Fatalf("database not created in requested directory: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			ro, err := OpenReadOnly(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			row, ok, err := ro.GetProject(context.Background(), "marker")
			if err != nil || !ok || row.Path != filepath.Join(dir, "repo") {
				t.Fatalf("read-only opened wrong database: row=%+v ok=%v err=%v", row, ok, err)
			}
			if err := ro.UpsertProject(context.Background(), domain.ProjectRecord{ID: "forbidden", Path: "/forbidden"}); err == nil {
				t.Fatal("read-only URI allowed a write")
			}
		})
	}
}

func TestFragmentPathsKeepDatabasesIsolated(t *testing.T) {
	base := t.TempDir()
	first, err := Open(filepath.Join(base, "data#first"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := first.UpsertProject(context.Background(), domain.ProjectRecord{ID: "private-to-first", Path: "/first"}); err != nil {
		t.Fatal(err)
	}
	second, err := Open(filepath.Join(base, "data#second"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, ok, err := second.GetProject(context.Background(), "private-to-first"); err != nil || ok {
		t.Fatalf("distinct data paths shared records: found=%v err=%v", ok, err)
	}
}

func TestOpenRefusesToHideLegacyDatabase(t *testing.T) {
	for _, tc := range []struct{ name, directory string }{
		{"fragment", "state#old"}, {"query", "state?old"}, {"percent", "state%23old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.name == "query" {
				t.Skip("question mark is not a Windows filename character")
			}
			dir := filepath.Join(t.TempDir(), tc.directory)
			createLegacyDatabase(t, dir)
			st, err := Open(dir)
			if st != nil {
				_ = st.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "recover the legacy database explicitly") {
				t.Fatalf("expected actionable legacy-data error, got %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "ao.db")); !os.IsNotExist(err) {
				t.Fatalf("must not create an empty replacement database: %v", err)
			}
			db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "ao.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var marker string
			if err := db.QueryRow("SELECT value FROM legacy_marker").Scan(&marker); err != nil || marker != "preserved" {
				t.Fatalf("legacy data changed: marker=%q err=%v", marker, err)
			}
		})
	}
}

func TestOpenPrefersExistingIntendedDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state#old")
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProject(context.Background(), domain.ProjectRecord{ID: "intended", Path: "/intended"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	createLegacyDatabase(t, dir)
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, ok, err := st.GetProject(context.Background(), "intended"); err != nil || !ok {
		t.Fatalf("existing intended database was not retained: found=%v err=%v", ok, err)
	}
}

func createLegacyDatabase(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	// The old URI percent-decoded this directory before opening ao.db.
	if strings.Contains(dir, "%23") {
		if err := os.MkdirAll(strings.ReplaceAll(dir, "%23", "#"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "ao.db")+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE legacy_marker(value TEXT); INSERT INTO legacy_marker VALUES ('preserved')"); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseURIPreservesWindowsPaths(t *testing.T) {
	for _, dir := range []string{`C:\Users\AO\state#one`, `\\server\share\state%23one`} {
		uri := databaseURI(dir)
		decoded, err := url.PathUnescape(strings.TrimPrefix(uri, "file:"))
		if err != nil || decoded != filepath.Join(dir, "ao.db") {
			t.Fatalf("path did not round trip: %q => %q (%v)", dir, decoded, err)
		}
		if strings.HasPrefix(uri, "file://") {
			t.Fatalf("filesystem path became a URI authority: %q", uri)
		}
	}
}
