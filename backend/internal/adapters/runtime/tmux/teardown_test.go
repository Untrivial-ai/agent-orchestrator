package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func testOwnedProcess(pid, parent int, session string) ownedProcess {
	return ownedProcess{processIdentity: processIdentity{PID: pid, Start: "boot:100", Session: session}, Parent: parent}
}

func TestDestroyRejectsUnknownInventory(t *testing.T) {
	for _, problem := range []string{"pane query", "invalid pid", "missing dead state", "invalid dead state", "process query", "missing process"} {
		t.Run(problem, func(t *testing.T) {
			r, fr := newTestRuntime(t, 0)
			fr.outputs = [][]byte{[]byte("4242 0\n")}
			switch problem {
			case "pane query":
				fr.err = errors.New("inventory unavailable")
			case "invalid pid":
				fr.outputs = [][]byte{[]byte("1 0\n")}
			case "missing dead state":
				fr.outputs = [][]byte{[]byte("4242\n")}
			case "invalid dead state":
				fr.outputs = [][]byte{[]byte("4242 unknown\n")}
			case "process query":
				r.processes = func(context.Context) ([]ownedProcess, error) { return nil, errors.New("process inventory unavailable") }
			}
			if err := r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err == nil {
				t.Fatal("unknown process ownership must not report successful teardown")
			}
			if countCalls(fr, "kill-session") != 0 {
				t.Fatal("pane removed before process ownership was established")
			}
		})
	}
}

func TestDestroyRetainsUnanchoredSessionAcrossOtherProcessExit(t *testing.T) {
	r, fr := newTestRuntime(t, 0)
	fr.outputs = [][]byte{[]byte("4242 0\n"), nil}
	pane := testOwnedProcess(4242, 1, "4242")
	detached := testOwnedProcess(4243, 4242, "4243")
	orphan := testOwnedProcess(4244, 1, "4243")
	table := []ownedProcess{pane, detached}
	r.processes = func(context.Context) ([]ownedProcess, error) { return table, nil }
	var signalled []int
	r.signalProcess = func(_ context.Context, p ownedProcess, _ bool) error {
		signalled = append(signalled, p.PID)
		switch p.PID {
		case pane.PID:
			// The detached process forks and exits before the next scan,
			// while the pane remains a live ownership anchor elsewhere.
			table = []ownedProcess{pane, orphan}
		case detached.PID:
			return nil // already exited; signal implementations check birth
		default:
			t.Fatalf("unproven process signalled: %+v", p)
		}
		return nil
	}
	r.reapGrace = time.Millisecond
	// On escalation the pane exits; the vanished detached-session evidence
	// must not have been dropped while the pane was still alive.
	signal := r.signalProcess
	r.signalProcess = func(ctx context.Context, p ownedProcess, force bool) error {
		if force && p.PID == pane.PID {
			table = []ownedProcess{orphan}
			return nil
		}
		return signal(ctx, p, force)
	}
	if err := r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err == nil || !strings.Contains(err.Error(), "cleanup unconfirmed") {
		t.Fatalf("Destroy = %v, want retained uncertainty", err)
	}
	pending, err := r.loadTeardown("sess-1")
	if err != nil || pending == nil || !containsIdentity(pending.Processes, detached.processIdentity) {
		t.Fatalf("lost detached-session evidence: %+v, %v", pending, err)
	}
	if len(signalled) == 0 {
		t.Fatal("fixture never entered shutdown")
	}

	r2, fr2 := newTestRuntime(t, 0)
	r2.cleanupDir = r.cleanupDir
	fr2.err = &exec.ExitError{}
	fr2.outputs = [][]byte{[]byte("can't find session: sess-1"), []byte("can't find session: sess-1")}
	r2.processes = func(context.Context) ([]ownedProcess, error) { return table, nil }
	r2.signalProcess = func(context.Context, ownedProcess, bool) error {
		t.Fatal("retry signalled unproven process")
		return nil
	}
	if err := r2.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err == nil {
		t.Fatal("daemon restart lost uncertainty")
	}
	table = nil
	fr2.outputs = [][]byte{[]byte("can't find session: sess-1"), []byte("can't find session: sess-1")}
	if err := r2.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err != nil {
		t.Fatalf("retry after orphan exit: %v", err)
	}
}

func TestDestroyRetainedDeadPane(t *testing.T) {
	for _, tc := range []struct {
		name      string
		processes []ownedProcess
		uncertain bool
	}{
		{name: "reaped leader"},
		{name: "recycled pid in another session", processes: []ownedProcess{testOwnedProcess(4242, 1, "9000")}},
		{name: "unowned orphan", processes: []ownedProcess{testOwnedProcess(4243, 1, "4242")}, uncertain: true},
		{name: "recycled pid and session", processes: []ownedProcess{testOwnedProcess(4242, 1, "4242")}, uncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, fr := newTestRuntime(t, 0)
			fr.outputs = [][]byte{[]byte("4242 1\n"), nil}
			r.processes = func(context.Context) ([]ownedProcess, error) { return tc.processes, nil }
			r.signalProcess = func(context.Context, ownedProcess, bool) error {
				t.Fatal("dead pane conferred signal authority")
				return nil
			}
			err := r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"})
			if (err != nil) != tc.uncertain {
				t.Fatalf("Destroy = %v, uncertainty=%v", err, tc.uncertain)
			}
			if countCalls(fr, "kill-session") != 1 {
				t.Fatal("retained pane was not removed")
			}
			pending, loadErr := r.loadTeardown("sess-1")
			if loadErr != nil || (pending != nil) != tc.uncertain {
				t.Fatalf("pending = %+v, %v", pending, loadErr)
			}
			if tc.uncertain {
				if err := r.requireCompletedTeardown("sess-1"); err == nil {
					t.Fatal("uncertain dead pane cleanup allows runtime reuse")
				}
			}
		})
	}
}

func TestRetainIdentitiesPreservesPreviousSession(t *testing.T) {
	original := testOwnedProcess(100, 1, "100")
	escaped := original
	escaped.Session = "200"
	known := retainIdentities([]processIdentity{original.processIdentity}, []ownedProcess{escaped})
	if len(known) != 2 || known[0] != original.processIdentity || known[1] != escaped.processIdentity {
		t.Fatalf("session change lost ownership history: %+v", known)
	}
}

func TestDestroyVerifiesExitAfterForcedSignal(t *testing.T) {
	r, fr := newTestRuntime(t, 0)
	fr.outputs = [][]byte{[]byte("4242 0\n"), nil}
	r.reapGrace = time.Millisecond
	forced, probesAfterKill := false, 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.processes = func(context.Context) ([]ownedProcess, error) {
		if forced {
			probesAfterKill++
			cancel()
		}
		return []ownedProcess{testOwnedProcess(4242, 1, "pane")}, nil
	}
	r.signalProcess = func(_ context.Context, _ ownedProcess, force bool) error { forced = force; return nil }
	err := r.Destroy(ctx, ports.RuntimeHandle{ID: "sess-1"})
	if !errors.Is(err, context.Canceled) || !forced || probesAfterKill == 0 {
		t.Fatalf("Destroy = %v, forced=%v, post-KILL probes=%d", err, forced, probesAfterKill)
	}
	if _, err := os.Stat(r.teardownPath("sess-1")); err != nil {
		t.Fatalf("failed teardown lost retry evidence: %v", err)
	}
}

func TestDestroyRetainsOwnershipAcrossDaemonRestart(t *testing.T) {
	r, fr := newTestRuntime(t, 0)
	fr.outputs = [][]byte{[]byte("4242 0\n"), nil}
	pane := testOwnedProcess(4242, 1, "pane")
	child := testOwnedProcess(4243, 4242, "pane")
	other := testOwnedProcess(5000, 1, "unrelated")
	table := []ownedProcess{pane, child, other}
	r.processes = func(context.Context) ([]ownedProcess, error) { return table, nil }
	r.signalProcess = func(context.Context, ownedProcess, bool) error { return errors.New("signal permission denied") }
	if err := r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err == nil {
		t.Fatal("failed signal reported success")
	}
	// Pane and daemon disappeared. Only the retained child identity can find
	// the workload; a numeric pane PID may already belong to somebody else.
	r2, fr2 := newTestRuntime(t, 0)
	r2.cleanupDir = r.cleanupDir
	fr2.outputs = [][]byte{[]byte("can't find session: sess-1"), []byte("can't find session: sess-1")}
	fr2.err = &exec.ExitError{}
	pane.Start = "boot:200"
	pane.Session = "reused"
	child.Parent = 1
	table = []ownedProcess{pane, child, other}
	r2.processes = func(context.Context) ([]ownedProcess, error) { return table, nil }
	var signalled []int
	r2.signalProcess = func(_ context.Context, p ownedProcess, _ bool) error {
		signalled = append(signalled, p.PID)
		table = []ownedProcess{pane, other}
		return nil
	}
	if err := r2.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(signalled, []int{4243}) {
		t.Fatalf("signalled %v, want only original child", signalled)
	}
	if _, err := os.Stat(r.teardownPath("sess-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified cleanup retained stale record: %v", err)
	}
	fr2.outputs = [][]byte{[]byte("can't find session: sess-1"), []byte("can't find session: sess-1")}
	if err := r2.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
}

func TestDestroyRefusesReplacementPane(t *testing.T) {
	r, fr := newTestRuntime(t, 0)
	old := testOwnedProcess(4242, 1, "pane")
	if err := r.saveTeardown(context.Background(), &pendingTeardown{Version: 1, ID: "sess-1", Panes: []processIdentity{old.processIdentity}, Processes: []processIdentity{old.processIdentity}}); err != nil {
		t.Fatal(err)
	}
	fr.outputs = [][]byte{[]byte("4242 0\n")}
	old.Start = "boot:200"
	r.processes = func(context.Context) ([]ownedProcess, error) { return []ownedProcess{old}, nil }
	err := r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"})
	if err == nil || !strings.Contains(err.Error(), "generation changed") || countCalls(fr, "kill-session") != 0 {
		t.Fatalf("replacement pane teardown: %v, calls=%v", err, fr.calls)
	}
}

func TestDestroyRetainsEvidenceOnProbeFailure(t *testing.T) {
	r, fr := newTestRuntime(t, 0)
	fr.outputs = [][]byte{[]byte("4242 0\n"), nil}
	calls := 0
	r.processes = func(context.Context) ([]ownedProcess, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("process inventory unavailable")
		}
		return []ownedProcess{testOwnedProcess(4242, 1, "pane")}, nil
	}
	if err := r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err == nil {
		t.Fatal("failed exit probe reported success")
	}
	if _, err := r.loadTeardown("sess-1"); err != nil {
		t.Fatal(err)
	}
}

func TestDestroyRequiresDurableOwnershipBeforeRemovingPane(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "unwritable", true: "corrupt"}[corrupt], func(t *testing.T) {
			r, fr := newTestRuntime(t, 0)
			if corrupt {
				if err := os.MkdirAll(r.cleanupDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(r.teardownPath("sess-1"), []byte(`{"version":1,"id":"sess-1","panes":[{"pid":1}]}`), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				r.cleanupDir = filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(r.cleanupDir, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err == nil {
				t.Fatal("unrecoverable ownership must stop teardown")
			}
			if countCalls(fr, "kill-session") != 0 {
				t.Fatal("removed pane without recoverable evidence")
			}
		})
	}
}

func TestOwnedDescendantsIncludesEscapedGroupAndRejectsReusedIdentity(t *testing.T) {
	pane := testOwnedProcess(100, 1, "pane")
	background := testOwnedProcess(101, 1, "pane")
	newSession := testOwnedProcess(102, 100, "child-session")
	grandchild := testOwnedProcess(103, 102, "child-session")
	unrelated := testOwnedProcess(104, 1, "other")
	table := []ownedProcess{pane, background, grandchild, newSession, unrelated}
	got := ownedDescendants(table, []processIdentity{pane.processIdentity})
	if len(got) != 4 {
		t.Fatalf("owned=%v, want all four descendants/session members", got)
	}
	pane.Start = "boot:200"
	if got := ownedDescendants(table, []processIdentity{pane.processIdentity}); len(got) != 0 {
		t.Fatalf("reused PID conferred ownership: %v", got)
	}
}

func TestDestroyTreatsZombiesAsExited(t *testing.T) {
	r, fr := newTestRuntime(t, 0)
	fr.outputs = [][]byte{[]byte("4242 0\n"), nil}
	p := testOwnedProcess(4242, 1, "pane")
	p.Stopped = true
	r.processes = func(context.Context) ([]ownedProcess, error) { return []ownedProcess{p}, nil }
	if err := r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedDescendantsTracksProcessThatChangesSession(t *testing.T) {
	original := testOwnedProcess(100, 1, "pane")
	escaped := original
	escaped.Session = "detached-session"
	child := testOwnedProcess(101, 1, "detached-session")
	got := ownedDescendants([]ownedProcess{escaped, child}, []processIdentity{original.processIdentity})
	if len(got) != 2 {
		t.Fatalf("changing OS session lost known process ownership: %+v", got)
	}
}

func TestPendingTeardownPreventsRuntimeReuse(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "restart"}[restart], func(t *testing.T) {
			r, fr := newTestRuntime(t, 0)
			p := testOwnedProcess(100, 1, "pane").processIdentity
			if err := r.saveTeardown(context.Background(), &pendingTeardown{Version: 1, ID: "sess-1", Panes: []processIdentity{p}, Processes: []processIdentity{p}}); err != nil {
				t.Fatal(err)
			}
			cfg := ports.RuntimeConfig{SessionID: "sess-1", WorkspacePath: t.TempDir(), Argv: []string{"worker"}}
			var err error
			if restart {
				_, err = r.Restart(context.Background(), ports.RuntimeHandle{ID: "sess-1"}, cfg)
			} else {
				_, err = r.Create(context.Background(), cfg)
			}
			if err == nil || len(fr.calls) != 0 {
				t.Fatalf("runtime reused before pending teardown: %v, calls=%v", err, fr.calls)
			}
		})
	}
}

func TestRuntimeReplacementWaitsForVerifiedTeardown(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "restart"}[restart], func(t *testing.T) {
			r, fr := newTestRuntime(t, 0)
			fr.outputs = [][]byte{[]byte("4242 0\n"), nil}
			discovered, release := make(chan struct{}), make(chan struct{})
			var exitProbed, earlyLaunch atomic.Bool
			calls := 0
			r.processes = func(context.Context) ([]ownedProcess, error) {
				calls++
				if calls == 1 {
					close(discovered)
					<-release
					return []ownedProcess{testOwnedProcess(4242, 1, "pane")}, nil
				}
				exitProbed.Store(true)
				return nil, nil
			}
			launchErr := errors.New("fixture launch rejected")
			fr.hook = func(_ context.Context, n int) error {
				command := fr.calls[n-1].args[0]
				if command == "new-session" || command == "respawn-pane" {
					earlyLaunch.Store(!exitProbed.Load())
					return launchErr
				}
				return nil
			}
			destroyed := make(chan error, 1)
			go func() { destroyed <- r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}) }()
			<-discovered
			launched := make(chan error, 1)
			go func() {
				cfg := ports.RuntimeConfig{SessionID: "sess-1", WorkspacePath: "/workspace", Argv: []string{"worker"}}
				var err error
				if restart {
					_, err = r.Restart(context.Background(), ports.RuntimeHandle{ID: "sess-1"}, cfg)
				} else {
					_, err = r.Create(context.Background(), cfg)
				}
				launched <- err
			}()
			select {
			case err := <-launched:
				close(release)
				<-destroyed
				t.Fatalf("replacement proceeded during teardown: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			if err := <-destroyed; err != nil {
				t.Fatal(err)
			}
			if err := <-launched; !errors.Is(err, launchErr) || earlyLaunch.Load() {
				t.Fatalf("replacement=%v, before exit=%v", err, earlyLaunch.Load())
			}
		})
	}
}

func TestRuntimeMutationWaitHonorsCancellation(t *testing.T) {
	for _, operation := range []string{"create", "restart", "destroy"} {
		t.Run(operation, func(t *testing.T) {
			r, fr := newTestRuntime(t, 0)
			fr.outputs = [][]byte{[]byte("4242 0\n"), nil}
			discovered, release := make(chan struct{}), make(chan struct{})
			calls := 0
			r.processes = func(context.Context) ([]ownedProcess, error) {
				calls++
				if calls == 1 {
					close(discovered)
					<-release
					return []ownedProcess{testOwnedProcess(4242, 1, "pane")}, nil
				}
				return nil, nil
			}
			destroyed := make(chan error, 1)
			go func() { destroyed <- r.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-1"}) }()
			<-discovered
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waiting := make(chan error, 1)
			go func() {
				cfg := ports.RuntimeConfig{SessionID: "sess-1", WorkspacePath: "/workspace", Argv: []string{"worker"}}
				var err error
				switch operation {
				case "create":
					_, err = r.Create(ctx, cfg)
				case "restart":
					_, err = r.Restart(ctx, ports.RuntimeHandle{ID: "sess-1"}, cfg)
				case "destroy":
					err = r.Destroy(ctx, ports.RuntimeHandle{ID: "sess-1"})
				}
				waiting <- err
			}()
			cancel()
			timedOut := false
			select {
			case err := <-waiting:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("waiting mutation returned %v, want cancellation", err)
				}
			case <-time.After(time.Second):
				timedOut = true
				t.Error("cancelled mutation remained blocked behind teardown")
			}
			close(release)
			if err := <-destroyed; err != nil {
				t.Fatal(err)
			}
			if timedOut {
				<-waiting
			}
		})
	}
}
