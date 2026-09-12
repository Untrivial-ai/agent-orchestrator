package devimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestRunProjectsDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	sourceDir := filepath.Join(t.TempDir(), "source")
	targetDir := filepath.Join(t.TempDir(), "target")
	writeProject(t, sourceDir, "alpha", "/repos/alpha", time.Unix(100, 0).UTC())
	target := openStore(t, targetDir)
	svc := New(Deps{Store: target, TargetDataDir: targetDir, OpenSource: openReadOnlySource})

	rep, err := svc.RunProjects(ctx, RunInput{SourceDataDir: sourceDir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || rep.Inserted != 1 {
		t.Fatalf("report = %#v, want dry-run planned insert", rep)
	}
	projects, err := target.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("target projects = %#v, want none", projects)
	}
}

func TestRunProjectsReadOnlySourceDoesNotCreateMissingSource(t *testing.T) {
	ctx := context.Background()
	sourceDir := filepath.Join(t.TempDir(), "missing-source")
	targetDir := filepath.Join(t.TempDir(), "target")
	target := openStore(t, targetDir)
	svc := New(Deps{Store: target, TargetDataDir: targetDir, OpenSource: openReadOnlySource})

	_, err := svc.RunProjects(ctx, RunInput{SourceDataDir: sourceDir, DryRun: true})
	if err == nil {
		t.Fatal("RunProjects succeeded for missing source")
	}
	if _, statErr := os.Stat(sourceDir); !os.IsNotExist(statErr) {
		t.Fatalf("source data dir stat err = %v, want not exist", statErr)
	}
}

func TestRunProjectsImportsIntoTarget(t *testing.T) {
	ctx := context.Background()
	sourceDir := filepath.Join(t.TempDir(), "source")
	targetDir := filepath.Join(t.TempDir(), "target")
	registeredAt := time.Unix(200, 0).UTC()
	source := writeProject(t, sourceDir, "alpha", "/repos/alpha", registeredAt)
	original, ok, err := source.GetProject(ctx, "alpha")
	if err != nil || !ok {
		t.Fatalf("source project: %v", err)
	}
	original.RepoOriginURL = "https://gitlab.com/alice/repo"
	original.Config.CanonicalRepoURL = "https://gitlab.com/group/subgroup/repo"
	if err := source.UpsertProject(ctx, original); err != nil {
		t.Fatal(err)
	}

	target := openStore(t, targetDir)
	svc := New(Deps{Store: target, TargetDataDir: targetDir, OpenSource: openReadOnlySource})

	rep, err := svc.RunProjects(ctx, RunInput{SourceDataDir: sourceDir})
	if err != nil {
		t.Fatal(err)
	}
	wantTargetDir, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 1 || rep.TargetDataDir != wantTargetDir {
		t.Fatalf("report = %#v, want insert into target dir", rep)
	}
	got, ok, err := target.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !got.RegisteredAt.Equal(registeredAt) {
		t.Fatalf("target project = %#v, want registered_at %s", got, registeredAt)
	}
	if got.Config.CanonicalRepoURL != original.Config.CanonicalRepoURL {
		t.Fatalf("canonical identity lost during native import: %+v", got.Config)
	}

}

func TestRunProjectsRejectsSameSourceAndTarget(t *testing.T) {
	ctx := context.Background()
	targetDir := filepath.Join(t.TempDir(), "target")
	target := openStore(t, targetDir)
	svc := New(Deps{Store: target, TargetDataDir: targetDir, OpenSource: openReadOnlySource})

	_, err := svc.RunProjects(ctx, RunInput{SourceDataDir: targetDir, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "sourceDataDir must be different") {
		t.Fatalf("err = %v, want same source/target rejection", err)
	}
}

func TestRunProjectsRejectsSymlinkedSameSourceAndTarget(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	targetDir := filepath.Join(parent, "target")
	target := openStore(t, targetDir)
	sourceLink := filepath.Join(parent, "source-link")
	if err := os.Symlink(targetDir, sourceLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	svc := New(Deps{Store: target, TargetDataDir: targetDir, OpenSource: openReadOnlySource})

	_, err := svc.RunProjects(ctx, RunInput{SourceDataDir: sourceLink, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "sourceDataDir must be different") {
		t.Fatalf("err = %v, want same source/target rejection", err)
	}
}

func openStore(t *testing.T, dataDir string) *sqlite.Store {
	t.Helper()
	store, err := sqlitetest.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func openReadOnlySource(ctx context.Context, dataDir string) (SourceStore, error) {
	return sqlite.OpenReadOnly(ctx, dataDir)
}

func writeProject(t *testing.T, dataDir string, id string, path string, registeredAt time.Time) *sqlite.Store {
	t.Helper()
	store := openStore(t, dataDir)
	project := domain.ProjectRecord{
		ID:            id,
		Path:          path,
		RepoOriginURL: "https://example.com/" + id + ".git",
		DisplayName:   id,
		RegisteredAt:  registeredAt,
		Kind:          domain.ProjectKindSingleRepo,
		Config:        domain.ProjectConfig{DefaultBranch: "main"},
	}
	if err := store.UpsertWorkspaceProject(context.Background(), project, nil); err != nil {
		t.Fatal(err)
	}
	return store
}
