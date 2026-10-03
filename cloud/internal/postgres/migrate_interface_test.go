package postgres

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
)

func TestMigrateInterfaceHandoffFromMainVersion(t *testing.T) {
	databaseURL := os.Getenv("AO_CLOUD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AO_CLOUD_TEST_DATABASE_URL is not set; skipping PostgreSQL migration upgrade test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := "ao_upgrade_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop upgrade test schema: %v", err)
		}
	})
	parsedURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedURL.Query()
	query.Set("options", "-csearch_path="+schema)
	parsedURL.RawQuery = query.Encode()
	upgradeURL := parsedURL.String()
	db, err := sql.Open("pgx", upgradeURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	goose.SetBaseFS(migrationFiles)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 50); err != nil {
		t.Fatalf("apply main migrations through 00050: %v", err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version_id FROM goose_db_version
		WHERE is_applied ORDER BY id DESC LIMIT 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 50 {
		t.Fatalf("pre-upgrade migration version = %d, want 50", version)
	}
	if err := Migrate(ctx, upgradeURL); err != nil {
		t.Fatalf("upgrade from main: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version_id FROM goose_db_version
		WHERE is_applied ORDER BY id DESC LIMIT 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 55 {
		t.Fatalf("post-upgrade migration version = %d, want 55", version)
	}
	var transitionTableExists bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass('ao_interface_transitions') IS NOT NULL`,
	).Scan(&transitionTableExists); err != nil {
		t.Fatal(err)
	}
	if !transitionTableExists {
		t.Fatal("interface transition table missing after upgrade")
	}
	var constraint string
	if err := db.QueryRowContext(ctx, `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'ao_worker_requests'::regclass
			AND conname = 'ao_worker_requests_kind_check'`).Scan(&constraint); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"interface.start", "chat.models", "chat.steer", "harness.inspect", "harness.install"} {
		if !strings.Contains(constraint, kind) {
			t.Errorf("worker request constraint missing %q", kind)
		}
	}
}
