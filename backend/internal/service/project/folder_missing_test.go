package project_test

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// T1. FolderMissing false when directory exists (happy path).
func TestFolderMissing_FalseWhenDirectoryExists(t *testing.T) {
	ctx := context.Background()
	configureCommitter(t)
	m := newManager(t)
	repo := gitRepoWithCommit(t, t.TempDir())

	_, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != "ao" {
		t.Fatalf("List = %#v, want [ao]", list)
	}
	if list[0].FolderMissing {
		t.Fatal("List FolderMissing = true, want false")
	}

	res, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if res.Status != "ok" || res.Project == nil {
		t.Fatalf("Get = %#v", res)
	}
	if res.Project.FolderMissing {
		t.Fatal("Get FolderMissing = true, want false")
	}
}

// T2. FolderMissing true after directory deleted + recovery.
func TestFolderMissing_LifecycleDeleteAndRecovery(t *testing.T) {
	ctx := context.Background()
	configureCommitter(t)
	m := newManager(t)
	repo := gitRepoWithCommit(t, t.TempDir())

	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Initially present.
	if got, err := m.List(ctx); err != nil || len(got) != 1 || got[0].FolderMissing {
		t.Fatalf("pre-delete List = %#v, %v", got, err)
	}

	// Delete the directory.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	// Assert FolderMissing true via List.
	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list) != 1 || !list[0].FolderMissing {
		t.Fatalf("List after delete = %#v, want FolderMissing=true", list)
	}

	// Assert FolderMissing true via Get.
	res, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if !res.Project.FolderMissing {
		t.Fatal("Get FolderMissing = false after delete, want true")
	}
	// Default branch falls back to "auto" when folder is missing.
	if res.Project.DefaultBranch != domain.DefaultBranchAuto {
		t.Fatalf("Get DefaultBranch = %q, want %q (missing folder falls back to auto)", res.Project.DefaultBranch, domain.DefaultBranchAuto)
	}

	// Recovery: recreate the directory as a valid git repo at the same path.
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("MkdirAll for recovery: %v", err)
	}
	if out, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init recovery: %v (%s)", err, out)
	}
	commitEmpty(t, repo)

	// Assert FolderMissing false again via List.
	list2, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List after recovery: %v", err)
	}
	if len(list2) != 1 || list2[0].FolderMissing {
		t.Fatalf("List after recovery = %#v, want FolderMissing=false", list2)
	}

	// Assert FolderMissing false again via Get.
	res2, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if res2.Project.FolderMissing {
		t.Fatal("Get FolderMissing = true after recovery, want false")
	}
}

// T3. Non-directory file at the path is treated as missing.
func TestFolderMissing_NonDirectoryFileCountsAsMissing(t *testing.T) {
	ctx := context.Background()
	configureCommitter(t)
	m := newManager(t)
	repo := gitRepoWithCommit(t, t.TempDir())

	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Remove the directory and place a regular file at the same path.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.WriteFile(repo, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || !list[0].FolderMissing {
		t.Fatalf("List with file at path = %#v, want FolderMissing=true", list)
	}

	res, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !res.Project.FolderMissing {
		t.Fatal("Get FolderMissing = false with file at path, want true")
	}
}

// T4. Missing folder yields FolderMissing=true and falls back to DefaultBranchAuto.
func TestFolderMissing_MissingFolderFallsBackToAutoBranch(t *testing.T) {
	ctx := context.Background()
	configureCommitter(t)
	m := newManager(t)
	repo := gitRepoWithCommit(t, t.TempDir())

	proj, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if proj.DefaultBranch != domain.DefaultBranchAuto {
		t.Fatalf("initial DefaultBranch = %q, want %q", proj.DefaultBranch, domain.DefaultBranchAuto)
	}

	// Delete the directory to trigger the missing path.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	res, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !res.Project.FolderMissing {
		t.Fatal("FolderMissing = false, want true")
	}
	if res.Project.DefaultBranch != domain.DefaultBranchAuto {
		t.Fatalf("DefaultBranch = %q, want %q when folder missing", res.Project.DefaultBranch, domain.DefaultBranchAuto)
	}
}

// T7. Remove after external deletion proves Remove works when folder is gone.
func TestFolderMissing_RemoveAfterExternalDeletion(t *testing.T) {
	ctx := context.Background()
	configureCommitter(t)
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	teardown := &fakeProjectTeardowner{}
	m := project.NewWithDeps(project.Deps{Store: store, Sessions: teardown})

	repo := gitRepoWithCommit(t, t.TempDir())
	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Delete the directory externally.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	// Confirm FolderMissing.
	if got, err := m.List(ctx); err != nil || len(got) != 1 || !got[0].FolderMissing {
		t.Fatalf("List after delete = %#v, %v", got, err)
	}

	// Remove must succeed even though the folder is gone.
	rm, err := m.Remove(ctx, "ao")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if rm.ProjectID != "ao" {
		t.Fatalf("Remove ProjectID = %q, want ao", rm.ProjectID)
	}

	// Teardowner was invoked.
	if len(teardown.projects) != 1 || teardown.projects[0] != "ao" {
		t.Fatalf("teardown projects = %#v, want [ao]", teardown.projects)
	}

	// Project is gone from List.
	if list, _ := m.List(ctx); len(list) != 0 {
		t.Fatalf("List after remove = %d, want 0", len(list))
	}

	// Get returns PROJECT_NOT_FOUND.
	_, err = m.Get(ctx, "ao")
	wantCode(t, err, "PROJECT_NOT_FOUND")
}
