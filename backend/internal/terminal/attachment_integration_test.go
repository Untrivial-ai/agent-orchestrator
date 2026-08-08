//go:build !windows

package terminal

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestAttachmentStreamsRealTmuxPane attaches a real PTY to a real tmux session
// and asserts output streams back, then that killing the session stops the
// attachment without a re-attach storm. Skipped when tmux is unavailable.
func TestAttachmentStreamsRealTmuxPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	// See TestAttachmentReattachAdoptsNewSize: tmux needs a usable TERM to attach.
	t.Setenv("TERM", "xterm-256color")

	name := "ao-term-it-" + strconv.Itoa(os.Getpid())
	rt := tmux.New(tmux.Options{Timeout: 10 * time.Second})
	handle, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     domain.SessionID(name),
		WorkspacePath: t.TempDir(),
		Argv:          []string{"sh", "-lc", "printf AO_READY\\n; exec sh -i"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = rt.Destroy(context.Background(), handle) })

	var got safeBytes
	a := newAttachment(name, handle, rt, nil, got.add, nil, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	// Type a unique marker and expect it echoed back through the PTY.
	eventually(t, 3*time.Second, func() bool { return a.writeLeased([]byte("echo AO_MARKER_42\n"), nil) == nil })
	eventually(t, 5*time.Second, func() bool { return strings.Contains(got.string(), "AO_MARKER_42") })

	// Kill the session: the attachment must observe it as gone and not re-attach.
	if err := rt.Destroy(context.Background(), handle); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	eventually(t, 5*time.Second, func() bool { return a.isExited() })
}

// TestAttachmentReattachAdoptsNewSize is the end-to-end regression for the
// stale-size desync: client A holds the session at one grid, detaches, and
// client B immediately attaches at a different grid (the frontend's
// remount/reconnect flow). B's tmux client must adopt B's size, not A's.
func TestAttachmentReattachAdoptsNewSize(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	// tmux refuses to attach a client without a usable TERM, printing
	// "open terminal failed: terminal does not support clear". The daemon sets a
	// default TERM in production (Finder-launched attach fix); CI runners have
	// none, so set it here to match the real environment.
	t.Setenv("TERM", "xterm-256color")

	name := "ao-term-size-it-" + strconv.Itoa(os.Getpid())
	rt := tmux.New(tmux.Options{Timeout: 10 * time.Second})
	handle, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     domain.SessionID(name),
		WorkspacePath: t.TempDir(),
		Argv:          []string{"sh", "-lc", "printf AO_READY\\n; exec sh -i"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = rt.Destroy(context.Background(), handle) })

	attachAt := func(rows, cols uint16) (*attachment, *safeBytes, <-chan struct{}, context.CancelFunc) {
		var got safeBytes
		opened := make(chan struct{})
		a := newAttachment(name, handle, rt, func() { close(opened) }, got.add, nil, testLogger())
		if err := a.resize(rows, cols); err != nil {
			t.Fatalf("record size: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go a.run(ctx)
		return a, &got, opened, cancel
	}

	// Client A at 115x37: wait for the pane shell, then detach.
	a, _, openedA, cancelA := attachAt(37, 115)
	select {
	case <-openedA:
	case <-time.After(10 * time.Second):
		t.Fatal("client A did not attach")
	}
	a.close()
	cancelA()

	// Client B re-attaches immediately at 148x40. The inner pane must see B's
	// grid (tmux may shave a row/col; assert cols land near 148 and far from 115).
	b, gotB, openedB, cancelB := attachAt(40, 148)
	defer cancelB()
	defer b.close()
	select {
	case <-openedB:
	case <-time.After(10 * time.Second):
		t.Fatal("client B did not attach")
	}

	// Drive the reattached shell until it reports its width. We RESEND the probe
	// each iteration: onOpen means the stream accepts input, not that the inner
	// `sh -i` is already at a prompt reading stdin after the reattach, so an early
	// keystroke can be dropped; retrying covers that. Real tmux + shell output is
	// also slow under -race on CI, hence the long deadline. On timeout we dump
	// exactly what the pane produced so the failure is self-explaining (e.g. the
	// probe echoed but never executed, or stty errored).
	var captured string
	gotWidth := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_ = b.writeLeased([]byte("echo SIZE:$(stty size)\n"), nil)
		time.Sleep(250 * time.Millisecond)
		captured = gotB.string()
		i := strings.LastIndex(captured, "SIZE:")
		if i < 0 {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(captured[i:], "SIZE:"))
		if len(fields) < 2 {
			continue
		}
		cols, err := strconv.Atoi(strings.TrimFunc(fields[1], func(r rune) bool { return r < '0' || r > '9' }))
		if err != nil {
			continue
		}
		if cols > 130 { // B's 148 minus any tmux chrome; a stale A-layout reports <=115
			gotWidth = true
			break
		}
	}
	if !gotWidth {
		t.Fatalf("reattached pane never reported B's width (cols>130) within 30s; captured:\n%q", captured)
	}
}
