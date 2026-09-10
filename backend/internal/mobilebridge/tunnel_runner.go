package mobilebridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"time"
)

// TunnelRunner supervises one managed cloudflared connector: spawn, stream its
// output into a TunnelRuntime, restart with backoff, and never leave the
// process behind.
//
// Deliberately thin. Every decision worth testing — which binary, when the
// tunnel may be advertised, how long to back off, whether a recorded pid is
// safe to kill — lives in tunnel.go and is unit tested there. What is left here
// is process plumbing, verified by running it against the real binary.
type TunnelRunner struct {
	Binary    string
	LocalPort int
	PIDPath   string
	Runtime   *TunnelRuntime
	Log       *slog.Logger
	// SettleDelay overrides how long after registration the hostname is
	// considered resolvable. Zero uses DefaultTunnelSettleDelay.
	SettleDelay time.Duration
}

// Run supervises the connector until ctx is cancelled. It blocks, so callers
// run it in a goroutine. Restarts are spaced by TunnelBackoff; a run that
// stayed up long enough to be useful resets the ladder.
func (t *TunnelRunner) Run(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		startedAt := time.Now()
		err := t.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		// A connector that stayed up is not a failing one; only rapid exits
		// should escalate the wait.
		if time.Since(startedAt) >= stableTunnelRun {
			attempt = 0
		}
		wait := TunnelBackoff(attempt)
		attempt++
		t.logger().Warn("mobile tunnel exited; restarting",
			"error", err, "in", wait, "attempt", attempt)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// stableTunnelRun is how long a connector must survive before its exit is
// treated as a fresh fault rather than a continuing failure.
const stableTunnelRun = 60 * time.Second

func (t *TunnelRunner) runOnce(ctx context.Context) error {
	metricsPort, err := freeLoopbackPort()
	if err != nil {
		return fmt.Errorf("reserve metrics port: %w", err)
	}

	cmd := exec.CommandContext(ctx, t.Binary, CloudflaredArgs(t.LocalPort, metricsPort)...)
	// cloudflared logs to stderr; the hostname is only available there.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cloudflared: %w", err)
	}
	t.Runtime.Started()
	pidOwner := newTunnelPIDOwner(t.PIDPath)

	// Record the pid before anything can go wrong, so a daemon that dies in the
	// next instant still leaves a trail to clean up.
	if err := pidOwner.claim(cmd.Process.Pid); err != nil {
		t.logger().Warn("could not record tunnel pid; a crash may orphan the connector", "error", err)
	}

	// Hold the endpoint back until its hostname has had time to propagate: DNS
	// for a brand-new quick tunnel lags the connector's own "registered" signal.
	settleCtx, stopSettle := context.WithCancel(ctx)
	defer stopSettle()
	go t.awaitSettled(settleCtx)

	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		t.Runtime.Line(line)
		t.logger().Debug("cloudflared", "line", line)
	}

	waitErr := cmd.Wait()
	t.Runtime.Exited(waitErr)
	// Only if this runner still owns the record: a replacement started while
	// this one was unwinding will already have written its own pid there.
	_ = pidOwner.release(cmd.Process.Pid)
	if waitErr != nil && ctx.Err() == nil {
		return waitErr
	}
	return errors.New("connector exited")
}

// awaitSettled holds the tunnel out of the endpoint list until its freshly
// issued hostname has had time to appear in DNS.
//
// A delay, not a probe. Measured against real cloudflared: dig began resolving
// a new quick tunnel hostname about twenty seconds after the connector
// registered — and querying it any earlier cached an NXDOMAIN locally that
// outlived propagation, leaving curl on this machine still failing thirty
// seconds after dig had started succeeding. A probe would also be testing the
// daemon's resolver rather than the phone's, which is the one that matters.
func (t *TunnelRunner) awaitSettled(ctx context.Context) {
	for ctx.Err() == nil {
		if t.Runtime.UnsettledHostname() != "" {
			break
		}
		if t.Runtime.Endpoint() != nil {
			return // already settled
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(settlePollInterval):
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(t.settleDelay()):
	}
	t.Runtime.Settled()
	t.logger().Info("mobile tunnel advertisable", "hostname", t.Runtime.Snapshot().Hostname)
}

func (t *TunnelRunner) settleDelay() time.Duration {
	if t.SettleDelay > 0 {
		return t.SettleDelay
	}
	return DefaultTunnelSettleDelay
}

const (
	// DefaultTunnelSettleDelay is how long after registration the hostname is
	// treated as resolvable.
	//
	// Measured propagation was ~20s on one network, and this was 25s to cover a
	// slower run. That margin is guesswork the daemon cannot improve on: probing
	// from the machine that owns the tunnel tests its own resolver, and probing
	// early caches an NXDOMAIN locally that outlives propagation.
	//
	// The phone now retries a tunnel candidate whose hostname does not resolve
	// yet (see the mobile probeRetry policy), which is where the resolver that
	// actually decides lives. With that absorbing the tail, this only has to
	// cover the common case, and the whole enable is quicker for it.
	DefaultTunnelSettleDelay = 12 * time.Second
	settlePollInterval       = 500 * time.Millisecond
)

func (t *TunnelRunner) logger() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

// freeLoopbackPort reserves an ephemeral port by binding and releasing it.
// Inherently racy, but the metrics listener is loopback-only and a collision
// merely costs one restart.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address %T", l.Addr())
	}
	return addr.Port, nil
}
