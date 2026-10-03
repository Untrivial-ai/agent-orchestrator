package httpd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
)

// LANManager owns the daemon's second, network-facing HTTP listener. It binds
// 0.0.0.0 only while Connect Mobile is enabled and wraps the shared router in
// authMiddleware. The loopback listener is unaffected.
type LANManager struct {
	handler     http.Handler // shared router, already auth-wrapped
	defaultPort int
	log         *slog.Logger
	state       *authState // shared with authMiddleware; SetPasswordHash writes through here
	lock        *lockout

	transitionMu sync.Mutex // serializes Start and Stop without blocking status reads
	mu           sync.Mutex
	srv          *http.Server
	ln           *lanListener
	cancel       context.CancelFunc
	bound        int
	bindHost     string
}

// NewLANManager wraps handler in the LAN control-block and authMiddleware
// (backed by the shared state) and returns a manager that can start/stop the
// network-facing listener. Most callers want NewMobileLAN, which owns the state.
func NewLANManager(handler http.Handler, state *authState, defaultPort int, log *slog.Logger, sink ports.EventSink) *LANManager {
	lock := newLockout(time.Now)
	return &LANManager{
		handler:     lanControlBlock(authMiddleware(state, lock, newMobileConnectReporter(sink, time.Now))(handler)),
		defaultPort: defaultPort,
		log:         loggerOrDefault(log),
		state:       state,
		lock:        lock,
	}
}

// lanControlBlockedPrefixes are the loopback-only daemon-control route
// prefixes that must never be reachable through the LAN listener: /shutdown,
// the telemetry routes under /internal/, and the Connect Mobile control
// surface under /api/v1/mobile, developer maintenance routes under /api/v1/dev,
// host-mutating installer routes under /api/v1/system/install, and personal
// Codex account-management routes under /api/v1/agents/codex/accounts and
// /api/v1/agents/codex/account-switches (the harmless read-only Codex model
// routes stay reachable so mobile can pick a model). Some routes
// are gated in the shared router by localControlRequest, which trusts the
// client-supplied Host header. That header is spoofable by any LAN client. The
// LAN listener is the one thing a caller cannot spoof: it is the physical socket
// the request arrived on. So the block below is applied only to the LAN-served
// handler, outermost (wrapping authMiddleware), independent of any header.
var lanControlBlockedPrefixes = []string{
	"/shutdown",
	"/internal/",
	"/api/v1/mobile",
	"/api/v1/dev",
	"/api/v1/browser",
	"/api/v1/desktop",
	"/api/v1/system/install",
	"/api/v1/agents/codex/accounts",
	"/api/v1/agents/codex/account-switches",
}

// lanControlBlock returns 404 for any request whose path is, or is nested
// under, a loopback-only control-route prefix, before it ever reaches auth or
// the shared router. It answers as if the route were never mounted at all —
// no 403/401 that would confirm the path exists.
func lanControlBlock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLANControlBlockedPath(r.URL.Path) || isLANControlBlockedRequest(r.Method, r.URL.Path) {
			notFoundJSON(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(requestscope.WithLAN(r.Context())))
	})
}

func isLANControlBlockedRequest(method, path string) bool {
	trimmed := strings.TrimSuffix(path, "/")
	if method != http.MethodPost || !strings.HasPrefix(trimmed, "/api/v1/agents/") || !strings.HasSuffix(trimmed, "/install") {
		return false
	}
	target := strings.TrimSuffix(strings.TrimPrefix(trimmed, "/api/v1/agents/"), "/install")
	return !systeminstall.IsAgentTarget(systeminstall.Target(target))
}

// isLANControlBlockedPath reports whether path matches a blocked prefix on an
// exact segment boundary: "/api/v1/mobile" blocks itself and everything
// beneath it ("/api/v1/mobile/status") but must not catch unrelated siblings
// such as "/api/v1/mobileapp".
func isLANControlBlockedPath(path string) bool {
	if strings.HasPrefix(path, "/api/v1/sessions/") && strings.HasSuffix(strings.TrimSuffix(path, "/"), "/preview/server") {
		return true
	}
	for _, prefix := range lanControlBlockedPrefixes {
		trimmed := prefix
		if len(trimmed) > 1 && trimmed[len(trimmed)-1] == '/' {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if path == trimmed || strings.HasPrefix(path, trimmed+"/") {
			return true
		}
	}
	return false
}

// IsLANControlBlockedPathForTest exposes the LAN block check to package-external
// tests so route-level invariants can be asserted without a live listener.
func IsLANControlBlockedPathForTest(path string) bool { return isLANControlBlockedPath(path) }

// NewMobileLAN constructs a LANManager with its own private authState. Callers
// outside this package (the daemon) cannot construct an authState directly
// since it is unexported; this gives them a LANManager that owns one, and the
// daemon rotates the connection password exclusively via SetPasswordHash.
func NewMobileLAN(handler http.Handler, hostID string, defaultPort int, log *slog.Logger, sink ports.EventSink) *LANManager {
	return NewLANManager(expectedHostGuard(hostID)(handler), &authState{}, defaultPort, log, sink)
}

// expectedHostGuard prevents a saved address from sending work to a different
// AO installation after that address is reassigned. Older clients omit the
// header; auth still applies to them as before.
func expectedHostGuard(hostID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if expected := r.Header.Get("X-AO-Expected-Host-ID"); expected != "" && expected != hostID {
				envelope.WriteAPIError(w, r, http.StatusMisdirectedRequest, "conflict", "HOST_ID_MISMATCH", "This address belongs to another AO host; reconnect to the intended host.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SetPasswordHash rotates the connection password, clears lockouts inherited
// from the old credential, and closes existing LAN connections (including
// upgraded WebSockets). Satisfies controllers.LANController.
func (m *LANManager) SetPasswordHash(hash string) {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	if m.state.currentHash() == hash {
		return
	}
	m.state.setHash(hash)
	m.lock.resetAll()
	m.mu.Lock()
	ln := m.ln
	m.mu.Unlock()
	if ln != nil {
		if err := ln.closeConnections(); err != nil {
			m.log.Warn("close LAN connections after password rotation", "err", err)
		}
	}
}

// PasswordHash returns the current connection password hash. Used to snapshot the
// prior hash before an enable/regenerate so a failed persist can be rolled back.
// Satisfies controllers.LANController.
func (m *LANManager) PasswordHash() string {
	return m.state.currentHash()
}

// Start binds the authenticated listener on every interface for Connect Mobile.
func (m *LANManager) Start(port int) (int, error) {
	return m.start(port, "0.0.0.0")
}

// StartLoopback binds the same authenticated handler only where a local tunnel
// connector can reach it. A running listener cannot silently change bind mode.
func (m *LANManager) StartLoopback(port int) (int, error) {
	return m.start(port, "127.0.0.1")
}

func (m *LANManager) start(port int, host string) (int, error) {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	m.mu.Lock()
	if m.srv != nil {
		defer m.mu.Unlock()
		if m.bindHost != host {
			return 0, fmt.Errorf("authenticated listener already bound to %s; disable it before changing modes", m.bindHost)
		}
		return m.bound, nil // idempotent
	}
	if port == 0 {
		port = m.defaultPort
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if !isAddrInUse(err) {
			m.mu.Unlock()
			return 0, fmt.Errorf("bind authenticated listener %s: %w", addr, err)
		}
		if ln, err = net.Listen("tcp", fmt.Sprintf("%s:0", host)); err != nil {
			m.mu.Unlock()
			return 0, fmt.Errorf("bind authenticated listener ephemeral on %s: %w", host, err)
		}
		m.log.Warn("LAN port in use; bound ephemeral", "wanted", port, "bound", ln.Addr())
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		m.mu.Unlock()
		_ = ln.Close()
		return 0, fmt.Errorf("bind LAN: unexpected listener address type %T", ln.Addr())
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	tracked := &lanListener{Listener: ln, conns: make(map[*lanConn]struct{})}
	m.ln, m.cancel = tracked, cancel
	m.bound = tcpAddr.Port
	m.bindHost = host
	m.srv = &http.Server{
		Handler:           m.handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return streamCtx },
	}
	srv := m.srv
	boundPort := m.bound
	m.mu.Unlock()
	go func() {
		if err := srv.Serve(tracked); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("LAN listener serve", "err", err)
		}
	}()
	m.log.Info("LAN listener started", "addr", ln.Addr())
	return boundPort, nil
}

// Stop cancels LAN request contexts and drains the listener within ctx. It
// closes remaining connections, including hijacked WebSockets, before clearing
// the bound state. A grace-period error is returned after cleanup completes.
func (m *LANManager) Stop(ctx context.Context) error {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	m.mu.Lock()
	srv, ln, cancel := m.srv, m.ln, m.cancel
	m.mu.Unlock()
	if srv == nil {
		return nil
	}
	cancel()
	err := srv.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, srv.Close())
	}
	// Stop may run before Serve registers its listener with http.Server.
	// Close our listener explicitly even when Shutdown had nothing to close.
	err = errors.Join(err, ln.Close())
	// Shutdown and Close deliberately leave hijacked connections alone. The
	// listener owns them until their actual Close, even after HTTP's handoff.
	err = errors.Join(err, ln.closeConnections())
	m.mu.Lock()
	m.srv, m.ln, m.cancel, m.bound, m.bindHost = nil, nil, nil, 0, ""
	m.mu.Unlock()
	return err
}

// lanListener tracks the lifetime of the underlying connections rather than
// HTTP states: a WebSocket leaves http.Server's registry when it is hijacked.
type lanListener struct {
	net.Listener
	mu        sync.Mutex
	conns     map[*lanConn]struct{}
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func (l *lanListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	tracked := &lanConn{Conn: conn, owner: l}
	l.conns[tracked] = struct{}{}
	return tracked, nil
}

func (l *lanListener) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		l.closeErr = l.Listener.Close()
	})
	return l.closeErr
}

func (l *lanListener) closeConnections() error {
	l.mu.Lock()
	conns := make([]*lanConn, 0, len(l.conns))
	for conn := range l.conns {
		conns = append(conns, conn)
	}
	l.mu.Unlock()
	var errs []error
	for _, conn := range conns {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type lanConn struct {
	net.Conn
	owner *lanListener
}

// Preserve the TCP capabilities net/http uses for response copying and sending
// a FIN before closing connections with unread request bodies.
func (c *lanConn) ReadFrom(r io.Reader) (int64, error) { return io.Copy(c.Conn, r) }

func (c *lanConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return nil
}

func (c *lanConn) Close() error {
	err := c.Conn.Close()
	c.owner.mu.Lock()
	delete(c.owner.conns, c)
	c.owner.mu.Unlock()
	return err
}

// Running reports whether the LAN listener is currently serving.
func (m *LANManager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.srv != nil
}

// BoundPort returns the port the listener is bound to, or 0 when not running.
func (m *LANManager) BoundPort() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bound
}
