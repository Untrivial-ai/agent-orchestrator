package workerexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestCursorACPStandingRulePreservesRepository(t *testing.T) {
	workspace := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", workspace}, args...)...)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "GIT_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	userRule := filepath.Join(workspace, ".cursor", "rules", "user.mdc")
	if err := writePrivateFile(userRule, []byte("user repository instructions\n")); err != nil {
		t.Fatal(err)
	}
	before := git("status", "--porcelain")
	if err := writeCursorACPStandingRule(workspace, "session-1", "ROLE PROJECT MARKER"); err != nil {
		t.Fatal(err)
	}
	path, err := cursorACPStandingRulePath(workspace, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, path, "ROLE PROJECT MARKER")
	if after := git("status", "--porcelain"); after != before {
		t.Fatalf("standing rule changed Git status: %q -> %q", before, after)
	}
	contents, err := os.ReadFile(userRule)
	if err != nil || string(contents) != "user repository instructions\n" {
		t.Fatal("user repository rule changed")
	}
	if err := removeCursorACPStandingRule(workspace, "session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("managed rule survived cleanup: %v", err)
	}
}

func TestCursorACPStandingRuleRefusesUnknownFilesAndSymlinks(t *testing.T) {
	for _, kind := range []string{"file", "file-symlink", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			path, err := cursorACPStandingRulePath(workspace, "session-1")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "directory-symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Dir(filepath.Dir(path))); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if kind == "file" {
					if err := os.WriteFile(path, []byte("user file"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					target := filepath.Join(t.TempDir(), "user.mdc")
					if err := os.WriteFile(target, []byte("user file"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := writeCursorACPStandingRule(workspace, "session-1", "new role"); err == nil {
				t.Fatal("unmanaged path was overwritten")
			}
			if kind != "directory-symlink" {
				contents, err := os.ReadFile(path)
				if err != nil || string(contents) != "user file" {
					t.Fatal("unmanaged file changed")
				}
			}
		})
	}
}

func TestCursorACPStandingRuleCleansUpFailedProviderLaunch(t *testing.T) {
	workspace := t.TempDir()
	control := &promptControl{controlStub: controlStub{credential: worker.CredentialResponse{Provider: "cursor", CredentialType: "api_key", Secret: "test-secret"}}}
	supervisor := &Supervisor{
		Control: control, Workspace: workspace, UseProviderProtocol: true,
		CancelInterval: time.Millisecond, CompletionRetry: time.Millisecond,
		Builder: HarnessBuilder{DataDir: t.TempDir(), Launch: worker.LaunchContext{SessionID: "session-1", Kind: "worker", Harness: "cursor"}, Binaries: map[string]string{"cursor": filepath.Join(t.TempDir(), "missing-provider")}},
	}
	if err := supervisor.execute(context.Background(), worker.Turn{ID: "turn-1", Attempt: 1, Harness: "cursor", Mode: "trusted", Prompt: "task"}); err != nil {
		t.Fatal(err)
	}
	if control.failed == "" {
		t.Fatal("missing provider launch was not reported")
	}
	path, err := cursorACPStandingRulePath(workspace, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("managed rule survived failed launch: %v", err)
	}
}

func TestCursorTerminalClearsStaleChatStandingRule(t *testing.T) {
	workspace := t.TempDir()
	if err := writeCursorACPStandingRule(workspace, "session-1", "STALE CHAT ROLE"); err != nil {
		t.Fatal(err)
	}
	_, err := (HarnessBuilder{DataDir: t.TempDir()}).BuildInteractive(
		worker.LaunchContext{SessionID: "session-1", Kind: "worker", Harness: "cursor", Mode: "trusted"},
		worker.CredentialResponse{Provider: "cursor", CredentialType: "api_key", Secret: "test-secret"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	path, err := cursorACPStandingRulePath(workspace, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Terminal retained stale Chat rule: %v", err)
	}
}
