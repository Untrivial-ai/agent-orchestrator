package gitworktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCommandArgs(t *testing.T) {
	repo := "/repo"
	path := "/managed/proj/sess"
	branch := "feature/test"

	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"check ref", checkRefFormatBranchArgs(repo, branch), []string{"-C", repo, "check-ref-format", "--branch", branch}},
		{"rev parse", revParseVerifyArgs(repo, "origin/main"), []string{"-C", repo, "rev-parse", "--verify", "--quiet", "origin/main"}},
		{"add existing", worktreeAddBranchArgs(repo, path, branch, false), []string{"-C", repo, "worktree", "add", path, branch}},
		{"add new", worktreeAddNewBranchArgs(repo, branch, path, "origin/main", false), []string{"-C", repo, "worktree", "add", "-b", branch, path, "origin/main"}},
		// --force is git's documented override for a registration whose directory
		// is gone, and is passed exactly once: `-f -f` would also override an
		// operator's `git worktree lock`.
		{"add existing forced", worktreeAddBranchArgs(repo, path, branch, true), []string{"-C", repo, "worktree", "add", "--force", path, branch}},
		{"add new forced", worktreeAddNewBranchArgs(repo, branch, path, "origin/main", true), []string{"-C", repo, "worktree", "add", "--force", "-b", branch, path, "origin/main"}},
		// No --force: a dirty worktree must cause `git worktree remove` to fail so
		// the post-prune safety check surfaces the refusal instead of deleting
		// uncommitted agent work (review item RA).
		{"remove", worktreeRemoveArgs(repo, path), []string{"-C", repo, "worktree", "remove", path}},
		{"prune", worktreePruneArgs(repo), []string{"-C", repo, "worktree", "prune"}},
		{"list", worktreeListPorcelainArgs(repo), []string{"-C", repo, "worktree", "list", "--porcelain"}},
		{"status", statusPorcelainArgs(path), []string{"-C", path, "status", "--porcelain"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("args = %#v, want %#v", tc.got, tc.want)
			}
		})
	}
}

func TestConfiguredBaseRefCandidates(t *testing.T) {
	got := configuredBaseRefCandidates("main")
	want := []string{"origin/main", "refs/heads/main"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}

	got = configuredBaseRefCandidates("upstream/main")
	want = []string{"upstream/main"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("qualified candidates = %#v, want %#v", got, want)
	}
}

func TestParseWorktreePorcelain(t *testing.T) {
	input := strings.Join([]string{
		"worktree /repo",
		"HEAD abc123",
		"branch refs/heads/main",
		"",
		"worktree /managed/proj/sess1",
		"HEAD def456",
		"branch refs/heads/feature/test",
		"",
		"worktree /managed/proj/sess2",
		"HEAD 789abc",
		"detached",
		"",
		"worktree /bare",
		"bare",
		"",
	}, "\n")

	recs, err := parseWorktreePorcelain(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("len = %d, want 4: %#v", len(recs), recs)
	}
	if recs[1].Path != "/managed/proj/sess1" || recs[1].Branch != "feature/test" {
		t.Fatalf("normal record = %#v", recs[1])
	}
	if !recs[2].Detached || recs[2].Branch != "" {
		t.Fatalf("detached record = %#v", recs[2])
	}
	if !recs[3].Bare {
		t.Fatalf("bare record = %#v", recs[3])
	}
}

func TestManagedPathSafety(t *testing.T) {
	root := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": root}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path, err := ws.managedPath(ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess"})
	if err != nil {
		t.Fatalf("managed path: %v", err)
	}
	if want := filepath.Join(ws.managedRoot, "proj", "sess"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := ws.validateManagedPath(filepath.Join(root, "..", "outside")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("outside error = %v, want ErrUnsafePath", err)
	}
	if _, err := ws.validateManagedPath("relative/path"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("relative error = %v, want ErrUnsafePath", err)
	}
}

func TestOrchestratorManagedPath(t *testing.T) {
	root := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": root}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	t.Run("explicit prefix", func(t *testing.T) {
		cfg := ports.WorkspaceConfig{
			ProjectID:     "proj",
			SessionID:     "proj-1",
			Kind:          domain.KindOrchestrator,
			SessionPrefix: "ao-agents",
		}
		path, err := ws.managedPath(cfg)
		if err != nil {
			t.Fatalf("managed path: %v", err)
		}
		want := filepath.Join(ws.managedRoot, "proj", "orchestrator", "ao-agents-orchestrator")
		if path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
	})

	t.Run("prefix derived from project id", func(t *testing.T) {
		cfg := ports.WorkspaceConfig{
			ProjectID: "longprojectid123",
			SessionID: "longprojectid123-1",
			Kind:      domain.KindOrchestrator,
		}
		path, err := ws.managedPath(cfg)
		if err != nil {
			t.Fatalf("managed path: %v", err)
		}
		want := filepath.Join(ws.managedRoot, "longprojectid123", "orchestrator", "longprojecti-orchestrator")
		if path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
	})

	t.Run("short project id used as prefix", func(t *testing.T) {
		cfg := ports.WorkspaceConfig{
			ProjectID: "proj",
			SessionID: "proj-1",
			Kind:      domain.KindOrchestrator,
		}
		path, err := ws.managedPath(cfg)
		if err != nil {
			t.Fatalf("managed path: %v", err)
		}
		want := filepath.Join(ws.managedRoot, "proj", "orchestrator", "proj-orchestrator")
		if path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
	})
}

func TestCreateReusesRegisteredWorktreeAtExpectedPath(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "orchestrator", "proj-orchestrator")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("create registered worktree path: %v", err)
	}
	cfg := ports.WorkspaceConfig{
		ProjectID:     "proj",
		SessionID:     "proj-1",
		Kind:          domain.KindOrchestrator,
		SessionPrefix: "proj",
		Branch:        "ao/proj-orchestrator",
		BaseBranch:    "main",
	}
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "check-ref-format"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + path + "\nbranch refs/heads/ao/proj-orchestrator\n"), nil
		case strings.Contains(joined, "rev-parse --verify --quiet"):
			return []byte("sha\n"), nil
		default:
			t.Fatalf("unexpected git invocation: %v", args)
			return nil, nil
		}
	}

	info, err := ws.Create(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Path != path || info.Branch != "ao/proj-orchestrator" {
		t.Fatalf("info = %#v, want path %q branch ao/proj-orchestrator", info, path)
	}
}

// TestCreateRecreatesMissingRegisteredWorktreeWithForce covers Fix 3
// (issue #2775): a registration can survive in `git worktree list` after its
// directory is gone (a prior daemon or an out-of-band `rm -rf`). Create must
// not hand that dead path to the runtime; it must materialize a fresh worktree
// at the same path via `worktree add --force`, git's own override for a
// missing-but-registered path, without first removing or pruning anything.
func TestCreateRecreatesMissingRegisteredWorktreeWithForce(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Deliberately not created on disk: the registration is stale.
	path := filepath.Join(ws.managedRoot, "proj", "orchestrator", "proj-orchestrator")
	cfg := ports.WorkspaceConfig{
		ProjectID:     "proj",
		SessionID:     "proj-1",
		Kind:          domain.KindOrchestrator,
		SessionPrefix: "proj",
		Branch:        "ao/proj-orchestrator",
		BaseBranch:    "main",
	}

	// The stale registration is never cleared, so it stays in every listing:
	// `worktree add --force` re-registers the path itself.
	var calls []string
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.Contains(joined, "check-ref-format"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + path + "\nbranch refs/heads/ao/proj-orchestrator\n"), nil
		case strings.Contains(joined, "rev-parse --verify --quiet refs/heads/ao/proj-orchestrator"):
			return nil, nil
		case strings.Contains(joined, "rev-parse --verify --quiet"):
			return []byte("sha\n"), nil
		case strings.Contains(joined, "worktree add --force "+path+" ao/proj-orchestrator"):
			return nil, nil
		default:
			t.Fatalf("unexpected git invocation: %v", args)
			return nil, nil
		}
	}

	info, err := ws.Create(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Path != path || info.Branch != cfg.Branch {
		t.Fatalf("info = %#v, want path %q branch %q", info, path, cfg.Branch)
	}
	got := strings.Join(calls, "\n")
	if !strings.Contains(got, "worktree add --force "+path+" "+cfg.Branch) {
		t.Fatalf("Create did not recreate the missing worktree with --force:\n%s", got)
	}
	assertNoDestructiveRegistrationCleanup(t, "Create", got)
}

// assertNoDestructiveRegistrationCleanup pins the two cleanup mechanisms
// stale-registration recovery must not use (PR #3098 review, illegalcall):
// the repo-wide `git worktree prune`, which also drops sibling sessions'
// registrations, and `git worktree remove --force`, which is check-then-delete
// against an earlier os.Stat and deletes a live worktree, uncommitted agent work
// included, if the directory reappears in between.
func assertNoDestructiveRegistrationCleanup(t *testing.T, op, calls string) {
	t.Helper()
	if strings.Contains(calls, "worktree prune") {
		t.Fatalf("%s used repo-wide worktree prune to recover a stale registration:\n%s", op, calls)
	}
	if strings.Contains(calls, "worktree remove") {
		t.Fatalf("%s used worktree remove to recover a stale registration:\n%s", op, calls)
	}
}

// TestRestoreRecreatesMissingRegisteredWorktreeWithForce is the Restore
// counterpart: session_manager.RestoreAll relies on workspace.Restore to
// re-materialize a worktree whose directory disappeared, but Restore only
// exercised that path when the git registration was ALSO gone. The observed
// #2775 case (session agent-orchestrator-78) had a registration and DB row
// that survived a directory deletion, so Restore returned a handle to a
// missing directory and the tmux launch command's `cd <path> || exit` guard
// exited instantly with no diagnostic.
func TestRestoreRecreatesMissingRegisteredWorktreeWithForce(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "orchestrator", "proj-orchestrator")
	cfg := ports.WorkspaceConfig{
		ProjectID:     "proj",
		SessionID:     "proj-1",
		Kind:          domain.KindOrchestrator,
		SessionPrefix: "proj",
		Branch:        "ao/proj-orchestrator",
		BaseBranch:    "main",
		Path:          path,
	}

	var calls []string
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.Contains(joined, "check-ref-format"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + path + "\nbranch refs/heads/ao/proj-orchestrator\n"), nil
		case strings.Contains(joined, "rev-parse --verify --quiet refs/heads/ao/proj-orchestrator"):
			return nil, nil
		case strings.Contains(joined, "rev-parse --verify --quiet"):
			return []byte("sha\n"), nil
		case strings.Contains(joined, "worktree add --force "+path+" ao/proj-orchestrator"):
			return nil, nil
		default:
			t.Fatalf("unexpected git invocation: %v", args)
			return nil, nil
		}
	}

	info, err := ws.Restore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if info.Path != path || info.Branch != cfg.Branch {
		t.Fatalf("info = %#v, want path %q branch %q", info, path, cfg.Branch)
	}
	got := strings.Join(calls, "\n")
	if !strings.Contains(got, "worktree add --force "+path+" "+cfg.Branch) {
		t.Fatalf("Restore did not recreate the missing worktree with --force:\n%s", got)
	}
	assertNoDestructiveRegistrationCleanup(t, "Restore", got)
}

// TestRestoreRecreatesOnRegisteredBranchNotCfgBranch is the regression test
// for the real #2775 case: session agent-orchestrator-78 had its worktree
// registered on a child branch (ao/agent-orchestrator-78/gh-pages-landing),
// not the root branch AO would pass as cfg.Branch. When the directory is
// missing and Restore falls through to recreate the worktree, it must
// recreate it on the registration's OWN branch, not cfg.Branch: otherwise a
// session on a child branch is silently checked out on root instead, which
// looks to the agent like its work vanished.
func TestRestoreRecreatesOnRegisteredBranchNotCfgBranch(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "orchestrator", "proj-orchestrator")
	const registeredBranch = "ao/proj-orchestrator/gh-pages-landing"
	// cfg.Branch deliberately differs from the stale registration's branch
	// (and is not a prefix of it, so a substring match on the recorded git
	// invocations cannot accidentally pass either way), mirroring how AO
	// passes the session's root branch through Restore while the on-disk
	// worktree may have been registered on a child branch.
	cfg := ports.WorkspaceConfig{
		ProjectID:     "proj",
		SessionID:     "proj-1",
		Kind:          domain.KindOrchestrator,
		SessionPrefix: "proj",
		Branch:        "ao/proj-orchestrator/root",
		BaseBranch:    "main",
		Path:          path,
	}

	var calls []string
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.Contains(joined, "check-ref-format"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + path + "\nbranch refs/heads/" + registeredBranch + "\n"), nil
		case strings.Contains(joined, "rev-parse --verify --quiet refs/heads/"+registeredBranch):
			return nil, nil
		case strings.Contains(joined, "rev-parse --verify --quiet"):
			return []byte("sha\n"), nil
		case strings.Contains(joined, "worktree add --force "+path+" "+registeredBranch):
			return nil, nil
		default:
			t.Fatalf("unexpected git invocation: %v", args)
			return nil, nil
		}
	}

	info, err := ws.Restore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if info.Branch != registeredBranch {
		t.Fatalf("info.Branch = %q, want the registered branch %q (not cfg.Branch %q)", info.Branch, registeredBranch, cfg.Branch)
	}
	got := strings.Join(calls, "\n")
	if strings.Contains(got, "worktree add --force "+path+" "+cfg.Branch) {
		t.Fatalf("Restore recreated the worktree on cfg.Branch instead of the registered branch:\n%s", got)
	}
	if !strings.Contains(got, "worktree add --force "+path+" "+registeredBranch) {
		t.Fatalf("Restore did not recreate the worktree on the registered branch %q:\n%s", registeredBranch, got)
	}
}

// workspaceProjectRepoFake wires the git calls createWorkspaceProjectRepo makes
// for branch "feature/test" based on origin/main. worktreeList is the porcelain
// `worktree list` output the pre-check reads, and addResponses answers the
// `worktree add` invocations: the first matching key wins, an entry with a nil
// error succeeds. Every recorded call lands in *calls.
func workspaceProjectRepoFake(t *testing.T, ws *Workspace, output, worktreeList string, calls *[]string, addResponses func(joined string, binary string, args []string) ([]byte, error, bool)) {
	t.Helper()
	exitErr := exitStatusOne(t)
	ws.run = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		*calls = append(*calls, joined)
		if out, err, ok := addResponses(joined, binary, args); ok {
			return out, err
		}
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte(worktreeList), nil
		case strings.Contains(joined, "symbolic-ref --quiet --short refs/remotes/origin/HEAD"):
			return []byte("origin/main\n"), nil
		case strings.Contains(joined, "rev-parse --verify --quiet origin/feature/test"):
			return nil, commandError{args: append([]string{binary}, args...), err: exitErr}
		case strings.Contains(joined, "rev-parse --verify --quiet origin/main"):
			return nil, nil
		case strings.Contains(joined, "rev-parse --verify origin/main"):
			return []byte("abc123\n"), nil
		default:
			t.Fatalf("unexpected git invocation: %v", args)
			return nil, nil
		}
	}
}

// TestCreateWorkspaceProjectRepoAddsWithForceWhenRegistrationIsStale: the
// ordinary #2775 shape (a registration at the output path whose directory is
// gone) is recognised from the `worktree list` pre-check, so the very first add
// carries git's own `--force` override. Nothing repo-wide (`worktree prune`)
// and nothing destructive (`worktree remove`) is used to clear the registration
// first, so a sibling session's registration cannot be collateral.
func TestCreateWorkspaceProjectRepoAddsWithForceWhenRegistrationIsStale(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	output := filepath.Join(root, "proj", "orchestrator", "proj-orchestrator", "api")
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// output is registered but was never created on disk: the stale shape.
	worktreeList := "worktree " + repo + "\nbranch refs/heads/main\n\nworktree " + output + "\nbranch refs/heads/stale\n\n"

	var calls []string
	addAttempts := 0
	workspaceProjectRepoFake(t, ws, output, worktreeList, &calls, func(joined, _ string, _ []string) ([]byte, error, bool) {
		if strings.Contains(joined, "worktree add --force -b feature/test "+output+" origin/main") {
			addAttempts++
			return nil, nil, true
		}
		return nil, nil, false
	})

	baseSHA, err := ws.createWorkspaceProjectRepo(context.Background(), workspaceProjectRepo{
		name:       "api",
		repoPath:   repo,
		outputPath: output,
		baseRef:    "origin/main",
	}, "feature/test")
	if err != nil {
		t.Fatalf("createWorkspaceProjectRepo: %v", err)
	}
	if baseSHA != "abc123" {
		t.Fatalf("baseSHA = %q, want abc123", baseSHA)
	}
	if addAttempts != 1 {
		t.Fatalf("add attempts = %d, want 1 (the stale registration is known before the add)", addAttempts)
	}
	assertNoDestructiveRegistrationCleanup(t, "createWorkspaceProjectRepo", strings.Join(calls, "\n"))
}

// TestCreateWorkspaceProjectRepoRecoveryRetriesOnExistingBranchForm covers the
// branch-already-created finding (PR #3098 review, illegalcall). When the
// registration goes stale only AFTER the pre-check, git rejects the plain
// `add -b` — but it has already created refs/heads/feature/test by then,
// because git creates the branch before it validates the target path. This fake
// models that side effect (the previous one modelled the failed `-b` as
// side-effect-free, which is why the bug slipped through): after the failed
// attempt, `rev-parse --verify --quiet refs/heads/feature/test` resolves.
//
// The recovery must therefore retry on the existing-branch form
// `worktree add --force <path> <branch>`. Repeating the `-b` form with --force
// fails against real git with "a branch named 'feature/test' already exists"
// (exit 255), so this test fails on the previous commit.
func TestCreateWorkspaceProjectRepoRecoveryRetriesOnExistingBranchForm(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	output := filepath.Join(root, "proj", "orchestrator", "proj-orchestrator", "api")
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// The pre-check sees no registration at output, so the first add is plain.
	worktreeList := "worktree " + repo + "\nbranch refs/heads/main\n\n"

	// What refExists sees for a branch that does not exist yet.
	absentErr := exitStatusOne(t)

	var calls []string
	addAttempts := 0
	branchCreated := false
	workspaceProjectRepoFake(t, ws, output, worktreeList, &calls, func(joined, binary string, args []string) ([]byte, error, bool) {
		switch {
		case strings.Contains(joined, "rev-parse --verify --quiet refs/heads/feature/test"):
			if !branchCreated {
				return nil, commandError{args: append([]string{binary}, args...), err: absentErr}, true
			}
			return nil, nil, true
		case strings.Contains(joined, "worktree add -b feature/test "+output+" origin/main"):
			addAttempts++
			// git creates the branch, THEN rejects the path.
			branchCreated = true
			return nil, commandError{
				args:   append([]string{binary}, args...),
				output: "Preparing worktree (new branch 'feature/test')\nfatal: '" + output + "' is a missing but already registered worktree;\nuse 'add -f' to override, or 'prune' or 'remove' to clear",
				err:    errors.New("exit status 128"),
			}, true
		case strings.Contains(joined, "worktree add --force -b feature/test "+output+" origin/main"):
			addAttempts++
			// The form real git refuses once the branch exists.
			return nil, commandError{
				args:   append([]string{binary}, args...),
				output: "Preparing worktree (new branch 'feature/test')\nfatal: a branch named 'feature/test' already exists",
				err:    errors.New("exit status 255"),
			}, true
		case strings.Contains(joined, "worktree add --force "+output+" feature/test"):
			addAttempts++
			return nil, nil, true
		}
		return nil, nil, false
	})

	baseSHA, err := ws.createWorkspaceProjectRepo(context.Background(), workspaceProjectRepo{
		name:       "api",
		repoPath:   repo,
		outputPath: output,
		baseRef:    "origin/main",
	}, "feature/test")
	if err != nil {
		t.Fatalf("createWorkspaceProjectRepo: %v", err)
	}
	if baseSHA != "abc123" {
		t.Fatalf("baseSHA = %q, want abc123", baseSHA)
	}
	if addAttempts != 2 {
		t.Fatalf("add attempts = %d, want 2", addAttempts)
	}
	got := strings.Join(calls, "\n")
	if !strings.Contains(got, "worktree add --force "+output+" feature/test") {
		t.Fatalf("recovery did not retry on the existing-branch form:\n%s", got)
	}
	if strings.Contains(got, "worktree add --force -b feature/test") {
		t.Fatalf("recovery repeated the -b form, which real git refuses once the branch exists:\n%s", got)
	}
	assertNoDestructiveRegistrationCleanup(t, "createWorkspaceProjectRepo", got)
}

// TestAddNewBranchWorktreeRecoveryFailureReportsBothErrors pins the diagnostics
// on the recovery path. When the retry fails, the original error names the
// condition recovery was FOR ("is a missing but already registered worktree"),
// not why recovery failed. The failure that matters most is a lost race:
// another restore materialized the worktree at path first, so git refuses with
// "already exists" — reporting only the original sends the reader looking for a
// stale registration that is no longer there.
func TestAddNewBranchWorktreeRecoveryFailureReportsBothErrors(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	output := filepath.Join(root, "proj", "orchestrator", "proj-orchestrator", "api")
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// No registration at output, so the first add is plain and the recovery
	// below is reached only because the path went stale after the pre-check.
	worktreeList := "worktree " + repo + "\nbranch refs/heads/main\n\n"

	absentErr := exitStatusOne(t)
	var calls []string
	branchCreated := false
	workspaceProjectRepoFake(t, ws, output, worktreeList, &calls, func(joined, binary string, args []string) ([]byte, error, bool) {
		switch {
		case strings.Contains(joined, "rev-parse --verify --quiet refs/heads/feature/test"):
			if !branchCreated {
				return nil, commandError{args: append([]string{binary}, args...), err: absentErr}, true
			}
			return nil, nil, true
		case strings.Contains(joined, "worktree add -b feature/test "+output+" origin/main"):
			branchCreated = true
			return nil, commandError{
				args:   append([]string{binary}, args...),
				output: "fatal: '" + output + "' is a missing but already registered worktree;\nuse 'add -f' to override, or 'prune' or 'remove' to clear",
				err:    errors.New("exit status 128"),
			}, true
		case strings.Contains(joined, "worktree add --force "+output+" feature/test"):
			// A concurrent restore won the race and materialized the worktree.
			return nil, commandError{
				args:   append([]string{binary}, args...),
				output: "fatal: '" + output + "' already exists",
				err:    errors.New("exit status 128"),
			}, true
		}
		return nil, nil, false
	})

	_, err = ws.createWorkspaceProjectRepo(context.Background(), workspaceProjectRepo{
		name:       "api",
		repoPath:   repo,
		outputPath: output,
		baseRef:    "origin/main",
	}, "feature/test")
	if err == nil {
		t.Fatal("createWorkspaceProjectRepo: want error when recovery fails, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error does not report why recovery failed:\n%v", err)
	}
	if !strings.Contains(err.Error(), "missing but already registered worktree") {
		t.Fatalf("error dropped the original failure:\n%v", err)
	}
}

// TestValidateConfigRejectsPathEscapingIDs covers review item RB: filepath.Join
// in managedPath cleans `..` segments before validateManagedPath sees them, so a
// session id of "../other" would stay inside managedRoot while jumping projects.
// validateConfig must reject these at the source — before any path is composed.
func TestValidateConfigRejectsPathEscapingIDs(t *testing.T) {
	root := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": root}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	cases := []struct {
		name string
		cfg  ports.WorkspaceConfig
	}{
		{"session contains slash escapes project root", ports.WorkspaceConfig{ProjectID: "proj", SessionID: "../other", Branch: "main"}},
		{"session is .. is rejected", ports.WorkspaceConfig{ProjectID: "proj", SessionID: "..", Branch: "main"}},
		{"session is . is rejected", ports.WorkspaceConfig{ProjectID: "proj", SessionID: ".", Branch: "main"}},
		{"session contains backslash is rejected", ports.WorkspaceConfig{ProjectID: "proj", SessionID: `evil\sess`, Branch: "main"}},
		{"project contains slash escapes managed root", ports.WorkspaceConfig{ProjectID: "../proj", SessionID: "sess", Branch: "main"}},
		{"project is .. is rejected", ports.WorkspaceConfig{ProjectID: "..", SessionID: "sess", Branch: "main"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Create rejects it directly through validateConfig.
			if _, err := ws.Create(context.Background(), tc.cfg); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Create err = %v, want ErrUnsafePath", err)
			}
			// Restore also goes through validateConfig, so the same guarantee holds.
			if _, err := ws.Restore(context.Background(), tc.cfg); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Restore err = %v, want ErrUnsafePath", err)
			}
		})
	}
}

// TestValidateConfigAcceptsBenignIDs is a positive guard so the rejection rule
// above does not creep into normal session/project naming. Hyphens, underscores,
// dots inside (e.g. "foo.bar"), and digits all stay allowed.
func TestValidateConfigAcceptsBenignIDs(t *testing.T) {
	cases := []ports.WorkspaceConfig{
		{ProjectID: "proj-1", SessionID: "sess_2", Branch: "main"},
		{ProjectID: "foo.bar", SessionID: "abc-42", Branch: "main"},
		{ProjectID: "p", SessionID: "..hidden", Branch: "main"}, // leading dots != ".."
	}
	for i, cfg := range cases {
		if err := validateConfig(cfg); err != nil {
			t.Errorf("case %d %+v: unexpected error: %v", i, cfg, err)
		}
	}
}

func TestRestoreRefusesNonEmptyUnregisteredPath(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ws.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("worktree " + repo + "\nbranch refs/heads/main\n"), nil
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := mkdirFile(path, "keep.txt"); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	_, err = ws.Restore(context.Background(), ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err == nil || !strings.Contains(err.Error(), "path exists and is not a registered worktree") {
		t.Fatalf("restore error = %v", err)
	}
}

func TestRestoreWithRepoPathMovesStrayPathAside(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess", "api")
	if err := mkdirFile(path, "keep.txt"); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	var addPath string
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + repo + "\nbranch refs/heads/main\n"), nil
		case strings.Contains(joined, "check-ref-format"):
			return nil, nil
		case strings.Contains(joined, "rev-parse"):
			return []byte("commit"), nil
		case strings.Contains(joined, "worktree add"):
			if len(args) >= 2 {
				addPath = args[len(args)-2]
			}
			if addPath == "" {
				t.Fatalf("could not find worktree add path in args: %v", args)
			}
			return nil, nil
		default:
			t.Fatalf("unexpected git invocation: %v", args)
			return nil, nil
		}
	}

	info, err := ws.Restore(context.Background(), ports.WorkspaceConfig{
		ProjectID:  "proj",
		SessionID:  "proj-1",
		Branch:     "ao/proj-1",
		BaseBranch: "main",
		RepoPath:   repo,
		Path:       path,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if info.Path != path || addPath != path {
		t.Fatalf("restored path=%q addPath=%q, want %q", info.Path, addPath, path)
	}
	if _, err := os.Stat(filepath.Join(path+".stray", "keep.txt")); err != nil {
		t.Fatalf("stray path was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "keep.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original path still has stray file: %v", err)
	}
}

func TestDestroyRefusesStillRegisteredPathAndPreservesDirectory(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := mkdirFile(path, "keep.txt"); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	var removeArgs []string
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree remove"):
			removeArgs = append([]string{}, args...)
			return []byte("locked"), errors.New("remove failed")
		case strings.Contains(joined, "worktree prune"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + path + "\nbranch refs/heads/feature/one\n"), nil
		default:
			return nil, nil
		}
	}
	err = ws.Destroy(context.Background(), ports.WorkspaceInfo{Path: path, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err == nil || !strings.Contains(err.Error(), "still registered") {
		t.Fatalf("destroy error = %v", err)
	}
	// The stub reports a clean `git status`, so the refusal must NOT be typed as
	// a dirty workspace — Kill/Cleanup would otherwise silently skip a refusal
	// that has a different cause (e.g. a locked worktree).
	if errors.Is(err, ports.ErrWorkspaceDirty) {
		t.Fatalf("destroy error = %v, want non-dirty refusal for clean status", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, "keep.txt")); statErr != nil {
		t.Fatalf("expected directory to be preserved: %v", statErr)
	}
	// Belt-and-braces: --force must NEVER be passed to `git worktree remove` from
	// Destroy. If it ever is, dirty worktrees would be deleted instead of routed
	// to Skipped by the Session Manager's Cleanup (review item RA).
	for _, a := range removeArgs {
		if a == "--force" || a == "-f" {
			t.Fatalf("git worktree remove was called with %q; --force must never be passed", a)
		}
	}
}

// TestDestroyClassifiesDirtyWorktree covers the typed dirty refusal: when
// `git worktree remove` fails, the path stays registered, and `git status`
// reports uncommitted work, Destroy must wrap ports.ErrWorkspaceDirty so the
// Session Manager can preserve the workspace (Kill freed=false, Cleanup
// skipped-with-reason) instead of surfacing an opaque 500.
func TestDestroyClassifiesDirtyWorktree(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := mkdirFile(path, "keep.txt"); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree remove"):
			return []byte("contains modified or untracked files"), errors.New("remove failed")
		case strings.Contains(joined, "worktree prune"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + path + "\nbranch refs/heads/feature/one\n"), nil
		case strings.Contains(joined, "status --porcelain"):
			return []byte("?? keep.txt\n"), nil
		default:
			return nil, nil
		}
	}
	err = ws.Destroy(context.Background(), ports.WorkspaceInfo{Path: path, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if !errors.Is(err, ports.ErrWorkspaceDirty) {
		t.Fatalf("destroy error = %v, want ports.ErrWorkspaceDirty", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, "keep.txt")); statErr != nil {
		t.Fatalf("expected dirty worktree to be preserved: %v", statErr)
	}
}

func TestStashUncommittedClassifiesMissingManagedPathAsStale(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")

	_, err = ws.StashUncommitted(context.Background(), ports.WorkspaceInfo{Path: path, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if !errors.Is(err, ports.ErrWorkspaceStale) {
		t.Fatalf("stash error = %v, want ports.ErrWorkspaceStale", err)
	}
}

func TestStashUncommittedClassifiesUnregisteredManagedPathAsStale(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "worktree list --porcelain") {
			return []byte("worktree " + repo + "\nbranch refs/heads/main\n"), nil
		}
		return nil, nil
	}

	_, err = ws.StashUncommitted(context.Background(), ports.WorkspaceInfo{Path: path, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if !errors.Is(err, ports.ErrWorkspaceStale) {
		t.Fatalf("stash error = %v, want ports.ErrWorkspaceStale", err)
	}
}

func TestStashUncommittedClassifiesNotGitRepositoryAsStale(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	ws.run = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + path + "\nbranch refs/heads/feature/one\n"), nil
		case strings.Contains(joined, "status --porcelain"):
			return nil, commandError{args: append([]string{binary}, args...), output: "fatal: not a git repository", err: errors.New("exit status 128")}
		default:
			return nil, nil
		}
	}

	_, err = ws.StashUncommitted(context.Background(), ports.WorkspaceInfo{Path: path, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if !errors.Is(err, ports.ErrWorkspaceStale) {
		t.Fatalf("stash error = %v, want ports.ErrWorkspaceStale", err)
	}
}

func TestStashUncommittedOutsideManagedPathIsUnsafeNotStale(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	outside := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	_, err = ws.StashUncommitted(context.Background(), ports.WorkspaceInfo{Path: outside, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("stash error = %v, want ErrUnsafePath", err)
	}
	if errors.Is(err, ports.ErrWorkspaceStale) {
		t.Fatalf("outside managed path must not be classified stale: %v", err)
	}
}

// TestAddWorktreeRefusesBranchCheckedOutElsewhere covers Bug 3 (a): if the
// requested branch is already checked out in another worktree of the same repo,
// Create must surface ports.ErrWorkspaceBranchCheckedOutElsewhere so the HTTP
// layer can render a typed 409 instead of leaking raw git stderr through a 500.
func TestAddWorktreeRefusesBranchCheckedOutElsewhere(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	otherPath := filepath.Join(root, "proj", "other")
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "check-ref-format"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + otherPath + "\nbranch refs/heads/feature/x\n"), nil
		case strings.Contains(joined, "rev-parse"):
			return []byte("commit"), nil
		default:
			t.Fatalf("unexpected git invocation: %v", args)
			return nil, nil
		}
	}
	_, err = ws.Create(context.Background(), ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/x"})
	if !errors.Is(err, ports.ErrWorkspaceBranchCheckedOutElsewhere) {
		t.Fatalf("err = %v, want ports.ErrWorkspaceBranchCheckedOutElsewhere", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(otherPath)) {
		t.Fatalf("err = %v, want message to include conflicting path %q", err, otherPath)
	}
}

// TestCreateRejectsInvalidBranchName covers the residual of #152 Bug 3: a branch
// name rejected by `git check-ref-format` must surface
// ports.ErrWorkspaceBranchInvalid so the HTTP layer renders a typed 400 instead
// of leaking raw git stderr through a 500.
func TestCreateRejectsInvalidBranchName(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "check-ref-format") {
			return nil, errors.New("fatal: 'bad branch!!' is not a valid branch name")
		}
		t.Fatalf("no git beyond check-ref-format should run for an invalid branch: %v", args)
		return nil, nil
	}
	_, err = ws.Create(context.Background(), ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "bad branch!!"})
	if !errors.Is(err, ports.ErrWorkspaceBranchInvalid) {
		t.Fatalf("err = %v, want ports.ErrWorkspaceBranchInvalid", err)
	}
	if !strings.Contains(err.Error(), "bad branch!!") {
		t.Fatalf("err = %v, want message to include the rejected branch name", err)
	}
}

// TestAddWorktreeReportsBranchNotFetched covers Bug 3 (b): if no local head,
// no origin remote-tracking branch, no default branch ref, and no tag of the
// same name is reachable, Create must surface ports.ErrWorkspaceBranchNotFetched
// so the HTTP layer can render a typed 400 with a `git fetch` suggestion.
func TestAddWorktreeReportsBranchNotFetched(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Build a real exit-1 error so refExists treats every probe as "absent".
	exitOne := exitStatusOne(t)
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "check-ref-format"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return nil, nil
		case strings.Contains(joined, "rev-parse"):
			return nil, commandError{args: args, err: exitOne}
		default:
			t.Fatalf("unexpected git invocation: %v", args)
			return nil, nil
		}
	}
	_, err = ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "proj", SessionID: "sess", Branch: "feature/missing", BaseBranch: "main",
	})
	if !errors.Is(err, ports.ErrWorkspaceBranchNotFetched) {
		t.Fatalf("err = %v, want ports.ErrWorkspaceBranchNotFetched", err)
	}
}

func TestResolveWorktreeRefsInfersRepoDefaultBranchWhenUnset(t *testing.T) {
	ws, err := New(Options{ManagedRoot: t.TempDir(), RepoResolver: StaticRepoResolver{"proj": t.TempDir()}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	exitOne := exitStatusOne(t)
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasSuffix(joined, " remote"):
			return []byte("origin\n"), nil
		case strings.Contains(joined, "config --get checkout.defaultRemote"):
			return nil, commandError{args: args, err: exitOne}
		case strings.Contains(joined, "ls-remote --symref -- origin HEAD"):
			return []byte("ref: refs/heads/master\tHEAD\nabc123\tHEAD\n"), nil
		case strings.Contains(joined, "check-ref-format --branch master"):
			return nil, nil
		case strings.Contains(joined, " fetch "):
			return nil, nil
		case strings.Contains(joined, "symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/master"):
			return nil, nil
		case strings.Contains(joined, "refs/remotes/origin/master"):
			return []byte("sha\n"), nil
		case strings.Contains(joined, "rev-parse --verify"):
			return nil, commandError{args: args, err: exitOne}
		default:
			return nil, nil
		}
	}
	refs, err := ws.resolveWorktreeRefs(context.Background(), context.Background(), "/repo/child", "ao/work", "")
	if err != nil {
		t.Fatalf("resolveWorktreeRefs err = %v", err)
	}
	if refs.seedRef != "refs/remotes/origin/master" || refs.baseRef != "refs/remotes/origin/master" {
		t.Fatalf("refs = %#v, want child default for both seed and base", refs)
	}
}

func mkdirFile(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644)
}

func exitStatusOne(t *testing.T) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestGitWorktreeExitStatusOneHelper")
	cmd.Env = append(os.Environ(), "GO_WANT_GITWORKTREE_EXIT_STATUS_ONE=1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected exit error")
	}
	return err
}

func TestGitWorktreeExitStatusOneHelper(t *testing.T) {
	if os.Getenv("GO_WANT_GITWORKTREE_EXIT_STATUS_ONE") != "1" {
		return
	}
	os.Exit(1)
}
