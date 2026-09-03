package workerlauncher

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const dangerousLocalGitConfigPattern = `^(credential\.|http\.extraheader$|http\..*\.(extraheader|proxy)$|http\.proxy$|https\..*\.proxy$|https\.proxy$|core\.(sshcommand|hookspath|gitproxy)$|ssh\.variant$|url\..*\.(insteadof|pushinsteadof)$|remote\..*\.(proxy|receivepack)$|include\.|includeif\.)`

// ValidateWorkerRepositoryGitConfig fails closed when a managed Worker
// repository already contains credentials or transport overrides. Workers do
// not need these settings for local status/diff/commit operations, and must not
// inherit them as an alternate remote-write path.
func ValidateWorkerRepositoryGitConfig(worktree, gitExecutable string) error {
	if strings.TrimSpace(gitExecutable) == "" {
		var err error
		gitExecutable, err = exec.LookPath("git")
		if err != nil {
			return fmt.Errorf("worker launcher: locate git for repository hygiene: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, gitExecutable, append([]string{"-C", worktree}, args...)...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := run("rev-parse", "--git-dir"); err != nil {
		if _, statErr := os.Lstat(filepath.Join(worktree, ".git")); os.IsNotExist(statErr) {
			return nil // Non-Git workspaces have no repository-local credentials.
		}
		return fmt.Errorf("worker launcher: identify repository for Git hygiene: %w", err)
	}
	if out, err := run("config", "--local", "--name-only", "--get-regexp", dangerousLocalGitConfigPattern); err == nil {
		return fmt.Errorf("worker launcher: repository-local Git transport configuration is not allowed: %s", strings.ReplaceAll(out, "\n", ", "))
	} else if !isGitNoMatch(err) {
		return fmt.Errorf("worker launcher: inspect repository-local Git configuration: %w", err)
	}
	if out, err := run("config", "--local", "--get-regexp", `^remote\..*\.(url|pushurl)$`); err == nil {
		for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && remoteURLContainsCredential(strings.Join(fields[1:], " ")) {
				return errors.New("worker launcher: repository remote URL contains embedded credentials; clean the local Git config before starting a Worker")
			}
		}
	} else if !isGitNoMatch(err) {
		return fmt.Errorf("worker launcher: inspect repository remote URLs: %w", err)
	}
	return nil
}

func isGitNoMatch(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func remoteURLContainsCredential(raw string) bool {
	raw = strings.TrimSpace(raw)
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, "git@") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return false
	}
	if strings.EqualFold(parsed.Scheme, "ssh") && parsed.User.Username() == "git" {
		_, password := parsed.User.Password()
		return password
	}
	return true
}
