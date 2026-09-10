package workertransport

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

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
	nextID int64
	broken bool
}

func (t *terminalStream) sendOutput(data []byte) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.broken {
		return false
	}
	t.nextID++
	frame, err := json.Marshal(worker.TerminalStreamFrame{
		Type: "output", Data: data, ID: t.nextID,
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
		permanent := s.readTerminalStream(ctx, conn, terminal)
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
		case "error":
			if frame.Code == "STALE_WORKER_TOKEN" || frame.Code == "TRANSPORT_EXPIRED" {
				return true
			}
			return false
		}
	}
}
