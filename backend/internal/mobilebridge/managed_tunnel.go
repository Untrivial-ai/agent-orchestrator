package mobilebridge

import (
	"context"
	"log/slog"
	"sync"
)

// ManagedTunnelDeps is what a ManagedTunnel needs. Run is injected so the
// start/stop lifecycle can be tested without spawning a process.
type ManagedTunnelDeps struct {
	// Run supervises a connector for localPort until ctx is cancelled,
	// reporting progress into rt. Nil uses the real TunnelRunner.
	Run func(ctx context.Context, localPort int, rt *TunnelRuntime)
	// Binary is the resolved cloudflared path, used by the default Run.
	Binary string
	// PIDPath records the connector's pid so a crashed daemon can reap it.
	PIDPath string
	Log     *slog.Logger
}

// ManagedTunnel owns the lifecycle of the remote-access connector: at most one
// running at a time, restarted when the port it should target changes, and
// always stopped on Stop.
type ManagedTunnel struct {
	deps ManagedTunnelDeps

	mu      sync.Mutex
	runtime *TunnelRuntime
	cancel  context.CancelFunc
	port    int
	done    chan struct{}
}

// NewManagedTunnel builds a stopped tunnel controller.
func NewManagedTunnel(deps ManagedTunnelDeps) *ManagedTunnel {
	return &ManagedTunnel{deps: deps, runtime: &TunnelRuntime{}}
}

// Start ensures a connector is running for localPort. It returns immediately;
// the connector takes tens of seconds to become advertisable.
//
// Idempotent by port. Status polling and repeated enables both call this, and
// relaunching each time would rotate the hostname continuously so none ever
// settles long enough to be advertised. A different port does restart it: the
// LAN listener can come back on an ephemeral port, and a connector aimed at the
// old one tunnels nothing.
func (m *ManagedTunnel) Start(localPort int) {
	m.mu.Lock()
	if m.cancel != nil && m.port == localPort {
		m.mu.Unlock()
		return
	}
	m.stopLocked()

	ctx, cancel := context.WithCancel(context.Background())
	rt := &TunnelRuntime{}
	done := make(chan struct{})
	m.cancel, m.port, m.runtime, m.done = cancel, localPort, rt, done
	run := m.deps.Run
	if run == nil {
		run = m.runReal
	}
	m.mu.Unlock()

	go func() {
		defer close(done)
		run(ctx, localPort, rt)
	}()
}

// Stop ends the connector and withdraws the advertised endpoint. Safe to call
// when nothing is running, and safe to call twice.
//
// Returns as soon as the connector is cancelled rather than waiting for its
// goroutine to finish. Disable runs inside an HTTP handler with a deadline, and
// a connector can take seconds to wind down — killing the process, draining its
// stderr, reaping it — which blew that budget and failed the request with the
// bridge left half-disabled.
//
// Not waiting is safe: exec.CommandContext kills the process on cancellation,
// and if the daemon dies before the goroutine finishes, the pid file and the
// startup reaper clean up whatever is left. The endpoint is withdrawn
// synchronously below, so nothing hands out a dead address after this returns.
func (m *ManagedTunnel) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// stopLocked cancels any running connector. Caller holds mu.
func (m *ManagedTunnel) stopLocked() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.port = 0
	m.done = nil
	// A fresh runtime, so nothing from the stopped connector is still
	// advertised: its hostname is dead the moment it exits.
	m.runtime = &TunnelRuntime{}
}

// Endpoint is the advertisable tunnel, or nil.
func (m *ManagedTunnel) Endpoint() *TunnelEndpoint {
	m.mu.Lock()
	rt := m.runtime
	m.mu.Unlock()
	return rt.Endpoint()
}

// Status is the connector's current state, for display.
func (m *ManagedTunnel) Status() TunnelStatus {
	m.mu.Lock()
	rt := m.runtime
	m.mu.Unlock()
	return rt.Snapshot()
}

func (m *ManagedTunnel) runReal(ctx context.Context, localPort int, rt *TunnelRuntime) {
	(&TunnelRunner{
		Binary:    m.deps.Binary,
		LocalPort: localPort,
		PIDPath:   m.deps.PIDPath,
		Runtime:   rt,
		Log:       m.deps.Log,
	}).Run(ctx)
}
