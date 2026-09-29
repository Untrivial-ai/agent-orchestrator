package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

const (
	// terminalInputPushLease bounds how long a pushed keystroke's queue row
	// stays claimed before the worker's own poll may retry it.
	terminalInputPushLease = 10 * time.Second
	// terminalStreamSendBuffer bounds input frames queued toward one worker.
	// Overflow falls back to the durable queue via the worker's poll.
	terminalStreamSendBuffer = 64
	// terminalRelayOutputBuffer limits data held only for a currently attached
	// browser. Durable replay remains the recovery path after a disconnect.
	terminalRelayOutputBuffer = 128
	// terminalRelayAuditBuffer bounds background output mirroring. Reaching it
	// makes the worker stream apply backpressure rather than silently losing
	// replay/audit data.
	terminalRelayAuditBuffer = 256
)

// terminalStreams tracks which terminals have a live worker stream (input
// push targets) and which client writers want output wakes. PostgreSQL rows
// remain the durable source of truth; notifications accelerate recovery.
type terminalStreams struct {
	mu                  sync.Mutex
	workers             map[string]*workerTerminalStream
	watchers            map[string]map[chan struct{}]struct{}
	clients             map[string]map[chan terminalRelayOutput]struct{}
	notificationClients map[string]map[chan terminalRelayNotification]struct{}
}

type workerTerminalStream struct {
	claims worker.Claims
	send   chan []byte
	done   chan struct{}
}

// terminalRelayOutput is the live branch of the terminal data plane. Sequence
// is assigned by the worker and is also used by the durable replay log.
type terminalRelayOutput struct {
	sequence int64
	data     []byte
}

// terminalRelayNotification is deliberately separate from terminal output:
// unlike PTY bytes it has no durable terminal cursor and must never disturb
// replay ordering. The notification inbox is its durable recovery path.
type terminalRelayNotification struct {
	eventID    string
	typeName   string
	occurredAt time.Time
	payload    json.RawMessage
}

func newTerminalStreams() *terminalStreams {
	return &terminalStreams{
		workers:             make(map[string]*workerTerminalStream),
		watchers:            make(map[string]map[chan struct{}]struct{}),
		clients:             make(map[string]map[chan terminalRelayOutput]struct{}),
		notificationClients: make(map[string]map[chan terminalRelayNotification]struct{}),
	}
}

func (t *terminalStreams) subscribeRelayNotifications(terminalID string) (chan terminalRelayNotification, func()) {
	notifications := make(chan terminalRelayNotification, terminalRelayOutputBuffer)
	t.mu.Lock()
	set := t.notificationClients[terminalID]
	if set == nil {
		set = make(map[chan terminalRelayNotification]struct{})
		t.notificationClients[terminalID] = set
	}
	set[notifications] = struct{}{}
	t.mu.Unlock()
	return notifications, func() {
		t.mu.Lock()
		if set := t.notificationClients[terminalID]; set != nil {
			delete(set, notifications)
			if len(set) == 0 {
				delete(t.notificationClients, terminalID)
			}
		}
		t.mu.Unlock()
	}
}

func (t *terminalStreams) relayNotification(terminalID string, notification terminalRelayNotification) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	dropped := 0
	for client := range t.notificationClients[terminalID] {
		frame := notification
		frame.payload = append(json.RawMessage(nil), notification.payload...)
		select {
		case client <- frame:
		default:
			dropped++
		}
	}
	return dropped
}

// subscribeRelayOutput attaches a browser to the in-process relay. Its
// buffered channel means a slow browser never blocks the worker's PTY read;
// overflow is handled by the normal durable replay path after reconnect.
func (t *terminalStreams) subscribeRelayOutput(terminalID string) (chan terminalRelayOutput, func()) {
	output := make(chan terminalRelayOutput, terminalRelayOutputBuffer)
	t.mu.Lock()
	set := t.clients[terminalID]
	if set == nil {
		set = make(map[chan terminalRelayOutput]struct{})
		t.clients[terminalID] = set
	}
	set[output] = struct{}{}
	t.mu.Unlock()
	return output, func() {
		t.mu.Lock()
		if set := t.clients[terminalID]; set != nil {
			delete(set, output)
			if len(set) == 0 {
				delete(t.clients, terminalID)
			}
		}
		t.mu.Unlock()
	}
}

// relayOutput fans a worker frame straight to local browser sockets. It does
// not persist or inspect terminal bytes; the caller mirrors the exact frame to
// Postgres independently. It returns the count of saturated client buffers so
// callers can make overload visible without logging terminal content.
func (t *terminalStreams) relayOutput(terminalID string, output terminalRelayOutput) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	dropped := 0
	for client := range t.clients[terminalID] {
		frame := terminalRelayOutput{sequence: output.sequence, data: append([]byte(nil), output.data...)}
		select {
		case client <- frame:
		default:
			dropped++
		}
	}
	return dropped
}

// registerWorker installs stream as the terminal's push target, replacing
// (and closing) any previous stream — a worker redial supersedes its old
// socket. The returned func deregisters, but only if stream still owns the
// slot.
func (t *terminalStreams) registerWorker(
	terminalID string,
	stream *workerTerminalStream,
) func() {
	t.mu.Lock()
	if previous := t.workers[terminalID]; previous != nil {
		close(previous.done)
	}
	t.workers[terminalID] = stream
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		if t.workers[terminalID] == stream {
			delete(t.workers, terminalID)
		}
		t.mu.Unlock()
	}
}

func (t *terminalStreams) lookupWorker(terminalID string) *workerTerminalStream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.workers[terminalID]
}

// pushInput delivers a keystroke straight into the worker's stream when this
// replica owns it, bypassing the durable queue entirely. It returns true only
// when the frame was accepted in-memory; a missing stream or a full send
// buffer returns false so the caller falls back to the durable queue path.
// Losing an in-flight keystroke if the worker dies before the PTY write is the
// SSH contract this fast path adopts — identical to the notify-driven push.
func (t *terminalStreams) pushInput(terminalID string, data []byte) bool {
	t.mu.Lock()
	stream := t.workers[terminalID]
	t.mu.Unlock()
	if stream == nil {
		return false
	}
	frame := append([]byte(nil), data...)
	select {
	case stream.send <- frame:
		return true
	case <-stream.done:
		return false
	default:
		// Buffer full: fall back to the durable queue rather than block the
		// client's read loop.
		return false
	}
}

// subscribeOutput returns a channel that receives (at least) one signal per
// output notification for the terminal, and a cancel func.
func (t *terminalStreams) subscribeOutput(terminalID string) (chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	t.mu.Lock()
	set := t.watchers[terminalID]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		t.watchers[terminalID] = set
	}
	set[wake] = struct{}{}
	t.mu.Unlock()
	return wake, func() {
		t.mu.Lock()
		if set := t.watchers[terminalID]; set != nil {
			delete(set, wake)
			if len(set) == 0 {
				delete(t.watchers, terminalID)
			}
		}
		t.mu.Unlock()
	}
}

func (t *terminalStreams) notifyOutput(terminalID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for wake := range t.watchers[terminalID] {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// HandleTerminalOutputNotify is the Postgres NOTIFY callback for
// ao_terminal_output; the payload is the terminal id.
func (s *Server) HandleTerminalOutputNotify(terminalID string) {
	if s.terminalStreams == nil {
		return
	}
	s.terminalStreams.notifyOutput(terminalID)
}

// HandleTerminalInputNotify is the Postgres NOTIFY callback for
// ao_terminal_input; the payload is the terminal id. If this replica holds
// the terminal's worker stream, pending input rows are claimed and pushed.
func (s *Server) HandleTerminalInputNotify(terminalID string) {
	if s.terminalStreams == nil {
		return
	}
	stream := s.terminalStreams.lookupWorker(terminalID)
	if stream == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.pushPendingTerminalInput(ctx, stream, terminalID)
}

// pushPendingTerminalInput drains claimable terminal.input rows into the
// worker stream. Rows stay leased until completed, so the worker's transport
// poll can never double-deliver; a push marks the row complete — losing an
// in-flight keystroke if the worker dies before writing the PTY is the SSH
// contract this path deliberately adopts.
func (s *Server) pushPendingTerminalInput(
	ctx context.Context,
	stream *workerTerminalStream,
	terminalID string,
) {
	claims := stream.claims
	for {
		request, found, err := s.store.ClaimTerminalInput(
			ctx, claims.OrgID, claims.SessionID, claims.WorkerID,
			claims.Epoch, terminalID, terminalInputPushLease,
		)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				s.logger.Warn("claim terminal input for push",
					"error", err, "terminal_id", terminalID)
			}
			return
		}
		if !found {
			return
		}
		var payload struct {
			Data []byte `json:"data"`
		}
		if err := json.Unmarshal(request.Payload, &payload); err != nil || len(payload.Data) == 0 {
			_ = s.store.FailWorkerRequest(
				ctx, claims.OrgID, claims.SessionID, claims.WorkerID,
				request.ID, claims.Epoch, request.Attempt,
				"validation_error", "terminal input payload is invalid",
			)
			continue
		}
		select {
		case stream.send <- payload.Data:
		case <-stream.done:
			// Lease expiry hands the row back to the worker's poll.
			return
		case <-ctx.Done():
			return
		}
		if err := s.store.CompleteWorkerRequest(
			ctx, claims.OrgID, claims.SessionID, claims.WorkerID,
			request.ID, claims.Epoch, request.Attempt, nil,
		); err != nil {
			s.logger.Warn("complete pushed terminal input",
				"error", err, "terminal_id", terminalID)
		}
	}
}

// workerTerminalStream is the persistent duplex terminal socket a worker
// holds per open terminal. In relay mode, output frames go immediately to a
// local browser and then through an ordered background durable mirror. The
// original persist-before-wake behavior remains available when relay mode is
// disabled.
func (s *Server) workerTerminalStream(w http.ResponseWriter, r *http.Request) {
	if !s.terminalStreamEnabled {
		writeError(w, r, http.StatusNotFound, "not_found", "The terminal stream is not enabled.")
		return
	}
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:transport") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:transport scope is required.")
		return
	}
	terminalID := chi.URLParam(r, "terminalId")
	if requireUUID(terminalID, "terminalId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "terminalId must be a UUID.")
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	connection.SetReadLimit(maxTerminalFrame * 2)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stream := &workerTerminalStream{
		claims: claims,
		send:   make(chan []byte, terminalStreamSendBuffer),
		done:   make(chan struct{}),
	}
	deregister := s.terminalStreams.registerWorker(terminalID, stream)
	defer deregister()

	var writeMu sync.Mutex
	writeFrame := func(frame worker.TerminalStreamFrame) error {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return connection.Write(ctx, websocket.MessageText, encoded)
	}

	// One mirror loop preserves frame order for this terminal. The relay sends
	// the same worker-assigned sequence to the browser first; the database uses
	// that sequence when it records replay state.
	type auditFrame struct {
		frame worker.TerminalStreamFrame
	}
	audit := make(chan auditFrame, terminalRelayAuditBuffer)
	if s.terminalRelayEnabled {
		go func() {
			for queued := range audit {
				sequence, persistErr := s.store.AppendTerminalOutputAt(
					ctx, claims.OrgID, claims.SessionID, claims.WorkerID,
					terminalID, claims.Epoch, queued.frame.ID, queued.frame.Data,
				)
				if persistErr != nil {
					if ctx.Err() == nil {
						s.logger.Error("terminal relay durable mirror failed",
							"error", persistErr, "terminal_id", terminalID,
							"sequence", queued.frame.ID)
					}
					cancel()
					return
				}
				if err := writeFrame(worker.TerminalStreamFrame{
					Type: "ack", ID: queued.frame.ID, Sequence: sequence,
				}); err != nil {
					cancel()
					return
				}
				if s.logger != nil {
					s.logger.Debug("terminal relay output mirrored",
						"terminal_id", terminalID, "sequence", sequence,
						"bytes", len(queued.frame.Data))
				}
			}
		}()
		defer close(audit)
	}

	// Push pending input queued before the stream connected, then keep
	// draining pushes queued by input notifications.
	go s.pushPendingTerminalInput(ctx, stream, terminalID)
	go func() {
		for {
			select {
			case data := <-stream.send:
				if err := writeFrame(worker.TerminalStreamFrame{
					Type: "input", Data: data,
				}); err != nil {
					cancel()
					return
				}
			case <-stream.done:
				cancel()
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, message, err := connection.Read(ctx)
		if err != nil {
			_ = connection.Close(websocket.StatusNormalClosure, "stream closed")
			return
		}
		var frame worker.TerminalStreamFrame
		if json.Unmarshal(message, &frame) != nil {
			_ = connection.Close(websocket.StatusPolicyViolation, "invalid stream frame")
			return
		}
		if frame.Type == "notification" {
			accepted, err := s.acceptWorkerNotification(ctx, claims, worker.NotificationEventRequest{
				EventID: frame.EventID, Type: frame.EventType, OccurredAt: frame.OccurredAt, Payload: frame.Payload,
			})
			if err != nil {
				_ = writeFrame(worker.TerminalStreamFrame{Type: "error", Code: notificationStreamErrorCode(err)})
				continue
			}
			if s.terminalStreams != nil {
				s.terminalStreams.relayNotification(terminalID, terminalRelayNotification{
					eventID: frame.EventID, typeName: frame.EventType, occurredAt: frame.OccurredAt, payload: frame.Payload,
				})
			}
			if err := writeFrame(worker.TerminalStreamFrame{Type: "notification_ack", EventID: frame.EventID, Duplicate: accepted.Duplicate}); err != nil {
				return
			}
			continue
		}
		if frame.Type != "output" || len(frame.Data) == 0 || len(frame.Data) > maxTerminalFrame {
			_ = connection.Close(websocket.StatusPolicyViolation, "invalid stream frame")
			return
		}
		if s.terminalRelayEnabled && s.terminalStreams != nil && frame.ID > 0 {
			dropped := s.terminalStreams.relayOutput(terminalID, terminalRelayOutput{
				sequence: frame.ID, data: frame.Data,
			})
			if s.logger != nil {
				s.logger.Debug("terminal relay output forwarded",
					"terminal_id", terminalID, "sequence", frame.ID,
					"bytes", len(frame.Data), "saturated_clients", dropped)
			}
			select {
			case audit <- auditFrame{frame: frame}:
			case <-ctx.Done():
				return
			}
			continue
		}
		sequence, err := s.store.AppendTerminalOutput(
			ctx, claims.OrgID, claims.SessionID, claims.WorkerID,
			terminalID, claims.Epoch, frame.Data,
		)
		if err != nil {
			code := "TRANSPORT_FAILED"
			if errors.Is(err, postgres.ErrStaleWorker) {
				code = "STALE_WORKER_TOKEN"
			} else if errors.Is(err, postgres.ErrTransportExpired) {
				code = "TRANSPORT_EXPIRED"
			}
			_ = writeFrame(worker.TerminalStreamFrame{Type: "error", Code: code})
			_ = connection.Close(websocket.StatusPolicyViolation, code)
			return
		}
		if err := writeFrame(worker.TerminalStreamFrame{
			Type: "ack", ID: frame.ID, Sequence: sequence,
		}); err != nil {
			return
		}
	}
}
