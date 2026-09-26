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

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/websession"
)

// LANManager owns the daemon's second, network-facing HTTP listener. It binds
// the configured host (default 0.0.0.0) only while Connect Mobile is enabled and
// wraps the shared router in authMiddleware. The loopback listener is unaffected.
type LANManager struct {
	handler        http.Handler // shared router, already auth-wrapped
	defaultPort    int
	log            *slog.Logger
	state          *authState // shared with authMiddleware; SetPasswordHash writes through here
	bindHost       string     // bind address (0.0.0.0 or 127.0.0.1)
	strictPort     bool       // if true, fail on port conflict instead of ephemeral fallback
	identityConfig *IdentityAuthConfig

	transitionMu sync.Mutex // serializes Start and Stop without blocking status reads
	mu           sync.Mutex
	srv          *http.Server
	ln           *lanListener
	cancel       context.CancelFunc
	bound        int
}

// LANManagerConfig holds options for NewLANManager.
type LANManagerConfig struct {
	DefaultPort    int
	BindHost       string // default "0.0.0.0"
	StrictPort     bool
	SessionStore   *websession.Store
	IdentityConfig *IdentityAuthConfig
	// AuthState, when non-nil (via NewAuthState), is shared with the router's
	// login route so both validate against the identical password hash. Only
	// consulted by NewMobileLAN; NewLANManager always takes state explicitly.
	AuthState *authState
	// Lockout, when non-nil, is the same instance passed to
	// NewWebSessionHandlers for the shared router's login route, so a
	// brute-force attempt there trips the identical per-source lockout as any
	// other failed credential (ADR-0005 R5). Nil constructs a private one (matches
	// upstream behavior for callers that don't wire the web login route).
	Lockout *lockout
}

// isLoopbackHost reports whether host is a loopback bind address. Used both to
// derive trustXFF (ADR-0005 R5) and to enforce the identity-trust loopback invariant
// (ADR-0005 R7).
func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1"
}

// NewLANManager wraps handler in the LAN control-block and authMiddleware
// (backed by the shared state) and returns a manager that can start/stop the
// network-facing listener. Most callers want NewMobileLAN, which owns the state.
func NewLANManager(handler http.Handler, state *authState, cfg LANManagerConfig, log *slog.Logger, sink ports.EventSink) *LANManager {
	lock := cfg.Lockout
	if lock == nil {
		lock = newLockout(5, time.Minute, time.Now)
	}
	bindHost := cfg.BindHost
	if bindHost == "" {
		bindHost = "0.0.0.0"
	}

	sessChecker := cfg.SessionStore
	identityConfig := cfg.IdentityConfig

	// trustXFF only on the trusted-proxy path: a loopback bind reached solely
	// through a local proxy, whose X-Forwarded-For is genuine (ADR-0005 R5). An
	// untrusted 0.0.0.0 bind must never trust a client-supplied XFF.
	trustXFF := isLoopbackHost(bindHost)

	auth := authMiddlewareWithTrust(state, lock, newMobileConnectReporter(sink, time.Now), sessChecker, identityConfig, trustXFF)

	// csrfMiddleware runs as auth's "next" so it can read the AuthKind auth just
	// set on the request context (ADR-0005 R1, R2): it enforces same-origin only for
	// the ambient credentials (cookie, identity) that have no SameSite backstop,
	// and is a no-op for Bearer. It is scoped to the LAN listener only — the
	// loopback listener never wraps handler in auth/csrf at all, so the
	// Electron renderer's app://renderer origin and the CLI's no-Origin
	// requests are unaffected there, matching today's trust model.
	return &LANManager{
		handler:        lanControlBlock(capturePeerMiddleware(auth(csrfMiddleware(handler)))),
		defaultPort:    cfg.DefaultPort,
		log:            loggerOrDefault(log),
		state:          state,
		bindHost:       bindHost,
		strictPort:     cfg.StrictPort,
		identityConfig: identityConfig,
	}
}

// capturePeerMiddleware wraps the handler and captures the real transport peer
// address into the request context before authMiddleware runs. This ensures the
// login lockout keys off the genuine source, not a spoofed X-Forwarded-For.
func capturePeerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the real peer before RealIP can rewrite it.
		ctx := WithRealPeer(r.Context(), r.RemoteAddr)
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)
	})
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
		next.ServeHTTP(w, r)
	})
}

func isLANControlBlockedRequest(method, path string) bool {
	trimmed := strings.TrimSuffix(path, "/")
	return method == http.MethodPost &&
		strings.HasPrefix(trimmed, "/api/v1/agents/") &&
		strings.HasSuffix(trimmed, "/install")
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

// NewMobileLAN constructs a LANManager with its own private authState, unless
// cfg.AuthState is set (the daemon uses this to share the identical instance
// the shared router's login route validates against — see NewAuthState). The
// daemon rotates the connection password exclusively via SetPasswordHash.
func NewMobileLAN(handler http.Handler, cfg LANManagerConfig, log *slog.Logger, sink ports.EventSink) *LANManager {
	state := cfg.AuthState
	if state == nil {
		state = &authState{}
	}
	return NewLANManager(handler, state, cfg, log, sink)
}

// SetPasswordHash stores the current connection password hash on the shared
// authState so the auth middleware (already wrapping handler) validates
// against it. Satisfies controllers.LANController.
func (m *LANManager) SetPasswordHash(hash string) {
	m.state.setHash(hash)
}

// PasswordHash returns the current connection password hash. Used to snapshot the
// prior hash before an enable/regenerate so a failed persist can be rolled back.
// Satisfies controllers.LANController.
func (m *LANManager) PasswordHash() string {
	return m.state.currentHash()
}

// Start binds the network-facing listener on bindHost:port and serves the wrapped
// handler. If strictPort is true, it fails on port conflict; otherwise it falls
// back to an ephemeral port. It is idempotent: a second call while running
// returns the already-bound port.
func (m *LANManager) Start(port int) (int, error) {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	m.mu.Lock()
	if m.srv != nil {
		defer m.mu.Unlock()
		return m.bound, nil // idempotent
	}
	if port == 0 {
		port = m.defaultPort
	}

	// Check loopback-bind invariant: identity trust is only safe on loopback.
	// Refuse to start if this invariant is violated (ADR-0005 R7) — refuse, not warn.
	if m.identityConfig != nil && m.identityConfig.Enabled && !isLoopbackHost(m.bindHost) {
		m.mu.Unlock()
		return 0, fmt.Errorf("refusing to start: AO_CONNECT_TRUST_TAILSCALE_IDENTITY requires a loopback bind host (127.0.0.1 or ::1), got %q", m.bindHost)
	}

	addr := fmt.Sprintf("%s:%d", m.bindHost, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if !isAddrInUse(err) {
			m.mu.Unlock()
			return 0, fmt.Errorf("bind LAN %s: %w", addr, err)
		}
		if m.strictPort {
			m.mu.Unlock()
			return 0, fmt.Errorf("bind LAN %s: port already in use (strict mode)", addr)
		}
		//nolint:gosec // G102: binding all interfaces is the deliberate purpose of the Connect Mobile LAN listener; it runs only while the bridge is enabled and behind authMiddleware.
		if ln, err = net.Listen("tcp", fmt.Sprintf("%s:0", m.bindHost)); err != nil {
			m.mu.Unlock()
			return 0, fmt.Errorf("bind LAN %s ephemeral: %w", m.bindHost, err)
		}
		m.log.Warn("LAN port in use; bound ephemeral", "wanted", addr, "bound", ln.Addr())
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
	m.srv, m.ln, m.cancel, m.bound = nil, nil, nil, 0
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
