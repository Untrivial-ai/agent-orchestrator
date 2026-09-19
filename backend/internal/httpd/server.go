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
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
)

// Server is the daemon's HTTP server together with its lifecycle: bind the
// loopback port, publish the running.json handshake, serve until the context
// is cancelled, then shut down gracefully and clean up the handshake file.
type Server struct {
	cfg    config.Config
	log    *slog.Logger
	http   *http.Server
	listen net.Listener

	shutdownRequested chan struct{}
	shutdownOnce      sync.Once
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
		cfg:               cfg,
		log:               log,
		listen:            ln,
		shutdownRequested: make(chan struct{}),
	}
	srv.http = &http.Server{
		Handler: NewRouterWithControl(cfg, log, termMgr, deps, ControlDeps{
			RequestShutdown: srv.requestShutdown,
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

// RunWithReady is Run with a callback invoked after the listener has been
// published and its serving goroutine has started. The callback must return
// promptly; it is intended for boot work that can safely continue while the
// daemon serves read-only durable state.
func (s *Server) RunWithReady(ctx context.Context, onReady func()) error {
	return s.run(ctx, onReady)
}

func (s *Server) run(ctx context.Context, onReady func()) error {
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
	if onReady != nil {
		onReady()
	}

	select {
	case err := <-serveErr:
		// Serve died on its own (bind already happened, so this is a real
		// runtime failure) before any shutdown signal.
		return err
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
		return fmt.Errorf("graceful shutdown exceeded %s: %w", s.cfg.ShutdownTimeout, err)
	}

	s.log.Info("daemon stopped cleanly")
	return <-serveErr
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
