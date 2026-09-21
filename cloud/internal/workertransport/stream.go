package workertransport

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/cloud/internal/notificationoutbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

var ErrNoNotificationStream = errors.New("no live terminal stream for notification")

// StreamDialer opens the persistent duplex terminal stream to the control
// plane. Nil disables streaming and keeps the polled transport untouched.
type StreamDialer interface {
	DialTerminalStream(ctx context.Context, terminalID string) (*websocket.Conn, error)
}

const (
	streamRedialFloor   = 500 * time.Millisecond
	streamRedialCeiling = 5 * time.Second
	maxStreamInputBytes = 16 << 10
)

// terminalStream is one live socket. Output writes are serialized; a failed
// write retires the stream so the copy loop falls back to the HTTP publish
// path (the same at-most-once contract that path already has).
type terminalStream struct {
	conn   *websocket.Conn
	ctx    context.Context
	mu     sync.Mutex
	broken bool
	acksMu sync.Mutex
	acks   map[string]chan bool
}

func (t *terminalStream) sendOutput(id int64, data []byte) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.broken {
		return false
	}
	frame, err := json.Marshal(worker.TerminalStreamFrame{
		Type: "output", Data: data, ID: id,
	})
	if err != nil {
		return false
	}
	writeCtx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
	defer cancel()
	if err := t.conn.Write(writeCtx, websocket.MessageText, frame); err != nil {
		t.broken = true
		return false
	}
	return true
}

func (t *terminalStream) sendNotification(ctx context.Context, event notificationoutbox.Event) error {
	ack := make(chan bool, 1)
	t.acksMu.Lock()
	if t.acks == nil {
		t.acks = make(map[string]chan bool)
	}
	t.acks[event.EventID] = ack
	t.acksMu.Unlock()
	defer func() {
		t.acksMu.Lock()
		delete(t.acks, event.EventID)
		t.acksMu.Unlock()
	}()
	t.mu.Lock()
	if t.broken {
		t.mu.Unlock()
		return ErrNoNotificationStream
	}
	frame, err := json.Marshal(worker.TerminalStreamFrame{Type: "notification", EventID: event.EventID, EventType: event.EventType, OccurredAt: event.OccurredAt, Payload: event.Payload})
	if err == nil {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = t.conn.Write(writeCtx, websocket.MessageText, frame)
		cancel()
	}
	if err != nil {
		t.broken = true
	}
	t.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case accepted := <-ack:
		if !accepted {
			return errors.New("control plane rejected notification")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("notification acknowledgement timed out")
	}
}

func (t *terminalStream) acknowledgeNotification(eventID string, accepted bool) {
	t.acksMu.Lock()
	ack := t.acks[eventID]
	t.acksMu.Unlock()
	if ack != nil {
		select {
		case ack <- accepted:
		default:
		}
	}
}

func (t *terminalStream) rejectPendingNotifications() {
	t.acksMu.Lock()
	defer t.acksMu.Unlock()
	for _, ack := range t.acks {
		select {
		case ack <- false:
		default:
		}
	}
}

// DeliverNotification sends through any live terminal stream. A failed or
// absent stream deliberately returns an error: the caller retains its outbox
// row and can use the authenticated HTTP intake as the durable fallback.
func (s *Supervisor) DeliverNotification(ctx context.Context, event notificationoutbox.Event) error {
	s.mu.Lock()
	var stream *terminalStream
	for candidate := range s.notificationStreams {
		stream = candidate
		break
	}
	s.mu.Unlock()
	if stream == nil {
		return ErrNoNotificationStream
	}
	return stream.sendNotification(ctx, event)
}

// runTerminalStream keeps one stream alive for a terminal's lifetime,
// writing pushed input straight to the PTY and letting the output copy loop
// prefer the socket. A control-plane rejection that can never heal (stale
// epoch, expired terminal) stops redialing for good; everything else backs
// off and redials.
func (s *Supervisor) runTerminalStream(
	ctx context.Context,
	terminalID string,
	terminal *terminalProcess,
) {
	backoff := streamRedialFloor
	for ctx.Err() == nil {
		conn, err := s.Streams.DialTerminalStream(ctx, terminalID)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < streamRedialCeiling {
				backoff *= 2
			}
			continue
		}
		backoff = streamRedialFloor
		stream := &terminalStream{conn: conn, ctx: ctx}
		terminal.stream.Store(stream)
		s.mu.Lock()
		if s.notificationStreams == nil {
			s.notificationStreams = make(map[*terminalStream]struct{})
		}
		s.notificationStreams[stream] = struct{}{}
		s.mu.Unlock()
		permanent := s.readTerminalStream(ctx, conn, terminal)
		s.mu.Lock()
		delete(s.notificationStreams, stream)
		s.mu.Unlock()
		terminal.stream.CompareAndSwap(stream, nil)
		_ = conn.CloseNow()
		if permanent {
			return
		}
	}
}

// readTerminalStream consumes frames until the socket dies. It reports true
// when the control plane rejected the stream permanently.
func (s *Supervisor) readTerminalStream(
	ctx context.Context,
	conn *websocket.Conn,
	terminal *terminalProcess,
) bool {
	for {
		_, message, err := conn.Read(ctx)
		if err != nil {
			return false
		}
		var frame worker.TerminalStreamFrame
		if json.Unmarshal(message, &frame) != nil {
			return false
		}
		switch frame.Type {
		case "input":
			if len(frame.Data) == 0 || len(frame.Data) > maxStreamInputBytes {
				continue
			}
			if _, err := terminal.pty.Write(frame.Data); err != nil {
				return false
			}
		case "ack":
			// Output rows are persisted before the ack; nothing to do.
		case "notification_ack":
			stream := terminal.stream.Load()
			if stream != nil {
				stream.acknowledgeNotification(frame.EventID, true)
			}
		case "error":
			if stream := terminal.stream.Load(); stream != nil {
				stream.rejectPendingNotifications()
			}
			if frame.Code == "STALE_WORKER_TOKEN" || frame.Code == "TRANSPORT_EXPIRED" {
				return true
			}
			return false
		}
	}
}
