package importer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
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
	wantActions(t, result.Root.RequiredActions, []string{GitPreparationActionInit, GitPreparationActionCommit, GitPreparationActionSetRemote})
	if _, err := os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Validate mutated git metadata: %v", err)
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
	wantActions(t, result.Root.RequiredActions, []string{GitPreparationActionCommit, GitPreparationActionSetRemote})
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

func TestPrepareGitRejectsMalformedRemoteBeforeMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	svc := New(Deps{Store: newFakeStore()})

	_, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            root,
		ApprovedActions: []string{GitPreparationActionInit, GitPreparationActionCommit, GitPreparationActionSetRemote},
		RemoteURL:       "not a remote",
	})
	wantCode(t, err, "INVALID_GIT_URL")
	if _, err := os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PrepareGit mutated before validating remote: %v", err)
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
			GitPreparationActionSetRemote,
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
		GitPreparationActionSetRemote,
		GitPreparationActionSetRemote,
		GitPreparationActionSetRemote,
	})
	if out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").CombinedOutput(); err != nil || string(out) != "https://example.invalid/repo.git\n" {
		t.Fatalf("origin = %q, %v", out, err)
	}
}

func TestPrepareGitDoesNotOverwriteExistingOrigin(t *testing.T) {
	ctx := context.Background()
	repo := gitRepoWithOrigin(t)
	svc := New(Deps{Store: newFakeStore()})

	result, err := svc.PrepareGit(ctx, GitPreparationInput{
		ImportKind:      ImportKindProject,
		Path:            repo,
		ApprovedActions: []string{GitPreparationActionSetRemote},
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
