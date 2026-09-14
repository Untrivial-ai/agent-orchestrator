package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

const (
	// Browsers can take far longer than the handshake itself to actually open
	// the socket: a cold hostname costs a fresh TLS session, and Firefox burns
	// tens of seconds probing HTTP/3 — which it cannot carry a WebSocket over —
	// before falling back. A 30s ticket expired mid-fallback, so every upgrade
	// arrived already dead and the client retried forever. The ticket stays
	// single-use and session-scoped; only the window to redeem it is wider.
	terminalTicketTTL          = 5 * time.Minute
	terminalReadyTimeout       = 20 * time.Second
	terminalSessionTTL         = 30 * time.Minute
	agentTerminalTTL           = 24 * time.Hour
	terminalInteractionTTL     = 2 * time.Minute
	terminalInteractionRefresh = 30 * time.Second
	terminalPingInterval       = 20 * time.Second
	terminalPingTimeout        = 5 * time.Second
)

var errTerminalProcessUnavailable = errors.New("terminal process unavailable")

func (s *Server) createTerminalTicket(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	var input struct {
		Kind string `json:"kind"`
	}
	if err := decodeJSONLimit(w, r, &input, maxWorkerControlBody); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.Kind != "workspace" && input.Kind != "agent" {
		writeError(w, r, http.StatusUnprocessableEntity, "TERMINAL_KIND_UNSUPPORTED", "Terminal kind must be agent or workspace.")
		return
	}
	token, scopes, err := s.store.IssueTerminalTicket(
		r.Context(), principalFrom(r), orgID, sessionID, input.Kind, terminalTicketTTL,
	)
	if errors.Is(err, postgres.ErrForbidden) {
		writeError(w, r, http.StatusForbidden, "TERMINAL_POLICY_DENIED", "Terminal access is not allowed for this session.")
		return
	}
	if err != nil {
		s.writeWorkspaceStoreError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// routingKey is the terminal's stable affinity shard. An affinity-aware
	// entry can use it to co-locate this client's socket with the worker's
	// terminal stream on one replica, making the same-replica fast path the
	// norm. Inert until such routing is deployed; safe for clients to ignore.
	writeJSON(w, http.StatusCreated, map[string]any{
		"ticket": token, "expiresIn": int(terminalTicketTTL.Seconds()),
		"scopes": scopes, "routingKey": routingKeyString(sessionID),
	})
}

func (s *Server) connectTerminal(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("ticket"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "workspace"
	}
	after, err := strconv.ParseInt(defaultString(r.URL.Query().Get("after"), "0"), 10, 64)
	if token == "" || (kind != "workspace" && kind != "agent") || err != nil || after < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid ticket, kind, and after cursor are required.")
		return
	}
	ttl := terminalSessionTTL
	if kind == "agent" {
		ttl = agentTerminalTTL
	}
	terminal, err := s.store.OpenTerminal(r.Context(), token, kind, ttl)
	if errors.Is(err, postgres.ErrInvalidTicket) {
		if s.logger != nil {
			s.logger.Warn(
				"terminal ticket rejected",
				"reason", err,
				"kind", kind,
				"request_id", requestID(r),
			)
		}
		writeError(w, r, http.StatusUnauthorized, "INVALID_TERMINAL_TICKET", "The terminal ticket is invalid, expired, or already used.")
		return
	}
	if err != nil {
		s.writeWorkspaceStoreError(w, r, err)
		return
	}

	// Browser clients may be hosted on a separate Cloud UI origin. The
	// cryptographically random, single-use ticket is the request's CSRF and
	// authorization boundary, so origin affinity is neither required nor used.
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.closeTerminal(r, terminal)
		return
	}
	if s.logger != nil {
		s.logger.Info("browser terminal attached",
			"session_id", terminal.SessionID,
			"terminal_id", terminal.ID,
			"kind", terminal.Kind,
			"worker_epoch", terminal.WorkerEpoch,
		)
	}
	connection.SetReadLimit(maxTerminalFrame)
	defer func() {
		if terminal.Kind != "agent" {
			s.closeTerminal(r, terminal)
		}
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if err := s.store.RefreshTerminalInteraction(ctx, terminal, terminalInteractionTTL); err != nil {
		if s.logger != nil {
			s.logger.Debug("start terminal interaction lease", "error", err, "terminal_id", terminal.ID)
		}
	}
	go s.refreshTerminalInteraction(ctx, terminal)
	structured := r.URL.Query().Get("protocol") == "2"
	if structured && terminal.Kind == "workspace" {
		// Workspace reconnects create a fresh shell. Tell the client to discard
		// output from the previous shell and replay this one from sequence zero.
		after = 0
		if err := writeTerminalMessage(ctx, connection, terminalServerMessage{Type: "reset"}); err != nil {
			return
		}
	}
	readResult := make(chan error, 1)
	var writeMu sync.Mutex
	go func() {
		readResult <- s.readTerminalInput(ctx, connection, terminal, &writeMu)
	}()
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- s.writeTerminalOutput(ctx, connection, terminal, after, structured, &writeMu)
	}()
	pingResult := make(chan error, 1)
	go func() {
		pingResult <- keepTerminalAlive(ctx, connection)
	}()

	select {
	case err = <-readResult:
	case err = <-writeResult:
	case err = <-pingResult:
	case <-ctx.Done():
		err = ctx.Err()
	}
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) &&
		websocket.CloseStatus(err) == -1 {
		s.logger.Warn("terminal stream ended unexpectedly", "error", err, "terminal_id", terminal.ID)
	}
	status, reason := terminalStreamClose(err, terminal.Kind)
	_ = connection.Close(status, reason)
}

// keepTerminalAlive sends protocol-level pings often enough to keep idle
// terminal connections active through the public load balancer. Browsers
// answer WebSocket pings automatically while readTerminalInput continuously
// reads the corresponding pong control frames. Ping serializes with Write on
// the coder/websocket connection's internal writeFrameMu, so it is safe to call
// here without the writeMu that guards writeTerminalOutput.
func keepTerminalAlive(ctx context.Context, connection *websocket.Conn) error {
	ticker := time.NewTicker(terminalPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, terminalPingTimeout)
			err := connection.Ping(pingCtx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

func (s *Server) refreshTerminalInteraction(ctx context.Context, terminal domain.TerminalSession) {
	ticker := time.NewTicker(terminalInteractionRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.RefreshTerminalInteraction(ctx, terminal, terminalInteractionTTL); err != nil {
				if !errors.Is(err, context.Canceled) && s.logger != nil {
					s.logger.Debug("refresh terminal interaction lease", "error", err, "terminal_id", terminal.ID)
				}
				return
			}
		}
	}
}

func terminalStreamClose(err error, kind string) (websocket.StatusCode, string) {
	if errors.Is(err, errTerminalProcessUnavailable) {
		// An agent harness can be permanently absent from an image, so surface
		// that as a stable policy failure. A workspace shell open can instead
		// race a worker restart/resume and must remain reconnectable.
		if kind == "workspace" {
			return websocket.StatusTryAgainLater, "workspace terminal is restarting"
		}
		return websocket.StatusPolicyViolation, "terminal process unavailable"
	}
	if err != nil && !errors.Is(err, context.Canceled) &&
		websocket.CloseStatus(err) == -1 {
		return websocket.StatusInternalError, "terminal stream interrupted"
	}
	return websocket.StatusNormalClosure, "terminal closed"
}

func (s *Server) readTerminalInput(
	ctx context.Context,
	connection *websocket.Conn,
	terminal domain.TerminalSession,
	writeMu *sync.Mutex,
) error {
	operate := terminalScope(terminal.Scopes, "terminal:operate")
	for {
		_, data, err := connection.Read(ctx)
		if err != nil {
			return err
		}
		if !operate {
			return connection.Close(websocket.StatusPolicyViolation, "terminal is read-only")
		}
		if len(data) == 0 || len(data) > maxTerminalFrame {
			return connection.Close(websocket.StatusMessageTooBig, "terminal input is too large")
		}
		var message struct {
			Type    string `json:"type"`
			InputID string `json:"inputId,omitempty"`
			Data    string `json:"data,omitempty"`
			Columns uint16 `json:"columns,omitempty"`
			Rows    uint16 `json:"rows,omitempty"`
		}
		if json.Unmarshal(data, &message) == nil {
			if message.Type == "resize" {
				if message.Columns == 0 || message.Rows == 0 {
					return connection.Close(websocket.StatusPolicyViolation, "invalid terminal size")
				}
				if err := retryTerminalRequest(ctx, func() error {
					return s.store.QueueTerminalResize(
						ctx, terminal, message.Columns, message.Rows,
					)
				}); err != nil {
					return err
				}
				continue
			}
			if message.Type == "input" {
				data = []byte(message.Data)
			}
		}
		if len(data) == 0 {
			continue
		}
		// Same-replica fast path: when this control-plane task also holds the
		// worker's terminal stream, hand the keystroke to it in memory and skip
		// the durable queue's insert + NOTIFY + claim round trip (~15-20ms of
		// intra-region Postgres latency off the hot path). Falls back to the
		// durable path when the worker stream lives on another replica, is
		// absent, or its buffer is full — so delivery is never dropped silently.
		if s.terminalStreamEnabled && s.terminalStreams.pushInput(terminal.ID, data) {
			// Delivered in memory. The open terminal WebSocket already refreshes
			// the interaction lease on its own timer, so no durable row is
			// needed to keep the session from idle-pausing.
		} else if err := retryTerminalRequest(ctx, func() error {
			return s.store.QueueTerminalInput(ctx, terminal, message.InputID, data)
		}); err != nil {
			return err
		}
		if message.InputID != "" {
			writeMu.Lock()
			err := writeTerminalMessage(ctx, connection, terminalServerMessage{
				Type: "input_ack", InputID: message.InputID,
			})
			writeMu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

func retryTerminalRequest(ctx context.Context, operation func() error) error {
	deadline := time.NewTimer(terminalReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := operation()
		if err == nil {
			return nil
		}
		if !errors.Is(err, postgres.ErrWorkerUnavailable) &&
			!errors.Is(err, postgres.ErrConflict) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("terminal request queue did not become ready")
		case <-ticker.C:
		}
	}
}

func (s *Server) writeTerminalOutput(
	ctx context.Context,
	connection *websocket.Conn,
	terminal domain.TerminalSession,
	after int64,
	structured bool,
	writeMu *sync.Mutex,
) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	startupDeadline := time.NewTimer(terminalReadyTimeout)
	defer startupDeadline.Stop()
	// With the stream enabled, a Postgres NOTIFY wakes this loop the moment a
	// new output row commits; the ticker stays as the cross-replica and
	// missed-notification fallback.
	var wake chan struct{}
	if s.terminalStreamEnabled {
		var cancelWake func()
		wake, cancelWake = s.terminalStreams.subscribeOutput(terminal.ID)
		defer cancelWake()
	}
	replayComplete := false
	startingSent := false
	ready := false
	for {
		frames, state, err := s.store.ListTerminalOutput(ctx, terminal, after, 100)
		if err != nil {
			return err
		}
		if structured && !ready && (!startingSent || state == "open") {
			writeMu.Lock()
			messageType := "starting"
			if state == "open" {
				messageType = "ready"
				ready = true
				if s.logger != nil {
					s.logger.Info("browser terminal ready",
						"session_id", terminal.SessionID,
						"terminal_id", terminal.ID,
						"kind", terminal.Kind,
						"worker_epoch", terminal.WorkerEpoch,
					)
				}
			} else {
				startingSent = true
			}
			err := writeTerminalMessage(ctx, connection, terminalServerMessage{
				Type:     messageType,
				Sequence: after,
			})
			writeMu.Unlock()
			if err != nil {
				return err
			}
		}
		for _, frame := range frames {
			var err error
			writeMu.Lock()
			if structured {
				err = writeTerminalMessage(ctx, connection, terminalServerMessage{
					Type:     "output",
					Data:     base64.StdEncoding.EncodeToString(frame.Data),
					Sequence: frame.Sequence,
				})
			} else {
				// PTY reads are arbitrary byte chunks and can split a multi-byte
				// UTF-8 sequence. Legacy clients receive binary frames so partial
				// code points never make the WebSocket library reject the output.
				err = connection.Write(ctx, websocket.MessageBinary, frame.Data)
			}
			writeMu.Unlock()
			if err != nil {
				return err
			}
			after = frame.Sequence
		}
		if structured && ready && !replayComplete {
			writeMu.Lock()
			if err := writeTerminalMessage(ctx, connection, terminalServerMessage{
				Type: "replay_complete", Sequence: after,
			}); err != nil {
				writeMu.Unlock()
				return err
			}
			writeMu.Unlock()
			replayComplete = true
		}
		if state == "failed" {
			return errTerminalProcessUnavailable
		}
		if state == "closed" {
			return connection.Close(websocket.StatusNormalClosure, "terminal process exited")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-startupDeadline.C:
			if !ready {
				return errTerminalProcessUnavailable
			}
		case <-wake:
		case <-ticker.C:
		}
	}
}

type terminalServerMessage struct {
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	Message  string `json:"message,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
	InputID  string `json:"inputId,omitempty"`
}

func writeTerminalMessage(
	ctx context.Context,
	connection *websocket.Conn,
	message terminalServerMessage,
) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return connection.Write(ctx, websocket.MessageText, data)
}

func (s *Server) closeTerminal(r *http.Request, terminal domain.TerminalSession) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer cancel()
	if err := s.store.CloseTerminal(ctx, terminal); err != nil {
		s.logger.Debug("close terminal session", "error", err, "terminal_id", terminal.ID)
	}
}

func terminalScope(scopes []string, expected string) bool {
	for _, scope := range scopes {
		if scope == expected {
			return true
		}
	}
	return false
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
