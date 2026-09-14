package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNormalizeProjectConstraintError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "live repository index reports the project conflict",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: activeProjectRepositoryIndex},
			want: ErrProjectRepositoryExists,
		},
		{
			name: "wrapped violation is still recognized",
			err:  fmt.Errorf("insert project: %w", &pgconn.PgError{Code: "23505", ConstraintName: activeProjectRepositoryIndex}),
			want: ErrProjectRepositoryExists,
		},
		{
			// Only the repository index carries an actionable message; every
			// other unique violation stays a generic conflict.
			name: "another unique violation stays generic",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "ao_commands_org_id_idempotency_key_key"},
			want: ErrConflict,
		},
		{
			name: "check violation keeps its own mapping",
			err:  &pgconn.PgError{Code: "23514", ConstraintName: "ao_projects_display_name_check"},
			want: ErrInvalid,
		},
		{
			name: "foreign key violation keeps its own mapping",
			err:  &pgconn.PgError{Code: "23503", ConstraintName: "ao_projects_org_id_fkey"},
			want: ErrNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeProjectConstraintError(tc.err); !errors.Is(got, tc.want) {
				t.Fatalf("normalizeProjectConstraintError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeProjectConstraintErrorPassesThroughUnknownErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("connection reset")
	if got := normalizeProjectConstraintError(sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("normalizeProjectConstraintError() = %v, want %v", got, sentinel)
	}
}
