package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func Migrate(ctx context.Context, databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	lockConnection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer lockConnection.Close()
	if _, err := lockConnection.ExecContext(
		ctx,
		`SELECT pg_advisory_lock(hashtextextended('ao-cloud-migrations', 0))`,
	); err != nil {
		return fmt.Errorf("lock database migrations: %w", err)
	}
	defer func() {
		_, _ = lockConnection.ExecContext(
			context.Background(),
			`SELECT pg_advisory_unlock(hashtextextended('ao-cloud-migrations', 0))`,
		)
	}()

	goose.SetBaseFS(migrationFiles)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}

func GrantRuntimeRole(ctx context.Context, databaseURL, role string) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return errors.New("runtime database role is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	var exists bool
	var databaseName string
	if err := db.QueryRowContext(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1), current_database()`,
		role,
	).Scan(&exists, &databaseName); err != nil {
		return fmt.Errorf("find runtime database role: %w", err)
	}
	if !exists {
		return fmt.Errorf("runtime database role %q does not exist", role)
	}

	quotedRole := pgx.Identifier{role}.Sanitize()
	statements := []string{
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{databaseName}.Sanitize() + " TO " + quotedRole,
		"GRANT USAGE ON SCHEMA public TO " + quotedRole,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO " + quotedRole,
		"GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO " + quotedRole,
		"GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO " + quotedRole,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO " + quotedRole,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO " + quotedRole,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO " + quotedRole,
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("grant runtime database privileges: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit runtime database privileges: %w", err)
	}
	return nil
}
