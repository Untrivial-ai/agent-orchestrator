package httpd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// LANManager owns the daemon's second, network-facing HTTP listener. It binds
// 0.0.0.0 only while Connect Mobile is enabled and wraps the shared router in
// authMiddleware. The loopback listener is unaffected.
type LANManager struct {
	handler     http.Handler // shared router, already auth-wrapped
	defaultPort int
	log         *slog.Logger
	state       *authState // shared with authMiddleware; SetPasswordHash writes through here

	mu    sync.Mutex
	srv   *http.Server
	ln    net.Listener
	bound int
}

// NewLANManager wraps handler in the LAN control-block and authMiddleware
// (backed by the shared state) and returns a manager that can start/stop the
// network-facing listener. Most callers want NewMobileLAN, which owns the state.
func NewLANManager(handler http.Handler, state *authState, defaultPort int, log *slog.Logger, sink ports.EventSink) *LANManager {
	lock := newLockout(5, time.Minute, time.Now)
	return &LANManager{
		handler:     lanControlBlock(authMiddleware(state, lock, newMobileConnectReporter(sink, time.Now))(handler)),
		defaultPort: defaultPort,
		log:         loggerOrDefault(log),
		state:       state,
	}
}

// lanControlBlockedPrefixes are the loopback-only daemon-control route
// prefixes that must never be reachable through the LAN listener: /shutdown,
// the telemetry routes under /internal/, and the Connect Mobile control
// surface under /api/v1/mobile, developer maintenance routes under /api/v1/dev,
// host-mutating installer routes under /api/v1/system/install, and personal
// Codex account-management routes under /api/v1/agents/codex. Some routes
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
	"/api/v1/agents/codex",
}

// loopbackOnlyJSON answers a request for a route this daemon serves, but not on
// this listener. It is deliberately NOT ROUTE_NOT_FOUND: that code reads as
// "this daemon is too old / that endpoint does not exist", so an operator who
// hits a policy block goes and audits daemon builds and finds nothing wrong,
// because nothing is wrong. This says the block is a decision.
//
// What it does not do is resolve the route. Every path under a blocked prefix
// gets this same answer whether or not a handler exists behind it, so the
// response is a compile-time constant of the AO build and never an oracle for
// which loopback-only routes this particular daemon has. That is also why it is
// safe to serve before authMiddleware: see the note on lanControlBlock.
//
// The status stays 404. The route genuinely is not mounted on this listener —
// the LAN handler chain is a different one — and holding the status still keeps
// every existing client's error handling working; the code and message are
// what carry the diagnosis.
func loopbackOnlyJSON(w http.ResponseWriter, r *http.Request) {
	envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "ROUTE_LOOPBACK_ONLY",
		r.Method+" "+r.URL.Path+" is served on this daemon's loopback listener only and is deliberately "+
			"not exposed on the network listener — run the command on that machine, or over loopback", nil)
}

// lanControlBlock returns 404 for any request whose path is, or is nested
// under, a loopback-only control-route prefix, before it ever reaches auth or
// the shared router. It also stamps the LAN-listener marker every layer below
// reads.
//
// It answers before authMiddleware, and stays there. Being outermost is the
// load-bearing property: no code below — not auth, not the router's middleware
// stack — can run for a blocked path, whatever headers the caller forged. A
// message that only an authenticated caller could see would mean moving this
// inside auth, or evaluating the credential twice; the first trades a
// structural guarantee for wording, and neither buys anything, because what an
// unauthenticated caller learns here is a constant of the AO build (the prefix
// list, which is in the source) and not a fact about this machine. It still
// never confirms that a specific route exists — see loopbackOnlyJSON.
func lanControlBlock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLANControlBlockedPath(r.URL.Path) || isLANControlBlockedRequest(r.Method, r.URL.Path) {
			loopbackOnlyJSON(w, r)
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

// NewMobileLAN constructs a LANManager with its own private authState. Callers
// outside this package (the daemon) cannot construct an authState directly
// since it is unexported; this gives them a LANManager that owns one, and the
// daemon rotates the connection password exclusively via SetPasswordHash.
func NewMobileLAN(handler http.Handler, defaultPort int, log *slog.Logger, sink ports.EventSink) *LANManager {
	return NewLANManager(handler, &authState{}, defaultPort, log, sink)
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

// Start binds the network-facing listener on 0.0.0.0:port (falling back to an
// ephemeral port if that port is in use) and serves the wrapped handler. It is
// idempotent: a second call while running returns the already-bound port.
func (m *LANManager) Start(port int) (int, error) {
	m.mu.Lock()
	if m.srv != nil {
		defer m.mu.Unlock()
		return m.bound, nil // idempotent
	}
	if port == 0 {
		port = m.defaultPort
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		if !isAddrInUse(err) {
			m.mu.Unlock()
			return 0, fmt.Errorf("bind LAN 0.0.0.0:%d: %w", port, err)
		}
		//nolint:gosec // G102: binding all interfaces is the deliberate purpose of the Connect Mobile LAN listener; it runs only while the bridge is enabled and behind authMiddleware.
		if ln, err = net.Listen("tcp", "0.0.0.0:0"); err != nil {
			m.mu.Unlock()
			return 0, fmt.Errorf("bind LAN ephemeral: %w", err)
		}
		m.log.Warn("LAN port in use; bound ephemeral", "wanted", port, "bound", ln.Addr())
	}
	m.ln = ln
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		m.mu.Unlock()
		_ = ln.Close()
		return 0, fmt.Errorf("bind LAN: unexpected listener address type %T", ln.Addr())
	}
	m.bound = tcpAddr.Port
	m.srv = &http.Server{Handler: m.handler, ReadHeaderTimeout: 10 * time.Second}
	srv := m.srv
	boundPort := m.bound
	m.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("LAN listener serve", "err", err)
		}
	}()
	m.log.Info("LAN listener started", "addr", ln.Addr())
	return boundPort, nil
}

// Stop gracefully shuts down the listener (honoring ctx) and clears the bound
// state. It is a no-op if the listener is not running.
func (m *LANManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	srv := m.srv
	m.srv, m.ln, m.bound = nil, nil, 0
	m.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
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
