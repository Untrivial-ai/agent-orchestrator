package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNormalizeErrorMapsUniqueViolationsToConflict(t *testing.T) {
	err := normalizeError(&pgconn.PgError{
		Code:           "23505",
		ConstraintName: "ao_organizations_slug_key",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("normalizeError() = %v, want ErrConflict", err)
	}
}
