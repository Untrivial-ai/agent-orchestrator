package terminal

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestAttachment(src Source, onData func([]byte), onExit func()) *attachment {
	return newTestAttachmentWithOpen(src, nil, onData, onExit)
}

func newTestAttachmentWithOpen(src Source, onOpen func(), onData func([]byte), onExit func()) *attachment {
	return newAttachment("t1", ports.RuntimeHandle{ID: "t1"}, src, onOpen, onData, onExit, testLogger())
}

func currentPTY(a *attachment) ports.Stream {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pty
}

func TestAttachmentStreamsOutputToSink(t *testing.T) {
	pty := newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{pty}}
	src := &fakeSource{alive: true, spawner: sp}

	var sink safeBytes
	a := newTestAttachment(src, sink.add, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	pty.push([]byte("hello"))
	eventually(t, time.Second, func() bool { return sink.string() == "hello" })
}

type exitedProcessStream struct{}

func (exitedProcessStream) Read([]byte) (int, error)       { return 0, ports.ErrRuntimeProcessExited }
func (exitedProcessStream) Write(p []byte) (int, error)    { return len(p), nil }
func (exitedProcessStream) Close() error                   { return nil }
func (exitedProcessStream) Resize(rows, cols uint16) error { return nil }

func TestAttachmentReportsDefinitiveHostedProcessExitWithoutReattach(t *testing.T) {
	exited := make(chan struct{})
	attachCalls := 0
	src := &fakeSource{alive: true, attachFn: func(context.Context, uint16, uint16) (ports.Stream, error) {
		attachCalls++
		return exitedProcessStream{}, nil
	}}
	a := newTestAttachment(src, nil, func() { close(exited) })

	go a.run(context.Background())
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("definitive hosted process exit was not reported")
	}
	if attachCalls != 1 {
		t.Fatalf("attach calls = %d, want 1", attachCalls)
	}
}

func TestAttachmentWriteAndResizeReachPTY(t *testing.T) {
	pty := newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{pty}}
	src := &fakeSource{alive: true, spawner: sp}
	a := newTestAttachment(src, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	eventually(t, time.Second, func() bool { return a.writeLeased([]byte("ls\n"), nil) == nil })
	eventually(t, time.Second, func() bool { return string(pty.writtenBytes()) == "ls\n" })

	if err := a.resize(24, 80); err != nil {
		t.Fatalf("resize: %v", err)
	}
	eventually(t, time.Second, func() bool {
		rs := pty.resizeCalls()
		return len(rs) == 1 && rs[0] == [2]uint16{24, 80}
	})
}

func TestAttachmentSignalsOpenOnlyAfterPTYIsPublished(t *testing.T) {
	pty := newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{pty}}
	src := &fakeSource{alive: true, spawner: sp}

	opened := make(chan bool, 1)
	var a *attachment
	a = newTestAttachmentWithOpen(src, func() {
		opened <- currentPTY(a) == pty
	}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	select {
	case sawPTY := <-opened:
		if !sawPTY {
			t.Fatal("open callback fired before the PTY was published")
		}
	case <-time.After(time.Second):
		t.Fatal("open callback did not fire")
	}
}

func TestAttachmentBuffersInputUntilPTYReady(t *testing.T) {
	pty := newFakePTY()
	spawnStarted := make(chan struct{})
	releaseSpawn := make(chan struct{})
	src := &fakeSource{alive: true, attachFn: func(context.Context, uint16, uint16) (ports.Stream, error) {
		close(spawnStarted)
		<-releaseSpawn
		return pty, nil
	}}
	a := newTestAttachment(src, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	select {
	case <-spawnStarted:
	case <-time.After(time.Second):
		t.Fatal("spawn was not reached")
	}
	if err := a.writeLeased([]byte("hello\n"), nil); err != nil {
		t.Fatalf("write before PTY ready: %v", err)
	}
	close(releaseSpawn)

	eventually(t, time.Second, func() bool { return string(pty.writtenBytes()) == "hello\n" })
}

func TestAttachmentReleasesBufferedInputLeaseWhenClosedBeforePTYReady(t *testing.T) {
	a := newTestAttachment(&fakeSource{alive: true}, nil, nil)
	released := make(chan struct{})
	if err := a.writeLeased([]byte("hello\n"), func() { close(released) }); err != nil {
		t.Fatalf("write before PTY ready: %v", err)
	}
	select {
	case <-released:
		t.Fatal("buffered input lease released before write or discard")
	default:
	}

	a.close()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("closing attachment did not release buffered input lease")
	}
}

// A size requested before the PTY exists (the open frame's cols/rows, or a
// resize racing the attach) must not be lost: the attach applies it the moment
// the PTY is up, instead of leaving the pane at the kernel default grid.
func TestAttachmentAppliesRequestedSizeOnAttach(t *testing.T) {
	pty := newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{pty}}
	src := &fakeSource{alive: true, spawner: sp}
	a := newTestAttachment(src, nil, nil)

	if err := a.resize(30, 100); err != nil {
		t.Fatalf("resize before attach: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	eventually(t, time.Second, func() bool {
		rs := pty.resizeCalls()
		return len(rs) == 1 && rs[0] == [2]uint16{30, 100}
	})
}

// The Stream must be OPENED at the recorded grid, not sized after attach: the
// attach client reads the tty size once at startup, and a post-attach resize
// depends on SIGWINCH delivery that can race the client installing its handler
// — a missed signal left the session laid out for the previous client's size
// (the "terminal doesn't repaint after a resize" desync). Also covers
// re-attach: a later resize must reach the NEXT attach, not the first grid
// forever.
func TestAttachmentSpawnsPTYAtRecordedSize(t *testing.T) {
	first := newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{first}}
	src := &fakeSource{alive: true, spawner: sp}
	a := newTestAttachment(src, nil, nil)
	a.resetGrace = time.Hour // keep the failure counter deterministic

	if err := a.resize(37, 115); err != nil {
		t.Fatalf("resize before attach: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	eventually(t, time.Second, func() bool {
		ss := sp.spawnSizes()
		return len(ss) == 1 && ss[0] == [2]uint16{37, 115}
	})

	// Client resized, then the PTY dropped: the re-attach spawn must start at
	// the latest grid.
	if err := a.resize(40, 148); err != nil {
		t.Fatalf("resize while attached: %v", err)
	}
	_ = first.Close()

	eventually(t, time.Second, func() bool {
		ss := sp.spawnSizes()
		return len(ss) == 2 && ss[1] == [2]uint16{40, 148}
	})
}

func TestAttachmentSkipsReattachOnCleanExit(t *testing.T) {
	pty := newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{pty}}
	src := &fakeSource{alive: true, spawner: sp} // alive for the first attach

	exited := make(chan struct{})
	a := newTestAttachment(src, nil, func() { close(exited) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	eventually(t, time.Second, func() bool { return sp.calls() == 1 })
	src.setAlive(false) // runtime session gone -> no re-attach
	pty.Close()         // pane ends
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("expected exit callback after clean pane exit")
	}
	if got := sp.calls(); got != 1 {
		t.Fatalf("expected exactly one attach, got %d", got)
	}
}

// TestAttachmentNeverAttachesToDeadRuntime covers the resurrection bug: a mux
// attach on a killed-but-cached session resurrects it, re-running the agent
// command. An attachment whose runtime probes definitively dead must therefore
// report exited WITHOUT ever opening an attach Stream — even on the very first
// open (the original code only checked liveness on re-attach).
func TestAttachmentNeverAttachesToDeadRuntime(t *testing.T) {
	sp := &fakeSpawner{}
	src := &fakeSource{alive: false, spawner: sp}

	exited := make(chan struct{})
	a := newTestAttachment(src, nil, func() { close(exited) })

	go a.run(context.Background())
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("expected exit when runtime is dead before first attach")
	}
	if got := sp.calls(); got != 0 {
		t.Fatalf("attach must never run against a dead runtime, got %d attaches", got)
	}
}

// TestAttachmentRetriesProbeErrorsBeforeAttaching pins the hard rule that a
// failed liveness probe is NOT proof of death: a transient probe error must
// not flip the terminal to exited, and the attach proceeds once the probe
// recovers.
func TestAttachmentRetriesProbeErrorsBeforeAttaching(t *testing.T) {
	pty := newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{pty}}
	src := &fakeSource{aliveErr: io.ErrUnexpectedEOF, spawner: sp}
	a := newTestAttachment(src, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	// While probes error the attachment must neither exit nor attach.
	time.Sleep(50 * time.Millisecond)
	if a.isExited() {
		t.Fatal("probe error must not be treated as runtime death")
	}
	if got := sp.calls(); got != 0 {
		t.Fatalf("attach must wait for a successful probe, got %d attaches", got)
	}

	// Probe recovers -> the attach goes through.
	src.setAliveResult(true, nil)
	eventually(t, 2*time.Second, func() bool { return sp.calls() == 1 })
	if a.isExited() {
		t.Fatal("attachment exited despite a live runtime")
	}
}

func TestAttachmentReattachesWhileSessionAlive(t *testing.T) {
	p1, p2 := newFakePTY(), newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{p1, p2}}
	src := &fakeSource{alive: true, spawner: sp} // session still alive -> re-attach on drop
	a := newTestAttachment(src, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	eventually(t, time.Second, func() bool { return sp.calls() >= 1 })
	if err := a.resize(24, 80); err != nil {
		t.Fatalf("resize: %v", err)
	}
	p1.Close() // first attach drops
	eventually(t, 2*time.Second, func() bool { return sp.calls() >= 2 })

	// The client's grid survives the re-attach: the fresh PTY is sized to the
	// last requested grid without waiting for the client to resize again.
	eventually(t, time.Second, func() bool {
		for _, rs := range p2.resizeCalls() {
			if rs == [2]uint16{24, 80} {
				return true
			}
		}
		return false
	})

	// Now the session is gone: the next drop must not re-attach.
	src.setAlive(false)
	p2.Close()
	eventually(t, 2*time.Second, func() bool { return a.isExited() })
}

// A persistent Attach error is treated like the old spawn-error path: it backs
// off and retries up to the failure cap, then reports exited. (The old
// AttachCommand-error path failed immediately; folding the argv build into
// Attach means an attach failure now shares the spawn-failure retry policy,
// which is the correct behavior for a transient dial/exec failure.)
func TestAttachmentFailsWhenAttachErrors(t *testing.T) {
	src := &fakeSource{alive: true, attachErr: io.ErrUnexpectedEOF}

	exited := make(chan struct{})
	a := newTestAttachment(src, nil, func() { close(exited) })
	a.maxReattach = 2 // keep the retry budget small so the test is fast

	go a.run(context.Background())
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("expected exit after attach errors exhaust the retry budget")
	}
}

// close() is a detach, not a pane death: it must stop the attach loop and kill
// the client's PTY without firing onExit — the runtime session is still alive
// and an exited frame would wrongly flip the client UI to its terminal state.
func TestAttachmentCloseDoesNotFireExit(t *testing.T) {
	pty := newFakePTY()
	sp := &fakeSpawner{ptys: []*fakePTY{pty}}
	src := &fakeSource{alive: true, spawner: sp}

	exited := make(chan struct{})
	a := newTestAttachment(src, nil, func() { close(exited) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		a.run(ctx)
		close(done)
	}()

	eventually(t, time.Second, func() bool { return sp.calls() == 1 })
	a.close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run must return after close")
	}
	select {
	case <-exited:
		t.Fatal("close must not fire onExit")
	default:
	}
	if got := sp.calls(); got != 1 {
		t.Fatalf("close must stop re-attaching, got %d attaches", got)
	}
}

type closeOrderPTY struct {
	*fakePTY
	ctx    context.Context
	before chan struct{}
	after  chan struct{}
	once   sync.Once
}

func (p *closeOrderPTY) Close() error {
	p.once.Do(func() {
		select {
		case <-p.ctx.Done():
			close(p.after)
		default:
			close(p.before)
		}
	})
	return p.fakePTY.Close()
}

func TestAttachmentCloseClosesPTYBeforeCancel(t *testing.T) {
	beforeCancel := make(chan struct{})
	afterCancel := make(chan struct{})
	var spawnCtx context.Context
	src := &fakeSource{alive: true, attachFn: func(ctx context.Context, _, _ uint16) (ports.Stream, error) {
		spawnCtx = ctx
		return &closeOrderPTY{
			fakePTY: newFakePTY(),
			ctx:     ctx,
			before:  beforeCancel,
			after:   afterCancel,
		}, nil
	}}
	a := newTestAttachment(src, nil, nil)

	done := make(chan struct{})
	go func() {
		a.run(context.Background())
		close(done)
	}()
	eventually(t, time.Second, func() bool { return currentPTY(a) != nil })

	a.close()
	select {
	case <-beforeCancel:
	case <-afterCancel:
		t.Fatal("attachment cancelled the run context before closing the PTY")
	case <-time.After(time.Second):
		t.Fatal("PTY was not closed")
	}
	select {
	case <-spawnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("close must still cancel the attach context")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run must return after close")
	}
}
