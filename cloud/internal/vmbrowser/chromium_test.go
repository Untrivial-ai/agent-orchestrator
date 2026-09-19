package vmbrowser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"io"
	"log/slog"
)

type fakeProcess struct {
	mu      sync.Mutex
	stopped bool
	exit    chan struct{}
}

func (f *fakeProcess) Wait() error {
	<-f.exit
	return nil
}

func (f *fakeProcess) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return nil
	}
	f.stopped = true
	close(f.exit)
	return nil
}

type fakeSpawner struct {
	mu     sync.Mutex
	starts int
	specs  []ProcessSpec
	next   func(starts int, spec ProcessSpec) Process
}

func (f *fakeSpawner) Start(_ context.Context, spec ProcessSpec) (Process, error) {
	f.mu.Lock()
	f.starts++
	count := f.starts
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if f.next == nil {
		f.next = func(_ int, _ ProcessSpec) Process {
			proc := &fakeProcess{exit: make(chan struct{})}
			return proc
		}
	}
	return f.next(count, spec), nil
}

func (f *fakeSpawner) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

// writePortFile simulates Chromium writing DevToolsActivePort into the
// user-data dir: "<port>\n<browser path>".
func writePortFile(t *testing.T, userDataDir string) {
	t.Helper()
	if err := os.MkdirAll(userDataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDataDir, "DevToolsActivePort"), []byte("9333\n/devtools/browser/test-uuid"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestChromium(t *testing.T, spawner *fakeSpawner) *Chromium {
	t.Helper()
	return NewChromium(ChromiumOptions{
		BinaryPath:        "/usr/bin/chromium",
		UserDataDir:       filepath.Join(t.TempDir(), "profile"),
		Spawner:           spawner,
		StartTimeout:      2 * time.Second,
		StartBackoffFloor: 5 * time.Millisecond,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestChromiumEnsureRunningLazyStartsAndDiscoversEndpoint(t *testing.T) {
	spawner := &fakeSpawner{}
	spawner.next = func(_ int, spec ProcessSpec) Process {
		writePortFile(t, spec.Dir)
		return &fakeProcess{exit: make(chan struct{})}
	}
	c := newTestChromium(t, spawner)
	if c.CurrentStatus().Running {
		t.Fatal("chromium must not start until first use")
	}
	endpoint, err := c.EnsureRunning(context.Background())
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	want := "ws://127.0.0.1:9333/devtools/browser/test-uuid"
	if endpoint.WebSocketURL != want {
		t.Fatalf("endpoint = %q, want %q", endpoint.WebSocketURL, want)
	}
	if got := spawner.startCount(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	if !c.CurrentStatus().Running {
		t.Error("status must report running")
	}
}

func TestChromiumEnsureRunningIsIdempotent(t *testing.T) {
	spawner := &fakeSpawner{}
	spawner.next = func(_ int, spec ProcessSpec) Process {
		writePortFile(t, spec.Dir)
		return &fakeProcess{exit: make(chan struct{})}
	}
	c := newTestChromium(t, spawner)
	for i := 0; i < 3; i++ {
		if _, err := c.EnsureRunning(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if spawner.startCount() != 1 {
		t.Fatalf("starts = %d, want 1 (coalesced)", spawner.startCount())
	}
}

func TestChromiumRestartsAfterCrash(t *testing.T) {
	spawner := &fakeSpawner{}
	crashed := false
	spawner.next = func(_ int, spec ProcessSpec) Process {
		writePortFile(t, spec.Dir)
		if !crashed {
			crashed = true
			proc := &fakeProcess{exit: make(chan struct{})}
			// Simulate immediate crash.
			go func() {
				close(proc.exit)
				_ = os.Remove(filepath.Join(spec.Dir, "DevToolsActivePort"))
			}()
			return proc
		}
		return &fakeProcess{exit: make(chan struct{})}
	}
	c := newTestChromium(t, spawner)
	if _, err := c.EnsureRunning(context.Background()); err != nil {
		t.Fatalf("first start: %v", err)
	}
	// Wait for the watcher to observe the crash.
	deadline := time.Now().Add(2 * time.Second)
	for c.CurrentStatus().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.CurrentStatus().Running {
		t.Fatal("crashed chromium must not report running")
	}
	if _, err := c.EnsureRunning(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := spawner.startCount(); got != 2 {
		t.Fatalf("starts = %d, want 2", got)
	}
	if c.CurrentStatus().Restarts != 1 {
		t.Errorf("restarts = %d, want 1", c.CurrentStatus().Restarts)
	}
}

func TestChromiumParksAfterRepeatedFailures(t *testing.T) {
	spawner := &fakeSpawner{}
	spawner.next = func(_ int, _ ProcessSpec) Process {
		// Never writes the port file: readiness always fails.
		return &fakeProcess{exit: make(chan struct{})}
	}
	c := newTestChromium(t, spawner)
	c.opts.ParkFailureLimit = 3
	c.opts.StartTimeout = 50 * time.Millisecond
	deadline := time.Now().Add(5 * time.Second)
	parked := false
	for !parked && time.Now().Before(deadline) {
		_, err := c.EnsureRunning(context.Background())
		if err != nil && errors.Is(err, ErrParked) {
			parked = true
		}
	}
	if !parked {
		t.Fatal("chromium never parked after repeated readiness failures")
	}
	if !c.CurrentStatus().Parked {
		t.Error("status must report Parked")
	}
	if _, err := c.EnsureRunning(context.Background()); !errors.Is(err, ErrParked) {
		t.Fatalf("EnsureRunning while parked = %v, want ErrParked", err)
	}
	c.Unpark()
	if c.CurrentStatus().Parked {
		t.Error("Unpark must clear the parked flag")
	}
}

func TestChromiumStopIsSafeWhenNeverStarted(t *testing.T) {
	c := newTestChromium(t, &fakeSpawner{})
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on never-started chromium: %v", err)
	}
}

func TestChromiumLaunchFlags(t *testing.T) {
	spawner := &fakeSpawner{}
	spawner.next = func(_ int, spec ProcessSpec) Process {
		writePortFile(t, spec.Dir)
		return &fakeProcess{exit: make(chan struct{})}
	}
	c := newTestChromium(t, spawner)
	if _, err := c.EnsureRunning(context.Background()); err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	spec := spawner.specs[0]
	joined := strings.Join(spec.Args, " ")
	for _, want := range []string{
		"--headless=new",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=0",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--user-data-dir=" + c.opts.UserDataDir,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("launch args missing %q: %v", want, spec.Args)
		}
	}
}

func TestChromiumEnsureRunningHonorsContextCancel(t *testing.T) {
	spawner := &fakeSpawner{}
	spawner.next = func(_ int, _ ProcessSpec) Process {
		return &fakeProcess{exit: make(chan struct{})}
	}
	c := newTestChromium(t, spawner)
	c.opts.StartTimeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := c.EnsureRunning(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureRunning with cancelled ctx = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v; readiness poll must honor ctx", elapsed)
	}
}
