package gitdefault

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUsesLiveRemoteHeadInsteadOfCheckedOutBranch(t *testing.T) {
	origin, repo := remoteRepo(t, "dev")
	runGit(t, repo, "switch", "-c", "feature/temporary")
	updater := filepath.Join(filepath.Dir(repo), "updater")
	run(t, "git", "clone", origin, updater)
	configureGit(t, updater)
	if err := os.WriteFile(filepath.Join(updater, "remote.txt"), []byte("new remote tip\n"), 0o644); err != nil {
		t.Fatalf("write remote update: %v", err)
	}
	runGit(t, updater, "add", "remote.txt")
	runGit(t, updater, "commit", "-m", "advance remote default")
	runGit(t, updater, "push", "origin", "dev")
	wantSHA := gitOutput(t, origin, "rev-parse", "refs/heads/dev")

	// Make the local cache stale. The live bare repository still advertises dev.
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	resolution, err := New("", nil).Resolve(context.Background(), context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Branch != "dev" || resolution.Remote != "origin" || resolution.Source != SourceLiveRemoteHead {
		t.Fatalf("resolution = %#v, want live origin/dev (origin %s)", resolution, origin)
	}
	if got := gitOutput(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/dev" {
		t.Fatalf("cached remote HEAD = %q, want refs/remotes/origin/dev", got)
	}
	if got := gitOutput(t, repo, "rev-parse", resolution.Ref); got != wantSHA {
		t.Fatalf("fetched default SHA = %s, want live remote tip %s", got, wantSHA)
	}
}

func TestResolveFallsBackToCachedHeadWhenRemoteIsOffline(t *testing.T) {
	origin, repo := remoteRepo(t, "trunk")
	if err := os.Rename(origin, origin+".offline"); err != nil {
		t.Fatalf("take origin offline: %v", err)
	}

	resolution, err := New("", nil).Resolve(context.Background(), context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Branch != "trunk" || resolution.Source != SourceCachedRemoteHead {
		t.Fatalf("resolution = %#v, want cached trunk", resolution)
	}
}

func TestResolveNeverFallsBackToCurrentOrConventionalBranch(t *testing.T) {
	repo := localRepo(t, "main")
	runGit(t, repo, "switch", "-c", "feature/temporary")

	_, err := New("", nil).Resolve(context.Background(), context.Background(), repo)
	if !errors.Is(err, ErrUnresolved) {
		t.Fatalf("Resolve error = %v, want ErrUnresolved", err)
	}
	if !strings.Contains(err.Error(), "no remote or AO-recorded default") {
		t.Fatalf("Resolve error = %v, want missing authoritative metadata detail", err)
	}
}

func TestResolveUsesBranchAORecordedAtInitialization(t *testing.T) {
	repo := localRepo(t, "main")
	runGit(t, repo, "config", "--local", ManagedDefaultConfigKey, "main")
	runGit(t, repo, "switch", "-c", "feature/temporary")

	resolution, err := New("", nil).Resolve(context.Background(), context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Branch != "main" || resolution.Ref != "refs/heads/main" || resolution.Source != SourceAOInitialized {
		t.Fatalf("resolution = %#v, want AO-initialized main", resolution)
	}
}

func TestResolveBackfillsLegacyAOInitializedRepository(t *testing.T) {
	for _, tc := range []struct {
		name    string
		author  string
		email   string
		subject string
	}{
		{name: "initialized folder", author: "Agent Orchestrator", email: "ao@example.com", subject: legacyInitialCommitSubject},
		{name: "workspace root", author: "Developer", email: "developer@example.com", subject: legacyWorkspaceCommitSubject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			run(t, "git", "init", "-b", legacyDefaultBranch, repo)
			runGit(t, repo, "config", "user.name", tc.author)
			runGit(t, repo, "config", "user.email", tc.email)
			runGit(t, repo, "commit", "--allow-empty", "-m", tc.subject)
			runGit(t, repo, "switch", "-c", "feature/temporary")

			resolution, err := New("", nil).Resolve(context.Background(), context.Background(), repo)
			if err != nil {
				t.Fatalf("Resolve legacy repository: %v", err)
			}
			if resolution.Branch != legacyDefaultBranch || resolution.Ref != "refs/heads/main" || resolution.Source != SourceAOInitialized {
				t.Fatalf("resolution = %#v, want backfilled AO main", resolution)
			}
			if got := gitOutput(t, repo, "config", "--local", "--get", ManagedDefaultConfigKey); got != legacyDefaultBranch {
				t.Fatalf("backfilled marker = %q, want %q", got, legacyDefaultBranch)
			}
		})
	}
}

func TestFindCachedRemoteBranchDoesNotRequireRemoteHead(t *testing.T) {
	_, repo := remoteRepo(t, "main")
	runGit(t, repo, "update-ref", "refs/remotes/origin/feature/resume", "HEAD")
	runGit(t, repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")

	remote, ref, ok, err := New("", nil).FindCachedRemoteBranch(context.Background(), repo, "feature/resume")
	if err != nil {
		t.Fatalf("FindCachedRemoteBranch: %v", err)
	}
	if !ok || remote != "origin" || ref != "refs/remotes/origin/feature/resume" {
		t.Fatalf("cached branch = remote:%q ref:%q ok:%v", remote, ref, ok)
	}
}

func TestResolveRequiresPrimaryRemoteWhenSeveralExist(t *testing.T) {
	repo := localRepo(t, "main")
	runGit(t, repo, "remote", "add", "company", filepath.Join(t.TempDir(), "company.git"))
	runGit(t, repo, "remote", "add", "upstream", filepath.Join(t.TempDir(), "upstream.git"))
	runGit(t, repo, "update-ref", "refs/remotes/upstream/dev", "HEAD")
	runGit(t, repo, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/dev")

	resolver := New("", nil)
	if _, err := resolver.Resolve(context.Background(), context.Background(), repo); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("ambiguous Resolve error = %v, want ErrUnresolved", err)
	}

	runGit(t, repo, "config", "checkout.defaultRemote", "upstream")
	resolution, err := resolver.Resolve(context.Background(), context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve with checkout.defaultRemote: %v", err)
	}
	if resolution.Remote != "upstream" || resolution.Branch != "dev" || resolution.Source != SourceCachedRemoteHead {
		t.Fatalf("resolution = %#v, want cached upstream/dev", resolution)
	}
}

func TestParseRemoteHEADRequiresSymbolicBranch(t *testing.T) {
	branch, err := parseRemoteHEAD("ref: refs/heads/release/v2\tHEAD\nabc123\tHEAD\n")
	if err != nil || branch != "release/v2" {
		t.Fatalf("parseRemoteHEAD = %q, %v", branch, err)
	}
	if _, err := parseRemoteHEAD("abc123\tHEAD\n"); err == nil {
		t.Fatal("parseRemoteHEAD accepted a detached remote HEAD")
	}
}

func remoteRepo(t *testing.T, defaultBranch string) (string, string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	repo := filepath.Join(root, "repo")
	run(t, "git", "init", "--bare", origin)
	run(t, "git", "init", "-b", defaultBranch, seed)
	configureGit(t, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "commit", "-m", "seed")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "origin", defaultBranch)
	runGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)
	run(t, "git", "clone", origin, repo)
	configureGit(t, repo)
	return origin, repo
}

func localRepo(t *testing.T, branch string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	run(t, "git", "init", "-b", branch, repo)
	configureGit(t, repo)
	runGit(t, repo, "commit", "--allow-empty", "-m", "seed")
	return repo
}

func configureGit(t *testing.T, repo string) {
	t.Helper()
	runGit(t, repo, "config", "user.email", "ao@example.com")
	runGit(t, repo, "config", "user.name", "AO Test")
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", repo, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	run(t, "git", append([]string{"-C", repo}, args...)...)
}

func run(t *testing.T, binary string, args ...string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, out)
	}
}
