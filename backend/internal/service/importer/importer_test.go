package importer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/gitdefault"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type fakeStore struct {
	projects map[string]domain.ProjectRecord
}

func newFakeStore() *fakeStore { return &fakeStore{projects: map[string]domain.ProjectRecord{}} }
func (f *fakeStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	r, ok := f.projects[id]
	return r, ok, nil
}
func (f *fakeStore) UpsertProject(_ context.Context, r domain.ProjectRecord) error {
	f.projects[r.ID] = r
	return nil
}

func writeLegacyRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agent-orchestrator")
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "projects:\n  alpha:\n    path: /repos/alpha\n    name: Alpha\n"
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStatus_NoLegacyData(t *testing.T) {
	svc := New(Deps{Store: newFakeStore(), Root: filepath.Join(t.TempDir(), "nope")})
	st, err := svc.Status(context.Background())
	if err != nil || st.Available {
		t.Fatalf("want unavailable; got %+v err=%v", st, err)
	}
}

func TestStatus_LegacyPresentStaysAvailableAfterImport(t *testing.T) {
	root := writeLegacyRoot(t)
	svc := New(Deps{Store: newFakeStore(), Root: root})
	st, err := svc.Status(context.Background())
	if err != nil || !st.Available || st.LegacyRoot != root {
		t.Fatalf("want available at %q; got %+v err=%v", root, st, err)
	}
	if _, err := svc.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Availability is physical (legacy data still on disk), so it stays true; the
	// app marker is what stops the prompt after a completed import.
	st, _ = svc.Status(context.Background())
	if !st.Available {
		t.Fatal("availability must remain true after import (marker governs prompting)")
	}
}

func TestRun_ImportsProjects(t *testing.T) {
	root := writeLegacyRoot(t)
	svc := New(Deps{Store: newFakeStore(), Root: root})
	rep, err := svc.Run(context.Background())
	if err != nil || rep.ProjectsImported != 1 {
		t.Fatalf("projectsImported=%d err=%v", rep.ProjectsImported, err)
	}
}

func TestNew_DefaultsRoot(t *testing.T) {
	if New(Deps{Store: newFakeStore()}).root == "" {
		t.Fatal("empty Root should fall back to the default legacy root")
	}
}

func TestValidateProjectImportReadyRepositoryContinues(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithOrigin(t)
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: repo})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepContinue {
		t.Fatalf("result = %#v, want valid continue", result)
	}
	if !result.Root.IsRepo || !result.Root.HasCommit || !result.Root.HasOrigin || len(result.Root.RequiredActions) != 0 {
		t.Fatalf("root status = %#v, want ready git repo", result.Root)
	}
	if result.Warning != "" {
		t.Fatalf("warning = %q, want none", result.Warning)
	}
}

func TestValidateProjectImportPlainFolderNeedsPreparation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepPrepareGit {
		t.Fatalf("result = %#v, want prepare_git", result)
	}
	wantActions(t, result.Root.RequiredActions, []string{GitPreparationActionInit, GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository})
	if _, err := os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Validate mutated git metadata: %v", err)
	}
}

func TestPrepareGitProjectImportCommitsExistingFolderContents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("existing project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            root,
		ApprovedActions: []string{GitPreparationActionInit, GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository},
		RemoteURL:       "https://example.invalid/existing.git",
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	if result.Validation.NextStep != ImportNextStepContinue {
		t.Fatalf("validation = %#v, want continue", result.Validation)
	}
	if out, err := exec.Command("git", "-C", root, "show", "HEAD:README.md").CombinedOutput(); err != nil || string(out) != "existing project\n" {
		t.Fatalf("committed README = %q, %v", out, err)
	}
}

func TestValidateProjectImportMissingPathReturnsBlockingError(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing")
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: missing})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.IsValid || result.NextStep != ImportNextStepError {
		t.Fatalf("result = %#v, want invalid error", result)
	}
	wantActions(t, result.BlockingErrors, []string{"INVALID_PATH"})
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Validate created missing path: %v", err)
	}
}

func TestValidateProjectImportRejectsFilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(context.Background(), ImportValidationInput{ImportKind: ImportKindProject, Path: path})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.IsValid || result.NextStep != ImportNextStepError {
		t.Fatalf("result = %#v, want path-not-directory error", result)
	}
	wantActions(t, result.BlockingErrors, []string{"PATH_NOT_DIRECTORY"})
}

func TestValidateProjectImportRejectsAOStatePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	statePath := filepath.Join(home, ".ao", "data")
	if err := os.MkdirAll(statePath, 0o750); err != nil {
		t.Fatal(err)
	}

	svc := New(Deps{Store: newFakeStore()})
	result, err := svc.Validate(context.Background(), ImportValidationInput{ImportKind: ImportKindProject, Path: statePath})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.IsValid || result.NextStep != ImportNextStepError {
		t.Fatalf("result = %#v, want unsafe-path error", result)
	}
	wantActions(t, result.BlockingErrors, []string{"IMPORT_PATH_UNSAFE"})
}

func TestValidateProjectImportUnbornRepositoryNeedsCommitAndRemote(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init unborn: %v (%s)", err, out)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: repo})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepPrepareGit || !result.Root.IsRepo || result.Root.HasCommit {
		t.Fatalf("result = %#v, want unborn repo needing preparation", result)
	}
	wantActions(t, result.Root.RequiredActions, []string{GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository})
}

func TestValidateProjectImportParentWithChildReposChoosesImportKind(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	gitRepoWithCommitNoOrigin(t, child)
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepChooseImportKind || len(result.ChildRepos) != 1 {
		t.Fatalf("result = %#v, want child repo choice", result)
	}
	if result.ChildRepos[0].RepoPath != child || !result.ChildRepos[0].IsRepo {
		t.Fatalf("childRepos = %#v, want direct child repo", result.ChildRepos)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Validate mutated parent git metadata: %v", err)
	}
	if result.Warning != "" {
		t.Fatalf("warning = %q, want none when import kind must still be chosen", result.Warning)
	}
}

func TestValidateProjectImportRejectsDetachedHead(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithOrigin(t)
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo, "checkout", "--detach", strings.TrimSpace(string(out))).CombinedOutput(); err != nil {
		t.Fatalf("checkout --detach: %v (%s)", err, out)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: repo})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.IsValid || result.NextStep != ImportNextStepError || len(result.BlockingErrors) != 1 || result.BlockingErrors[0] != "DETACHED_HEAD" {
		t.Fatalf("result = %#v, want detached-head error", result)
	}
}

func TestValidateProjectImportRejectsBareRepository(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "init", "--bare", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v (%s)", err, out)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: repo})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.IsValid || result.NextStep != ImportNextStepError {
		t.Fatalf("result = %#v, want bare-repository error", result)
	}
	wantActions(t, result.BlockingErrors, []string{"BARE_REPOSITORY"})
}

func TestValidateProjectImportRootWithOriginAndChildReposWarnsProjectImport(t *testing.T) {
	ctx := context.Background()
	root := gitRepoWithOrigin(t)
	child := filepath.Join(root, "child")
	gitRepoWithCommitNoOrigin(t, child)
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepContinue {
		t.Fatalf("result = %#v, want valid continue", result)
	}
	if len(result.ChildRepos) != 1 || result.ChildRepos[0].RepoPath != child {
		t.Fatalf("childRepos = %#v, want direct child repo", result.ChildRepos)
	}
	if result.Warning == "" {
		t.Fatal("warning = empty, want project import warning")
	}
	if got, want := result.Warning, "Selected folder has direct child repositories, but because the root repository already has an origin remote AO will import it as a project, not a workspace."; got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
}

func TestValidateProjectImportRootWithoutOriginAndChildReposDoesNotWarn(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	gitRepoWithCommitNoOrigin(t, root)
	child := filepath.Join(root, "child")
	gitRepoWithCommitNoOrigin(t, child)
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepPrepareGit {
		t.Fatalf("result = %#v, want prepare_git", result)
	}
	if result.Warning != "" {
		t.Fatalf("warning = %q, want none without root origin", result.Warning)
	}
}

func TestPrepareGitRequiresApprovalBeforeMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	svc := New(Deps{Store: newFakeStore()})

	_, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            root,
		ApprovedActions: []string{GitPreparationActionInit},
		RemoteURL:       "https://example.invalid/repo.git",
	})
	wantCode(t, err, "IMPORT_ACTION_APPROVAL_REQUIRED")
	if _, err := os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PrepareGit mutated without full approval: %v", err)
	}
}

func TestPrepareGitRejectsInvalidRemoteBeforeMutation(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		code      string
	}{
		{name: "malformed", remoteURL: "not a remote", code: "INVALID_GIT_URL"},
		{name: "hostless HTTPS", remoteURL: "https:///owner/repo.git", code: "INVALID_GIT_URL"},
		{name: "hostless SSH", remoteURL: "ssh:///owner/repo.git", code: "INVALID_GIT_URL"},
		{name: "hostless Git", remoteURL: "git:///owner/repo.git", code: "INVALID_GIT_URL"},
		{name: "HTTPS username", remoteURL: "https://token@github.com/acme/repository.git", code: "GIT_URL_CONTAINS_CREDENTIALS"},
		{name: "HTTPS password", remoteURL: "https://user:secret@github.com/acme/repository.git", code: "GIT_URL_CONTAINS_CREDENTIALS"},
		{name: "SSH password", remoteURL: "ssh://git:secret@github.com/acme/repository.git", code: "GIT_URL_CONTAINS_CREDENTIALS"},
		{name: "Git userinfo", remoteURL: "git://token@git.example.test/acme/repository.git", code: "GIT_URL_CONTAINS_CREDENTIALS"},
		{name: "file userinfo", remoteURL: "file://token@localhost/tmp/acme/repository.git", code: "GIT_URL_CONTAINS_CREDENTIALS"},
		{name: "access token query", remoteURL: "https://github.com/acme/repository.git?access_token=secret", code: "GIT_URL_CONTAINS_CREDENTIALS"},
		{name: "generic token query", remoteURL: "https://github.com/acme/repository.git?token=secret", code: "GIT_URL_CONTAINS_CREDENTIALS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			svc := New(Deps{Store: newFakeStore()})

			_, err := svc.PrepareGit(context.Background(), GitPreparationInput{
				ImportKind:      ImportKindProject,
				Path:            root,
				ApprovedActions: []string{GitPreparationActionInit, GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository},
				RemoteURL:       tc.remoteURL,
			})
			wantCode(t, err, tc.code)
			if _, err := os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("PrepareGit mutated before rejecting remote: %v", err)
			}
		})
	}
}

func TestValidImportRemoteURLPreservesSupportedCredentialFreeForms(t *testing.T) {
	for _, remoteURL := range []string{
		"https://github.com/acme/repository.git",
		"http://git.example.test/acme/repository.git",
		"ssh://git@github.com/acme/repository.git",
		"git://github.com/acme/repository.git",
		"git@github.com:acme/repository.git",
		"file:///tmp/acme/repository.git",
	} {
		t.Run(remoteURL, func(t *testing.T) {
			if err := validateImportRemoteURL(remoteURL); err != nil {
				t.Fatalf("validateImportRemoteURL(%q) = %v, want nil", remoteURL, err)
			}
		})
	}
}

func TestPrepareGitRunsApprovedMissingActionsInOrder(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind: ImportKindProject,
		Path:       root,
		ApprovedActions: []string{
			GitPreparationActionInit,
			GitPreparationActionCommit,
			GitPreparationActionCreateRemoteRepository,
		},
		RemoteURL: "https://example.invalid/repo.git",
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	if result.Validation.NextStep != ImportNextStepContinue || !result.Validation.Root.HasCommit || !result.Validation.Root.HasOrigin {
		t.Fatalf("validation = %#v, want ready repository", result.Validation)
	}
	wantEventActions(t, result.Events, []string{
		GitPreparationActionInit,
		GitPreparationActionInit,
		GitPreparationActionInit,
		GitPreparationActionCommit,
		GitPreparationActionCommit,
		GitPreparationActionCommit,
		GitPreparationActionCreateRemoteRepository,
		GitPreparationActionCreateRemoteRepository,
		GitPreparationActionCreateRemoteRepository,
	})
	if out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").CombinedOutput(); err != nil || string(out) != "https://example.invalid/repo.git\n" {
		t.Fatalf("origin = %q, %v", out, err)
	}
}

func TestPrepareGitProjectImportCreatesPublicGitHubRepository(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "main", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", root, "-c", "user.email=ao@example.com", "-c", "user.name=AO Test", "commit", "--allow-empty", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	var gotDir string
	var gotArgs []string
	originalGhOutput := importGhOutputFunc
	importGhOutputFunc = func(_ context.Context, dir string, args ...string) (string, error) {
		gotDir = dir
		gotArgs = append([]string(nil), args...)
		return "", nil
	}
	var gitCalls [][]string
	originalGitOutput := importGitOutputFunc
	importGitOutputFunc = func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "push" {
			gitCalls = append(gitCalls, append([]string(nil), args...))
			setTestUpstream(t, dir)
			return "", nil
		}
		if isImportGitMutation(args) {
			gitCalls = append(gitCalls, append([]string(nil), args...))
		}
		return originalGitOutput(ctx, dir, args...)
	}
	t.Cleanup(func() { importGhOutputFunc = originalGhOutput })
	t.Cleanup(func() { importGitOutputFunc = originalGitOutput })
	svc := New(Deps{Store: newFakeStore()})

	private := false
	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            root,
		ApprovedActions: []string{GitPreparationActionCreateRemoteRepository},
		GitHubRepository: &GitHubRepositoryPreparation{
			Owner:   "octo",
			Name:    "project",
			Private: &private,
		},
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	if result.Validation.NextStep != ImportNextStepContinue || !result.Validation.Root.HasOrigin {
		t.Fatalf("validation = %#v, want ready repository", result.Validation)
	}
	if gotDir != root {
		t.Fatalf("gh dir = %q, want %q", gotDir, root)
	}
	wantArgs := []string{"repo", "create", "octo/project", "--public"}
	wantActions(t, gotArgs, wantArgs)
	wantGitCalls := [][]string{
		{"remote", "add", "origin", "https://github.com/octo/project.git"},
		{"config", "--local", importGitHubRepositoryConfigKey, "octo/project"},
		{"push", "-u", "origin", "HEAD"},
	}
	if !reflect.DeepEqual(gitCalls, wantGitCalls) {
		t.Fatalf("git calls = %#v, want %#v", gitCalls, wantGitCalls)
	}
}

func TestPrepareGitStepwiseCompletesInThreeCalls(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	svc := New(Deps{Store: newFakeStore()})
	request := GitPreparationInput{
		ImportKind: ImportKindProject,
		Path:       root,
		ApprovedActions: []string{
			GitPreparationActionInit,
			GitPreparationActionCommit,
			GitPreparationActionCreateRemoteRepository,
		},
		RemoteURL: "https://example.invalid/repo.git",
		Stepwise:  true,
	}

	for index, wantAction := range []string{GitPreparationActionInit, GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository} {
		result, err := svc.PrepareGit(ctx, request)
		if err != nil {
			t.Fatalf("PrepareGit call %d: %v", index+1, err)
		}
		wantEventActions(t, result.Events, []string{wantAction, wantAction, wantAction})
		if index == 0 {
			if !result.Validation.Root.IsRepo || result.Validation.Root.HasCommit || result.Validation.Root.HasOrigin {
				t.Fatalf("first validation = %#v, want only git init complete", result.Validation)
			}
			wantActions(t, result.Validation.Root.RequiredActions, []string{GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository})
		}
		if index < 2 && result.Validation.NextStep != ImportNextStepPrepareGit {
			t.Fatalf("call %d validation = %#v, want prepare_git", index+1, result.Validation)
		}
		if index == 2 && result.Validation.NextStep != ImportNextStepContinue {
			t.Fatalf("final validation = %#v, want continue", result.Validation)
		}
	}
}

func TestPrepareGitStepwiseValidatesFullApprovalBeforeMutation(t *testing.T) {
	root := t.TempDir()
	svc := New(Deps{Store: newFakeStore()})

	_, err := svc.PrepareGit(context.Background(), GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            root,
		ApprovedActions: []string{GitPreparationActionInit},
		RemoteURL:       "https://example.invalid/repo.git",
		Stepwise:        true,
	})
	if err == nil {
		t.Fatal("PrepareGit succeeded without approval for the full plan")
	}
	if _, statErr := os.Stat(filepath.Join(root, ".git")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("PrepareGit mutated before validating the full plan: %v", statErr)
	}
}

func TestPrepareGitStepwiseRetryResumesAtMissingAction(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(Deps{Store: newFakeStore()})
	request := GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            repo,
		ApprovedActions: []string{GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository},
		RemoteURL:       "https://example.invalid/retry.git",
		Stepwise:        true,
	}

	failed, err := svc.PrepareGit(ctx, request)
	if err != nil {
		t.Fatalf("PrepareGit failed attempt: %v", err)
	}
	if len(failed.Events) != 3 || failed.Events[2].Action != GitPreparationActionCommit || failed.Events[2].State != GitPreparationEventError {
		t.Fatalf("events = %#v, want commit failure", failed.Events)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	retried, err := svc.PrepareGit(ctx, request)
	if err != nil {
		t.Fatalf("PrepareGit retry: %v", err)
	}
	wantEventActions(t, retried.Events, []string{
		GitPreparationActionCommit,
		GitPreparationActionCommit,
		GitPreparationActionCommit,
	})
	if retried.Validation.Root.HasOrigin {
		t.Fatalf("retry ran more than the failed commit step: %#v", retried.Validation)
	}
}

func TestPrepareGitDoesNotOverwriteExistingOrigin(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithOrigin(t)
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            repo,
		ApprovedActions: []string{GitPreparationActionCreateRemoteRepository},
		RemoteURL:       "https://example.invalid/new.git",
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	if len(result.Events) != 0 {
		t.Fatalf("events = %#v, want no missing actions", result.Events)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").CombinedOutput(); err != nil || string(out) != "https://example.invalid/original.git\n" {
		t.Fatalf("origin = %q, %v", out, err)
	}
}

func TestPrepareGitAddsOnlyMissingOriginToCommittedRepository(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithCommitWithOrigin(t, t.TempDir(), "")
	headBefore, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v (%s)", err, headBefore)
	}
	svc := New(Deps{Store: newFakeStore()})

	before, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: repo})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	wantActions(t, before.Root.RequiredActions, []string{GitPreparationActionCreateRemoteRepository})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            repo,
		ApprovedActions: []string{GitPreparationActionCreateRemoteRepository},
		RemoteURL:       "https://example.invalid/missing-origin.git",
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	wantEventActions(t, result.Events, []string{
		GitPreparationActionCreateRemoteRepository,
		GitPreparationActionCreateRemoteRepository,
		GitPreparationActionCreateRemoteRepository,
	})
	headAfter, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD after preparation: %v (%s)", err, headAfter)
	}
	if string(headAfter) != string(headBefore) {
		t.Fatalf("HEAD changed from %q to %q while adding origin", headBefore, headAfter)
	}
}

func TestPrepareGitSetsURLOnExistingOriginWithoutURL(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithCommitWithOrigin(t, t.TempDir(), "")
	if out, err := exec.Command("git", "-C", repo, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*").CombinedOutput(); err != nil {
		t.Fatalf("create origin without URL: %v (%s)", err, out)
	}
	svc := New(Deps{Store: newFakeStore()})

	before, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: repo})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	wantActions(t, before.Root.RequiredActions, []string{GitPreparationActionCreateRemoteRepository})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            repo,
		ApprovedActions: []string{GitPreparationActionCreateRemoteRepository},
		RemoteURL:       "https://example.invalid/repaired.git",
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	if result.Validation.NextStep != ImportNextStepContinue {
		t.Fatalf("validation = %#v, want continue", result.Validation)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").CombinedOutput(); err != nil || string(out) != "https://example.invalid/repaired.git\n" {
		t.Fatalf("origin = %q, %v", out, err)
	}
}

func TestPrepareGitGitHubRepositoryRepairsExistingOriginWithoutURL(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithCommitWithOrigin(t, t.TempDir(), "")
	if out, err := exec.Command("git", "-C", repo, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*").CombinedOutput(); err != nil {
		t.Fatalf("create origin without URL: %v (%s)", err, out)
	}
	var ghArgs []string
	originalGhOutput := importGhOutputFunc
	importGhOutputFunc = func(_ context.Context, _ string, args ...string) (string, error) {
		ghArgs = append([]string(nil), args...)
		return "", nil
	}
	var gitCalls [][]string
	originalGitOutput := importGitOutputFunc
	importGitOutputFunc = func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "push" {
			gitCalls = append(gitCalls, append([]string(nil), args...))
			setTestUpstream(t, dir)
			return "", nil
		}
		if isImportGitMutation(args) {
			gitCalls = append(gitCalls, append([]string(nil), args...))
		}
		return originalGitOutput(ctx, dir, args...)
	}
	t.Cleanup(func() { importGhOutputFunc = originalGhOutput })
	t.Cleanup(func() { importGitOutputFunc = originalGitOutput })
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            repo,
		ApprovedActions: []string{GitPreparationActionCreateRemoteRepository},
		GitHubRepository: &GitHubRepositoryPreparation{
			Owner: "octo",
			Name:  "repaired",
		},
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	if result.Validation.NextStep != ImportNextStepContinue {
		t.Fatalf("validation = %#v, want continue", result.Validation)
	}
	wantActions(t, ghArgs, []string{"repo", "create", "octo/repaired", "--private"})
	wantGitCalls := [][]string{
		{"remote", "set-url", "origin", "https://github.com/octo/repaired.git"},
		{"config", "--local", importGitHubRepositoryConfigKey, "octo/repaired"},
		{"push", "-u", "origin", "HEAD"},
	}
	if !reflect.DeepEqual(gitCalls, wantGitCalls) {
		t.Fatalf("git calls = %#v, want %#v", gitCalls, wantGitCalls)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").CombinedOutput(); err != nil || string(out) != "https://github.com/octo/repaired.git\n" {
		t.Fatalf("origin = %q, %v", out, err)
	}
}

func TestPrepareGitGitHubRepositoryRetriesPushAfterPartialSuccess(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithCommitWithOrigin(t, t.TempDir(), "")
	var ghCalls [][]string
	originalGhOutput := importGhOutputFunc
	importGhOutputFunc = func(_ context.Context, _ string, args ...string) (string, error) {
		ghCalls = append(ghCalls, append([]string(nil), args...))
		return "", nil
	}
	var gitCalls [][]string
	pushAttempts := 0
	originalGitOutput := importGitOutputFunc
	importGitOutputFunc = func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "push" {
			gitCalls = append(gitCalls, append([]string(nil), args...))
			pushAttempts++
			if pushAttempts == 1 {
				return "", errors.New("network failed")
			}
			setTestUpstream(t, dir)
			return "", nil
		}
		if isImportGitMutation(args) {
			gitCalls = append(gitCalls, append([]string(nil), args...))
		}
		return originalGitOutput(ctx, dir, args...)
	}
	t.Cleanup(func() { importGhOutputFunc = originalGhOutput })
	t.Cleanup(func() { importGitOutputFunc = originalGitOutput })
	svc := New(Deps{Store: newFakeStore()})
	request := GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            repo,
		ApprovedActions: []string{GitPreparationActionCreateRemoteRepository},
		GitHubRepository: &GitHubRepositoryPreparation{
			Owner: "octo",
			Name:  "partial",
		},
	}

	first, err := svc.PrepareGit(ctx, request)
	if err != nil {
		t.Fatalf("first PrepareGit: %v", err)
	}
	wantEventActions(t, first.Events, []string{
		GitPreparationActionCreateRemoteRepository,
		GitPreparationActionCreateRemoteRepository,
		GitPreparationActionCreateRemoteRepository,
	})
	if first.Events[2].State != GitPreparationEventError {
		t.Fatalf("first final event = %#v, want push error", first.Events[2])
	}
	if first.Validation.NextStep != ImportNextStepPrepareGit {
		t.Fatalf("first validation = %#v, want retryable prepare_git", first.Validation)
	}
	wantActions(t, first.Validation.Root.RequiredActions, []string{GitPreparationActionCreateRemoteRepository})

	second, err := svc.PrepareGit(ctx, request)
	if err != nil {
		t.Fatalf("second PrepareGit: %v", err)
	}
	if second.Validation.NextStep != ImportNextStepContinue {
		t.Fatalf("second validation = %#v, want continue", second.Validation)
	}
	if len(ghCalls) != 1 {
		t.Fatalf("gh calls = %#v, want repo creation only once", ghCalls)
	}
	wantActions(t, ghCalls[0], []string{"repo", "create", "octo/partial", "--private"})
	wantGitCalls := [][]string{
		{"remote", "add", "origin", "https://github.com/octo/partial.git"},
		{"config", "--local", importGitHubRepositoryConfigKey, "octo/partial"},
		{"push", "-u", "origin", "HEAD"},
		{"config", "--local", importGitHubRepositoryConfigKey, "octo/partial"},
		{"push", "-u", "origin", "HEAD"},
	}
	if !reflect.DeepEqual(gitCalls, wantGitCalls) {
		t.Fatalf("git calls = %#v, want %#v", gitCalls, wantGitCalls)
	}
}

func TestPrepareGitProjectImportCanRetryAfterCommitFailure(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(Deps{Store: newFakeStore()})
	request := GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            repo,
		ApprovedActions: []string{GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository},
		RemoteURL:       "https://example.invalid/retry.git",
	}

	failed, err := svc.PrepareGit(ctx, request)
	if err != nil {
		t.Fatalf("PrepareGit failed attempt: %v", err)
	}
	if len(failed.Events) != 3 || failed.Events[2].Action != GitPreparationActionCommit || failed.Events[2].State != GitPreparationEventError {
		t.Fatalf("events = %#v, want commit failure", failed.Events)
	}
	wantActions(t, failed.Validation.Root.RequiredActions, []string{GitPreparationActionCommit, GitPreparationActionCreateRemoteRepository})
	if importRemoteExists(repo, "origin") {
		t.Fatal("origin added after commit failure")
	}

	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	retried, err := svc.PrepareGit(ctx, request)
	if err != nil {
		t.Fatalf("PrepareGit retry: %v", err)
	}
	if retried.Validation.NextStep != ImportNextStepContinue || !retried.Validation.Root.HasCommit || !retried.Validation.Root.HasOrigin {
		t.Fatalf("validation = %#v, want ready repository after retry", retried.Validation)
	}
}

func TestValidateWorkspaceImportReadyChildrenContinue(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	gitRepoWithCommitWithOrigin(t, filepath.Join(root, "api"), "https://example.invalid/api.git")
	gitRepoWithCommitWithOrigin(t, filepath.Join(root, "web"), "https://example.invalid/web.git")
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindWorkspace, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepContinue || len(result.ChildRepos) != 2 {
		t.Fatalf("result = %#v, want ready workspace", result)
	}
	for _, child := range result.ChildRepos {
		if !child.IsRepo || !child.HasCommit || !child.HasOrigin || len(child.RequiredActions) != 0 {
			t.Fatalf("child = %#v, want ready repo", child)
		}
	}
}

func TestValidateWorkspaceImportOfGitRepoRequiresProjectChoice(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithOrigin(t)
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindWorkspace, Path: repo})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepChooseImportKind || result.Warning == "" {
		t.Fatalf("result = %#v, want project import choice", result)
	}
}

func TestValidateWorkspaceImportOfRootRepoWithoutOriginUsesChildRepos(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "workspace")
	gitRepoWithCommitNoOrigin(t, root)
	child := filepath.Join(root, "child")
	gitRepoWithCommitWithOrigin(t, child, "https://example.invalid/child.git")
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindWorkspace, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepContinue || result.Warning != "" {
		t.Fatalf("result = %#v, want workspace import to continue", result)
	}
	if len(result.ChildRepos) != 1 || result.ChildRepos[0].RepoPath != child || !result.ChildRepos[0].HasOrigin {
		t.Fatalf("childRepos = %#v, want ready child repository", result.ChildRepos)
	}
}

func TestValidateWorkspaceImportReportsBareChildRepository(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v (%s)", err, out)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindWorkspace, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.IsValid || result.NextStep != ImportNextStepError || len(result.ChildRepos) != 1 {
		t.Fatalf("result = %#v, want blocked bare child", result)
	}
	if got := result.ChildRepos[0].BlockingErrors; len(got) != 1 || got[0] != "BARE_REPOSITORY" {
		t.Fatalf("blocking errors = %#v, want bare repository", got)
	}
}

func TestValidateWorkspaceImportPartialChildrenExposeMissingActions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	unborn := filepath.Join(root, "unborn")
	if out, err := exec.Command("git", "init", "-b", "main", unborn).CombinedOutput(); err != nil {
		t.Fatalf("git init unborn: %v (%s)", err, out)
	}
	noRemote := gitRepoWithCommitWithOrigin(t, filepath.Join(root, "no-remote"), "")
	plain := filepath.Join(root, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindWorkspace, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepPrepareGit || len(result.ChildRepos) != 2 {
		t.Fatalf("result = %#v, want workspace needing preparation", result)
	}
	byPath := childStatusByPath(result.ChildRepos)
	wantActions(t, byPath[unborn].RequiredActions, []string{GitPreparationActionCommit, GitPreparationActionSetRemote})
	wantActions(t, byPath[noRemote].RequiredActions, []string{GitPreparationActionSetRemote})
	if _, ok := byPath[plain]; ok {
		t.Fatalf("childRepos = %#v, plain folder must not be surfaced as a workspace repo", result.ChildRepos)
	}
}

func TestValidateWorkspaceImportRequiresInitializedChildRepo(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindWorkspace, Path: root})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.IsValid || result.NextStep != ImportNextStepError {
		t.Fatalf("result = %#v, want invalid workspace child repo requirement", result)
	}
	wantActions(t, result.BlockingErrors, []string{"WORKSPACE_CHILD_REPO_REQUIRED"})
}

func TestPrepareGitWorkspaceRunsPerRepositoryEvents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	noRemote := gitRepoWithCommitWithOrigin(t, filepath.Join(root, "no-remote"), "")
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind: ImportKindWorkspace,
		Path:       root,
		Repositories: []GitRepositoryPreparationInput{
			{
				RepoPath:        noRemote,
				ApprovedActions: []string{GitPreparationActionSetRemote},
				RemoteURL:       "https://example.invalid/no-remote.git",
			},
		},
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	if result.Validation.NextStep != ImportNextStepContinue {
		t.Fatalf("validation = %#v, want continue", result.Validation)
	}
	if len(result.Events) != 3 {
		t.Fatalf("events = %#v, want 3 state events", result.Events)
	}
	if result.Events[0].RepoPath != noRemote || result.Events[0].Action != GitPreparationActionSetRemote {
		t.Fatalf("first event = %#v, want noRemote set_remote", result.Events[0])
	}
	if result.Events[2].RepoPath != noRemote || result.Events[2].Action != GitPreparationActionSetRemote {
		t.Fatalf("last event = %#v, want noRemote set_remote complete", result.Events[2])
	}
}

func TestPrepareGitWorkspaceCanInitializeAnApprovedPlainChild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	plain := filepath.Join(root, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind: ImportKindWorkspace,
		Path:       root,
		Repositories: []GitRepositoryPreparationInput{{
			RepoPath: plain,
			ApprovedActions: []string{
				GitPreparationActionInit,
				GitPreparationActionCommit,
				GitPreparationActionSetRemote,
			},
			RemoteURL: "https://example.invalid/plain.git",
		}},
	})
	if err != nil {
		t.Fatalf("PrepareGit: %v", err)
	}
	if result.Validation.NextStep != ImportNextStepContinue {
		t.Fatalf("validation = %#v, want continue", result.Validation)
	}
	wantEventActions(t, result.Events, []string{
		GitPreparationActionInit, GitPreparationActionInit, GitPreparationActionInit,
		GitPreparationActionCommit, GitPreparationActionCommit, GitPreparationActionCommit,
		GitPreparationActionSetRemote, GitPreparationActionSetRemote, GitPreparationActionSetRemote,
	})
	status := inspectImportRepo(ctx, plain)
	if !status.IsRepo || !status.HasCommit || !status.HasOrigin {
		t.Fatalf("plain child status = %#v, want ready repository", status)
	}
}

func gitRepoWithOrigin(t *testing.T) string {
	t.Helper()
	return gitRepoWithCommitWithOrigin(t, t.TempDir(), "https://example.invalid/original.git")
}

func gitRepoWithCommitNoOrigin(t *testing.T, dir string) {
	t.Helper()
	gitRepoWithCommitWithOrigin(t, dir, "")
}

func gitRepoWithCommitWithOrigin(t *testing.T, dir, origin string) string {
	t.Helper()
	if out, err := exec.Command("git", "init", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "-c", "user.email=ao@example.com", "-c", "user.name=AO Test", "commit", "--allow-empty", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	if origin != "" {
		if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
			t.Fatalf("git remote add: %v (%s)", err, out)
		}
	}
	return dir
}

func setTestUpstream(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "config", "branch.main.remote", "origin").CombinedOutput(); err != nil {
		t.Fatalf("set branch remote: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "branch.main.merge", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("set branch merge: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "update-ref", "refs/remotes/origin/main", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("set remote-tracking ref: %v (%s)", err, out)
	}
}

func isImportGitMutation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return args[0] == "remote" || (args[0] == "config" && len(args) > 1 && args[1] == "--local")
}

func wantActions(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("actions = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("actions = %#v, want %#v", got, want)
		}
	}
}

func wantEventActions(t *testing.T, got []GitPreparationEvent, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %#v, want actions %#v", got, want)
	}
	for i := range want {
		if got[i].Action != want[i] {
			t.Fatalf("events = %#v, want actions %#v", got, want)
		}
	}
}

func childStatusByPath(children []RepoGitStatus) map[string]RepoGitStatus {
	out := make(map[string]RepoGitStatus, len(children))
	for _, child := range children {
		out[child.RepoPath] = child
	}
	return out
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *apierr.Error", err)
	}
	if apiErr.Code != code {
		t.Fatalf("code = %q, want %q", apiErr.Code, code)
	}
}

func TestPrepareGitEmptyCloneRecordsInitialBranchForWorkspaceResolution(t *testing.T) {
	for _, branch := range []string{"main", "trunk"} {
		t.Run(branch, func(t *testing.T) {
			ctx := context.Background()
			origin := filepath.Join(t.TempDir(), "empty.git")
			repo := filepath.Join(t.TempDir(), "clone")
			for _, args := range [][]string{
				{"init", "--bare", "-b", branch, origin},
				{"clone", origin, repo},
				{"-C", repo, "symbolic-ref", "HEAD", "refs/heads/" + branch},
			} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v (%s)", args, err, out)
				}
			}
			svc := New(Deps{Store: newFakeStore()})
			result, err := svc.PrepareGit(ctx, GitPreparationInput{
				ImportKind: ImportKindProject, Path: repo,
				ApprovedActions:  []string{GitPreparationActionCommit},
				InitialCommitMsg: "Start my project",
			})
			if err != nil || result.Validation.NextStep != ImportNextStepContinue {
				t.Fatalf("PrepareGit: %#v, %v", result, err)
			}
			resolved, err := gitdefault.New("", nil).Resolve(ctx, ctx, repo)
			if err != nil || resolved.Branch != branch || resolved.Source != gitdefault.SourceAOInitialized {
				t.Fatalf("resolve prepared clone: %#v, %v", resolved, err)
			}
		})
	}
}
