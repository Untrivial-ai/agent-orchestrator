package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

// gitHubOrgFixture is one seeded organization plus an active install attempt
// primed at the oauth phase, ready to drive CompleteGitHubInstallation.
type gitHubOrgFixture struct {
	orgID     string
	userID    string
	stateHash []byte
}

func randomStateHash(t *testing.T) []byte {
	t.Helper()
	hash := make([]byte, 32)
	if _, err := rand.Read(hash); err != nil {
		t.Fatalf("random state hash: %v", err)
	}
	return hash
}

// randomGitHubInstallationID returns a fresh positive installation id so each
// run is isolated from any residue left by earlier runs on the shared schema
// (the routes table is not row-level-security scoped).
func randomGitHubInstallationID(t *testing.T) int64 {
	t.Helper()
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("random installation id: %v", err)
	}
	value := int64(binary.BigEndian.Uint64(buffer) & 0x7fffffffffffffff)
	if value == 0 {
		value = 1
	}
	return value
}

func seedGitHubOrg(t *testing.T, admin *pgxpool.Pool, githubInstallationID int64) gitHubOrgFixture {
	t.Helper()
	ctx := context.Background()
	fixture := gitHubOrgFixture{
		orgID:     uuid.NewString(),
		userID:    uuid.NewString(),
		stateHash: randomStateHash(t),
	}
	attemptID := uuid.NewString()

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		fixture.userID, fixture.orgID,
	); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO ao_users (id, auth_provider, external_user_id, email, display_name, password_hash)
			VALUES ($1::uuid, 'local', $1::uuid::text, $1::uuid::text || '@example.test', 'GitHub Test', 'hash')`,
			[]any{fixture.userID}},
		{`INSERT INTO ao_organizations (id, auth_provider, slug, display_name, kind, owner_user_id, created_by_user_id)
			VALUES ($1::uuid, 'local', $2, $1::uuid::text || '@example.test', 'personal', $3::uuid, $3::uuid)`,
			[]any{fixture.orgID, "gh-" + uuid.NewString(), fixture.userID}},
		{`INSERT INTO ao_org_memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
			[]any{fixture.orgID, fixture.userID}},
		{`INSERT INTO ao_github_install_attempts (
			id, org_id, initiating_user_id, state_hash, phase,
			pending_github_installation_id, expires_at
		) VALUES ($1, $2, $3, $4, 'oauth', $5, now() + interval '1 hour')`,
			[]any{attemptID, fixture.orgID, fixture.userID, randomStateHash(t), githubInstallationID}},
	}
	for index, step := range steps {
		if _, err := tx.Exec(ctx, step.query, step.args...); err != nil {
			t.Fatalf("seed github org step %d: %v", index, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit github org fixture: %v", err)
	}

	// ao_github_callback_routes is not row-level-security scoped.
	if _, err := admin.Exec(ctx,
		`INSERT INTO ao_github_callback_routes (state_hash, attempt_id, org_id, user_id, phase, expires_at)
		 VALUES ($1, $2, $3, $4, 'oauth', now() + interval '1 hour')`,
		fixture.stateHash, attemptID, fixture.orgID, fixture.userID,
	); err != nil {
		t.Fatalf("seed callback route: %v", err)
	}

	t.Cleanup(func() {
		// Best-effort cleanup, mirroring the other integration fixtures. The
		// organization row is row-level-security scoped, so the delete carries the
		// org context; it cascades memberships, installations, attempts, routes
		// and grants. Errors are ignored — every test uses a fresh random
		// installation id, so any residue is harmless.
		cleanupCtx := context.Background()
		if tx, err := admin.Begin(cleanupCtx); err == nil {
			_, _ = tx.Exec(cleanupCtx, `SELECT set_config('ao.org_id', $1, true)`, fixture.orgID)
			_, _ = tx.Exec(cleanupCtx, `DELETE FROM ao_organizations WHERE id = $1`, fixture.orgID)
			_ = tx.Commit(cleanupCtx)
		}
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM ao_users WHERE id = $1`, fixture.userID)
	})
	return fixture
}

func testGitHubInstallation(githubInstallationID int64) domain.GitHubInstallation {
	return domain.GitHubInstallation{
		GitHubInstallationID: githubInstallationID,
		GitHubAccountID:      7,
		AccountLogin:         "octo-org",
		AccountType:          "Organization",
		Status:               "active",
		RepositorySelection:  "all",
		Permissions:          json.RawMessage(`{}`),
		Events:               []string{},
	}
}

func openGitHubMultiOrgStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("AO_CLOUD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AO_CLOUD_TEST_DATABASE_URL is not set; skipping PostgreSQL multi-org integration test")
	}
	ctx := context.Background()
	if err := Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	t.Cleanup(admin.Close)
	return store, admin
}

// Two organizations, each with a verified GitHub admin, connect the SAME GitHub
// App installation. Both must succeed with their own isolated installation
// record, and each must see only its own — the core of the multi-org fix.
func TestCompleteGitHubInstallationAllowsMultipleOrgs(t *testing.T) {
	store, admin := openGitHubMultiOrgStore(t)
	ctx := context.Background()
	githubInstallationID := randomGitHubInstallationID(t)

	orgA := seedGitHubOrg(t, admin, githubInstallationID)
	orgB := seedGitHubOrg(t, admin, githubInstallationID)

	installationA, err := store.CompleteGitHubInstallation(ctx, orgA.stateHash, testGitHubInstallation(githubInstallationID))
	if err != nil {
		t.Fatalf("org A connect: %v", err)
	}
	installationB, err := store.CompleteGitHubInstallation(ctx, orgB.stateHash, testGitHubInstallation(githubInstallationID))
	if err != nil {
		t.Fatalf("org B connect (second org must not hit a wall): %v", err)
	}

	if installationA.ID == installationB.ID {
		t.Fatal("both orgs received the same installation record; they must be isolated")
	}
	if installationA.OrgID != orgA.orgID || installationB.OrgID != orgB.orgID {
		t.Fatalf("installation org mismatch: A=%s B=%s", installationA.OrgID, installationB.OrgID)
	}

	principalA := domain.Principal{UserID: orgA.userID, Provider: "local"}
	principalB := domain.Principal{UserID: orgB.userID, Provider: "local"}
	listA, err := store.ListGitHubInstallations(ctx, principalA, orgA.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].ID != installationA.ID {
		t.Fatalf("org A sees %d installations, want only its own", len(listA))
	}
	listB, err := store.ListGitHubInstallations(ctx, principalB, orgB.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 || listB[0].ID != installationB.ID {
		t.Fatalf("org B sees %d installations, want only its own", len(listB))
	}

	routes, err := store.GitHubInstallationRoutes(ctx, githubInstallationID)
	if err != nil {
		t.Fatalf("installation routes: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %+v, want one per connected org", routes)
	}
	byOrg := map[string]string{}
	for _, route := range routes {
		byOrg[route.OrgID] = route.InstallationID
	}
	if byOrg[orgA.orgID] != installationA.ID || byOrg[orgB.orgID] != installationB.ID {
		t.Fatalf("routes did not map each org to its own installation: %+v", byOrg)
	}
}

func TestApplyGitHubInstallationDeletedEventRecordsReconciliation(t *testing.T) {
	store, admin := openGitHubMultiOrgStore(t)
	ctx := context.Background()
	githubInstallationID := randomGitHubInstallationID(t)
	fixture := seedGitHubOrg(t, admin, githubInstallationID)
	installation, err := store.CompleteGitHubInstallation(ctx, fixture.stateHash, testGitHubInstallation(githubInstallationID))
	if err != nil {
		t.Fatalf("connect installation: %v", err)
	}
	if err := store.ApplyGitHubInstallationEvent(ctx, fixture.orgID, installation.ID, "deleted", "reconcile"); err != nil {
		t.Fatalf("reconcile deleted installation: %v", err)
	}
	installations, err := store.ListGitHubInstallations(ctx, domain.Principal{UserID: fixture.userID, Provider: "local"}, fixture.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 1 || installations[0].Status != "deleted" {
		t.Fatalf("installations = %+v, want one deleted installation", installations)
	}
}

func TestRepositorySyncRebindsExistingProjectAfterReconnect(t *testing.T) {
	store, admin := openGitHubMultiOrgStore(t)
	ctx := context.Background()
	installationID := randomGitHubInstallationID(t)
	fixture := seedGitHubOrg(t, admin, installationID)
	installation, err := store.CompleteGitHubInstallation(ctx, fixture.stateHash, testGitHubInstallation(installationID))
	if err != nil {
		t.Fatal(err)
	}
	repository := domain.GitHubRepository{
		GitHubRepositoryID: randomGitHubInstallationID(t), GitHubOwnerID: 7,
		Name: "repo", FullName: "octo-org/repo", HTMLURL: "https://github.com/octo-org/repo",
		CloneURL: "https://github.com/octo-org/repo.git", DefaultBranch: "ao/session", Visibility: "private", IsPrivate: true,
	}
	syncRepository := func() {
		t.Helper()
		generation, err := store.BeginGitHubRepositorySync(ctx, installation)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ReconcileGitHubRepositories(ctx, fixture.orgID, installation, generation, []domain.GitHubRepository{repository}); err != nil {
			t.Fatal(err)
		}
	}
	syncRepository()
	var projectID, oldGrantID, newGrantID string
	if err := store.withOrg(ctx, fixture.orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO ao_projects (org_id, display_name, repository_url, github_repository_id, github_repository_grant_id)
			SELECT $1, 'repo', $2, $3, id FROM ao_github_repository_grants
			WHERE org_id = $1 AND github_repository_id = $3 AND revoked_at IS NULL
			RETURNING id, github_repository_grant_id`, fixture.orgID, repository.HTMLURL, repository.GitHubRepositoryID).Scan(&projectID, &oldGrantID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyGitHubInstallationEvent(ctx, fixture.orgID, installation.ID, "deleted", "reconcile"); err != nil {
		t.Fatal(err)
	}
	if err := store.withOrg(ctx, fixture.orgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ao_github_installations SET status = 'active', deleted_at = NULL WHERE org_id = $1 AND id = $2`, fixture.orgID, installation.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A user can reopen an old session before the repository sync runs. The
	// project still holds its revoked grant, but the fresh active grant must
	// already authorize checkout and the same broker path used for push/pull.
	sessionID := uuid.NewString()
	if err := store.withOrg(ctx, fixture.orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ao_github_repository_grants
			(org_id, installation_id, github_repository_id, repository_selection)
			VALUES ($1, $2, $3, 'all')`, fixture.orgID, installation.ID, repository.GitHubRepositoryID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO ao_sessions
			(id, org_id, project_id, kind, harness, display_name, branch, created_by_user_id)
			VALUES ($1, $2, $3, 'worker', 'codex', 'old session', 'main', $4)`,
			sessionID, fixture.orgID, projectID, fixture.userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checkout, err := store.WorkerGitHubCheckoutContext(ctx, fixture.orgID, sessionID)
	if err != nil {
		t.Fatalf("old session checkout before repository sync: %v", err)
	}
	if checkout.GitHubInstallationID != installationID || checkout.GitHubRepositoryID != repository.GitHubRepositoryID {
		t.Fatalf("old session checkout selected wrong grant: %+v", checkout)
	}
	if checkout.DefaultBranch != "main" {
		t.Fatalf("checkout base = %q, want project default main", checkout.DefaultBranch)
	}
	var recoveredGrantID string
	if err := store.withOrg(ctx, fixture.orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT github_repository_grant_id FROM ao_projects WHERE org_id = $1 AND id = $2`,
			fixture.orgID, projectID).Scan(&recoveredGrantID)
	}); err != nil {
		t.Fatal(err)
	}
	if recoveredGrantID == oldGrantID {
		t.Fatal("worker checkout left the project bound to the revoked grant")
	}
	syncRepository()
	if err := store.withOrg(ctx, fixture.orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT github_repository_grant_id FROM ao_projects WHERE org_id = $1 AND id = $2`, fixture.orgID, projectID).Scan(&newGrantID)
	}); err != nil {
		t.Fatal(err)
	}
	if newGrantID == oldGrantID {
		t.Fatalf("project still points to revoked grant %s", oldGrantID)
	}
}

// Under the legacy global-unique constraint (schema at 00054, before the
// multi-org relaxation), a second org connecting an installation another org
// already holds must fail with a specific, owner-naming error so the callback
// can render a clear page instead of a generic failure.
func TestCompleteGitHubInstallationNamesOwnerUnderLegacyUnique(t *testing.T) {
	databaseURL := os.Getenv("AO_CLOUD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AO_CLOUD_TEST_DATABASE_URL is not set; skipping legacy-unique integration test")
	}
	ctx := context.Background()
	githubInstallationID := randomGitHubInstallationID(t)

	schema := "ao_legacy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// Registered before the drop-schema cleanup so it runs last (LIFO): the
	// connection must stay open long enough to drop the schema.
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop legacy schema: %v", err)
		}
	})

	schemaURL := withSearchPath(t, databaseURL, schema)
	migrateDB, err := sql.Open("pgx", schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer migrateDB.Close()
	goose.SetBaseFS(migrationFiles)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	// Stop at 00054: the pre-multi-org schema still has UNIQUE(github_installation_id).
	if err := goose.UpToContext(ctx, migrateDB, "migrations", 54); err != nil {
		t.Fatalf("apply migrations through 00054: %v", err)
	}

	pool, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &Store{pool: pool}

	orgA := seedGitHubOrg(t, pool, githubInstallationID)
	orgB := seedGitHubOrg(t, pool, githubInstallationID)

	// Org A already holds the installation and its route (seeded directly in one
	// transaction so the RLS org context applies to the installation insert).
	seedInstallationRow(t, pool, orgA.orgID, orgA.userID, githubInstallationID)

	_, err = store.CompleteGitHubInstallation(ctx, orgB.stateHash, testGitHubInstallation(githubInstallationID))
	var ownedErr *InstallationOwnedByAnotherOrgError
	if !errors.As(err, &ownedErr) {
		t.Fatalf("org B connect error = %v, want *InstallationOwnedByAnotherOrgError", err)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatal("owner error should classify as ErrConflict")
	}
	if ownedErr.AccountLogin != "octo-org" {
		t.Errorf("owner error account = %q, want octo-org", ownedErr.AccountLogin)
	}
	if ownedErr.OwnerOrgID != orgA.orgID {
		t.Errorf("owner error owner org = %q, want %q", ownedErr.OwnerOrgID, orgA.orgID)
	}
}

func withSearchPath(t *testing.T, databaseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// seedInstallationRow inserts one active installation row plus its webhook route
// for an organization, in a single transaction carrying the org's RLS context,
// and returns the new installation id.
func seedInstallationRow(t *testing.T, pool *pgxpool.Pool, orgID, userID string, githubInstallationID int64) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		userID, orgID,
	); err != nil {
		t.Fatal(err)
	}
	var installationID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO ao_github_installations (
			org_id, github_installation_id, github_account_id,
			account_login, account_type, repository_selection, installed_by_user_id
		) VALUES ($1, $2, 7, 'octo-org', 'Organization', 'all', $3)
		RETURNING id`,
		orgID, githubInstallationID, userID,
	).Scan(&installationID); err != nil {
		t.Fatalf("seed installation row: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ao_github_installation_routes (github_installation_id, org_id, installation_id)
		 VALUES ($1, $2, $3)`,
		githubInstallationID, orgID, installationID,
	); err != nil {
		t.Fatalf("seed installation route: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit installation row: %v", err)
	}
	return installationID
}
