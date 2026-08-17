package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/gitdefault"
)

func TestInitWorkspaceParentRollsBackWhenDefaultMarkerFails(t *testing.T) {
	parent := t.TempDir()
	gitignorePath := filepath.Join(parent, ".gitignore")
	originalGitignore := []byte("keep-me\n")
	if err := os.WriteFile(gitignorePath, originalGitignore, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	runGit := func(_ context.Context, dir string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 0 && args[0] == "init" {
			if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
				return "", err
			}
			return "", nil
		}
		return "", errors.New("config failed")
	}

	err := initWorkspaceParentWithGit(context.Background(), parent, nil, runGit)
	if err == nil {
		t.Fatal("initWorkspaceParentWithGit succeeded, want marker failure")
	}
	if _, statErr := os.Stat(filepath.Join(parent, ".git")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf(".git after marker failure = %v, want removed", statErr)
	}
	gotGitignore, readErr := os.ReadFile(gitignorePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(gotGitignore, originalGitignore) {
		t.Fatalf(".gitignore after rollback = %q, want %q", gotGitignore, originalGitignore)
	}
	wantCalls := [][]string{
		{"init", "-b", domain.DefaultBranchName},
		{"config", "--local", gitdefault.ManagedDefaultConfigKey, domain.DefaultBranchName},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", calls, wantCalls)
	}
}
