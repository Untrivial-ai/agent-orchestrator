package httpd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
)

const (
	startupRecoveryMaxAttempts           = 3
	startupRecoveryRetryDelay            = 250 * time.Millisecond
	defaultStartupRecoveryAttemptTimeout = 30 * time.Second
	defaultStartupRecoveryDrainWarning   = 5 * time.Second
)

var errStartupRecoveryInterrupted = errors.New("startup recovery interrupted")

// Server is the daemon's HTTP server together with its lifecycle: bind the
// loopback port, publish the running.json handshake, serve until the context
// is cancelled, then shut down gracefully and clean up the handshake file.
type Server struct {
	cfg    config.Config
	log    *slog.Logger
	http   *http.Server
	listen net.Listener

	shutdownRequested             chan struct{}
	shutdownOnce                  sync.Once
	ready                         atomic.Bool
	startupFailed                 atomic.Bool
	startupRecoveryAttemptTimeout time.Duration
	startupRecoveryDrainWarning   time.Duration
}

// NewWithDeps constructs a Server with API dependencies supplied by the daemon
// and binds the listener immediately, before any running.json is written. The
// caller owns the returned Server's lifecycle via Run. termMgr may be nil, in
// which case the /mux terminal surface is not mounted.
//
// If the configured port is already held, it falls back to an OS-assigned
// ephemeral port rather than failing. A genuine peer AO daemon is ruled out
// upstream (the running.json + /healthz check in daemon.Run), so a conflict here
// means a non-AO process owns the port; exiting would only leave the desktop
// supervisor stuck on "daemon not ready". The actual bound port is logged
// ("daemon listening") and written to running.json, both of which the supervisor
// reads, so the fallback propagates to the renderer with no UI changes.
func NewWithDeps(cfg config.Config, log *slog.Logger, termMgr *terminal.Manager, deps APIDeps) (*Server, error) {
	log = loggerOrDefault(log)
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		if !isAddrInUse(err) {
			return nil, fmt.Errorf("bind %s: %w", cfg.Addr(), err)
		}
		// Configured port is taken by a non-AO process: retry on an ephemeral port.
		fallback, ferr := net.Listen("tcp", net.JoinHostPort(cfg.Host, "0"))
		if ferr != nil {
			return nil, fmt.Errorf("bind %s (in use) and ephemeral fallback: %w", cfg.Addr(), ferr)
		}
		log.Warn("configured port in use; bound an ephemeral port instead",
			"configured", cfg.Addr(), "bound", fallback.Addr().String())
		ln = fallback
	}

	srv := &Server{
		cfg:                           cfg,
		log:                           log,
		listen:                        ln,
		shutdownRequested:             make(chan struct{}),
		startupRecoveryAttemptTimeout: defaultStartupRecoveryAttemptTimeout,
		startupRecoveryDrainWarning:   defaultStartupRecoveryDrainWarning,
	}
	srv.http = &http.Server{
		Handler: NewRouterWithControl(cfg, log, termMgr, deps, ControlDeps{
			RequestShutdown:   srv.requestShutdown,
			IsReady:           srv.ready.Load,
			StartupFailed:     srv.startupFailed.Load,
			AgentSwitchPolicy: deps.AgentSwitchPolicy,
		}),
		// ReadHeaderTimeout guards against slow-loris even on loopback;
		// per-request body/handler timeouts are applied per-surface.
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv, nil
}

// Addr returns the actual bound address (useful when the configured port was 0
// and the OS chose one — primarily in tests).
func (s *Server) Addr() net.Addr { return s.listen.Addr() }

// Handler returns the loopback server's built router so the daemon can share
// the exact same handler instance with the LAN listener (via NewMobileLAN),
// keeping the loopback and LAN surfaces identical.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run serves until ctx is cancelled (SIGINT/SIGTERM via signal.NotifyContext),
// then performs a graceful shutdown bounded by cfg.ShutdownTimeout. It writes
// running.json before serving and removes it on the way out. Run blocks until
// shutdown is complete.
func (s *Server) Run(ctx context.Context) error {
	return s.run(ctx, nil)
}

// RunWithReady is Run with startup work invoked after the listener has been
// published and its serving goroutine has started. Liveness is available while
// the callback runs; readiness is published only when the callback succeeds.
// The callback must be safe to retry: transient failures receive a small,
// bounded retry window before readiness reports a terminal recovery error. It
// must use the supplied context for every blocking recovery operation.
func (s *Server) RunWithReady(ctx context.Context, onReady func(context.Context) error) error {
	return s.run(ctx, onReady)
}

func (s *Server) run(ctx context.Context, onReady func(context.Context) error) error {
	// Run has no deferred startup work and is ready as soon as it serves. A
	// RunWithReady callback gates readiness until all recovery has completed.
	s.ready.Store(onReady == nil)
	s.startupFailed.Store(false)

	info := runfile.Info{
		PID:                   os.Getpid(),
		Port:                  s.boundPort(),
		StartedAt:             time.Now().UTC(),
		Owner:                 os.Getenv("AO_OWNER"),
		AppRunID:              s.cfg.AppRunID,
		BrowserRuntimeAddress: os.Getenv("AO_BROWSER_RUNTIME_ADDRESS"),
	}
	if err := runfile.Write(s.cfg.RunFilePath, info); err != nil {
		_ = s.listen.Close()
		return fmt.Errorf("write run-file: %w", err)
	}
	defer func() {
		if err := runfile.RemoveIfOwned(s.cfg.RunFilePath, info.PID); err != nil {
			s.log.Warn("failed to remove run-file", "path", s.cfg.RunFilePath, "err", err)
		}
	}()

	serveErr := make(chan error, 1)
	go func() {
		s.log.Info("daemon listening", "addr", s.Addr().String(), "pid", info.PID)
		// Serve returns ErrServerClosed on a clean Shutdown; that is success.
		if err := s.http.Serve(s.listen); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	var startupErr error
	if onReady != nil {
		startupErr = s.runStartupRecovery(ctx, onReady)
		switch {
		case startupErr == nil:
			s.ready.Store(true)
		case errors.Is(startupErr, errStartupRecoveryInterrupted):
			// Cancellation or an explicit shutdown request interrupted startup.
			// The select below performs the ordinary graceful shutdown, so this is
			// not a terminal recovery failure and should not make Run return an error.
			startupErr = nil
		default:
			s.startupFailed.Store(true)
			s.log.Error("startup recovery failed; daemon remains unready", "err", startupErr)
		}
	}

	select {
	case err := <-serveErr:
		// Serve died on its own (bind already happened, so this is a real
		// runtime failure) before any shutdown signal.
		return errors.Join(startupErr, err)
	case <-s.shutdownRequested:
		s.log.Info("shutdown requested over HTTP", "timeout", s.cfg.ShutdownTimeout)
	case <-ctx.Done():
		s.log.Info("shutdown signal received, draining connections", "timeout", s.cfg.ShutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		// The deadline elapsed with connections still open; force them closed.
		s.log.Warn("graceful shutdown timed out, forcing close", "err", err)
		_ = s.http.Close()
		return errors.Join(startupErr, fmt.Errorf("graceful shutdown exceeded %s: %w", s.cfg.ShutdownTimeout, err))
	}

	s.log.Info("daemon stopped cleanly")
	return errors.Join(startupErr, <-serveErr)
}

func (s *Server) runStartupRecovery(ctx context.Context, recoverStartup func(context.Context) error) error {
	var lastErr error
	for attempt := 1; attempt <= startupRecoveryMaxAttempts; attempt++ {
		attemptTimeout := s.startupRecoveryTimeout()
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
		// Keep the result buffered: a callback that ignores cancellation may finish
		// after the server has timed out the attempt or begun shutting down.
		result := make(chan error, 1)
		go func() {
			result <- recoverStartup(attemptCtx)
		}()

		select {
		case lastErr = <-result:
			if ctx.Err() != nil {
				cancelAttempt()
				return errors.Join(errStartupRecoveryInterrupted, ctx.Err())
			}
			if attemptCtx.Err() != nil {
				cancelAttempt()
				return fmt.Errorf("startup recovery attempt %d timed out after %s: %w",
					attempt, attemptTimeout, attemptCtx.Err())
			}
		case <-attemptCtx.Done():
			if ctx.Err() != nil {
				return s.drainInterruptedRecovery(
					cancelAttempt, result, errors.Join(errStartupRecoveryInterrupted, ctx.Err()),
				)
			}
			// A timed-out callback may still be unwinding. Never start another
			// recovery writer concurrently, and never tear its dependencies down
			// underneath it. drainInterruptedRecovery cancels and joins the callback
			// before Run can return to daemon teardown.
			return s.drainInterruptedRecovery(cancelAttempt, result, fmt.Errorf(
				"startup recovery attempt %d timed out after %s: %w",
				attempt, attemptTimeout, attemptCtx.Err(),
			))
		case <-s.shutdownRequested:
			return s.drainInterruptedRecovery(cancelAttempt, result, errStartupRecoveryInterrupted)
		}
		cancelAttempt()
		if lastErr == nil {
			return nil
		}
		if attempt == startupRecoveryMaxAttempts {
			break
		}
		s.log.Warn("startup recovery failed; retrying",
			"attempt", attempt,
			"maxAttempts", startupRecoveryMaxAttempts,
			"err", lastErr,
		)
		timer := time.NewTimer(startupRecoveryRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(errStartupRecoveryInterrupted, ctx.Err())
		case <-s.shutdownRequested:
			timer.Stop()
			return errStartupRecoveryInterrupted
		}
	}
	return fmt.Errorf("startup recovery failed after %d attempts: %w", startupRecoveryMaxAttempts, lastErr)
}

// drainInterruptedRecovery cancels and joins an in-flight recovery callback.
// Recovery owns lifecycle, controller, and store writers, so returning before it
// has unwound would race daemon teardown and could attach a controller after
// StopAll. The warning threshold is diagnostic rather than permission to leak the
// callback: our callback is required to honor its context, and safety wins over a
// concurrent teardown if a future implementation violates that contract.
func (s *Server) drainInterruptedRecovery(
	cancel context.CancelFunc,
	result <-chan error,
	cause error,
) error {
	cancel()
	warningAfter := s.startupRecoveryDrainWarning
	if warningAfter <= 0 {
		warningAfter = defaultStartupRecoveryDrainWarning
	}
	timer := time.NewTimer(warningAfter)
	defer timer.Stop()
	select {
	case callbackErr := <-result:
		return errors.Join(cause, callbackErr)
	case <-timer.C:
		s.startupFailed.Store(true)
		s.log.Error(
			"startup recovery did not stop promptly after cancellation; waiting before teardown",
			"warningAfter", warningAfter,
		)
		callbackErr := <-result
		return errors.Join(
			cause,
			fmt.Errorf("startup recovery ignored cancellation for at least %s", warningAfter),
			callbackErr,
		)
	}
}

func (s *Server) startupRecoveryTimeout() time.Duration {
	if s.startupRecoveryAttemptTimeout > 0 {
		return s.startupRecoveryAttemptTimeout
	}
	return defaultStartupRecoveryAttemptTimeout
}

func (s *Server) boundPort() int {
	if tcp, ok := s.listen.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	return s.cfg.Port
}

func (s *Server) requestShutdown() {
	s.shutdownOnce.Do(func() {
		close(s.shutdownRequested)
	})
}

// RequestShutdown triggers the same clean shutdown as POST /shutdown: it makes
// Run return so the daemon exits without tearing down sessions. Idempotent.
func (s *Server) RequestShutdown() { s.requestShutdown() }
