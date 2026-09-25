package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

const (
	browserViewerTicketTTL            = 5 * time.Minute
	browserControlBuffer              = 64
	browserWireLimit                  = browserstream.MaxFrameBytes + 512
	browserInteractionRefreshInterval = 30 * time.Second
)

type browserStreams struct {
	mu       sync.Mutex
	sessions map[string]*browserRelaySession
}

type browserRelaySession struct {
	worker *browserWorkerPeer
	viewer *browserViewerPeer
}

type browserWorkerPeer struct {
	claims worker.Claims
	send   chan []byte
	done   chan struct{}
	once   sync.Once
}

type browserViewerPeer struct {
	subject                string
	operate                bool
	controls               chan []byte
	frames                 *browserstream.Latest
	done                   chan struct{}
	once                   sync.Once
	rateMu                 sync.Mutex
	rateAt                 time.Time
	rate                   int
	started                time.Time
	framesReceived         atomic.Uint64
	framesReplaced         atomic.Uint64
	bytesReceived          atomic.Uint64
	first                  sync.Once
	replaced               atomic.Bool
	streamEpoch            atomic.Uint64
	lastInteraction        time.Time
	interactionWorkerEpoch int64
}

func newBrowserStreams() *browserStreams {
	return &browserStreams{sessions: make(map[string]*browserRelaySession)}
}

func browserRelayKey(orgID, sessionID string) string { return orgID + "/" + sessionID }

func (p *browserWorkerPeer) close() { p.once.Do(func() { close(p.done) }) }

func (p *browserViewerPeer) close() {
	p.once.Do(func() {
		close(p.done)
		p.frames.Close()
	})
}

func (p *browserViewerPeer) allow(now time.Time) bool {
	p.rateMu.Lock()
	defer p.rateMu.Unlock()
	if p.rateAt.IsZero() || now.Sub(p.rateAt) >= time.Second {
		p.rateAt = now
		p.rate = 0
	}
	if p.rate >= 120 {
		return false
	}
	p.rate++
	return true
}

func (b *browserStreams) registerWorker(key string, peer *browserWorkerPeer) (*browserViewerPeer, func()) {
	b.mu.Lock()
	session := b.sessions[key]
	if session == nil {
		session = &browserRelaySession{}
		b.sessions[key] = session
	}
	if session.worker != nil {
		session.worker.close()
	}
	session.worker = peer
	viewer := session.viewer
	b.mu.Unlock()
	return viewer, func() {
		b.mu.Lock()
		if current := b.sessions[key]; current != nil && current.worker == peer {
			current.worker = nil
			if current.viewer == nil {
				delete(b.sessions, key)
			}
		}
		b.mu.Unlock()
	}
}

func (b *browserStreams) registerViewer(key string, peer *browserViewerPeer) (*browserWorkerPeer, bool, func() bool) {
	b.mu.Lock()
	session := b.sessions[key]
	if session == nil {
		session = &browserRelaySession{}
		b.sessions[key] = session
	}
	if session.viewer != nil && session.viewer.subject != peer.subject {
		b.mu.Unlock()
		return nil, false, func() bool { return false }
	}
	if session.viewer != nil {
		session.viewer.replaced.Store(true)
		session.viewer.close()
	}
	session.viewer = peer
	workerPeer := session.worker
	b.mu.Unlock()
	return workerPeer, true, func() bool {
		b.mu.Lock()
		removed := false
		if current := b.sessions[key]; current != nil && current.viewer == peer {
			current.viewer = nil
			removed = true
			if current.worker == nil {
				delete(b.sessions, key)
			}
		}
		b.mu.Unlock()
		return removed
	}
}

func (b *browserStreams) worker(key string) *browserWorkerPeer {
	b.mu.Lock()
	defer b.mu.Unlock()
	if session := b.sessions[key]; session != nil {
		return session.worker
	}
	return nil
}

func (b *browserStreams) viewer(key string) *browserViewerPeer {
	b.mu.Lock()
	defer b.mu.Unlock()
	if session := b.sessions[key]; session != nil {
		return session.viewer
	}
	return nil
}

func enqueueBrowserControl(send chan []byte, done <-chan struct{}, control browserstream.Control) bool {
	payload, err := json.Marshal(control)
	if err != nil {
		return false
	}
	select {
	case send <- payload:
		return true
	case <-done:
		return false
	default:
		return false
	}
}

func browserTicketScope(scopes []string, expected string) bool {
	for _, scope := range scopes {
		if scope == expected {
			return true
		}
	}
	return false
}

func browserTicketSubject(scopes []string) string {
	for _, scope := range scopes {
		if subject, ok := strings.CutPrefix(scope, "subject:"); ok {
			return subject
		}
	}
	return ""
}

func (s *Server) createBrowserViewerTicket(w http.ResponseWriter, r *http.Request) {
	if !s.browserViewerEnabled {
		writeError(w, r, http.StatusNotFound, "not_found", "The browser viewer is not enabled.")
		return
	}
	if s.draining.Load() {
		writeError(w, r, http.StatusServiceUnavailable, "draining", "The control plane is draining.")
		return
	}
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	token, scopes, err := s.store.IssueBrowserViewerTicket(
		r.Context(), principalFrom(r), orgID, sessionID, browserViewerTicketTTL,
	)
	if errors.Is(err, postgres.ErrForbidden) {
		writeError(w, r, http.StatusForbidden, "BROWSER_POLICY_DENIED", "Browser viewing is not allowed for this session.")
		return
	}
	if errors.Is(err, postgres.ErrWorkerUnavailable) {
		writeError(w, r, http.StatusConflict, "BROWSER_SESSION_UNAVAILABLE", "The session browser is not ready yet.")
		return
	}
	if err != nil {
		s.writeWorkspaceStoreError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"ticket": token, "expiresIn": int(browserViewerTicketTTL.Seconds()),
		"protocolVersion": browserstream.Version,
		"canOperate":      browserTicketScope(scopes, "browser:operate"),
	})
	if s.logger != nil {
		s.logger.Debug("browser viewer ticket issued",
			"outcome", "issued", "can_operate", browserTicketScope(scopes, "browser:operate"))
	}
}

func (s *Server) connectBrowserViewer(w http.ResponseWriter, r *http.Request) {
	if !s.browserViewerEnabled {
		writeError(w, r, http.StatusNotFound, "not_found", "The browser viewer is not enabled.")
		return
	}
	if s.draining.Load() {
		writeError(w, r, http.StatusServiceUnavailable, "draining", "The control plane is draining.")
		return
	}
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	token := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil || token == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid orgId, sessionId, and ticket are required.")
		return
	}
	if !s.browserViewerOriginAllowed(r) {
		writeError(w, r, http.StatusForbidden, "BROWSER_ORIGIN_DENIED", "The browser viewer origin is not allowed.")
		return
	}
	ticket, err := s.store.OpenBrowserViewerTicket(r.Context(), token)
	if errors.Is(err, postgres.ErrInvalidTicket) || errors.Is(err, postgres.ErrStaleWorker) {
		writeError(w, r, http.StatusUnauthorized, "INVALID_BROWSER_TICKET", "The browser ticket is invalid, expired, replaced, or already used.")
		return
	}
	if err != nil {
		s.writeWorkspaceStoreError(w, r, err)
		return
	}
	subject := browserTicketSubject(ticket.Scopes)
	if ticket.OrgID != orgID || ticket.SessionID != sessionID || subject == "" || !browserTicketScope(ticket.Scopes, "browser:view") {
		writeError(w, r, http.StatusUnauthorized, "INVALID_BROWSER_TICKET", "The browser ticket does not grant access to this session.")
		return
	}
	peer := &browserViewerPeer{
		subject: subject, operate: browserTicketScope(ticket.Scopes, "browser:operate"),
		controls: make(chan []byte, browserControlBuffer), frames: browserstream.NewLatest(),
		done: make(chan struct{}), started: time.Now(),
	}
	key := browserRelayKey(orgID, sessionID)
	workerPeer, accepted, unregister := s.browserStreams.registerViewer(key, peer)
	if !accepted {
		writeError(w, r, http.StatusConflict, "BROWSER_VIEWER_IN_USE", "Another user is controlling this browser.")
		return
	}
	defer func() {
		removed := unregister()
		peer.close()
		if current := s.browserStreams.worker(key); removed && current != nil {
			enqueueBrowserControl(current.send, current.done, browserstream.Control{Type: "detach", Version: browserstream.Version})
		}
		if s.logger != nil {
			s.logger.Info("browser viewer relay closed",
				"duration_ms", time.Since(peer.started).Milliseconds(),
				"frames_received", peer.framesReceived.Load(),
				"frames_replaced", peer.framesReplaced.Load(),
				"bytes_received", peer.bytesReceived.Load(),
			)
		}
	}()

	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled, InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(browserstream.MaxControlBytes)
	if workerPeer != nil {
		enqueueBrowserControl(workerPeer.send, workerPeer.done, browserstream.Control{Type: "attach", Version: browserstream.Version})
	} else {
		enqueueBrowserControl(peer.controls, peer.done, browserstream.Control{
			Type: "error", Version: browserstream.Version, Code: "BROWSER_SESSION_UNAVAILABLE",
			Message: "Waiting for the session browser.",
		})
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	result := make(chan error, 4)
	var writeMu sync.Mutex
	go func() { result <- writeBrowserControls(ctx, connection, peer.controls, peer.done, &writeMu) }()
	go func() { result <- writeBrowserFrames(ctx, connection, peer.frames, &writeMu) }()
	go func() { result <- s.readBrowserViewer(ctx, connection, key, peer) }()
	go func() { result <- keepBrowserConnectionAlive(ctx, connection) }()
	select {
	case <-peer.done:
	case <-ctx.Done():
	case <-result:
	case <-s.drain:
		_ = connection.Close(websocket.StatusTryAgainLater, "control plane draining")
	}
	if peer.replaced.Load() {
		_ = connection.Close(browserstream.ViewerReplacedCloseCode, "browser viewer opened in another window")
	}
}

func (s *Server) readBrowserViewer(ctx context.Context, connection *websocket.Conn, key string, peer *browserViewerPeer) error {
	for {
		kind, payload, err := connection.Read(ctx)
		if err != nil {
			return err
		}
		if s.browserStreams.viewer(key) != peer {
			return nil
		}
		if kind != websocket.MessageText || len(payload) == 0 || len(payload) > browserstream.MaxControlBytes {
			return errors.New("invalid browser viewer message")
		}
		var control browserstream.Control
		if json.Unmarshal(payload, &control) != nil || control.Version != browserstream.Version || !viewerControlAllowed(control.Type) {
			return errors.New("unsupported browser viewer message")
		}
		if control.Type != "ping" && control.Type != "detach" && !peer.allow(time.Now()) {
			enqueueBrowserControl(peer.controls, peer.done, browserstream.Control{
				Type: "input_rejected", Version: browserstream.Version, InputSeq: control.InputSeq,
				Code: "BROWSER_INPUT_RATE_EXCEEDED",
			})
			continue
		}
		if viewerControlOperates(control.Type) && !peer.operate {
			enqueueBrowserControl(peer.controls, peer.done, browserstream.Control{
				Type: "input_rejected", Version: browserstream.Version, InputSeq: control.InputSeq,
				Code: "BROWSER_POLICY_DENIED",
			})
			continue
		}
		if control.Type == "detach" {
			return nil
		}
		workerPeer := s.browserStreams.worker(key)
		if workerPeer != nil && browserControlIsInteraction(control) && control.StreamEpoch == peer.streamEpoch.Load() &&
			(peer.interactionWorkerEpoch != workerPeer.claims.Epoch || time.Since(peer.lastInteraction) >= browserInteractionRefreshInterval) {
			leaseCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.store.RefreshBrowserInteraction(leaseCtx, domain.Principal{UserID: peer.subject},
				workerPeer.claims.OrgID, workerPeer.claims.SessionID, workerPeer.claims.Epoch)
			cancel()
			if err != nil {
				code := "BROWSER_SESSION_UNAVAILABLE"
				if errors.Is(err, postgres.ErrForbidden) {
					code = "BROWSER_POLICY_DENIED"
				}
				enqueueBrowserControl(peer.controls, peer.done, browserstream.Control{
					Type: "input_rejected", Version: browserstream.Version, StreamEpoch: control.StreamEpoch,
					InputSeq: control.InputSeq, Code: code,
				})
				continue
			}
			peer.lastInteraction = time.Now()
			peer.interactionWorkerEpoch = workerPeer.claims.Epoch
		}
		if s.browserStreams.viewer(key) != peer {
			return nil
		}
		if workerPeer == nil || !enqueueBrowserControl(workerPeer.send, workerPeer.done, control) {
			enqueueBrowserControl(peer.controls, peer.done, browserstream.Control{
				Type: "input_rejected", Version: browserstream.Version, InputSeq: control.InputSeq,
				Code: "BROWSER_SESSION_UNAVAILABLE", Message: "The session browser is reconnecting.",
			})
		}
	}
}

func (s *Server) workerBrowserStream(w http.ResponseWriter, r *http.Request) {
	if !s.browserViewerEnabled {
		writeError(w, r, http.StatusNotFound, "not_found", "The browser viewer is not enabled.")
		return
	}
	if s.draining.Load() {
		writeError(w, r, http.StatusServiceUnavailable, "draining", "The control plane is draining.")
		return
	}
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:transport") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:transport scope is required.")
		return
	}
	sessionID := chi.URLParam(r, "sessionId")
	if sessionID != claims.SessionID {
		writeError(w, r, http.StatusForbidden, "SESSION_SCOPE_MISMATCH", "The worker cannot open another session's browser stream.")
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled, InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(browserWireLimit)
	peer := &browserWorkerPeer{claims: claims, send: make(chan []byte, browserControlBuffer), done: make(chan struct{})}
	key := browserRelayKey(claims.OrgID, claims.SessionID)
	viewerPeer, unregister := s.browserStreams.registerWorker(key, peer)
	defer func() {
		unregister()
		peer.close()
		if s.browserStreams.worker(key) == nil {
			if currentViewer := s.browserStreams.viewer(key); currentViewer != nil {
				enqueueBrowserControl(currentViewer.controls, currentViewer.done, browserstream.Control{
					Type: "error", Version: browserstream.Version, Code: "BROWSER_SESSION_UNAVAILABLE",
					Message: "The session browser is reconnecting.",
				})
			}
		}
	}()
	if viewerPeer != nil {
		enqueueBrowserControl(peer.send, peer.done, browserstream.Control{Type: "attach", Version: browserstream.Version})
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	result := make(chan error, 3)
	go func() { result <- writeBrowserControls(ctx, connection, peer.send, peer.done, &sync.Mutex{}) }()
	go func() { result <- s.readBrowserWorker(ctx, connection, key, peer) }()
	go func() { result <- keepBrowserConnectionAlive(ctx, connection) }()
	select {
	case <-peer.done:
	case <-ctx.Done():
	case <-result:
	case <-s.drain:
		_ = connection.Close(websocket.StatusTryAgainLater, "control plane draining")
	}
}

func (s *Server) readBrowserWorker(ctx context.Context, connection *websocket.Conn, key string, peer *browserWorkerPeer) error {
	for {
		kind, payload, err := connection.Read(ctx)
		if err != nil {
			return err
		}
		if s.browserStreams.worker(key) != peer {
			return nil
		}
		viewerPeer := s.browserStreams.viewer(key)
		if kind == websocket.MessageBinary {
			if len(payload) > browserWireLimit {
				return errors.New("browser frame exceeds relay limit")
			}
			frame, err := browserstream.DecodeFrame(payload)
			if err != nil {
				return err
			}
			if viewerPeer != nil {
				viewerPeer.streamEpoch.Store(frame.StreamEpoch)
				viewerPeer.framesReceived.Add(1)
				viewerPeer.bytesReceived.Add(uint64(len(payload)))
				if viewerPeer.frames.Put(payload) {
					viewerPeer.framesReplaced.Add(1)
				}
				viewerPeer.first.Do(func() {
					if s.logger != nil {
						s.logger.Info("browser viewer first relayed frame",
							"attach_to_relay_ms", time.Since(viewerPeer.started).Milliseconds(),
							"frame_bytes", len(frame.JPEG), "width", frame.Width, "height", frame.Height,
						)
					}
				})
			}
			continue
		}
		if kind != websocket.MessageText || len(payload) == 0 || len(payload) > browserstream.MaxControlBytes {
			return errors.New("invalid browser worker message")
		}
		var control browserstream.Control
		if json.Unmarshal(payload, &control) != nil || control.Version != browserstream.Version || !workerControlAllowed(control.Type) {
			return errors.New("unsupported browser worker message")
		}
		if viewerPeer != nil && !enqueueBrowserControl(viewerPeer.controls, viewerPeer.done, control) {
			return errors.New("browser viewer control queue is full")
		}
	}
}

func writeBrowserControls(
	ctx context.Context,
	connection *websocket.Conn,
	controls <-chan []byte,
	done <-chan struct{},
	writeMu *sync.Mutex,
) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		case payload := <-controls:
			writeMu.Lock()
			err := connection.Write(ctx, websocket.MessageText, payload)
			writeMu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

func writeBrowserFrames(ctx context.Context, connection *websocket.Conn, frames *browserstream.Latest, writeMu *sync.Mutex) error {
	for {
		payload, ok := frames.Next(ctx)
		if !ok {
			return ctx.Err()
		}
		writeMu.Lock()
		err := connection.Write(ctx, websocket.MessageBinary, payload)
		writeMu.Unlock()
		if err != nil {
			return err
		}
	}
}

func viewerControlAllowed(messageType string) bool {
	switch messageType {
	case "ping", "detach", "viewport", "input", "navigate", "tab", "dialog", "devtools":
		return true
	default:
		return false
	}
}

func viewerControlOperates(messageType string) bool {
	switch messageType {
	case "input", "navigate", "tab", "dialog", "devtools":
		return true
	default:
		return false
	}
}

func browserControlIsInteraction(control browserstream.Control) bool {
	if control.InputSeq == 0 || control.InputSeq > (1<<53)-1 || control.StreamEpoch == 0 || control.StreamEpoch > (1<<53)-1 {
		return false
	}
	switch control.Type {
	case "navigate", "tab", "dialog", "devtools":
		return true
	case "input":
		switch control.Kind {
		case "pointerMove":
			return control.Buttons != 0
		case "pointerDown", "pointerUp", "doubleClick", "wheel", "keyDown", "keyUp", "text",
			"compositionStart", "compositionUpdate", "compositionCommit", "compositionCancel":
			return true
		}
	}
	return false
}

func workerControlAllowed(messageType string) bool {
	switch messageType {
	case "hello", "attached", "state", "viewport_ack", "input_ack", "input_rejected",
		"agent_action", "control_owner", "error", "ping", "pong":
		return true
	default:
		return false
	}
}

func (s *Server) browserViewerOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin == "null" || strings.EqualFold(origin, "file://") {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	normalized := strings.ToLower(strings.TrimRight(origin, "/"))
	for _, allowed := range s.browserViewerOrigins {
		if normalized == strings.ToLower(strings.TrimRight(strings.TrimSpace(allowed), "/")) {
			return true
		}
	}
	return false
}

func keepBrowserConnectionAlive(ctx context.Context, connection *websocket.Conn) error {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := connection.Ping(pingCtx)
			cancel()
			if err == nil {
				failures = 0
				continue
			}
			failures++
			if failures >= 3 {
				return err
			}
		}
	}
}
