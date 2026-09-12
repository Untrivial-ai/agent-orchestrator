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
)

// terminalStreams tracks, for this control-plane replica only, which
// terminals have a live worker stream (input push targets) and which client
// writers want output wakes. Cross-replica coordination rides Postgres
// NOTIFY: rows are the source of truth, notifications are accelerants.
type terminalStreams struct {
	mu       sync.Mutex
	workers  map[string]*workerTerminalStream
	watchers map[string]map[chan struct{}]struct{}
}

type workerTerminalStream struct {
	claims worker.Claims
	send   chan []byte
	done   chan struct{}
}

func newTerminalStreams() *terminalStreams {
	return &terminalStreams{
		workers:  make(map[string]*workerTerminalStream),
		watchers: make(map[string]map[chan struct{}]struct{}),
	}
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
// holds per open terminal: output frames come up and are persisted (rows
// stay authoritative for replay) before waking client writers; input rows
// are pushed down as they are queued.
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
		if json.Unmarshal(message, &frame) != nil || frame.Type != "output" ||
			len(frame.Data) == 0 || len(frame.Data) > maxTerminalFrame {
			_ = connection.Close(websocket.StatusPolicyViolation, "invalid stream frame")
			return
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
