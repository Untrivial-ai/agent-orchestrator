// Package postgres persists hosted control-plane identity and tenant data.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound means an identity or refresh session does not exist.
	ErrNotFound = errors.New("cloud record not found")
	// ErrInvalid means persisted input violates the foundation schema.
	ErrInvalid = errors.New("invalid cloud record")
	// ErrConflict means a persisted value violates a uniqueness constraint.
	ErrConflict = errors.New("cloud record conflicts with an existing record")
)

// Store owns the restricted runtime connection pool used by the control plane.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL and refuses roles that can bypass row-level
// security. Schema migrations use a separate privileged connection.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, strings.TrimSpace(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("open cloud database: %w", err)
	}
	store := &Store{pool: pool}
	if err := store.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := store.validateRuntimeRole(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the runtime database pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping verifies that the runtime database connection is available.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping cloud database: %w", err)
	}
	return nil
}

func (s *Store) validateRuntimeRole(ctx context.Context) error {
	var role string
	var superuser, bypassRLS, createRole, createDB, replication, ownsFoundation bool
	err := s.pool.QueryRow(
		ctx,
		`SELECT role.rolname,
		        role.rolsuper,
		        role.rolbypassrls,
		        role.rolcreaterole,
		        role.rolcreatedb,
		        role.rolreplication,
		        EXISTS (
		            SELECT 1
		            FROM pg_class relation
		            JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
		            WHERE namespace.nspname = 'public'
		              AND relation.relname IN (
		                  'ao_users', 'ao_auth_sessions',
			              'ao_organizations', 'ao_org_memberships',
			              'ao_cloud_workspaces', 'ao_cloud_session_runtimes'
		              )
		              AND relation.relowner = role.oid
		        )
		 FROM pg_roles role
		 WHERE role.rolname = current_user`,
	).Scan(&role, &superuser, &bypassRLS, &createRole, &createDB, &replication, &ownsFoundation)
	if err != nil {
		return fmt.Errorf("inspect cloud database role: %w", err)
	}
	if superuser || bypassRLS || createRole || createDB || replication || ownsFoundation {
		return fmt.Errorf("cloud runtime database role %q is privileged or owns foundation tables", role)
	}
	return nil
}

func normalizeError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
		case "22P02", "23502", "23514":
			return fmt.Errorf("%w: %s", ErrInvalid, pgErr.ConstraintName)
		}
	}
	return err
}
