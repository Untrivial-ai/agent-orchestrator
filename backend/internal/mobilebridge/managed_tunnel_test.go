package mobilebridge

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingRun stands in for TunnelRunner.Run: it records the ports it was
// launched for and blocks until its context is cancelled, exactly as the real
// supervisor loop does.
type recordingRun struct {
	mu     sync.Mutex
	ports  []int
	active int
}

func (r *recordingRun) run(ctx context.Context, port int, _ *TunnelRuntime) {
	r.mu.Lock()
	r.ports = append(r.ports, port)
	r.active++
	r.mu.Unlock()
	<-ctx.Done()
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
}

func (r *recordingRun) snapshot() ([]int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.ports...), r.active
}

func settleFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

func TestManagedTunnelStartIsIdempotent(t *testing.T) {
	// Status polling and repeated enables both call Start. Relaunching the
	// connector each time would rotate the hostname continuously and never let
	// one settle long enough to be advertisable.
	rec := &recordingRun{}
	m := NewManagedTunnel(ManagedTunnelDeps{Run: rec.run})
	defer m.Stop()

	m.Start(3011)
	m.Start(3011)
	m.Start(3011)

	settleFor(t, func() bool { p, _ := rec.snapshot(); return len(p) >= 1 })
	time.Sleep(50 * time.Millisecond)
	ports, active := rec.snapshot()
	if len(ports) != 1 {
		t.Fatalf("launched %d times %v, want 1", len(ports), ports)
	}
	if active != 1 {
		t.Fatalf("%d connectors alive, want 1", active)
	}
}

func TestManagedTunnelRestartsWhenThePortChanges(t *testing.T) {
	// The LAN listener can come back on a different (ephemeral) port. A
	// connector still aimed at the old one tunnels nothing.
	rec := &recordingRun{}
	m := NewManagedTunnel(ManagedTunnelDeps{Run: rec.run})
	defer m.Stop()

	m.Start(3011)
	settleFor(t, func() bool { p, _ := rec.snapshot(); return len(p) == 1 })
	m.Start(49152)
	settleFor(t, func() bool { p, _ := rec.snapshot(); return len(p) == 2 })

	ports, _ := rec.snapshot()
	if ports[0] != 3011 || ports[1] != 49152 {
		t.Fatalf("ports = %v, want [3011 49152]", ports)
	}
	settleFor(t, func() bool { _, active := rec.snapshot(); return active == 1 })
}

func TestManagedTunnelStopEndsTheConnectorAndClearsTheEndpoint(t *testing.T) {
	rec := &recordingRun{}
	m := NewManagedTunnel(ManagedTunnelDeps{Run: rec.run})

	m.Start(3011)
	settleFor(t, func() bool { _, active := rec.snapshot(); return active == 1 })

	m.Stop()

	settleFor(t, func() bool { _, active := rec.snapshot(); return active == 0 })
	if m.Endpoint() != nil {
		t.Error("still advertising after Stop")
	}
	if m.Status().Running {
		t.Error("Status still reports running after Stop")
	}
}

func TestManagedTunnelStopBeforeStartIsHarmless(t *testing.T) {
	m := NewManagedTunnel(ManagedTunnelDeps{Run: func(context.Context, int, *TunnelRuntime) {}})
	m.Stop()
	m.Stop()
	if m.Endpoint() != nil {
		t.Error("endpoint from a tunnel that never started")
	}
}

// Disable runs inside an HTTP handler with a deadline. A connector can take
// seconds to wind down — killing the process, draining its stderr, reaping it —
// and waiting for that blew the request budget: "context deadline exceeded
// (MOBILE_DISABLE)" after exactly 5s, with the bridge left half-disabled.
//
// Stop must therefore return as soon as the connector is cancelled and the
// endpoint withdrawn, and let the goroutine finish on its own. The process is
// still killed (exec.CommandContext owns that), and the pid file plus the
// startup reaper cover a daemon that dies before it finishes.
func TestManagedTunnelStopDoesNotWaitForASlowConnector(t *testing.T) {
	released := make(chan struct{})
	m := NewManagedTunnel(ManagedTunnelDeps{
		Run: func(ctx context.Context, _ int, _ *TunnelRuntime) {
			<-ctx.Done()
			<-released // still winding down long after cancellation
		},
	})
	m.Start(3011)
	settleFor(t, func() bool { return m.Status().Running || true })

	// Released from another goroutine so a regression blocks the test rather
	// than deadlocking it.
	go func() {
		time.Sleep(2 * time.Second)
		close(released)
	}()

	start := time.Now()
	m.Stop()
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("Stop blocked for %v waiting on the connector; it must return promptly", elapsed)
	}
}

// Returning promptly must not mean returning while still advertising: the
// endpoint has to be withdrawn synchronously, or a status read immediately
// after Disable would still hand out a dead address.
func TestManagedTunnelStopWithdrawsTheEndpointBeforeReturning(t *testing.T) {
	released := make(chan struct{})
	defer close(released)
	m := NewManagedTunnel(ManagedTunnelDeps{
		Run: func(ctx context.Context, _ int, rt *TunnelRuntime) {
			rt.Started()
			rt.Line(`INF |  https://abc.trycloudflare.com  |`)
			rt.Line(`INF Registered tunnel connection connIndex=0`)
			rt.Settled()
			<-ctx.Done()
			<-released
		},
	})
	m.Start(3011)
	settleFor(t, func() bool { return m.Endpoint() != nil })

	m.Stop()

	if m.Endpoint() != nil {
		t.Fatal("still advertising immediately after Stop returned")
	}
}
