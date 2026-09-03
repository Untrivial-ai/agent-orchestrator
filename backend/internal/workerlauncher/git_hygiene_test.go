package workerlauncher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerRepositoryGitHygiene(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"credential helper", "credential.helper", "!malicious-helper"},
		{"authorization header", "http.extraHeader", "Authorization: Bearer secret"},
		{"ssh command", "core.sshCommand", "malicious-ssh"},
		{"URL rewrite", "url.https://evil.example/.insteadOf", "https://approved.example/"},
		{"hooks path", "core.hooksPath", "malicious-hooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initHygieneRepository(t)
			runHygieneGit(t, repo, "config", "--local", tc.key, tc.value)
			if err := ValidateWorkerRepositoryGitConfig(repo, ""); err == nil {
				t.Fatalf("dangerous local config %s was accepted", tc.key)
			}
		})
	}
}

func TestWorkerRepositoryGitHygieneRejectsCredentialBearingRemote(t *testing.T) {
	repo := initHygieneRepository(t)
	runHygieneGit(t, repo, "remote", "add", "origin", "https://user:token@example.test/repo.git")
	if err := ValidateWorkerRepositoryGitConfig(repo, ""); err == nil || !strings.Contains(err.Error(), "embedded credentials") {
		t.Fatalf("credential-bearing remote error = %v", err)
	}
}

func TestWorkerRepositoryGitHygieneAllowsCleanRepository(t *testing.T) {
	repo := initHygieneRepository(t)
	runHygieneGit(t, repo, "remote", "add", "origin", "https://example.test/repo.git")
	if err := ValidateWorkerRepositoryGitConfig(repo, ""); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRepositoryGitHygieneFailsClosedForInvalidRepository(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkerRepositoryGitConfig(repo, ""); err == nil || !strings.Contains(err.Error(), "identify repository") {
		t.Fatalf("invalid repository error = %v", err)
	}
}

func TestWorkerRepositoryGitHygieneAllowsNonGitWorkspace(t *testing.T) {
	if err := ValidateWorkerRepositoryGitConfig(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
}

func initHygieneRepository(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runHygieneGit(t, repo, "init")
	return repo
}

func runHygieneGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
