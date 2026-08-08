package terminal

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// fakeSource is a scripted terminal Source: Attach hands out fake Streams from
// an embedded spawner (or a custom attachFn closure); IsAlive is scriptable.
// attachErr makes Attach fail.
type fakeSource struct {
	spawner   *fakeSpawner
	attachFn  func(ctx context.Context, rows, cols uint16) (ports.Stream, error)
	mu        sync.Mutex
	alive     bool
	aliveErr  error
	attachErr error
}

func (f *fakeSource) Attach(ctx context.Context, _ ports.RuntimeHandle, rows, cols uint16) (ports.Stream, error) {
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	if f.attachFn != nil {
		return f.attachFn(ctx, rows, cols)
	}
	if f.spawner == nil {
		f.spawner = &fakeSpawner{}
	}
	return f.spawner.spawn(rows, cols)
}

func (f *fakeSource) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive, f.aliveErr
}

func (f *fakeSource) setAlive(v bool) {
	f.mu.Lock()
	f.alive = v
	f.mu.Unlock()
}

func (f *fakeSource) setAliveResult(v bool, err error) {
	f.mu.Lock()
	f.alive = v
	f.aliveErr = err
	f.mu.Unlock()
}

// fakePTY is a scripted ports.Stream: Read drains the out channel, Write
// records, Resize records, and Close unblocks reads.
type fakePTY struct {
	out    chan []byte
	closed chan struct{}
	once   sync.Once

	mu           sync.Mutex
	written      []byte
	resizes      [][2]uint16
	writeStarted chan struct{}
	writeUnblock chan struct{}
}

func newFakePTY() *fakePTY {
	return &fakePTY{out: make(chan []byte, 64), closed: make(chan struct{})}
}

func (p *fakePTY) push(b []byte) { p.out <- b }

func (p *fakePTY) Read(b []byte) (int, error) {
	select {
	case chunk := <-p.out:
		return copy(b, chunk), nil
	case <-p.closed:
		return 0, io.EOF
	}
}

func (p *fakePTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writeStarted != nil {
		p.writeStarted <- struct{}{}
	}
	if p.writeUnblock != nil {
		<-p.writeUnblock
	}
	p.written = append(p.written, b...)
	return len(b), nil
}

func (p *fakePTY) Resize(rows, cols uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes = append(p.resizes, [2]uint16{rows, cols})
	return nil
}

func (p *fakePTY) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (p *fakePTY) writtenBytes() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]byte, len(p.written))
	copy(out, p.written)
	return out
}

func (p *fakePTY) resizeCalls() [][2]uint16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][2]uint16(nil), p.resizes...)
}

// fakeSpawner hands out pre-built fakePTYs in order; once exhausted it returns
// idle PTYs that block until closed (so a re-attach loop does not busy-spin).
// It is the attach seam the fakeSource backs: each Attach call is one spawn.
type fakeSpawner struct {
	mu      sync.Mutex
	ptys    []*fakePTY
	n       int
	err     error
	created []*fakePTY
	sizes   [][2]uint16 // rows×cols passed to each attach call, in order
}

func (f *fakeSpawner) spawn(rows, cols uint16) (ports.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.sizes = append(f.sizes, [2]uint16{rows, cols})
	var p *fakePTY
	if f.n < len(f.ptys) {
		p = f.ptys[f.n]
	} else {
		p = newFakePTY()
	}
	f.n++
	f.created = append(f.created, p)
	return p, nil
}

func (f *fakeSpawner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakeSpawner) spawnSizes() [][2]uint16 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]uint16(nil), f.sizes...)
}

// eventually polls cond until true or the deadline, failing the test otherwise.
func eventually(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within " + d.String())
}

// safeBytes is a concurrency-safe byte accumulator for subscriber callbacks.
type safeBytes struct {
	mu sync.Mutex
	b  []byte
}

func (s *safeBytes) add(p []byte) {
	s.mu.Lock()
	s.b = append(s.b, p...)
	s.mu.Unlock()
}

func (s *safeBytes) string() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}
