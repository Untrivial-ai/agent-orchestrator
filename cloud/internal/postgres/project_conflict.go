package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ProjectRepositoryConflictError reports that the caller's organization already
// has an active (non-archived) project registered for the same repository, so a
// second project for that repository cannot be created. It classifies as
// ErrConflict (via Is) so callers that only branch on the error class keep
// working, while carrying the repository URL so the API can render a specific,
// actionable message instead of the generic conflict string.
//
// The conflict is strictly within one organization: the uniqueness index is
// keyed on (org_id, repository_url), so a project owned by a different
// organization never triggers it. Tenant isolation is preserved and there is no
// cross-org record that wrongly blocks creation here.
type ProjectRepositoryConflictError struct {
	// RepositoryURL is the repository whose project already exists. It may be
	// empty when the creation path does not carry the URL (the GitHub App path
	// authorizes by repository id); the API then renders a repository-agnostic
	// message.
	RepositoryURL string
}

func (e *ProjectRepositoryConflictError) Error() string {
	if e.RepositoryURL != "" {
		return fmt.Sprintf(
			"a project already exists for repository %s in this organization",
			e.RepositoryURL,
		)
	}
	return "a project already exists for this repository in this organization"
}

// Is lets errors.Is(err, ErrConflict) recognise this as a conflict so existing
// conflict handling keeps working while errors.As can still recover the URL.
func (e *ProjectRepositoryConflictError) Is(target error) bool {
	return target == ErrConflict
}

// projectRepositoryConflict recognises the active-repository uniqueness
// violation raised when the organization already has a project for the same
// repository, returning a typed ProjectRepositoryConflictError that carries the
// repository so the API can render a specific message. The pre-00038 constraint
// name is matched too so the classification still holds on databases that
// predate the active-only partial index. Any other error is normalised as usual.
func projectRepositoryConflict(err error, repositoryURL string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "ao_projects_org_active_repository_url_key" ||
			pgErr.ConstraintName == "ao_projects_org_id_repository_url_key") {
		return &ProjectRepositoryConflictError{RepositoryURL: repositoryURL}
	}
	return normalizeConstraintError(err)
}
