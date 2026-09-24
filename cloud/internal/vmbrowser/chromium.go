// Package vmbrowser is the in-VM browser service ("browserd"): a loopback
// HTTP server speaking the desktop browser contract, backed by a supervised
// headless Chromium and the pinned agent-browser engine.
package vmbrowser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrParked reports that Chromium exhausted its restart budget and requires an
// explicit unpark (the next browser open) to try again.
var ErrParked = errors.New("chromium is parked after repeated failures")

const (
	defaultStartBackoffFloor   = 500 * time.Millisecond
	defaultStartBackoffCeiling = 8 * time.Second
	defaultParkFailureWindow   = 10 * time.Minute
	defaultParkFailureLimit    = 5
	defaultStartTimeout        = 15 * time.Second
	readinessPollInterval      = 50 * time.Millisecond
	stopGrace                  = 5 * time.Second
)

// ProcessSpec describes one supervised process launch.
type ProcessSpec struct {
	BinaryPath string
	Args       []string
	Dir        string
}

// Process is a supervised process handle.
type Process interface {
	Wait() error
	Stop() error
}

// Spawner starts supervised processes. Tests inject fakes.
type Spawner interface {
	Start(ctx context.Context, spec ProcessSpec) (Process, error)
}

// Endpoint is the discovered CDP entry point of the supervised Chromium.
type Endpoint struct {
	WebSocketURL string
}

// Status describes the supervised Chromium's current state.
type Status struct {
	Running   bool
	StartedAt time.Time
	Restarts  int
	Parked    bool
}

// ChromiumOptions configures the supervisor. Zero-value durations and limits
// fall back to package defaults; tests shrink them.
type ChromiumOptions struct {
	BinaryPath   string
	UserDataDir  string
	Spawner      Spawner
	StartTimeout time.Duration
	Logger       *slog.Logger
	// StartBackoffFloor and StartBackoffCeiling bound the exponential delay
	// between restart attempts.
	StartBackoffFloor   time.Duration
	StartBackoffCeiling time.Duration
	// ParkFailureWindow and ParkFailureLimit define the restart budget:
	// ParkFailureLimit failures inside ParkFailureWindow park the browser.
	ParkFailureWindow time.Duration
	ParkFailureLimit  int
}

// Chromium lazily starts and supervises one headless Chromium with a
// loopback-only CDP endpoint for the lifetime of one session.
type Chromium struct {
	opts    ChromiumOptions
	spawner Spawner
	logger  *slog.Logger

	mu        sync.Mutex
	running   bool
	startedAt time.Time
	launches  int
	restarts  int
	failures  []time.Time
	parked    bool
	proc      Process
	starting  *chromiumStartup
	stopping  chan struct{}
	// generation distinguishes the process a watcher goroutine belongs to;
	// a stale watcher must not clear state owned by a newer launch.
	generation int64
}

type chromiumStartup struct {
	done     chan struct{}
	cancel   context.CancelFunc
	endpoint Endpoint
	err      error
}

// NewChromium creates a supervisor. It starts nothing; call EnsureRunning.
func NewChromium(opts ChromiumOptions) *Chromium {
	if opts.Spawner == nil {
		opts.Spawner = execSpawner{}
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = defaultStartTimeout
	}
	if opts.StartBackoffFloor <= 0 {
		opts.StartBackoffFloor = defaultStartBackoffFloor
	}
	if opts.StartBackoffCeiling <= 0 {
		opts.StartBackoffCeiling = defaultStartBackoffCeiling
	}
	if opts.ParkFailureWindow <= 0 {
		opts.ParkFailureWindow = defaultParkFailureWindow
	}
	if opts.ParkFailureLimit <= 0 {
		opts.ParkFailureLimit = defaultParkFailureLimit
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Chromium{opts: opts, spawner: opts.Spawner, logger: opts.Logger}
}

// EnsureRunning returns the CDP endpoint, starting Chromium on first use and
// restarting it after unexpected exits. Concurrent callers share one startup.
func (c *Chromium) EnsureRunning(ctx context.Context) (endpoint Endpoint, err error) {
	c.mu.Lock()
	if c.stopping != nil {
		c.mu.Unlock()
		return Endpoint{}, context.Canceled
	}
	if startup := c.starting; startup != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return Endpoint{}, ctx.Err()
		case <-startup.done:
			return startup.endpoint, startup.err
		}
	}
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Endpoint{}, err
	}
	if c.parked {
		return Endpoint{}, ErrParked
	}
	if endpoint, ok := c.endpointLocked(); ok {
		return endpoint, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	startup := &chromiumStartup{done: make(chan struct{}), cancel: cancel}
	c.starting = startup
	defer func() {
		cancel()
		startup.endpoint, startup.err = endpoint, err
		c.starting = nil
		close(startup.done)
	}()

	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return Endpoint{}, ctx.Err()
		}
		if c.parked {
			return Endpoint{}, ErrParked
		}
		if attempt > 0 {
			delay := c.backoffLocked(attempt)
			c.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				c.mu.Lock()
				return Endpoint{}, ctx.Err()
			case <-timer.C:
			}
			c.mu.Lock()
			if c.parked {
				return Endpoint{}, ErrParked
			}
		}

		endpoint, err := c.launchLocked(ctx)
		if err == nil {
			return endpoint, nil
		}
		if ctx.Err() != nil {
			return Endpoint{}, ctx.Err()
		}
		c.recordFailureLocked()
		if len(c.failures) >= c.opts.ParkFailureLimit {
			c.parked = true
			c.logger.Error("chromium parked after repeated start failures",
				"failures", len(c.failures), "last_error", err)
			return Endpoint{}, ErrParked
		}
		c.logger.Warn("chromium start failed; will retry", "error", err, "attempt", attempt+1)
	}
}

// launchLocked spawns Chromium and waits for its DevToolsActivePort file.
// The startup record fences callers while readiness polling releases c.mu.
func (c *Chromium) launchLocked(ctx context.Context) (Endpoint, error) {
	if err := os.MkdirAll(c.opts.UserDataDir, 0o700); err != nil {
		return Endpoint{}, fmt.Errorf("create chromium user data dir: %w", err)
	}
	// The port file from a previous run would satisfy readiness instantly and
	// hide a dead browser, so remove it before launching.
	_ = os.Remove(c.devToolsActivePortPath())

	spec := ProcessSpec{
		BinaryPath: c.opts.BinaryPath,
		Args: []string{
			"--headless=new",
			"--remote-debugging-address=127.0.0.1",
			"--remote-debugging-port=0",
			"--user-data-dir=" + c.opts.UserDataDir,
			"--no-first-run",
			"--no-default-browser-check",
			// The container is the isolation boundary; Chromium's own
			// sandbox cannot work under Docker's default userns policy.
			"--no-sandbox",
			// Docker's default 64 MiB /dev/shm starves Chromium.
			"--disable-dev-shm-usage",
			"about:blank",
		},
		Dir: c.opts.UserDataDir,
	}
	proc, err := c.spawner.Start(ctx, spec)
	if err != nil {
		return Endpoint{}, fmt.Errorf("start chromium: %w", err)
	}

	c.generation++
	generation := c.generation
	c.proc = proc
	c.launches++
	if c.launches > 1 {
		c.restarts = c.launches - 1
	}
	c.running = true
	c.startedAt = time.Now().UTC()

	endpoint, err := c.awaitEndpointLocked(ctx)
	if err != nil {
		c.mu.Unlock()
		stopErr := proc.Stop()
		c.mu.Lock()
		if c.proc == proc {
			c.running = false
			c.proc = nil
		}
		if stopErr != nil {
			return Endpoint{}, fmt.Errorf("chromium readiness failed: %v (stop: %w)", err, stopErr)
		}
		return Endpoint{}, fmt.Errorf("chromium readiness failed: %w", err)
	}

	go c.watch(proc, generation)
	return endpoint, nil
}

// watch marks unexpected exits so the next EnsureRunning restarts. A watcher
// for a superseded generation stays silent.
func (c *Chromium) watch(proc Process, generation int64) {
	err := proc.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.generation || c.proc != proc {
		return
	}
	c.running = false
	c.proc = nil
	if err != nil {
		c.recordFailureLocked()
		if len(c.failures) >= c.opts.ParkFailureLimit {
			c.parked = true
			c.logger.Error("chromium parked after repeated crashes", "failures", len(c.failures), "last_error", err)
		} else {
			c.logger.Warn("chromium exited unexpectedly", "error", err)
		}
	}
}

// awaitEndpointLocked polls for the DevToolsActivePort file Chromium writes
// when it binds a loopback debugging port. Format: "<port>\n<browser path>".
func (c *Chromium) awaitEndpointLocked(ctx context.Context) (Endpoint, error) {
	deadline := time.Now().Add(c.opts.StartTimeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return Endpoint{}, ctx.Err()
		}
		if endpoint, ok := c.readEndpoint(); ok {
			return endpoint, nil
		}
		c.mu.Unlock()
		timer := time.NewTimer(readinessPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.mu.Lock()
			return Endpoint{}, ctx.Err()
		case <-timer.C:
		}
		c.mu.Lock()
	}
	return Endpoint{}, errors.New("chromium did not write DevToolsActivePort in time")
}

// endpointLocked returns the endpoint when the port file exists and Chromium
// is considered running.
func (c *Chromium) endpointLocked() (Endpoint, bool) {
	if !c.running || c.proc == nil {
		return Endpoint{}, false
	}
	return c.readEndpoint()
}

func (c *Chromium) readEndpoint() (Endpoint, bool) {
	file, err := os.Open(c.devToolsActivePortPath())
	if err != nil {
		return Endpoint{}, false
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return Endpoint{}, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || port <= 0 || port > 65535 {
		return Endpoint{}, false
	}
	path := "/"
	if scanner.Scan() {
		if trimmed := strings.TrimSpace(scanner.Text()); trimmed != "" {
			path = trimmed
		}
	}
	return Endpoint{WebSocketURL: "ws://127.0.0.1:" + strconv.Itoa(port) + path}, true
}

func (c *Chromium) devToolsActivePortPath() string {
	return filepath.Join(c.opts.UserDataDir, "DevToolsActivePort")
}

func (c *Chromium) recordFailureLocked() {
	now := time.Now()
	c.failures = append(c.failures, now)
	windowStart := now.Add(-c.opts.ParkFailureWindow)
	kept := c.failures[:0]
	for _, failure := range c.failures {
		if failure.After(windowStart) {
			kept = append(kept, failure)
		}
	}
	c.failures = kept
}

func (c *Chromium) backoffLocked(attempt int) time.Duration {
	delay := c.opts.StartBackoffFloor
	for i := 1; i < attempt && delay < c.opts.StartBackoffCeiling; i++ {
		delay *= 2
	}
	if delay > c.opts.StartBackoffCeiling {
		delay = c.opts.StartBackoffCeiling
	}
	return delay
}

// CurrentStatus snapshots supervisor state.
func (c *Chromium) CurrentStatus() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{
		Running:   c.running,
		StartedAt: c.startedAt,
		Restarts:  c.restarts,
		Parked:    c.parked,
	}
}

// Stop terminates the supervised Chromium. Safe when never started.
func (c *Chromium) Stop(ctx context.Context) error {
	c.mu.Lock()
	if stopping := c.stopping; stopping != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stopping:
			return nil
		}
	}
	c.stopping = make(chan struct{})
	startup := c.starting
	if startup != nil {
		startup.cancel()
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		close(c.stopping)
		c.stopping = nil
		c.mu.Unlock()
	}()
	if startup != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-startup.done:
		}
	}
	c.mu.Lock()
	proc := c.proc
	if proc != nil {
		c.generation++
		c.running = false
		c.proc = nil
	}
	c.mu.Unlock()
	if proc == nil {
		return nil
	}
	return proc.Stop()
}

// Unpark clears the parked state so the next EnsureRunning retries.
func (c *Chromium) Unpark() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parked = false
	c.failures = nil
}

type execSpawner struct{}

func (execSpawner) Start(_ context.Context, spec ProcessSpec) (Process, error) {
	cmd := exec.Command(spec.BinaryPath, spec.Args...)
	cmd.Dir = spec.Dir
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd}, nil
}

type execProcess struct {
	cmd      *exec.Cmd
	waitOnce sync.Once
	waitErr  error
}

func (p *execProcess) Wait() error {
	p.waitOnce.Do(func() { p.waitErr = p.cmd.Wait() })
	return p.waitErr
}

func (p *execProcess) Stop() error {
	if p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case <-done:
		return nil
	case <-time.After(stopGrace):
		_ = p.cmd.Process.Kill()
		<-done
		return nil
	}
}

var (
	_ Spawner = execSpawner{}
	_ Process = (*execProcess)(nil)
)
