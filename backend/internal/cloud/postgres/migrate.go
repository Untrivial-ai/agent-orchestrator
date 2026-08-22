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

// EnsureRuntimeRole creates the restricted login role used by ao-cloud when a
// deployment initializes a new PostgreSQL instance. Existing roles are never
// altered; they are only validated so a deployment cannot silently elevate or
// rotate a live runtime credential.
func EnsureRuntimeRole(ctx context.Context, databaseURL, runtimeRole, runtimePassword string) error {
	databaseURL = strings.TrimSpace(databaseURL)
	runtimeRole = strings.TrimSpace(runtimeRole)
	if databaseURL == "" || runtimeRole == "" || runtimePassword == "" {
		return errors.New("migration database URL, runtime role, and runtime password are required")
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect for runtime role bootstrap: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var migrationRole string
	if err := conn.QueryRow(ctx, `SELECT current_user`).Scan(&migrationRole); err != nil {
		return err
	}
	var canLogin, superuser, bypassRLS, createRole, createDB, replication bool
	err = conn.QueryRow(
		ctx,
		`SELECT rolcanlogin, rolsuper, rolbypassrls,
		        rolcreaterole, rolcreatedb, rolreplication
		 FROM pg_roles WHERE rolname = $1`,
		runtimeRole,
	).Scan(&canLogin, &superuser, &bypassRLS, &createRole, &createDB, &replication)
	if err == nil {
		if !canLogin || superuser || bypassRLS || createRole || createDB || replication || runtimeRole == migrationRole {
			return fmt.Errorf("runtime role %q must be a separate, unprivileged login role", runtimeRole)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	var quotedPassword string
	if err := conn.QueryRow(ctx, `SELECT quote_literal($1)`, runtimePassword).Scan(&quotedPassword); err != nil {
		return fmt.Errorf("quote runtime role password: %w", err)
	}
	statement := "CREATE ROLE " + pgx.Identifier{runtimeRole}.Sanitize() + " LOGIN PASSWORD " + quotedPassword
	if _, err := conn.Exec(ctx, statement); err != nil {
		return fmt.Errorf("create runtime role %q: %w", runtimeRole, err)
	}
	return nil
}

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
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.ao_users, public.ao_auth_sessions, public.ao_organizations, public.ao_org_memberships, public.ao_cloud_workspaces, public.ao_cloud_session_runtimes TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ao_current_user_id(), public.ao_current_org_id(), public.ao_is_org_member(uuid, uuid), public.ao_can_manage_org(uuid, uuid) TO " + role,
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
