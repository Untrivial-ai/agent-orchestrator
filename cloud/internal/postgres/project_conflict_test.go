package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func uniqueViolation(constraint string) error {
	return &pgconn.PgError{Code: "23505", ConstraintName: constraint}
}

func TestProjectRepositoryConflictClassifiesActiveRepoIndex(t *testing.T) {
	url := "https://github.com/octo/widgets"
	err := projectRepositoryConflict(uniqueViolation("ao_projects_org_active_repository_url_key"), url)

	var conflict *ProjectRepositoryConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *ProjectRepositoryConflictError", err)
	}
	if conflict.RepositoryURL != url {
		t.Fatalf("RepositoryURL = %q, want %q", conflict.RepositoryURL, url)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatal("repository conflict should still classify as ErrConflict")
	}
}

func TestProjectRepositoryConflictClassifiesLegacyConstraintName(t *testing.T) {
	// Databases predating migration 00038 name the constraint differently; the
	// classification must still hold so those deployments get the clear message.
	err := projectRepositoryConflict(uniqueViolation("ao_projects_org_id_repository_url_key"), "https://github.com/octo/widgets")

	var conflict *ProjectRepositoryConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *ProjectRepositoryConflictError", err)
	}
}

func TestProjectRepositoryConflictLeavesOtherUniqueViolationsGeneric(t *testing.T) {
	// A unique violation on some other constraint must not masquerade as a
	// repository conflict, but must remain a generic ErrConflict.
	err := projectRepositoryConflict(uniqueViolation("ao_some_other_key"), "https://github.com/octo/widgets")

	var conflict *ProjectRepositoryConflictError
	if errors.As(err, &conflict) {
		t.Fatalf("error = %v, want a generic conflict, not *ProjectRepositoryConflictError", err)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func TestProjectRepositoryConflictPassesThroughNonPgErrors(t *testing.T) {
	sentinel := errors.New("boom")
	if err := projectRepositoryConflict(sentinel, "https://github.com/octo/widgets"); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the original error passed through", err)
	}
}
