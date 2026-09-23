package devimport

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestRunImportsIntoEmptyTarget(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	project := testProject("alpha", "/repos/alpha")
	project.Config = domain.ProjectConfig{DefaultBranch: "develop", SessionPrefix: "a"}
	if err := source.UpsertWorkspaceProject(ctx, project, nil); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{SourceDataDir: "src", TargetDataDir: "dst"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 1 || rep.Updated != 0 || rep.Skipped != 0 {
		t.Fatalf("report = %#v, want 1 insert", rep)
	}
	got, ok, err := target.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("target project missing")
	}
	if got.Path != project.Path || got.Config.DefaultBranch != "develop" || got.Config.SessionPrefix != "a" {
		t.Fatalf("target project = %#v", got)
	}
}

func TestRunRerunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	project := testProject("alpha", "/repos/alpha")
	if err := source.UpsertWorkspaceProject(ctx, project, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(ctx, source, target, Options{}); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(ctx, source, target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 0 || rep.Updated != 1 || rep.Skipped != 0 {
		t.Fatalf("report = %#v, want one metadata update on rerun", rep)
	}
	projects, err := target.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("target projects = %d, want 1", len(projects))
	}
}

func TestRunSameIDPathUpdatesMetadataAndConfig(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	src := testProject("alpha", "/repos/alpha")
	src.RegisteredAt = time.Unix(200, 0).UTC()
	src.DisplayName = "Alpha New"
	src.RepoOriginURL = "https://example.com/new.git"
	src.Config = domain.ProjectConfig{DefaultBranch: "release"}
	dst := testProject("alpha", "/repos/alpha")
	dst.RegisteredAt = time.Unix(100, 0).UTC()
	dst.DisplayName = "Alpha Old"
	dst.Config = domain.ProjectConfig{DefaultBranch: "main"}
	if err := source.UpsertWorkspaceProject(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if err := target.UpsertWorkspaceProject(ctx, dst, nil); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Updated != 1 || rep.Inserted != 0 || rep.Skipped != 0 {
		t.Fatalf("report = %#v, want one update", rep)
	}
	got, _, err := target.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Alpha New" || got.RepoOriginURL != src.RepoOriginURL || got.Config.DefaultBranch != "release" || !got.RegisteredAt.Equal(src.RegisteredAt) {
		t.Fatalf("updated target = %#v", got)
	}
}

func TestRunSamePathDifferentIDConflicts(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	if err := source.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/shared"), nil); err != nil {
		t.Fatal(err)
	}
	if err := target.UpsertWorkspaceProject(ctx, testProject("beta", "/repos/shared"), nil); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != 1 || len(rep.Conflicts) != 1 {
		t.Fatalf("report = %#v, want one conflict", rep)
	}
	if rep.Conflicts[0].Reason != "same path with different active id" {
		t.Fatalf("conflict = %#v", rep.Conflicts[0])
	}
	if _, ok, err := target.GetProject(ctx, "alpha"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("conflicted source project was inserted")
	}
}

func TestRunReportsLiveIDConflictAtImportWrite(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	if err := source.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/source"), nil); err != nil {
		t.Fatal(err)
	}
	concurrent := &concurrentConflictTarget{
		Store: target,
		beforeImport: func() {
			if err := target.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/live"), nil); err != nil {
				t.Fatal(err)
			}
		},
	}

	rep, err := Run(ctx, source, concurrent, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 0 || rep.Updated != 0 || rep.Skipped != 1 || len(rep.Conflicts) != 1 {
		t.Fatalf("report = %#v, want one live conflict", rep)
	}
	if rep.Conflicts[0].Reason != "same id with different active path" || rep.Conflicts[0].TargetPath != "/repos/live" {
		t.Fatalf("conflict = %#v", rep.Conflicts[0])
	}
	got, ok, err := target.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Path != "/repos/live" {
		t.Fatalf("target project = %#v, want concurrent row preserved", got)
	}
}

func TestRunReportsLivePathConflictAtImportWrite(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	if err := source.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/shared"), nil); err != nil {
		t.Fatal(err)
	}
	concurrent := &concurrentConflictTarget{
		Store: target,
		beforeImport: func() {
			if err := target.UpsertWorkspaceProject(ctx, testProject("beta", "/repos/shared"), nil); err != nil {
				t.Fatal(err)
			}
		},
	}

	rep, err := Run(ctx, source, concurrent, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 0 || rep.Updated != 0 || rep.Skipped != 1 || len(rep.Conflicts) != 1 {
		t.Fatalf("report = %#v, want one live conflict", rep)
	}
	if rep.Conflicts[0].Reason != "same path with different active id" || rep.Conflicts[0].TargetID != "beta" {
		t.Fatalf("conflict = %#v", rep.Conflicts[0])
	}
	if _, ok, err := target.GetProject(ctx, "alpha"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("conflicted source project was inserted")
	}
}

func TestRunDryRunDetectsSourcePathConflictAgainstPlannedInsert(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	if err := source.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/shared"), nil); err != nil {
		t.Fatal(err)
	}
	if err := source.UpsertWorkspaceProject(ctx, testProject("beta", "/repos/shared"), nil); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 1 || rep.Skipped != 1 || len(rep.Conflicts) != 1 {
		t.Fatalf("report = %#v, want one planned insert and one conflict", rep)
	}
	if rep.Conflicts[0].Reason != "same path with different active id" {
		t.Fatalf("conflict = %#v", rep.Conflicts[0])
	}
	projects, err := target.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("dry-run wrote target projects: %#v", projects)
	}
}

func TestRunSameIDDifferentPathConflicts(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	if err := source.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/source"), nil); err != nil {
		t.Fatal(err)
	}
	if err := target.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/target"), nil); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != 1 || len(rep.Conflicts) != 1 {
		t.Fatalf("report = %#v, want one conflict", rep)
	}
	if rep.Conflicts[0].Reason != "same id with different active path" {
		t.Fatalf("conflict = %#v", rep.Conflicts[0])
	}
}

func TestRunDryRunArchivedTargetSameIDConflicts(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	if err := source.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/source"), nil); err != nil {
		t.Fatal(err)
	}
	if err := target.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/target"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ArchiveProject(ctx, "alpha", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 0 || rep.Updated != 0 || rep.Skipped != 1 || len(rep.Conflicts) != 1 {
		t.Fatalf("report = %#v, want archived same-id conflict", rep)
	}
	if rep.Conflicts[0].Reason != domain.ProjectImportConflictSameIDArchivedTarget || rep.Conflicts[0].TargetPath != "/repos/target" {
		t.Fatalf("conflict = %#v", rep.Conflicts[0])
	}
	got, ok, err := target.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.ArchivedAt.IsZero() || got.Path != "/repos/target" {
		t.Fatalf("target project = %#v, want archived target preserved", got)
	}
}

func TestRunArchivedTargetSameIDConflictsWithoutReactivating(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	if err := source.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/source"), nil); err != nil {
		t.Fatal(err)
	}
	if err := target.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/target"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ArchiveProject(ctx, "alpha", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 0 || rep.Updated != 0 || rep.Skipped != 1 || len(rep.Conflicts) != 1 {
		t.Fatalf("report = %#v, want archived same-id conflict", rep)
	}
	if rep.Conflicts[0].Reason != domain.ProjectImportConflictSameIDArchivedTarget {
		t.Fatalf("conflict = %#v", rep.Conflicts[0])
	}
	got, ok, err := target.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.ArchivedAt.IsZero() || got.Path != "/repos/target" {
		t.Fatalf("target project = %#v, want archived target preserved", got)
	}
}

func TestRunIgnoresArchivedSourceProjects(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	if err := source.UpsertWorkspaceProject(ctx, testProject("alpha", "/repos/alpha"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ArchiveProject(ctx, "alpha", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 0 || rep.Updated != 0 || rep.Skipped != 0 {
		t.Fatalf("report = %#v, want no changes", rep)
	}
}

func TestRunCopiesWorkspaceChildRepos(t *testing.T) {
	ctx := context.Background()
	source := newStore(t)
	target := newStore(t)
	project := testProject("workspace", "/repos/workspace")
	project.Kind = domain.ProjectKindWorkspace
	repos := []domain.WorkspaceRepoRecord{
		{ProjectID: domain.ProjectID(project.ID), Name: "api", RelativePath: "api", RepoOriginURL: "https://example.com/api.git", DefaultBranch: "dev", RegisteredAt: project.RegisteredAt},
		{ProjectID: domain.ProjectID(project.ID), Name: "web", RelativePath: "web", RepoOriginURL: "https://example.com/web.git", DefaultBranch: "main", RegisteredAt: project.RegisteredAt},
	}
	if err := source.UpsertWorkspaceProject(ctx, project, repos); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, source, target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 1 {
		t.Fatalf("report = %#v, want one insert", rep)
	}
	got, err := target.ListWorkspaceRepos(ctx, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "api" || got[1].Name != "web" {
		t.Fatalf("workspace repos = %#v", got)
	}
	if got[0].DefaultBranch != "dev" || got[1].DefaultBranch != "main" {
		t.Fatalf("workspace repo default branches = %#v", got)
	}
}

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type concurrentConflictTarget struct {
	*sqlite.Store
	beforeImport func()
	called       bool
}

func (s *concurrentConflictTarget) ImportWorkspaceProject(ctx context.Context, row domain.ProjectRecord, repos []domain.WorkspaceRepoRecord) error {
	if !s.called {
		s.called = true
		s.beforeImport()
	}
	return s.Store.ImportWorkspaceProject(ctx, row, repos)
}

func testProject(id string, path string) domain.ProjectRecord {
	return domain.ProjectRecord{
		ID:            id,
		Path:          path,
		RepoOriginURL: "https://example.com/" + id + ".git",
		DisplayName:   id,
		RegisteredAt:  time.Unix(100, 0).UTC(),
		Kind:          domain.ProjectKindSingleRepo,
	}
}
