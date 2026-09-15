//go:build !windows

package runtimeselect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/testutil/reviewerupdate"
)

// isolatedReviewerBackend replaces only teardown: it never signals process
// groups or discovers a legacy/default server. Creation and workload inspection
// use the real tmux adapter behind the production hybrid router.
type isolatedReviewerBackend struct {
	*tmux.Runtime
	binary, socket string
}

func (r isolatedReviewerBackend) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	out, err := exec.CommandContext(ctx, r.binary, "-L", r.socket, "kill-session", "-t", handle.ID).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "can't find session") {
		return fmt.Errorf("private teardown: %w: %s", err, out)
	}
	return nil
}

func quoteReviewerFixture(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func TestReviewerWorkloadControlledProcessIntegration(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		if os.Getenv("AO_TEST_REQUIRE_TMUX") == "1" {
			t.Fatal("required tmux integration dependency unavailable:", err)
		}
		t.Skip("tmux unavailable; isolated reviewer integration requires tmux")
	}
	for _, mode := range []string{"fresh", "resume", "restore"} {
		for _, manual := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/manual=%t", mode, manual), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				// A short private directory also keeps Unix socket paths below their limit.
				dir, err := os.MkdirTemp("/tmp", "ao-reviewer-integration-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(dir) })
				t.Setenv("TMUX_TMPDIR", dir)
				t.Setenv("HOME", dir)
				t.Setenv("HISTFILE", filepath.Join(dir, "shell-history"))
				t.Setenv("SHELL", "/bin/sh")
				t.Setenv("ENV", "")
				t.Setenv("BASH_ENV", "")
				const socket = "reviewer-workload"
				wrapper := filepath.Join(dir, "private-tmux")
				wrapperScript := "#!/bin/sh\n[ \"$1\" = -L ] && [ \"$2\" = " + quoteReviewerFixture(socket) + " ] || { echo 'non-private tmux command rejected' >&2; exit 1; }\nshift 2\nexec " + quoteReviewerFixture(binary) + " -L " + quoteReviewerFixture(socket) + " -f /dev/null \"$@\"\n"
				if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
					t.Fatal(err)
				}
				privateCommand := func(ctx context.Context, args ...string) ([]byte, error) {
					return exec.CommandContext(ctx, wrapper, append([]string{"-L", socket}, args...)...).CombinedOutput()
				}
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					out, err := privateCommand(cleanup, "kill-server")
					if err != nil && !strings.Contains(string(out), "no server running") && !strings.Contains(string(out), "No such file") {
						t.Errorf("private server cleanup: %v: %s", err, out)
					}
				})
				if out, err := privateCommand(ctx, "new-session", "-d", "-s", "keeper", "/bin/sh"); err != nil {
					t.Fatalf("private server: %v: %s", err, out)
				}
				backend := isolatedReviewerBackend{Runtime: tmux.New(tmux.Options{Binary: wrapper, LegacyBinary: wrapper, SocketName: socket, Shell: "/bin/sh", Timeout: time.Second}), binary: wrapper, socket: socket}
				rt := newHybridRuntime(backend, &fakeBackend{createErr: errors.New("fixture selects legacy reviewer runtime")}, nil, "test")
				ready := filepath.Join(dir, "ready")
				argvFile := filepath.Join(dir, "argv")
				command := filepath.Join(dir, "codex-fixture")
				script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + quoteReviewerFixture(argvFile) + "\nprintf '%s\\n' \"$$\" > " + quoteReviewerFixture(ready) + "\nwhile IFS= read -r request; do\n [ \"$request\" = exit ] && exit 0\ndone\n"
				if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				f := reviewerupdate.New(ctx, t, rt, mode, command)
				handle := ports.RuntimeHandle{ID: f.Result.HandleID}
				waitReviewerEvidence(ctx, t, func() bool { _, err := os.Stat(ready); return err == nil })
				argv, err := os.ReadFile(argvFile)
				if err != nil {
					t.Fatal(err)
				}
				if (mode != "fresh") != (string(argv) == "resume\nnative-history\n") {
					t.Fatalf("actual resume argv=%q, mode=%s", argv, mode)
				}
				f.Blocked(ctx, t, nil) // actual helper is alive and waiting, with no review output
				stop := func(ctx context.Context) {
					t.Helper()
					if err := rt.SendMessage(ctx, handle, "exit"); err != nil {
						t.Fatal(err)
					}
					waitReviewerEvidence(ctx, t, func() bool { s, err := f.Engine.SnapshotCodexReviewer(ctx, "worker"); return err == nil && !s.Running })
					if alive, err := rt.IsChildAlive(ctx, handle); err != nil || !alive {
						t.Fatalf("expected retained shell child: %v %v", alive, err)
					}
				}
				stop(ctx)              // real wrapper execs the retained interactive shell after helper exit
				f.Blocked(ctx, t, nil) // retained shell can consume input after a successful PTY write
				if !manual {
					if alive, err := rt.IsAlive(ctx, handle); err != nil || !alive {
						t.Fatalf("updater removed retained terminal: %v %v", alive, err)
					}
					f.Close(ctx, t) // user explicitly invokes Kill review session
					f.Ready(ctx, t) // only confirmed closure admits the fake installer
					return
				}
				for _, prefix := range []string{"", "exec "} {
					if err := os.Remove(ready); err != nil {
						t.Fatal(err)
					}
					if err := rt.SendMessage(ctx, handle, prefix+quoteReviewerFixture(command)+" resume native-history"); err != nil {
						t.Fatal(err)
					}
					waitReviewerEvidence(ctx, t, func() bool { _, err := os.Stat(ready); return err == nil })
					f.Blocked(ctx, t, nil)
					if prefix == "" {
						stop(ctx)
					}
				}
				// Cleanup terminates only this test's still-live manual-exec helper/server.
			})
		}
	}
}

func waitReviewerEvidence(ctx context.Context, t *testing.T, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatal("reviewer transition was not observed:", ctx.Err())
		case <-ticker.C:
		}
	}
}
