package shellterm

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestTmuxUserShellExitReconciliation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux requires a Unix shell")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	for _, tc := range []struct {
		name, socket string
		retainPane   bool
	}{
		{name: "legacy default socket"},
		{name: "private socket retained pane", socket: "ao-fixture", retainPane: true},
	} {
		for _, reconcile := range []string{"list", "desktop relaunch"} {
			t.Run(tc.name+"/"+reconcile, func(t *testing.T) {
				// A fresh, short socket root isolates even the legacy default
				// socket and stays below macOS's Unix socket path limit.
				socketRoot, err := os.MkdirTemp("/tmp", "ao-shell-exit-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
				t.Setenv("TMUX_TMPDIR", socketRoot)
				t.Setenv("TMUX", "")
				t.Setenv("AO_TMUX_SOCKET_NAME", "")
				t.Setenv("HOME", t.TempDir())
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				t.Setenv("SHELL", "/bin/sh")
				t.Setenv("ENV", "")
				socket := tc.socket
				if socket == "" {
					socket = "default"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				tmuxCommand := func(args ...string) string {
					t.Helper()
					args = append([]string{"-L", socket, "-f", "/dev/null"}, args...)
					out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
					if err != nil {
						t.Fatalf("fixture tmux %v: %v: %s", args, err, out)
					}
					return string(out)
				}
				t.Cleanup(func() {
					cleanupCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
					defer stop()
					// This socket exists only inside this subtest's temporary root.
					_ = exec.CommandContext(cleanupCtx, binary, "-L", socket, "kill-server").Run()
				})
				// Keep the fixture server reachable after a non-retained pane
				// exits, so its absence is conclusive rather than a probe outage.
				tmuxCommand("new-session", "-d", "-s", "anchor", "/bin/sh")
				rt := tmux.New(tmux.Options{Binary: binary, LegacyBinary: binary, SocketName: tc.socket, Shell: "/bin/sh", Timeout: time.Second})
				st := &fakeShellTerminalStore{}
				workspace := t.TempDir()
				svc := NewService(rt, st, nil, nil, workspace, "first-launch", testLogger())
				term, err := svc.OpenShellTerminal(ctx, OpenShellTerminalInput{Shell: "/bin/sh"})
				if err != nil {
					t.Fatal(err)
				}
				if len(st.records) != 1 || st.records[0].Transient {
					t.Fatalf("user shell must remain durable: %+v", st.records)
				}
				if tc.retainPane {
					tmuxCommand("set-option", "-w", "-t", "="+term.HandleID+":0", "remain-on-exit", "on")
				}
				// Relaunching preserves the still-running requested shell.
				svc = NewService(rt, st, nil, nil, workspace, "next-launch", testLogger())
				if n, err := svc.ReapShellTerminalsFromPreviousAppRuns(ctx); err != nil || n != 0 {
					t.Fatalf("live reap = %d, %v", n, err)
				}
				if listed, err := svc.ListShellTerminalsForCurrentAppRun(ctx); err != nil || len(listed) != 1 || listed[0] != term {
					t.Fatalf("live list = %+v, %v; want %+v", listed, err, term)
				}
				handle := ports.RuntimeHandle{ID: term.HandleID}
				if err := rt.SendMessage(ctx, handle, "exit"); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					alive, err := rt.IsChildAlive(ctx, handle)
					if err != nil {
						t.Fatal(err)
					}
					if !alive {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("requested shell exited but tmux child remains alive")
					}
					time.Sleep(20 * time.Millisecond)
				}
				if alive, err := rt.IsAlive(ctx, handle); err != nil || alive != tc.retainPane {
					t.Fatalf("host after child exit = %v, %v; want retained=%v", alive, err, tc.retainPane)
				}
				if reconcile == "desktop relaunch" {
					if n, err := svc.ReapShellTerminalsFromPreviousAppRuns(ctx); err != nil || n != 1 {
						t.Fatalf("exited reap = %d, %v", n, err)
					}
				} else if listed, err := svc.ListShellTerminalsForCurrentAppRun(ctx); err != nil || len(listed) != 0 {
					t.Fatalf("exited list = %+v, %v", listed, err)
				}
				if len(st.records) != 0 {
					t.Fatalf("exited shell row retained: %+v", st.records)
				}
				// Destroy clears the runtime's socket cache. Inspect this exact
				// fixture server instead of probing an uncached legacy fallback.
				if sessions := tmuxCommand("list-sessions", "-F", "#{session_name}"); sessions != "anchor\n" {
					t.Fatalf("fixture sessions after reconciliation = %q, want only anchor", sessions)
				}
			})
		}
	}
}
