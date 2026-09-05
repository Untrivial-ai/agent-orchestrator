package project_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/gitdefault"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// newManager builds a Manager over a real, isolated sqlite store cloned from a
// current migrated template — no in-memory store.
func newManager(t *testing.T) project.Manager {
	t.Helper()
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return project.New(store)
}

// gitRepo creates a real git repository in a fresh temp dir and returns its
// path. It pins the initial branch to `main` so default-branch detection is
// deterministic regardless of the host's init.defaultBranch.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git unavailable: %v (%s)", err, out)
	}
	commitEmpty(t, dir)
	return dir
}

func isolatedPlainFolder(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", base)
	dir := filepath.Join(base, "selected")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// gitRepoOnBranch creates a real git repository whose initial branch is
// `branch`, used to exercise default-branch detection for non-`main` repos.
func gitRepoOnBranch(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", branch, dir).CombinedOutput(); err != nil {
		t.Fatalf("git unavailable: %v (%s)", err, out)
	}
	commitEmpty(t, dir)
	return dir
}

// gitRepoWithOriginHead creates a repo whose remote default (origin/HEAD) points
// at defaultBranch while the working tree is checked out on featureBranch. This
// mirrors a user adding a project while sitting on a feature branch: detection
// must record the remote default, not the active branch.
func gitRepoWithOriginHead(t *testing.T, defaultBranch, featureBranch string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if out, err := exec.Command("git", "init", "-b", defaultBranch, dir).CombinedOutput(); err != nil {
		t.Fatalf("git unavailable: %v (%s)", err, out)
	}
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("commit", "--allow-empty", "-m", "init")
	// Fabricate a remote-tracking default without a real remote: point
	// refs/remotes/origin/<defaultBranch> at HEAD, then set origin/HEAD to it.
	run("remote", "add", "origin", "https://example.invalid/project.git")
	run("update-ref", "refs/remotes/origin/"+defaultBranch, "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+defaultBranch)
	run("checkout", "-b", featureBranch)
	return dir
}

func commitEmpty(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "-c", "user.email=ao@example.com", "-c", "user.name=AO Test", "commit", "--allow-empty", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
}
func ptr(s string) *string { return &s }

// wantCode asserts err is an *apierr.Error carrying the given machine code.
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *apierr.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want *apierr.Error", err)
	}
	if e.Code != code {
		t.Fatalf("code = %q, want %q", e.Code, code)
	}
}

type fakeProjectTeardowner struct {
	projects []domain.ProjectID
	err      error
}

type captureSink struct {
	events []ports.TelemetryEvent
}

func (s *captureSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.events = append(s.events, ev)
}

func (*captureSink) Close(context.Context) error { return nil }

func (f *fakeProjectTeardowner) TeardownProject(_ context.Context, project domain.ProjectID) error {
	f.projects = append(f.projects, project)
	return f.err
}

func TestManager_AddListGetRemove(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepo(t)

	if got, err := m.List(ctx); err != nil || len(got) != 0 {
		t.Fatalf("List() = %v, %v; want empty", got, err)
	}

	proj, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao"), Name: ptr("Agent Orchestrator")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if proj.ID != "ao" || proj.Name != "Agent Orchestrator" || proj.Path != repo || proj.DefaultBranch != domain.DefaultBranchAuto {
		t.Fatalf("Add returned %#v", proj)
	}

	list, err := m.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "ao" {
		t.Fatalf("List() = %v, %v; want [ao]", list, err)
	}

	res, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if res.Status != "ok" || res.Project == nil || res.Project.ID != "ao" {
		t.Fatalf("Get = %#v", res)
	}

	rm, err := m.Remove(ctx, "ao")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if rm.ProjectID != "ao" || rm.RemovedStorageDir {
		t.Fatalf("Remove = %#v", rm)
	}
	if list, _ := m.List(ctx); len(list) != 0 {
		t.Fatalf("active list after remove = %d, want 0", len(list))
	}
	_, err = m.Get(ctx, "ao")
	wantCode(t, err, "PROJECT_NOT_FOUND")

	_, err = m.Remove(ctx, "ao")
	wantCode(t, err, "PROJECT_NOT_FOUND")
}

func TestManager_CloneRegistersRepositoryAndPreservesOrigin(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	source := gitRepo(t)
	remoteURL := (&url.URL{Scheme: "file", Path: source}).String()
	destinationParent := t.TempDir()

	cloned, err := m.Clone(ctx, project.CloneInput{
		RemoteURL:         remoteURL,
		DestinationParent: destinationParent,
		Name:              ptr("Cloned repository"),
	})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	wantPath := filepath.Join(destinationParent, filepath.Base(source))
	if cloned.Path != wantPath || cloned.Name != "Cloned repository" || cloned.Repo != remoteURL {
		t.Fatalf("Clone returned %#v, want path=%q name=%q repo=%q", cloned, wantPath, "Cloned repository", remoteURL)
	}
	if out, err := exec.Command("git", "-C", wantPath, "rev-parse", "--verify", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("cloned repository has no checkout: %v (%s)", err, out)
	}
	if listed, err := m.List(ctx); err != nil || len(listed) != 1 || listed[0].Path != wantPath {
		t.Fatalf("List after Clone = %#v, %v", listed, err)
	}
}

func TestManager_PrepareClonePreservesEmptyRepositoryForImportSetup(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	emptySource := filepath.Join(t.TempDir(), "empty-repository")
	if out, err := exec.Command("git", "init", "-b", "main", emptySource).CombinedOutput(); err != nil {
		t.Fatalf("git init empty source: %v (%s)", err, out)
	}
	destinationParent := t.TempDir()
	emptyURL := (&url.URL{Scheme: "file", Path: emptySource}).String()

	prepared, err := m.PrepareClone(ctx, project.CloneInput{RemoteURL: emptyURL, DestinationParent: destinationParent})
	if err != nil {
		t.Fatalf("PrepareClone: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(prepared.Path) })
	if prepared.RemoteURL != emptyURL {
		t.Fatalf("PrepareClone remote URL = %q, want %q", prepared.RemoteURL, emptyURL)
	}
	if _, err := os.Stat(filepath.Join(prepared.Path, ".git")); err != nil {
		t.Fatalf("prepared checkout missing .git: %v", err)
	}
	if listed, err := m.List(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("List after PrepareClone = %#v, %v; preparation must not register", listed, err)
	}
	if err := m.CleanupPreparedClone(ctx, project.ClonePreparationCleanupInput{Path: prepared.Path}); err != nil {
		t.Fatalf("CleanupPreparedClone: %v", err)
	}
	if _, err := os.Stat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared checkout still exists after cleanup: %v", err)
	}
}

func TestManager_PreparedCloneRemainsCleanupEligibleWhenRegistrationFails(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	source := gitRepo(t)
	destinationParent := t.TempDir()

	prepared, err := m.PrepareClone(ctx, project.CloneInput{
		RemoteURL:         (&url.URL{Scheme: "file", Path: source}).String(),
		DestinationParent: destinationParent,
	})
	if err != nil {
		t.Fatalf("PrepareClone: %v", err)
	}
	_, err = m.Add(ctx, project.AddInput{
		Path:   prepared.Path,
		Config: &domain.ProjectConfig{AgentConfig: domain.AgentConfig{Permissions: "invalid"}},
	})
	wantCode(t, err, "INVALID_PROJECT_CONFIG")

	if err := m.CleanupPreparedClone(ctx, project.ClonePreparationCleanupInput{Path: prepared.Path}); err != nil {
		t.Fatalf("CleanupPreparedClone: %v", err)
	}
	if _, err := os.Stat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared checkout still exists after failed registration cleanup: %v", err)
	}
}

func TestManager_CloneRejectsUnsafeURLsAndExistingDestination(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	destinationParent := t.TempDir()

	for _, tc := range []struct {
		name, remoteURL, code string
	}{
		{name: "plain path", remoteURL: "owner/repository", code: "INVALID_GIT_URL"},
		{name: "unsupported helper", remoteURL: "ext::sh -c exploit", code: "INVALID_GIT_URL"},
		{name: "embedded credential", remoteURL: "https://token@github.com/acme/repository.git", code: "GIT_URL_CONTAINS_CREDENTIALS"},
		{name: "credential query", remoteURL: "https://github.com/acme/repository.git?access_token=secret", code: "GIT_URL_CONTAINS_CREDENTIALS"},
		{name: "ssh password", remoteURL: "ssh://git:secret@github.com/acme/repository.git", code: "GIT_URL_CONTAINS_CREDENTIALS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.Clone(ctx, project.CloneInput{RemoteURL: tc.remoteURL, DestinationParent: destinationParent})
			wantCode(t, err, tc.code)
		})
	}

	existing := filepath.Join(destinationParent, "repository")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existing, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := m.Clone(ctx, project.CloneInput{
		RemoteURL:         "https://github.com/acme/repository.git",
		DestinationParent: destinationParent,
	})
	wantCode(t, err, "CLONE_DESTINATION_EXISTS")
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatalf("existing destination changed: %q, %v", got, err)
	}
}

func TestManager_CloneCleansUpFailedAndEmptyCheckouts(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	destinationParent := t.TempDir()

	missing := filepath.Join(t.TempDir(), "missing-repository")
	missingURL := (&url.URL{Scheme: "file", Path: missing}).String()
	_, err := m.Clone(ctx, project.CloneInput{RemoteURL: missingURL, DestinationParent: destinationParent})
	wantCode(t, err, "GIT_CLONE_FAILED")
	if _, err := os.Stat(filepath.Join(destinationParent, "missing-repository")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed clone destination still exists: %v", err)
	}

	emptySource := filepath.Join(t.TempDir(), "empty-repository")
	if out, err := exec.Command("git", "init", "-b", "main", emptySource).CombinedOutput(); err != nil {
		t.Fatalf("git init empty source: %v (%s)", err, out)
	}
	emptyURL := (&url.URL{Scheme: "file", Path: emptySource}).String()
	_, err = m.Clone(ctx, project.CloneInput{RemoteURL: emptyURL, DestinationParent: destinationParent})
	wantCode(t, err, "CLONE_EMPTY_REPOSITORY")
	if _, err := os.Stat(filepath.Join(destinationParent, "empty-repository")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty clone destination still exists: %v", err)
	}
	if temporary, err := filepath.Glob(filepath.Join(destinationParent, ".ao-clone-*")); err != nil || len(temporary) != 0 {
		t.Fatalf("temporary clone directories = %#v, %v", temporary, err)
	}
}

func TestManager_AddEmitsProjectAndFirstProjectTelemetry(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "req-1")
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sink := &captureSink{}
	m := project.NewWithDeps(project.Deps{Store: store, Telemetry: sink})

	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("events = %#v, want projects.created + first_project_added", sink.events)
	}
	if sink.events[0].Name != "ao.projects.created" || sink.events[1].Name != "ao.onboarding.first_project_added" {
		t.Fatalf("event names = %#v", []string{sink.events[0].Name, sink.events[1].Name})
	}
	// The emit path detaches from the request context on purpose; the request id
	// must still be carried so the rows join to the HTTP request that added the
	// project.
	for _, ev := range sink.events {
		if ev.RequestID != "req-1" {
			t.Fatalf("%s RequestID = %q, want req-1", ev.Name, ev.RequestID)
		}
	}
}

func TestManager_AddDoesNotRepeatFirstProjectTelemetry(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sink := &captureSink{}
	m := project.NewWithDeps(project.Deps{Store: store, Telemetry: sink})

	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add first: %v", err)
	}
	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("ao2")}); err != nil {
		t.Fatalf("Add second: %v", err)
	}
	var firstProjectCount int
	for _, ev := range sink.events {
		if ev.Name == "ao.onboarding.first_project_added" {
			firstProjectCount++
		}
	}
	if firstProjectCount != 1 {
		t.Fatalf("first project telemetry count = %d, want 1", firstProjectCount)
	}
}

func TestManager_EnsureDefaultScratchProjectSeedsFreshRegistry(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.NewWithDeps(project.Deps{Store: store, DefaultHarness: domain.HarnessCodex})
	scratchPath := filepath.Join(t.TempDir(), "scratch", "default")

	proj, err := m.EnsureDefaultScratchProject(ctx, scratchPath)
	if err != nil {
		t.Fatalf("EnsureDefaultScratchProject: %v", err)
	}
	if proj.ID != "scratch" || proj.Name != "Scratch" || proj.Path != scratchPath || proj.Kind != domain.ProjectKindScratch {
		t.Fatalf("scratch project = %#v", proj)
	}
	if proj.Repo != "" || proj.DefaultBranch != "" {
		t.Fatalf("scratch repo/default branch = %q/%q, want empty", proj.Repo, proj.DefaultBranch)
	}
	if proj.Agent != string(domain.HarnessCodex) || proj.Config == nil ||
		proj.Config.Worker.Harness != domain.HarnessCodex ||
		proj.Config.Orchestrator.Harness != domain.HarnessCodex {
		t.Fatalf("scratch agents/config = agent:%q config:%#v, want codex role overrides", proj.Agent, proj.Config)
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != "scratch" || list[0].Kind != domain.ProjectKindScratch {
		t.Fatalf("List = %#v, want one scratch project", list)
	}
	if list[0].OrchestratorAgent != domain.HarnessCodex {
		t.Fatalf("summary orchestrator agent = %q, want codex", list[0].OrchestratorAgent)
	}
}

func TestManager_EnsureDefaultScratchProjectDoesNotReseedWithActiveProject(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:           "old",
		DisplayName:  "Old",
		Path:         "/tmp/old",
		Kind:         domain.ProjectKindSingleRepo,
		RegisteredAt: now,
	}); err != nil {
		t.Fatalf("seed old project: %v", err)
	}

	m := project.NewWithDeps(project.Deps{Store: store})
	proj, err := m.EnsureDefaultScratchProject(ctx, filepath.Join(t.TempDir(), "scratch", "default"))
	if err != nil {
		t.Fatalf("EnsureDefaultScratchProject: %v", err)
	}
	if proj.ID != "" {
		t.Fatalf("seeded scratch with active project: %#v", proj)
	}
	if list, err := m.List(ctx); err != nil || len(list) != 1 || list[0].ID != "old" {
		t.Fatalf("active projects = %#v, %v; want old project only", list, err)
	}
}

func TestManager_EnsureDefaultScratchProjectReseedsAfterArchivedScratchLeavesNoActiveProjects(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.NewWithDeps(project.Deps{Store: store})
	firstPath := filepath.Join(t.TempDir(), "scratch", "default")
	first, err := m.EnsureDefaultScratchProject(ctx, firstPath)
	if err != nil {
		t.Fatalf("first EnsureDefaultScratchProject: %v", err)
	}
	if first.ID != "scratch" {
		t.Fatalf("first scratch project = %#v", first)
	}
	if ok, err := store.ArchiveProject(ctx, "scratch", time.Now().UTC().Add(time.Minute)); err != nil || !ok {
		t.Fatalf("archive scratch project: ok=%v err=%v", ok, err)
	}
	scratchPath := filepath.Join(t.TempDir(), "scratch", "replacement")
	proj, err := m.EnsureDefaultScratchProject(ctx, scratchPath)
	if err != nil {
		t.Fatalf("EnsureDefaultScratchProject: %v", err)
	}
	if proj.ID != "scratch" || proj.Path != scratchPath || proj.Kind != domain.ProjectKindScratch {
		t.Fatalf("reseeded scratch project = %#v", proj)
	}
	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != "scratch" {
		t.Fatalf("active projects = %#v, want reseeded scratch", list)
	}
}

func TestManager_SetConfigRejectsScratchGitOnlyFields(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.NewWithDeps(project.Deps{Store: store})
	scratchPath := filepath.Join(t.TempDir(), "scratch", "default")
	if _, err := m.EnsureDefaultScratchProject(ctx, scratchPath); err != nil {
		t.Fatalf("EnsureDefaultScratchProject: %v", err)
	}

	_, err = m.SetConfig(ctx, "scratch", project.SetConfigInput{Config: domain.ProjectConfig{DefaultBranch: "main"}})
	wantCode(t, err, "INVALID_PROJECT_CONFIG")

	_, err = m.SetConfig(ctx, "scratch", project.SetConfigInput{Config: domain.ProjectConfig{
		TrackerIntake: domain.TrackerIntakeConfig{Enabled: true, Assignee: "alice"},
	}})
	wantCode(t, err, "INVALID_PROJECT_CONFIG")

	_, err = m.SetConfig(ctx, "scratch", project.SetConfigInput{Config: domain.ProjectConfig{
		Reviewers: []domain.ReviewerConfig{{Harness: domain.ReviewerCodex}},
	}})
	wantCode(t, err, "INVALID_PROJECT_CONFIG")

	proj, err := m.SetConfig(ctx, "scratch", project.SetConfigInput{Config: domain.ProjectConfig{
		AgentConfig: domain.AgentConfig{Model: "gpt-5"},
		Worker:      domain.RoleOverride{Harness: domain.HarnessCodex},
	}})
	if err != nil {
		t.Fatalf("allowed SetConfig: %v", err)
	}
	if proj.DefaultBranch != "" || proj.Config == nil || proj.Config.AgentConfig.Model != "gpt-5" {
		t.Fatalf("scratch config result = %#v", proj)
	}
}

func TestManager_RemoveTeardownsBeforeArchive(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	teardown := &fakeProjectTeardowner{}
	m := project.NewWithDeps(project.Deps{Store: store, Sessions: teardown})

	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := m.Remove(ctx, "ao"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(teardown.projects) != 1 || teardown.projects[0] != "ao" {
		t.Fatalf("teardown projects = %#v, want [ao]", teardown.projects)
	}
	_, err = m.Get(ctx, "ao")
	wantCode(t, err, "PROJECT_NOT_FOUND")
}

func TestManager_RemoveDoesNotArchiveWhenTeardownFails(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	boom := errors.New("teardown failed")
	m := project.NewWithDeps(project.Deps{Store: store, Sessions: &fakeProjectTeardowner{err: boom}})

	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := m.Remove(ctx, "ao"); !errors.Is(err, boom) {
		t.Fatalf("Remove err = %v, want teardown failure", err)
	}
	if got, err := m.Get(ctx, "ao"); err != nil || got.Project == nil || got.Project.ID != "ao" {
		t.Fatalf("project after failed remove = %#v, %v; want still active", got, err)
	}
}

func TestManager_DefaultsWhenUnconfigured(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepo(t)

	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// A remoteless project stays in automatic mode instead of deriving a default
	// from its current checkout. The empty config remains unpersisted.
	got, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Project == nil {
		t.Fatalf("Get returned no project: %#v", got)
	}
	if got.Project.DefaultBranch != domain.DefaultBranchAuto {
		t.Fatalf("default branch = %q, want %q", got.Project.DefaultBranch, domain.DefaultBranchAuto)
	}
	if got.Project.Agent != "claude-code" {
		t.Fatalf("default agent = %q, want claude-code", got.Project.Agent)
	}
	if got.Project.Config != nil {
		t.Fatalf("unconfigured project should omit config, got %#v", got.Project.Config)
	}

	list, err := m.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if list[0].SessionPrefix != "ao" {
		t.Fatalf("default session prefix = %q, want derived 'ao'", list[0].SessionPrefix)
	}
}

func TestManager_GetUsesConfiguredDefaultHarness(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.NewWithDeps(project.Deps{Store: store, DefaultHarness: domain.HarnessCodex})
	repo := gitRepo(t)

	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Project == nil {
		t.Fatalf("Get returned no project: %#v", got)
	}
	if got.Project.Agent != "codex" {
		t.Fatalf("default agent = %q, want codex", got.Project.Agent)
	}
}

func TestManager_AddDoesNotTreatCurrentBranchAsAutomaticDefault(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepoOnBranch(t, "master")

	proj, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// A lone local branch is not authoritative default-branch metadata.
	if proj.DefaultBranch != domain.DefaultBranchAuto {
		t.Fatalf("DefaultBranch = %q, want auto", proj.DefaultBranch)
	}
	if proj.Config != nil {
		t.Fatalf("automatic branch selection should not pin config, got %#v", proj.Config)
	}

	got, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Project == nil || got.Project.DefaultBranch != domain.DefaultBranchAuto {
		t.Fatalf("Get DefaultBranch = %#v, want auto", got.Project)
	}

	// An explicit config wins over detection.
	mainRepo := gitRepoOnBranch(t, "trunk")
	proj2, err := m.Add(ctx, project.AddInput{
		Path:      mainRepo,
		ProjectID: ptr("ao2"),
		Config:    &domain.ProjectConfig{DefaultBranch: "release"},
	})
	if err != nil {
		t.Fatalf("Add with config: %v", err)
	}
	if proj2.DefaultBranch != "release" {
		t.Fatalf("explicit DefaultBranch = %q, want release", proj2.DefaultBranch)
	}
}

// A repo checked out on a feature branch must NOT report that branch as the
// project default — resolution must prefer the remote default (origin/HEAD), so a
// repo whose origin/HEAD is `main` stays on `main` even when HEAD is elsewhere.
func TestManager_AddPrefersOriginHeadOverCheckedOutBranch(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepoWithOriginHead(t, "main", "fix/pr-attachment")

	proj, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Automatic mode resolves origin/HEAD to main — never the feature branch.
	if proj.DefaultBranch != domain.DefaultBranchName {
		t.Fatalf("DefaultBranch = %q, want %q (not the checked-out feature branch)",
			proj.DefaultBranch, domain.DefaultBranchName)
	}
}

// When origin/HEAD points at a non-main default (e.g. master), automatic mode
// reports that — not the feature branch the user happens to be on.
func TestManager_AddPrefersOriginHeadNonMain(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepoWithOriginHead(t, "master", "fix/pr-attachment")

	proj, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if proj.DefaultBranch != "master" {
		t.Fatalf("DefaultBranch = %q, want master (origin/HEAD), not feature branch", proj.DefaultBranch)
	}
}

func TestManager_UpdateSettings(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepo(t)

	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cfg := domain.ProjectConfig{
		DefaultBranch:     "develop",
		Env:               map[string]string{"FOO": "bar"},
		AgentRules:        "Run focused tests.",
		OrchestratorRules: "Delegate implementation.",
		AgentConfig:       domain.AgentConfig{Model: "claude-opus-4-5"},
	}
	proj, err := m.UpdateSettings(ctx, "ao", project.UpdateSettingsInput{
		DisplayName: "  AO Project  ",
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if proj.Name != "AO Project" || proj.Config == nil || proj.Config.AgentConfig.Model != "claude-opus-4-5" {
		t.Fatalf("returned project = %#v", proj)
	}
	if proj.DefaultBranch != "develop" {
		t.Fatalf("DefaultBranch = %q, want develop", proj.DefaultBranch)
	}

	// Both values persist and show up together on a fresh Get.
	got, err := m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Project == nil || got.Project.Name != "AO Project" || got.Project.Config == nil || got.Project.Config.Env["FOO"] != "bar" {
		t.Fatalf("Get project = %#v", got.Project)
	}
	if got.Project.Config.AgentRules != "Run focused tests." || got.Project.Config.OrchestratorRules != "Delegate implementation." {
		t.Fatalf("Get rules config = %#v", got.Project.Config)
	}

	// Invalid fields are rejected before either value is persisted.
	_, err = m.UpdateSettings(ctx, "ao", project.UpdateSettingsInput{
		DisplayName: "Should Not Persist",
		Config:      domain.ProjectConfig{AgentConfig: domain.AgentConfig{Permissions: "yolo"}},
	})
	wantCode(t, err, "INVALID_PROJECT_CONFIG")
	got, err = m.Get(ctx, "ao")
	if err != nil {
		t.Fatalf("Get after rejected update: %v", err)
	}
	if got.Project == nil || got.Project.Name != "AO Project" || got.Project.Config == nil || got.Project.Config.AgentConfig.Model != "claude-opus-4-5" {
		t.Fatalf("project changed after rejected update = %#v", got.Project)
	}
	_, err = m.UpdateSettings(ctx, "ao", project.UpdateSettingsInput{DisplayName: "  ", Config: cfg})
	wantCode(t, err, "DISPLAY_NAME_REQUIRED")

	_, err = m.UpdateSettings(ctx, "ao", project.UpdateSettingsInput{
		DisplayName: strings.Repeat("x", 21),
		Config:      cfg,
	})
	wantCode(t, err, "DISPLAY_NAME_TOO_LONG")

	_, err = m.UpdateSettings(ctx, "missing", project.UpdateSettingsInput{DisplayName: "Missing", Config: cfg})
	wantCode(t, err, "PROJECT_NOT_FOUND")
}

func TestManager_ListIncludesOnlySummarySafeProjectConfig(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepo(t)

	cfg := domain.ProjectConfig{
		DefaultBranch: "develop",
		Env:           map[string]string{"GITHUB_TOKEN": "secret"},
		Orchestrator:  domain.RoleOverride{Harness: domain.HarnessCodex},
	}
	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao"), Config: &cfg}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	if list[0].OrchestratorAgent != domain.HarnessCodex {
		t.Fatalf("summary orchestrator agent = %q, want codex", list[0].OrchestratorAgent)
	}
}

func TestManager_ReaddAfterRemove(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepo(t)

	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao")}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if _, err := m.Remove(ctx, "ao"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("ao2")}); err != nil {
		t.Fatalf("re-add same path after remove: %v", err)
	}

	otherRepo := gitRepo(t)
	if _, err := m.Remove(ctx, "ao2"); err != nil {
		t.Fatalf("Remove ao2: %v", err)
	}
	if _, err := m.Add(ctx, project.AddInput{Path: otherRepo, ProjectID: ptr("ao2")}); err != nil {
		t.Fatalf("re-add same id at different path after remove: %v", err)
	}
}

func TestManager_InitializeRepositoryRecovery(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	t.Run("plain folder", func(t *testing.T) {
		dir := isolatedPlainFolder(t)
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir})
		if err != nil {
			t.Fatalf("InitializeRepository: %v", err)
		}
		if result.Path != dir {
			t.Fatalf("Path = %q, want %q", result.Path, dir)
		}
		if _, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD").CombinedOutput(); err != nil {
			t.Fatalf("expected initial commit: %v", err)
		}
		out, err := exec.Command("git", "-C", dir, "show", "HEAD:notes.txt").CombinedOutput()
		if err != nil {
			t.Fatalf("expected existing file in initial commit: %v (%s)", err, out)
		}
		if got := string(out); got != "keep me\n" {
			t.Fatalf("HEAD:notes.txt = %q, want %q", got, "keep me\n")
		}
		proj, err := m.Add(ctx, project.AddInput{Path: dir, ProjectID: ptr("plain")})
		if err != nil {
			t.Fatalf("Add after init: %v", err)
		}
		if proj.DefaultBranch != domain.DefaultBranchName {
			t.Fatalf("AO-initialized default branch = %q, want %q", proj.DefaultBranch, domain.DefaultBranchName)
		}
	})

	t.Run("plain folder nested in parent repo initializes as separate repo root", func(t *testing.T) {
		configureCommitter(t)
		parent := filepath.Join(t.TempDir(), "parent")
		gitRepoWithCommitNoOrigin(t, parent)
		dir := filepath.Join(parent, "universe")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		if _, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir}); err != nil {
			t.Fatalf("InitializeRepository nested plain folder: %v", err)
		}
		if _, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD").CombinedOutput(); err != nil {
			t.Fatalf("expected nested folder initial commit: %v", err)
		}
		top, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").CombinedOutput()
		if err != nil {
			t.Fatalf("git show-toplevel: %v (%s)", err, top)
		}
		want, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		if got := strings.TrimSpace(string(top)); got != want {
			t.Fatalf("show-toplevel = %q, want %q", got, want)
		}
	})

	t.Run("unborn git repo", func(t *testing.T) {
		dir := t.TempDir()
		if out, err := exec.Command("git", "init", "-b", "trunk", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v (%s)", err, out)
		}
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("unborn file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir}); err != nil {
			t.Fatalf("InitializeRepository unborn: %v", err)
		}
		if _, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD").CombinedOutput(); err != nil {
			t.Fatalf("expected initial commit: %v", err)
		}
		out, err := exec.Command("git", "-C", dir, "show", "HEAD:notes.txt").CombinedOutput()
		if err != nil {
			t.Fatalf("expected unborn repo file in initial commit: %v (%s)", err, out)
		}
		if got := string(out); got != "unborn file\n" {
			t.Fatalf("HEAD:notes.txt = %q, want %q", got, "unborn file\n")
		}
		marker, err := exec.Command("git", "-C", dir, "config", "--local", "--get", gitdefault.ManagedDefaultConfigKey).CombinedOutput()
		if err != nil {
			t.Fatalf("read managed default marker: %v (%s)", err, marker)
		}
		if got := strings.TrimSpace(string(marker)); got != "trunk" {
			t.Fatalf("managed default marker = %q, want trunk", got)
		}
	})

	t.Run("already committed repo", func(t *testing.T) {
		_, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: gitRepo(t)})
		wantCode(t, err, "PROJECT_ALREADY_INITIALIZED")
	})

	t.Run("repo subdirectory initializes as separate repo root", func(t *testing.T) {
		repo := gitRepo(t)
		subdir := filepath.Join(repo, "nested")
		if err := os.Mkdir(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: subdir}); err != nil {
			t.Fatalf("InitializeRepository repo subdirectory: %v", err)
		}
		top, err := exec.Command("git", "-C", subdir, "rev-parse", "--show-toplevel").CombinedOutput()
		if err != nil {
			t.Fatalf("git show-toplevel: %v (%s)", err, top)
		}
		want, err := filepath.EvalSymlinks(subdir)
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		if got := strings.TrimSpace(string(top)); got != want {
			t.Fatalf("show-toplevel = %q, want %q", got, want)
		}
	})

	t.Run("bare repo is rejected", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "bare.git")
		if out, err := exec.Command("git", "init", "--bare", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init --bare: %v (%s)", err, out)
		}
		_, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir})
		wantCode(t, err, "PROJECT_BARE_REPOSITORY")
	})

	t.Run("unsupported git metadata is rejected", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: missing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir})
		wantCode(t, err, "UNSUPPORTED_GIT_REPO")
	})

	t.Run("broad setup paths are rejected before init", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		paths := []string{
			home,
			filepath.Join(home, "Desktop"),
			filepath.Join(home, "Documents"),
			filepath.Join(home, "Downloads"),
			filepath.Join(home, ".ao"),
			filepath.Join(home, ".ao", "data"),
		}
		for _, path := range paths {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", path, err)
			}
			_, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: path})
			wantCode(t, err, "PROJECT_SETUP_PATH_UNSAFE")
			if _, statErr := os.Lstat(filepath.Join(path, ".git")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unexpected .git after rejected broad path %s: %v", path, statErr)
			}
		}
	})

	t.Run("folder inside AO-managed worktrees is rejected before init", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		dir := filepath.Join(home, ".ao", "data", "worktrees", "project", "session")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		_, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir})
		wantCode(t, err, "PROJECT_SETUP_PATH_UNSAFE")
		if _, statErr := os.Lstat(filepath.Join(dir, ".git")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unexpected .git after rejected AO worktree setup: %v", statErr)
		}
	})

	t.Run("plain folder rolls back git init when staging fails", func(t *testing.T) {
		dir := isolatedPlainFolder(t)
		gitignore := []byte("node_modules/\n")
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), gitignore, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "missing", "index"))

		_, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir})
		wantCode(t, err, "GIT_ADD_FAILED")
		if _, statErr := os.Lstat(filepath.Join(dir, ".git")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf(".git still exists after rollback: %v", statErr)
		}
		got, readErr := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if readErr != nil {
			t.Fatalf("read .gitignore after rollback: %v", readErr)
		}
		if string(got) != string(gitignore) {
			t.Fatalf(".gitignore after rollback = %q, want %q", got, gitignore)
		}
	})

	t.Run("plain folder with nested repo is rejected before init", func(t *testing.T) {
		dir := isolatedPlainFolder(t)
		nested := filepath.Join(dir, "packages", "foo")
		if out, err := exec.Command("git", "init", "-b", "main", nested).CombinedOutput(); err != nil {
			t.Fatalf("git init nested: %v (%s)", err, out)
		}

		_, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir})
		wantCode(t, err, "PROJECT_NESTED_GIT_REPOSITORY")
		if _, statErr := os.Lstat(filepath.Join(dir, ".git")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unexpected .git after rejected nested repo setup: %v", statErr)
		}
	})

	t.Run("unborn repo with nested repo is rejected before staging", func(t *testing.T) {
		dir := t.TempDir()
		if out, err := exec.Command("git", "init", "-b", "main", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init root: %v (%s)", err, out)
		}
		nested := filepath.Join(dir, "vendor", "child")
		if out, err := exec.Command("git", "init", "-b", "main", nested).CombinedOutput(); err != nil {
			t.Fatalf("git init nested: %v (%s)", err, out)
		}

		_, err := m.InitializeRepository(ctx, project.InitializeRepositoryInput{Path: dir})
		wantCode(t, err, "PROJECT_NESTED_GIT_REPOSITORY")
		out, lsErr := exec.Command("git", "-C", dir, "ls-files", "-s").CombinedOutput()
		if lsErr != nil {
			t.Fatalf("git ls-files: %v (%s)", lsErr, out)
		}
		if strings.Contains(string(out), "160000") {
			t.Fatalf("nested repo was staged as a gitlink:\n%s", out)
		}
	})
}
func TestManager_AddValidationAndConflicts(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	_, err := m.Add(ctx, project.AddInput{Path: ""})
	wantCode(t, err, "PATH_REQUIRED")

	_, err = m.Add(ctx, project.AddInput{Path: t.TempDir()}) // exists but not a git repo
	wantCode(t, err, "NOT_A_GIT_REPO")

	configureCommitter(t)
	parent := filepath.Join(t.TempDir(), "parent")
	gitRepoWithCommitNoOrigin(t, parent)
	nestedPlain := filepath.Join(parent, "universe")
	if err := os.Mkdir(nestedPlain, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = m.Add(ctx, project.AddInput{Path: nestedPlain})
	wantCode(t, err, "NOT_A_GIT_REPO")

	unborn := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "main", unborn).CombinedOutput(); err != nil {
		t.Fatalf("git init unborn: %v (%s)", err, out)
	}
	_, err = m.Add(ctx, project.AddInput{Path: unborn})
	wantCode(t, err, "PROJECT_UNBORN")
	// An embedded ".." passes the id pattern but would yield an invalid git
	// branch (ao/a..b-1) at spawn time; reject it up front as a clear 400.
	_, err = m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("a..b")})
	wantCode(t, err, "INVALID_PROJECT_ID")

	repoA, repoB := gitRepo(t), gitRepo(t)
	if _, err := m.Add(ctx, project.AddInput{Path: repoA, ProjectID: ptr("shared")}); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	_, err = m.Add(ctx, project.AddInput{Path: repoA, ProjectID: ptr("other")})
	wantCode(t, err, "PATH_ALREADY_REGISTERED")

	_, err = m.Add(ctx, project.AddInput{Path: repoB, ProjectID: ptr("shared")})
	wantCode(t, err, "ID_ALREADY_REGISTERED")
}

func TestManager_AddRejectsEquivalentRepositoryPaths(t *testing.T) {
	for _, aliasFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("alias-first=%v", aliasFirst), func(t *testing.T) {
			m := newManager(t)
			repo := gitRepo(t)
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(repo, alias); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			first, second := repo, alias
			if aliasFirst {
				first, second = alias, repo
			}
			if _, err := m.Add(context.Background(), project.AddInput{Path: first, ProjectID: ptr("original")}); err != nil {
				t.Fatal(err)
			}
			_, err := m.Add(context.Background(), project.AddInput{Path: second, ProjectID: ptr("duplicate")})
			wantCode(t, err, "PATH_ALREADY_REGISTERED")
			var conflict *apierr.Error
			if !errors.As(err, &conflict) || conflict.Details["existingProjectId"] != "original" {
				t.Fatalf("conflict = %#v", err)
			}
			rows, err := m.List(context.Background())
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%v err=%v", rows, err)
			}
		})
	}
}

func TestManager_AddAllocatesUniqueIDForCollidingDerivedIDs(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.NewWithDeps(project.Deps{Store: store})

	t.Run("double collision", func(t *testing.T) {
		dir1 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "work", "app"))
		dir2 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "clients", "app"))

		p1, err := m.Add(ctx, project.AddInput{Path: dir1})
		if err != nil {
			t.Fatalf("first add: %v", err)
		}
		if p1.ID != "app" {
			t.Fatalf("first project id = %q, want %q", p1.ID, "app")
		}

		p2, err := m.Add(ctx, project.AddInput{Path: dir2})
		if err != nil {
			t.Fatalf("second add: %v", err)
		}
		if p2.ID != "app1" {
			t.Fatalf("second project id = %q, want %q", p2.ID, "app1")
		}
		if p2.Name != "app" {
			t.Fatalf("second project name = %q, want %q", p2.Name, "app")
		}
	})

	t.Run("triple collision", func(t *testing.T) {
		dir1 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "work", "svc"))
		dir2 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "clients", "svc"))
		dir3 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "extra", "svc"))

		p1, err := m.Add(ctx, project.AddInput{Path: dir1})
		if err != nil {
			t.Fatalf("first add: %v", err)
		}
		if p1.ID != "svc" {
			t.Fatalf("first project id = %q, want %q", p1.ID, "svc")
		}

		p2, err := m.Add(ctx, project.AddInput{Path: dir2})
		if err != nil {
			t.Fatalf("second add: %v", err)
		}
		if p2.ID != "svc1" {
			t.Fatalf("second project id = %q, want %q", p2.ID, "svc1")
		}

		p3, err := m.Add(ctx, project.AddInput{Path: dir3})
		if err != nil {
			t.Fatalf("third add: %v", err)
		}
		if p3.ID != "svc2" {
			t.Fatalf("third project id = %q, want %q", p3.ID, "svc2")
		}
		if p3.Name != "svc" {
			t.Fatalf("third project name = %q, want %q", p3.Name, "svc")
		}
	})

	t.Run("archived suffix reuse", func(t *testing.T) {
		dir1 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "work", "tool"))
		dir2 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "clients", "tool"))

		p1, err := m.Add(ctx, project.AddInput{Path: dir1})
		if err != nil {
			t.Fatalf("first add: %v", err)
		}
		if p1.ID != "tool" {
			t.Fatalf("first project id = %q, want %q", p1.ID, "tool")
		}

		p2, err := m.Add(ctx, project.AddInput{Path: dir2})
		if err != nil {
			t.Fatalf("second add: %v", err)
		}
		if p2.ID != "tool1" {
			t.Fatalf("second project id = %q, want %q", p2.ID, "tool1")
		}

		if ok, err := store.ArchiveProject(ctx, string(p2.ID), time.Now()); err != nil || !ok {
			t.Fatalf("archive project: ok=%v err=%v", ok, err)
		}

		dir3 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "extra", "tool"))
		p3, err := m.Add(ctx, project.AddInput{Path: dir3})
		if err != nil {
			t.Fatalf("third add after archive: %v", err)
		}
		if p3.ID != "tool2" {
			t.Fatalf("third project id = %q, want %q (should skip archived tool1)", p3.ID, "tool2")
		}
	})

	t.Run("explicit id collision still errors", func(t *testing.T) {
		dir1 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "work", "foo"))
		dir2 := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "clients", "foo"))

		if _, err := m.Add(ctx, project.AddInput{Path: dir1, ProjectID: ptr("mine")}); err != nil {
			t.Fatalf("first add: %v", err)
		}
		_, err := m.Add(ctx, project.AddInput{Path: dir2, ProjectID: ptr("mine")})
		wantCode(t, err, "ID_ALREADY_REGISTERED")
	})
}

// gitRepoWithOrigin creates a real git repo with an `origin` remote pointing
// at `originURL`. Used to assert project.Add captures the origin at add time.
func gitRepoWithOrigin(t *testing.T, originURL string) string {
	t.Helper()
	dir := gitRepo(t)
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", originURL).CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v (%s)", err, out)
	}
	return dir
}

func TestManager_AddPopulatesRepoOriginURL(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T) string
		wantURL string
	}{
		{
			name:    "git repo with origin populates url",
			setup:   func(t *testing.T) string { return gitRepoWithOrigin(t, "https://github.com/o/r.git") },
			wantURL: "https://github.com/o/r.git",
		},
		{
			name:    "git repo without origin leaves url empty",
			setup:   func(t *testing.T) string { return gitRepo(t) },
			wantURL: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newManager(t)
			path := tc.setup(t)
			proj, err := m.Add(ctx, project.AddInput{Path: path, ProjectID: ptr("p")})
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			if proj.Repo != tc.wantURL {
				t.Fatalf("Repo = %q, want %q", proj.Repo, tc.wantURL)
			}
		})
	}
}

func TestManager_GetUpdateRemoveErrors(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	_, err := m.Get(ctx, "nope")
	wantCode(t, err, "PROJECT_NOT_FOUND")

	_, err = m.Get(ctx, domain.ProjectID("bad/id"))
	wantCode(t, err, "INVALID_PROJECT_ID")

	_, err = m.Remove(ctx, "nope")
	wantCode(t, err, "PROJECT_NOT_FOUND")

	repo := gitRepo(t)
	if _, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("p")}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func configureCommitter(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "AO Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "ao@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "AO Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "ao@example.com")
}

func gitRepoWithCommit(t *testing.T, dir string) string {
	t.Helper()
	return gitRepoWithCommitWithOrigin(t, dir, "https://example.com/"+filepath.Base(dir)+".git")
}

func gitRepoWithCommitNoOrigin(t *testing.T, dir string) string {
	t.Helper()
	return gitRepoWithCommitWithOrigin(t, dir, "")
}

func gitRepoWithCommitWithOrigin(t *testing.T, dir, origin string) string {
	t.Helper()
	if out, err := exec.Command("git", "init", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	if origin != "" {
		if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
			t.Fatalf("git remote add: %v (%s)", err, out)
		}
	}
	return dir
}

func TestManager_AddWorkspaceInsideAncestorRepo(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)
	ancestor := t.TempDir()
	if out, err := exec.Command("git", "-C", ancestor, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init ancestor: %v (%s)", err, out)
	}
	commitEmpty(t, ancestor)
	parent := filepath.Join(ancestor, "universe")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRepoWithCommit(t, filepath.Join(parent, "api"))
	gitRepoWithCommit(t, filepath.Join(parent, "web"))
	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("ws-ancestor"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace inside ancestor: %v", err)
	}
	if proj.Kind != domain.ProjectKindWorkspace {
		t.Fatalf("Kind = %q, want workspace", proj.Kind)
	}
	if proj.DefaultBranch != domain.DefaultBranchName {
		t.Fatalf("AO-initialized workspace root default = %q, want %q", proj.DefaultBranch, domain.DefaultBranchName)
	}
	if len(proj.WorkspaceRepos) != 2 {
		t.Fatalf("expected 2 child repos, got %d", len(proj.WorkspaceRepos))
	}
	// Verify that a .git directory was created in the workspace parent (nested)
	if _, err := os.Stat(filepath.Join(parent, ".git")); err != nil {
		t.Fatalf("expected .git to exist in workspace parent (nested), but it does not: %v", err)
	}
	got, err := m.Get(ctx, "ws-ancestor")
	if err != nil {
		t.Fatalf("Get workspace: %v", err)
	}
	if got.Project == nil || got.Project.Kind != domain.ProjectKindWorkspace || len(got.Project.WorkspaceRepos) != 2 {
		t.Fatalf("Get = %#v", got)
	}
}

func TestManager_AddWorkspaceInitializesPlainParent(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.New(store)
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRepoWithCommit(t, filepath.Join(parent, "cli"))
	apiRepo := gitRepoWithCommit(t, filepath.Join(parent, "api"))
	if out, err := exec.Command("git", "-C", apiRepo, "branch", "-m", "main", "dev").CombinedOutput(); err != nil {
		t.Fatalf("rename api branch: %v (%s)", err, out)
	}

	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("ws"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace: %v", err)
	}
	if proj.Kind != domain.ProjectKindWorkspace {
		t.Fatalf("Kind = %q, want workspace", proj.Kind)
	}
	if len(proj.WorkspaceRepos) != 2 || proj.WorkspaceRepos[0].Name != "api" || proj.WorkspaceRepos[1].Name != "cli" {
		t.Fatalf("WorkspaceRepos = %#v", proj.WorkspaceRepos)
	}
	registeredRepos, err := store.ListWorkspaceRepos(ctx, "ws")
	if err != nil {
		t.Fatalf("list workspace repos: %v", err)
	}
	if len(registeredRepos) != 2 {
		t.Fatalf("registered workspace repos = %#v, want 2", registeredRepos)
	}
	if registeredRepos[0].Name != "api" || registeredRepos[0].DefaultBranch != "" {
		t.Fatalf("registered api repo = %#v, want no guessed local default branch", registeredRepos[0])
	}
	if registeredRepos[1].Name != "cli" || registeredRepos[1].DefaultBranch != "" {
		t.Fatalf("registered cli repo = %#v, want no guessed local default branch", registeredRepos[1])
	}
	ignored, err := os.ReadFile(filepath.Join(parent, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/api/", "/cli/", "node_modules/", "dist/"} {
		if !strings.Contains(string(ignored), want) {
			t.Fatalf(".gitignore missing %q:\n%s", want, ignored)
		}
	}
	out, err := exec.Command("git", "-C", parent, "ls-files", "-s").CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files: %v (%s)", err, out)
	}
	if strings.Contains(string(out), "160000") {
		t.Fatalf("parent tracked a child repo as a gitlink:\n%s", out)
	}
	if !strings.Contains(string(out), "package.json") || !strings.Contains(string(out), ".gitignore") {
		t.Fatalf("parent root files not committed:\n%s", out)
	}

	got, err := m.Get(ctx, "ws")
	if err != nil {
		t.Fatalf("Get workspace: %v", err)
	}
	if got.Project == nil || got.Project.Kind != domain.ProjectKindWorkspace || len(got.Project.WorkspaceRepos) != 2 {
		t.Fatalf("Get = %#v", got)
	}
}

func TestManager_AddWorkspaceDoesNotRequireChildDefaultCheckout(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)
	parent := t.TempDir()
	child := gitRepoWithCommit(t, filepath.Join(parent, "api"))
	if out, err := exec.Command("git", "-C", child, "checkout", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("detach child HEAD: %v (%s)", err, out)
	}

	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("ws-detached"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace with detached child: %v", err)
	}
	if len(proj.WorkspaceRepos) != 1 || proj.WorkspaceRepos[0].GitStatus != string(domain.GitStatusReady) {
		t.Fatalf("WorkspaceRepos = %#v, want detached child ready", proj.WorkspaceRepos)
	}
}

func TestManager_AddWorkspaceAcceptsUnbornChildAsNeedsInit(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)
	parent := t.TempDir()
	child := filepath.Join(parent, "cli")
	if out, err := exec.Command("git", "init", "-b", "main", child).CombinedOutput(); err != nil {
		t.Fatalf("git init child: %v (%s)", err, out)
	}
	gitRepoWithCommit(t, filepath.Join(parent, "ready"))

	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("ws"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace with unborn child: %v", err)
	}
	if len(proj.WorkspaceRepos) != 2 {
		t.Fatalf("expected 2 child repos, got %d", len(proj.WorkspaceRepos))
	}
	var foundNeedsInit, foundReady bool
	for _, r := range proj.WorkspaceRepos {
		switch r.GitStatus {
		case string(domain.GitStatusNeedsInit):
			foundNeedsInit = true
		case string(domain.GitStatusReady):
			foundReady = true
		}
	}
	if !foundNeedsInit {
		t.Fatalf("expected a needs_init child")
	}
	if !foundReady {
		t.Fatalf("expected a ready child")
	}
}

func TestManager_AddWorkspaceAcceptsChildWithoutOriginAsNeedsInit(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)
	parent := t.TempDir()
	gitRepoWithCommitNoOrigin(t, filepath.Join(parent, "api"))
	gitRepoWithCommit(t, filepath.Join(parent, "ready"))

	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("ws"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace with originless child: %v", err)
	}
	if len(proj.WorkspaceRepos) != 2 {
		t.Fatalf("expected 2 child repos, got %d", len(proj.WorkspaceRepos))
	}
	var needsInitRepo *project.WorkspaceRepo
	for i := range proj.WorkspaceRepos {
		if proj.WorkspaceRepos[i].GitStatus == string(domain.GitStatusNeedsInit) {
			needsInitRepo = &proj.WorkspaceRepos[i]
			break
		}
	}
	if needsInitRepo == nil {
		t.Fatalf("expected a needs_init child")
	}
	if needsInitRepo.Repo != "" {
		t.Fatalf("Repo = %q, want empty", needsInitRepo.Repo)
	}
}

// TestManager_AddWorkspaceAdoptsExistingParent verifies that when the parent is
// already a git repo, Add commits only .gitignore changes, preserves the prior
// commit history, and registers the children.
func TestManager_AddWorkspaceAdoptsExistingParent(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)

	parent := t.TempDir()
	// Parent is an existing repo with one commit and a pre-existing .gitignore.
	gitRepoWithCommit(t, parent)
	if err := os.WriteFile(filepath.Join(parent, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if out, err := exec.Command("git", "-C", parent, "add", ".gitignore").CombinedOutput(); err != nil {
		t.Fatalf("git add .gitignore: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", parent, "commit", "-m", "add gitignore").CombinedOutput(); err != nil {
		t.Fatalf("git commit .gitignore: %v (%s)", err, out)
	}

	gitRepoWithCommit(t, filepath.Join(parent, "api"))
	gitRepoWithCommit(t, filepath.Join(parent, "backend"))

	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("ws2"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace: %v", err)
	}
	if proj.Kind != domain.ProjectKindWorkspace {
		t.Fatalf("Kind = %q, want workspace", proj.Kind)
	}
	if len(proj.WorkspaceRepos) != 2 {
		t.Fatalf("WorkspaceRepos = %#v, want 2", proj.WorkspaceRepos)
	}

	// Original .gitignore line must be preserved.
	ignored, err := os.ReadFile(filepath.Join(parent, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(ignored), "*.log") {
		t.Fatalf(".gitignore lost original line; got:\n%s", ignored)
	}
	for _, want := range []string{"/api/", "/backend/"} {
		if !strings.Contains(string(ignored), want) {
			t.Fatalf(".gitignore missing %q:\n%s", want, ignored)
		}
	}

	// Exactly one new commit must have been created, touching only .gitignore.
	logOut, err := exec.Command("git", "-C", parent, "log", "--format=%s").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v (%s)", err, logOut)
	}
	lines := strings.Split(strings.TrimSpace(string(logOut)), "\n")
	// Expect: AO workspace commit + "add gitignore" + "initial" = 3 commits.
	if len(lines) != 3 {
		t.Fatalf("expected 3 commits, got %d:\n%s", len(lines), logOut)
	}

	showOut, err := exec.Command("git", "-C", parent, "show", "--name-only", "--format=", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show HEAD: %v (%s)", err, showOut)
	}
	files := strings.TrimSpace(string(showOut))
	if files != ".gitignore" {
		t.Fatalf("HEAD touched files other than .gitignore: %q", files)
	}
}

// TestManager_AddWorkspaceRejectsWorktreeParent verifies that a linked worktree
// of another repository is rejected as a workspace parent.
func TestManager_AddWorkspaceRejectsWorktreeParent(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)

	base := t.TempDir()
	mainRepo := filepath.Join(base, "main")
	wtDir := filepath.Join(base, "wt")
	gitRepoWithCommit(t, mainRepo)

	// Create a linked worktree from the main repo.
	if out, err := exec.Command("git", "-C", mainRepo, "worktree", "add", wtDir).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v (%s)", err, out)
	}

	// Put a committed child repo inside the worktree dir.
	gitRepoWithCommit(t, filepath.Join(wtDir, "child"))

	_, err := m.Add(ctx, project.AddInput{Path: wtDir, ProjectID: ptr("wt"), AsWorkspace: true})
	wantCode(t, err, "WORKSPACE_PARENT_IS_WORKTREE")
}

// TestManager_AddWorkspaceAdoptsSeparateGitDirParent verifies that a parent repo
// created with `git init --separate-git-dir=<elsewhere>` (whose .git is a file,
// not a dir) is correctly identified as a standalone repo and NOT rejected as a
// linked worktree. Add with AsWorkspace must succeed and register the child.
func TestManager_AddWorkspaceAdoptsSeparateGitDirParent(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)

	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	// The git directory lives outside the parent tree — this is the
	// separate-git-dir scenario. .git inside parent will be a file.
	separateGitDir := filepath.Join(base, "parent.git")
	if out, err := exec.Command("git", "init", "--separate-git-dir="+separateGitDir, "-b", "main", parent).CombinedOutput(); err != nil {
		t.Fatalf("git init --separate-git-dir: %v (%s)", err, out)
	}
	// Commit a file in the parent so the parent is a valid (non-bare) repo.
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if out, err := exec.Command("git", "-C", parent, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", parent, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}

	// Put a committed child repo inside the parent.
	gitRepoWithCommit(t, filepath.Join(parent, "svc"))

	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("sgd"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace with separate-git-dir parent: %v", err)
	}
	if proj.Kind != domain.ProjectKindWorkspace {
		t.Fatalf("Kind = %q, want workspace", proj.Kind)
	}
	if len(proj.WorkspaceRepos) != 1 || proj.WorkspaceRepos[0].Name != "svc" {
		t.Fatalf("WorkspaceRepos = %#v, want [{svc}]", proj.WorkspaceRepos)
	}
}

// TestManager_AddWorkspaceAcceptsWorktreeChildAsNeedsInit verifies that a
// child whose .git is a file (linked worktree) is accepted as needs_init.
func TestManager_AddWorkspaceAcceptsWorktreeChildAsNeedsInit(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)

	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	// An external standalone repo used as the source for a worktree child.
	extRepo := filepath.Join(base, "ext")
	gitRepoWithCommit(t, extRepo)

	// child is a linked worktree of extRepo, placed inside parent.
	child := filepath.Join(parent, "child")
	if out, err := exec.Command("git", "-C", extRepo, "worktree", "add", child).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add child: %v (%s)", err, out)
	}
	gitRepoWithCommit(t, filepath.Join(parent, "ready"))

	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("wc"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace with worktree child: %v", err)
	}
	if len(proj.WorkspaceRepos) != 2 {
		t.Fatalf("expected 2 child repos, got %d", len(proj.WorkspaceRepos))
	}
	var foundNeedsInit bool
	for _, r := range proj.WorkspaceRepos {
		if r.GitStatus == string(domain.GitStatusNeedsInit) {
			foundNeedsInit = true
			break
		}
	}
	if !foundNeedsInit {
		t.Fatalf("expected a needs_init child")
	}
}

// TestManager_AddWorkspaceRejectsReservedChildName verifies that a child repo
// named __root__ is rejected to avoid a PK collision in session_worktrees.
func TestManager_AddWorkspaceRejectsReservedChildName(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)

	parent := t.TempDir()
	gitRepoWithCommit(t, filepath.Join(parent, domain.RootWorkspaceRepoName))

	_, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("res"), AsWorkspace: true})
	wantCode(t, err, "WORKSPACE_CHILD_RESERVED_NAME")
}

// TestManager_AddWorkspaceNonGitChildWithNestedRepo verifies that a non-git
// child folder containing a nested git repo is imported as needs_init (not
// rejected as a gitlink). The child is gitignored in the parent, so the
// nested repo is never staged by git add -A and guardNoGitlinks never fires.
func TestManager_AddWorkspaceNonGitChildWithNestedRepo(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)

	parent := t.TempDir()
	// One direct committed child repo — valid on its own.
	gitRepoWithCommit(t, filepath.Join(parent, "app"))
	// A non-git child folder containing a nested git repo at depth 2.
	// detectWorkspaceChildren registers packages/ as needs_init; it gets
	// gitignored in the parent, so packages/foo is never staged as a gitlink.
	pkgs := filepath.Join(parent, "packages")
	if err := os.MkdirAll(pkgs, 0o755); err != nil {
		t.Fatalf("mkdir packages: %v", err)
	}
	gitRepoWithCommit(t, filepath.Join(pkgs, "foo"))

	proj, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("rbt"), AsWorkspace: true})
	if err != nil {
		t.Fatalf("Add workspace with non-git child: %v", err)
	}
	if len(proj.WorkspaceRepos) != 2 {
		t.Fatalf("expected 2 child repos (app + packages), got %d", len(proj.WorkspaceRepos))
	}
	var pkgsRepo *project.WorkspaceRepo
	for i := range proj.WorkspaceRepos {
		if proj.WorkspaceRepos[i].Name == "packages" {
			pkgsRepo = &proj.WorkspaceRepos[i]
		}
	}
	if pkgsRepo == nil {
		t.Fatalf("packages not in WorkspaceRepos = %#v", proj.WorkspaceRepos)
	}
	if pkgsRepo.GitStatus != string(domain.GitStatusNeedsInit) {
		t.Fatalf("packages GitStatus = %q, want %q", pkgsRepo.GitStatus, domain.GitStatusNeedsInit)
	}

	// Parent git repo and .gitignore must exist (no rollback).
	if _, statErr := os.Lstat(filepath.Join(parent, ".git")); statErr != nil {
		t.Fatalf(".git missing after successful init: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(parent, ".gitignore")); statErr != nil {
		t.Fatalf(".gitignore missing after successful init: %v", statErr)
	}
}

// TestManager_AddWorkspaceConcurrentSamePath verifies that two goroutines racing
// on the same parent path result in exactly one success and one PATH_ALREADY_REGISTERED
// error. The -race detector will catch any unsynchronised access.
func TestManager_AddWorkspaceConcurrentSamePath(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)

	parent := t.TempDir()
	gitRepoWithCommit(t, filepath.Join(parent, "svc"))

	type result struct {
		proj project.Project
		err  error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range results {
		go func() {
			defer wg.Done()
			p, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("con"), AsWorkspace: true})
			results[i] = result{p, err}
		}()
	}
	wg.Wait()

	successes, failures := 0, 0
	for _, r := range results {
		if r.err == nil {
			successes++
		} else {
			failures++
			wantCode(t, r.err, "PATH_ALREADY_REGISTERED")
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("expected 1 success and 1 PATH_ALREADY_REGISTERED; got successes=%d failures=%d (errors: %v %v)",
			successes, failures, results[0].err, results[1].err)
	}
}

// TestManager_AddWorkspaceRejectsBareParent verifies that a bare git repository
// is rejected as a workspace parent before any mutation occurs.
func TestManager_AddWorkspaceRejectsBareParent(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m := newManager(t)

	base := t.TempDir()
	bareParent := filepath.Join(base, "bare.git")
	if out, err := exec.Command("git", "init", "--bare", bareParent).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v (%s)", err, out)
	}

	// Place a committed child repo inside the bare parent directory.
	gitRepoWithCommit(t, filepath.Join(bareParent, "child"))

	_, err := m.Add(ctx, project.AddInput{Path: bareParent, ProjectID: ptr("bare"), AsWorkspace: true})
	wantCode(t, err, "WORKSPACE_PARENT_BARE")
}

func TestManager_CanonicalRepositoryConfigPersistence(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := gitRepo(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://gitlab.com/alice/repo").CombinedOutput(); err != nil {
		t.Fatalf("origin: %v %s", err, out)
	}
	// An unrelated remote must never enter durable claim trust automatically.
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "upstream", "https://gitlab.com/unrelated/repo").CombinedOutput(); err != nil {
		t.Fatalf("upstream: %v %s", err, out)
	}
	added, err := m.Add(ctx, project.AddInput{Path: repo, ProjectID: ptr("fork")})
	if err != nil {
		t.Fatal(err)
	}
	if added.Config != nil && added.Config.CanonicalRepoURL != "" {
		t.Fatal("remote inferred as trusted upstream")
	}
	cfg := domain.ProjectConfig{CanonicalRepoURL: "https://gitlab.com/group/subgroup/repo", DefaultBranch: "main"}
	if _, err := m.SetConfig(ctx, "fork", project.SetConfigInput{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(ctx, "fork")
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Config == nil || got.Project.Config.CanonicalRepoURL != cfg.CanonicalRepoURL {
		t.Fatalf("stored config = %+v", got.Project.Config)
	}
	for _, target := range []string{"https://github.com/group/repo", "https://gitlab.example.com/group/subgroup/repo"} {
		bad := domain.ProjectConfig{CanonicalRepoURL: target}
		if _, err := m.SetConfig(ctx, "fork", project.SetConfigInput{Config: bad}); err == nil {
			t.Fatalf("SetConfig accepted %s", target)
		}
		if _, err := m.UpdateSettings(ctx, "fork", project.UpdateSettingsInput{DisplayName: "Fork", Config: bad}); err == nil {
			t.Fatalf("UpdateSettings accepted %s", target)
		}
	}
	got, err = m.Get(ctx, "fork")
	if err != nil || got.Project.Config == nil || got.Project.Config.CanonicalRepoURL != cfg.CanonicalRepoURL {
		t.Fatalf("invalid write changed config: %+v %v", got.Project.Config, err)
	}
	if _, err := m.SetConfig(ctx, "fork", project.SetConfigInput{}); err != nil {
		t.Fatal(err)
	}
	got, err = m.Get(ctx, "fork")
	if err != nil || (got.Project.Config != nil && got.Project.Config.CanonicalRepoURL != "") {
		t.Fatalf("clear: %+v %v", got.Project.Config, err)
	}
}
