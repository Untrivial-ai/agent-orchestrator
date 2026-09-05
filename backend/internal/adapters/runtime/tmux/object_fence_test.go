package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	objectFenceSessionID = "sess-1"
	objectFenceLaunchID  = "launch-owned"
	objectFenceServerPID = 41001
	objectFenceTmuxSID   = "$41"
	objectFenceTmuxPID   = "%73"
)

type replacementAfterProofRunner struct {
	calls          []runnerCall
	replaced       bool
	foreignTouched bool
}

type replacementBeforeResolveRunner struct {
	calls []runnerCall
}

func (r *replacementBeforeResolveRunner) Run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, runnerCall{env: append([]string(nil), env...), name: name, args: append([]string(nil), args...)})
	command := tmuxCommandArgs(args)
	if len(command) == 0 {
		return nil, errors.New("missing tmux command")
	}
	switch command[0] {
	case "has-session":
		return nil, nil
	case "list-panes":
		return []byte(fmt.Sprintf("%d\t%s\t%s\t", objectFenceServerPID+1, objectFenceTmuxSID, objectFenceTmuxPID) +
			ownedPaneCommand("/tmp/ao/running.json", objectFenceSessionID, objectFenceLaunchID) + "\n"), nil
	default:
		return nil, fmt.Errorf("replacement reached unexpected command %s", command[0])
	}
}

func (r *replacementAfterProofRunner) Run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, runnerCall{env: append([]string(nil), env...), name: name, args: append([]string(nil), args...)})
	command := tmuxCommandArgs(args)
	if len(command) == 0 {
		return nil, errors.New("missing tmux command")
	}
	switch command[0] {
	case "has-session":
		return nil, nil
	case "list-panes":
		format := command[len(command)-1]
		if format == paneIdentityFormat && !r.replaced {
			r.replaced = true
			return []byte(fmt.Sprintf("%d\t%s\t%s\t", objectFenceServerPID, objectFenceTmuxSID, objectFenceTmuxPID) +
				ownedPaneCommand("/tmp/ao/running.json", objectFenceSessionID, objectFenceLaunchID) + "\n"), nil
		}
	case "if-shell":
		if r.replaced {
			return []byte(tmuxObjectFenceMismatch + "\n"), nil
		}
	}

	if r.replaced && command[0] != "has-session" && command[0] != "list-panes" {
		r.foreignTouched = true
		return []byte("foreign replacement accepted the name target"), nil
	}
	return nil, nil
}

func commandTarget(command []string) string {
	for i := 0; i+1 < len(command); i++ {
		if command[i] == "-t" {
			return command[i+1]
		}
	}
	return ""
}

func newReplacementFenceRuntime(t *testing.T) (*Runtime, ports.RuntimeHandle, *replacementAfterProofRunner, *recordingReaper) {
	t.Helper()
	owner := ports.SupervisedProcessRef{SessionID: objectFenceSessionID, LaunchID: objectFenceLaunchID}
	handle, err := qualifiedRuntimeHandleForRoute(runtimeRoute{
		id:            objectFenceSessionID,
		target:        socketTarget{kind: socketTargetNamed, value: "ao"},
		qualified:     true,
		owner:         owner,
		tmuxServerPID: objectFenceServerPID,
		tmuxSessionID: objectFenceTmuxSID,
		tmuxPaneID:    objectFenceTmuxPID,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &replacementAfterProofRunner{}
	reaper := &recordingReaper{}
	runtime := New(Options{
		Binary:      "tmux-test",
		SocketName:  "ao",
		RunFilePath: "/tmp/ao/running.json",
		Timeout:     time.Second,
		Shell:       "/bin/sh",
	})
	runtime.runner = runner
	runtime.reapSessions = reaper.reap
	runtime.enterDelay = 0
	return runtime, handle, runner, reaper
}

func TestCanonicalOperationsFailClosedWhenServerIsReplacedAfterOwnerProof(t *testing.T) {
	tests := []struct {
		name       string
		operation  func(*Runtime, ports.RuntimeHandle) error
		subcommand string
		wantTarget string
	}{
		{
			name: "send-message",
			operation: func(runtime *Runtime, handle ports.RuntimeHandle) error {
				return runtime.SendMessage(context.Background(), handle, "continue")
			},
			subcommand: "send-keys",
			wantTarget: objectFenceTmuxPID,
		},
		{
			name: "send-input",
			operation: func(runtime *Runtime, handle ports.RuntimeHandle) error {
				return runtime.SendInput(context.Background(), handle, "continue")
			},
			subcommand: "send-keys",
			wantTarget: objectFenceTmuxPID,
		},
		{
			name: "interrupt",
			operation: func(runtime *Runtime, handle ports.RuntimeHandle) error {
				return runtime.Interrupt(context.Background(), handle)
			},
			subcommand: "send-keys",
			wantTarget: objectFenceTmuxPID,
		},
		{
			name: "get-output",
			operation: func(runtime *Runtime, handle ports.RuntimeHandle) error {
				_, err := runtime.GetOutput(context.Background(), handle, 10)
				return err
			},
			subcommand: "capture-pane",
			wantTarget: objectFenceTmuxPID,
		},
		{
			name: "get-styled-output",
			operation: func(runtime *Runtime, handle ports.RuntimeHandle) error {
				_, err := runtime.GetStyledOutput(context.Background(), handle, 10)
				return err
			},
			subcommand: "capture-pane",
			wantTarget: objectFenceTmuxPID,
		},
		{
			name: "respawn-pane",
			operation: func(runtime *Runtime, handle ports.RuntimeHandle) error {
				_, err := runtime.Restart(context.Background(), handle, ports.RuntimeConfig{
					SessionID:     objectFenceSessionID,
					WorkspacePath: "/tmp/worktree",
					Argv:          []string{"codex"},
					Env: map[string]string{
						"AO_RUN_FILE":           "/tmp/ao/running.json",
						"AO_SESSION_ID":         objectFenceSessionID,
						"AO_SUPERVISED_PROCESS": "1",
						runtimeLaunchEnv:        "launch-next",
					},
				})
				return err
			},
			subcommand: "respawn-pane",
			wantTarget: objectFenceTmuxPID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, handle, runner, _ := newReplacementFenceRuntime(t)
			if err := tt.operation(runtime, handle); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
				t.Fatalf("operation error = %v, want ErrRuntimeProbeInconclusive", err)
			}
			if runner.foreignTouched {
				t.Fatal("operation targeted the same-name foreign replacement")
			}
			var guarded []string
			for _, call := range runner.calls {
				command := tmuxCommandArgs(call.args)
				if len(command) > 0 && command[0] == "if-shell" {
					guarded = command
				}
			}
			if got := commandTarget(guarded); got != tt.wantTarget {
				t.Fatalf("guard target = %q, want immutable %q; command=%#v", got, tt.wantTarget, guarded)
			}
			if len(guarded) < 7 || !strings.Contains(guarded[4], fmt.Sprint(objectFenceServerPID)) ||
				!strings.Contains(guarded[5], `"`+tt.subcommand+`"`) {
				t.Fatalf("guard does not bind server pid and %s action: %#v", tt.subcommand, guarded)
			}
		})
	}
}

func TestCanonicalResolversRejectReplacementServerReusingObjectIDsAndOwner(t *testing.T) {
	owner := ports.SupervisedProcessRef{SessionID: objectFenceSessionID, LaunchID: objectFenceLaunchID}
	tests := []struct {
		name    string
		resolve func(*Runtime, ports.RuntimeHandle) (ports.RuntimeHandle, bool, error)
	}{
		{
			name: "recovery",
			resolve: func(runtime *Runtime, handle ports.RuntimeHandle) (ports.RuntimeHandle, bool, error) {
				return runtime.ResolveRuntimeHandle(context.Background(), handle, owner)
			},
		},
		{
			name: "terminated reap",
			resolve: func(runtime *Runtime, handle ports.RuntimeHandle) (ports.RuntimeHandle, bool, error) {
				return runtime.ResolveExactRuntimeHandle(context.Background(), handle, owner)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, handle, _, _ := newReplacementFenceRuntime(t)
			runner := &replacementBeforeResolveRunner{}
			runtime.runner = runner
			resolved, found, err := tt.resolve(runtime, handle)
			if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) || found || resolved.ID != "" {
				t.Fatalf("resolve = (%q, %v, %v), want empty, false, ErrRuntimeProbeInconclusive", resolved.ID, found, err)
			}
			for _, call := range runner.calls {
				if got := probeNamespace(call.args); got != "named:ao" {
					t.Fatalf("canonical resolver rediscovered another namespace %q", got)
				}
			}
		})
	}
}

func TestOwnerProofTargetsImmutableSessionForAttachAfterNameReplacement(t *testing.T) {
	runtime, handle, runner, _ := newReplacementFenceRuntime(t)
	var attachArgv []string
	runtime.spawnAttach = func(_ context.Context, argv, _ []string, _, _ uint16) (ports.Stream, error) {
		attachArgv = append([]string(nil), argv...)
		return nil, fmt.Errorf("%w: immutable session disappeared", ports.ErrRuntimeProbeInconclusive)
	}
	if _, err := runtime.Attach(context.Background(), handle, 50, 220); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Attach error = %v, want ErrRuntimeProbeInconclusive", err)
	}
	if runner.foreignTouched {
		t.Fatal("Attach proof touched the same-name foreign replacement")
	}
	command := tmuxCommandArgs(attachArgv[1:])
	if got := commandTarget(command); got != objectFenceTmuxPID {
		t.Fatalf("attach guard target = %q, want immutable %q; argv=%#v", got, objectFenceTmuxPID, attachArgv)
	}
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, `"attach-session"`) ||
		!strings.Contains(joined, `"\$41"`) {
		t.Fatalf("attach was not queued behind the object guard: %#v", command)
	}
}

func TestDestroyUsesImmutableSessionForPaneListAndKillAfterNameReplacement(t *testing.T) {
	runtime, handle, runner, reaper := newReplacementFenceRuntime(t)
	if err := runtime.Destroy(context.Background(), handle); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Destroy error = %v, want ErrRuntimeProbeInconclusive", err)
	}
	if runner.foreignTouched {
		t.Fatal("Destroy targeted the same-name foreign replacement")
	}
	var guarded []string
	for _, call := range runner.calls {
		command := tmuxCommandArgs(call.args)
		if len(command) > 0 && command[0] == "if-shell" {
			guarded = command
		}
	}
	if len(guarded) < 7 || commandTarget(guarded) != objectFenceTmuxPID ||
		!strings.Contains(guarded[5], `"list-panes"`) || !strings.Contains(guarded[5], `"kill-session"`) {
		t.Fatalf("destroy was not one guarded list+kill queue: %#v", guarded)
	}
	for _, pids := range reaper.pids {
		if len(pids) != 0 {
			t.Fatalf("foreign replacement descendants were reaped: %v", pids)
		}
	}
}

func TestResolveExactWithoutHistoricalLaunchConcludesOnlyExhaustiveAbsence(t *testing.T) {
	const historicalSocket = "/tmp/ao-legacy-private.sock"
	missing := fakeRunnerResult{out: []byte("can't find session: " + objectFenceSessionID), err: &exec.ExitError{}}
	tests := []struct {
		name      string
		primary   fakeRunnerResult
		wantError bool
	}{
		{name: "absent from every namespace", primary: missing},
		{name: "same name still exists", primary: fakeRunnerResult{}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &namespaceProbeRunner{results: map[string]fakeRunnerResult{
				"named:ao":                 tt.primary,
				"path:" + historicalSocket: missing,
				"default":                  missing,
			}}
			runtime := New(Options{
				Binary:           "bundled-tmux-test",
				LegacyBinary:     "system-tmux-test",
				SocketName:       "ao",
				LegacySocketPath: historicalSocket,
				RunFilePath:      "/tmp/ao/running.json",
				Timeout:          time.Second,
			})
			runtime.runner = runner
			resolved, found, err := runtime.ResolveExactRuntimeHandle(
				context.Background(),
				ports.RuntimeHandle{ID: objectFenceSessionID},
				ports.SupervisedProcessRef{SessionID: objectFenceSessionID},
			)
			if tt.wantError {
				if found || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
					t.Fatalf("ResolveExactRuntimeHandle = (%q, %v, %v), want missing-launch failure", resolved.ID, found, err)
				}
			} else if err != nil || found || resolved.ID != "" {
				t.Fatalf("ResolveExactRuntimeHandle = (%q, %v, %v), want conclusive absence", resolved.ID, found, err)
			}
			assertOnlyLegacyProbes(t, runner.calls, "named:ao", "path:"+historicalSocket, "default")
			for _, call := range runner.calls {
				if strings.Contains(strings.Join(call.args, " "), "list-panes") {
					t.Fatalf("empty-launch absence check inspected pane ownership: %+v", call)
				}
			}
		})
	}
}
