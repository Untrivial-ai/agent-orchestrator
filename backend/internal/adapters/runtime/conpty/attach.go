// attach.go - conpty Attach: a loopback Stream over the B3 pty-host. Unlike
// tmux, conpty does not spawn an attach CLI; it dials the session's
// loopback host and speaks the B1 framing protocol directly. The host replays
// the scrollback Snapshot as the first MsgTerminalData on connect, so a fresh
// Read naturally yields the repaint first.
package conpty

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.Attacher = (*Runtime)(nil)

// Attach opens a fresh attach Stream for the session by dialing its loopback
// pty-host. rows/cols size the host's PTY from birth when known (a MsgResize is
// sent right after connect). ctx cancellation closes the Stream.
func (r *Runtime) Attach(ctx context.Context, handle ports.RuntimeHandle, rows, cols uint16) (ports.Stream, error) {
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return nil, fmt.Errorf("conpty: resolve session %q for attach: %w", handle.ID, err)
	}
	if sess == nil {
		return nil, fmt.Errorf("conpty: session %q not found", handle.ID)
	}
	conn, err := dialHost(sess.addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("conpty: dial host for %q: %w", handle.ID, err)
	}

	pr, pw := io.Pipe()
	s := &loopbackStream{conn: conn, pr: pr, pw: pw}

	// Pump host frames: MsgTerminalData payloads go into the pipe that Read
	// drains. The first such frame is the scrollback snapshot, so the replay
	// arrives before any live output.
	go s.pump()

	// ctx cancellation must terminate the stream (mirrors the unix/windows
	// spawn paths closing the PTY on ctx.Done).
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	if rows > 0 && cols > 0 {
		if err := s.Resize(rows, cols); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	// A detached host deliberately survives its child for scrollback. Ask for
	// the child status on every attach so a pane opened after process exit gets
	// the same definitive exit signal as a pane that observed the live event.
	statusFrame, _ := EncodeMessage(MsgStatusReq, nil)
	if _, err := conn.Write(statusFrame); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// loopbackStream is a ports.Stream backed by a single loopback connection to the
// pty-host. The pump goroutine reframes host output into an io.Pipe so Read
// presents a plain byte stream; Write/Resize encode client frames onto the conn.
type loopbackStream struct {
	conn io.ReadWriteCloser
	pr   *io.PipeReader
	pw   *io.PipeWriter

	closeOnce sync.Once
}

// pump reads framed host messages and writes MsgTerminalData payloads into the
// pipe. It closes the pipe when the connection ends so Read returns EOF.
func (s *loopbackStream) pump() {
	processExited := false
	parser := NewMessageParser(func(msgType byte, payload []byte) {
		switch msgType {
		case MsgTerminalData:
			// Write blocks until Read drains, preserving back-pressure and order.
			_, _ = s.pw.Write(payload)
		case MsgStatusRes:
			var status StatusPayload
			if json.Unmarshal(payload, &status) == nil && !status.Alive {
				processExited = true
			}
		}
	})
	buf := make([]byte, 4096)
	for {
		n, err := s.conn.Read(buf)
		if n > 0 {
			parser.Feed(buf[:n])
			if processExited {
				_ = s.conn.Close()
				_ = s.pw.CloseWithError(ports.ErrRuntimeProcessExited)
				return
			}
		}
		if err != nil {
			_ = s.pw.CloseWithError(err)
			return
		}
	}
}

func (s *loopbackStream) Read(p []byte) (int, error) { return s.pr.Read(p) }

func (s *loopbackStream) Write(p []byte) (int, error) {
	frame, err := EncodeMessage(MsgTerminalInput, p)
	if err != nil {
		return 0, err
	}
	if _, err := s.conn.Write(frame); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *loopbackStream) Resize(rows, cols uint16) error {
	payload, _ := json.Marshal(ResizePayload{Cols: int(cols), Rows: int(rows)})
	frame, err := EncodeMessage(MsgResize, payload) // small JSON payload, never overflows uint32
	if err != nil {
		return err
	}
	_, err = s.conn.Write(frame)
	return err
}

// Close closes the conn and the pipe. Idempotent. Closing the conn unblocks
// pump's Read, which then closes the pipe-writer too; closing both here makes
// Close safe to call directly (e.g. on ctx cancel) without waiting for pump.
func (s *loopbackStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.conn.Close()
		_ = s.pw.Close()
		_ = s.pr.Close()
	})
	return err
}
