package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stagingRecorderRunner fakes git for the non-empty-workspace clone path. It
// records the working directory of the clone (the staging root) and populates
// the clone destination with a .git dir and a tracked file so the merge and
// origin validation succeed.
type stagingRecorderRunner struct {
	cloneURL   string
	stagingDir string
}

func (r *stagingRecorderRunner) Run(_ context.Context, dir string, _ map[string]string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "clone" {
		r.stagingDir = dir
		dest := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("# repo\n"), 0o644); err != nil {
			return "", err
		}
		return "", nil
	}
	// validateOrigin: `remote get-url origin`
	return r.cloneURL, nil
}

// A workspace the coding agent has already written into (non-empty, no .git)
// must check out even when its parent is the provider's durable root that the
// worker user cannot write to. The fix stages inside the workspace itself, so
// the staging dir must live under the workspace, never under its parent.
func TestCloneIntoNonEmptyWorkspaceStagesInsideWorkspace(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "repository")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	// The agent wrote a file before checkout ran — this is what forces the
	// non-empty (staging) path.
	if err := os.WriteFile(filepath.Join(workspace, ".claude"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &stagingRecorderRunner{cloneURL: "https://github.com/acme/repo.git"}
	if err := PrepareCheckout(context.Background(), runner, workspace,
		CheckoutGrantResponse{CloneURL: "https://github.com/acme/repo.git"}); err != nil {
		t.Fatalf("PrepareCheckout: %v", err)
	}

	if r := runner.stagingDir; !strings.HasPrefix(filepath.Clean(r), filepath.Clean(workspace)+string(filepath.Separator)) {
		t.Fatalf("staging dir %q is not inside the workspace %q (would fail on a durable root the worker cannot write)", r, workspace)
	}
	// The clone's tracked files were merged in and the agent's file preserved.
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		t.Fatalf("cloned .git not merged into workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatalf("cloned file not merged into workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".claude")); err != nil {
		t.Fatalf("agent file not preserved: %v", err)
	}
	// The hidden staging dir is cleaned up before returning.
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ao-checkout-") {
			t.Fatalf("staging dir %q was left behind in the workspace", e.Name())
		}
	}
}

// noopGitRunner satisfies the git-config/checkout commands ConfigureWorkerGit
// runs without touching a real repository.
type noopGitRunner struct{}

func (noopGitRunner) Run(context.Context, string, map[string]string, ...string) (string, error) {
	return "", nil
}

// The sandbox git credential helper must scope the brokered token to the exact
// repository git is asking about. git supplies host+path (useHttpPath=true); the
// helper turns those into a ?repo=owner/repo query so a push to a declared extra
// dev-kit repository gets a correctly scoped token — while a malformed or
// non-GitHub request falls back to the broad grant (no ?repo=). This drives the
// actually-rendered shell (the fmt.Sprintf %-escaping is easy to get wrong)
// through stubbed curl/jq and asserts the URL the helper would call.
func TestConfigureWorkerGitCredentialHelperScopesRepo(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dataDir := t.TempDir()
	workspace := t.TempDir()
	if err := ConfigureWorkerGit(
		context.Background(), noopGitRunner{},
		workspace, dataDir, "https://cp.example.com", "sess-xyz", "ao/branch",
	); err != nil {
		t.Fatalf("ConfigureWorkerGit: %v", err)
	}
	helperPath := GitCredentialHelperPath(dataDir)

	// Syntax-check the rendered shell.
	if out, err := exec.Command("sh", "-n", helperPath).CombinedOutput(); err != nil {
		t.Fatalf("rendered helper is not valid shell: %v\n%s", err, out)
	}

	// The helper reads the rotating worker token from this file.
	if err := os.WriteFile(filepath.Join(dataDir, "worker-token"), []byte("WT\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Stub curl (records the URL it is asked to fetch) and jq (returns a token),
	// so the helper never touches the network and we can inspect the URL.
	stubBin := t.TempDir()
	urlFile := filepath.Join(dataDir, "captured-url")
	writeStub(t, filepath.Join(stubBin, "curl"),
		"#!/bin/sh\nfor a in \"$@\"; do url=\"$a\"; done\nprintf '%s' \"$url\" > \""+urlFile+"\"\nprintf '{\"token\":\"x\"}'\n")
	writeStub(t, filepath.Join(stubBin, "jq"),
		"#!/bin/sh\ncat >/dev/null 2>&1\nprintf 'x'\n")
	env := append(os.Environ(), "PATH="+stubBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	base := "https://cp.example.com/api/cloud/v1/worker/github-token"
	cases := []struct {
		name      string
		stdin     string
		wantURL   string // expected token-endpoint URL when a credential is issued
		wantToken bool   // false = helper must NOT contact the token endpoint (host gate)
	}{
		{"primary", "protocol=https\nhost=github.com\npath=octo/app.git\n\n", base + "?repo=octo/app", true},
		{"extra", "protocol=https\nhost=github.com\npath=octo/extra.git\n\n", base + "?repo=octo/extra", true},
		{"no path", "protocol=https\nhost=github.com\n\n", base, true},
		// A single-segment path is not a real owner/repo → broad grant, not a bogus scoped request.
		{"single segment", "protocol=https\nhost=github.com\npath=octoonly\n\n", base, true},
		// Host gate: a non-github remote (which the agent can add) must never be handed the token.
		{"non-github host", "protocol=https\nhost=example.com\npath=octo/app.git\n\n", "", false},
		{"gitlab host", "protocol=https\nhost=gitlab.com\npath=octo/app.git\n\n", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(urlFile)
			cmd := exec.Command("sh", helperPath, "get")
			cmd.Stdin = strings.NewReader(tc.stdin)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper get: %v\n%s", err, out)
			}
			if !tc.wantToken {
				// The gate must short-circuit before curl and emit no credential.
				if u, statErr := os.ReadFile(urlFile); statErr == nil {
					t.Fatalf("host %q must not fetch a token, but curl was called with %q", tc.name, u)
				}
				if strings.Contains(string(out), "password=") {
					t.Fatalf("host %q must not emit a credential; got %q", tc.name, out)
				}
				return
			}
			got, err := os.ReadFile(urlFile)
			if err != nil {
				t.Fatalf("read captured URL: %v", err)
			}
			if string(got) != tc.wantURL {
				t.Fatalf("helper called %q, want %q", got, tc.wantURL)
			}
		})
	}
}

func writeStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureWorkspaceReviewBaseRecordsRemoteMergeBaseOnce(t *testing.T) {
	repo := initReviewBaseRepository(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "update-ref", "refs/remotes/origin/main", base)
	gitRun(t, repo, "checkout", "-b", "ao/session")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("second\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "session change")

	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != base {
		t.Fatalf("review base = %s, want %s", got, base)
	}

	gitRun(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("second EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != base {
		t.Fatalf("existing review base moved to %s, want %s", got, base)
	}
}

func TestEnsureWorkspaceReviewBaseFallsBackToRootCommit(t *testing.T) {
	repo := initReviewBaseRepository(t)
	root := gitOutput(t, repo, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("second\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "later")

	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "missing"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != root {
		t.Fatalf("fallback review base = %s, want root %s", got, root)
	}
}

func TestEnsureWorkspaceReviewBaseUsesRemoteHEADWhenConfiguredBranchIsMissing(t *testing.T) {
	repo := initReviewBaseRepository(t)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("remote head\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "remote head")
	remoteHead := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "update-ref", "refs/remotes/origin/master", remoteHead)
	gitRun(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
	gitRun(t, repo, "checkout", "-b", "ao/session")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("session change\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "session change")

	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != remoteHead {
		t.Fatalf("review base = %s, want remote HEAD %s", got, remoteHead)
	}
}

func TestEnsureWorkspaceReviewBaseRepairsLegacyRootFallback(t *testing.T) {
	repo := initReviewBaseRepository(t)
	root := gitOutput(t, repo, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("remote head\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "remote head")
	remoteHead := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "update-ref", "refs/remotes/origin/master", remoteHead)
	gitRun(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
	gitRun(t, repo, "update-ref", WorkspaceReviewBaseRef, root)

	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != remoteHead {
		t.Fatalf("repaired review base = %s, want remote HEAD %s", got, remoteHead)
	}
}

// A scratch / no-repo workspace is `git init` with no commit (unborn HEAD).
// EnsureWorkspaceReviewBase must no-op there rather than error, otherwise every
// scratch session's worker aborts in prepareWorkspace across all providers.
func TestEnsureWorkspaceReviewBaseNoOpOnUnbornHEAD(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase on an empty repo: %v", err)
	}
	if _, err := (ExecGitRunner{}).Run(
		context.Background(), repo, nil, "rev-parse", "--verify", WorkspaceReviewBaseRef,
	); err == nil {
		t.Fatalf("review base ref must not be created for an empty repo")
	}
}

func initReviewBaseRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.name", "AO Test")
	gitRun(t, repo, "config", "user.email", "ao@example.test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "initial")
	return repo
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// TestCloneExtraRepoDoesNotPersistToken locks the security invariant behind the
// multi-repo dev kit: the checkout token must never be baked into the extra
// repo's origin URL / .git/config, where the coding agent could read it back.
func TestCloneExtraRepoDoesNotPersistToken(t *testing.T) {
	src := t.TempDir()
	gitRun(t, src, "init", "--initial-branch=main")
	gitRun(t, src, "config", "user.name", "AO Test")
	gitRun(t, src, "config", "user.email", "ao@example.test")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hi\n"), 0o600); err != nil {
		t.Fatalf("seed source repo: %v", err)
	}
	gitRun(t, src, "add", "README.md")
	gitRun(t, src, "commit", "-m", "init")

	parent := t.TempDir()
	dataDir := t.TempDir()
	dest := filepath.Join(parent, "extra")
	const token = "gho_SUPERSECRETTOKENVALUE000000000000"

	if err := CloneExtraRepo(context.Background(), ExecGitRunner{}, parent, dest, "file://"+src, "", token, dataDir); err != nil {
		t.Fatalf("CloneExtraRepo: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if strings.Contains(string(cfg), token) || strings.Contains(string(cfg), "x-access-token") {
		t.Fatalf("token/credential leaked into .git/config:\n%s", cfg)
	}
	origin := gitOutput(t, dest, "remote", "get-url", "origin")
	if strings.Contains(origin, token) || strings.Contains(origin, "x-access-token") {
		t.Fatalf("origin url is credentialed: %s", origin)
	}
}
