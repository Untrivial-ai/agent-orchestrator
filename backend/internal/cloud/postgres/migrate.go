package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	// Register pgx as the database/sql driver used by Goose.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies embedded Cloud migrations with a privileged migration URL,
// then grants the existing restricted runtime role access to the foundation.
func Migrate(ctx context.Context, databaseURL, runtimeRole string) error {
	databaseURL = strings.TrimSpace(databaseURL)
	runtimeRole = strings.TrimSpace(runtimeRole)
	if databaseURL == "" || runtimeRole == "" {
		return errors.New("migration database URL and runtime role are required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping migration database: %w", err)
	}
	goose.SetBaseFS(migrationFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("apply cloud migrations: %w", err)
	}
	if err := grantRuntimeRole(ctx, databaseURL, runtimeRole); err != nil {
		return fmt.Errorf("grant cloud runtime role: %w", err)
	}
	return nil
}

func grantRuntimeRole(ctx context.Context, databaseURL, runtimeRole string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	var exists, canLogin, superuser, bypassRLS, createRole, createDB, replication bool
	if err := conn.QueryRow(
		ctx,
		`SELECT true, rolcanlogin, rolsuper, rolbypassrls,
		        rolcreaterole, rolcreatedb, rolreplication
		 FROM pg_roles WHERE rolname = $1`,
		runtimeRole,
	).Scan(&exists, &canLogin, &superuser, &bypassRLS, &createRole, &createDB, &replication); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("runtime role %q does not exist", runtimeRole)
		}
		return err
	}
	var migrationRole string
	if err := conn.QueryRow(ctx, `SELECT current_user`).Scan(&migrationRole); err != nil {
		return err
	}
	if !exists || !canLogin || superuser || bypassRLS || createRole || createDB || replication || runtimeRole == migrationRole {
		return fmt.Errorf("runtime role %q must be a separate, unprivileged login role", runtimeRole)
	}
	role := pgx.Identifier{runtimeRole}.Sanitize()
	var databaseName string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		return err
	}
	statements := []string{
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{databaseName}.Sanitize() + " TO " + role,
		"GRANT USAGE ON SCHEMA public TO " + role,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.ao_users, public.ao_auth_sessions, public.ao_organizations, public.ao_org_memberships TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ao_current_user_id(), public.ao_current_org_id(), public.ao_is_org_member(uuid, uuid), public.ao_can_manage_org(uuid, uuid) TO " + role,
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
