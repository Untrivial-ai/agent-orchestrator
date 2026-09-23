// client.go - loopback TCP client helpers that mirror pty-client.ts.
// Each function dials the host addr fresh (short-lived connection) and
// returns without maintaining state. Cross-platform: uses only stdlib net.
package conpty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

const (
	// ptyInputChunkRunes is the max runes per terminal-input frame.
	// Mirrors PTY_INPUT_CHUNK_CHARS in pty-client.ts.
	ptyInputChunkRunes = 512
	// ptyInputChunkDelay is the inter-chunk delay. Mirrors PTY_INPUT_CHUNK_DELAY_MS.
	ptyInputChunkDelay = 15 * time.Millisecond
	// ptyInputEnterDelay is the pause before sending Enter. Mirrors PTY_INPUT_ENTER_DELAY_MS.
	ptyInputEnterDelay = 300 * time.Millisecond

	dialTimeout      = 3 * time.Second
	getOutputTimeout = 3 * time.Second
	isAliveTimeout   = 2 * time.Second
)

// dialHost opens a TCP connection to addr with a deadline. Callers close it.
func dialHost(addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, timeout)
}

func dialHostContext(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	dialer := net.Dialer{Timeout: timeout}
	return dialer.DialContext(ctx, "tcp", addr)
}

// armClientDeadline bounds host I/O by both the client's transport timeout and
// its caller context. AfterFunc also interrupts an in-flight Read immediately
// when a context without an earlier deadline is cancelled.
func armClientDeadline(ctx context.Context, conn net.Conn, timeout time.Duration) func() {
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	return func() { _ = stop() }
}

// clientSendMessage chunks message by 512 runes and sends each as a
// MsgTerminalInput frame with 15ms gaps, then pauses 300ms and sends "\r".
// Mirrors ptyHostSendMessage from pty-client.ts.
func clientSendMessage(addr, message string) error {
	conn, err := dialHost(addr, dialTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	runes := []rune(message)
	for i := 0; i < len(runes); i += ptyInputChunkRunes {
		end := i + ptyInputChunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunk := string(runes[i:end])
		frame, err := EncodeMessage(MsgTerminalInput, []byte(chunk))
		if err != nil {
			return err
		}
		if _, err := conn.Write(frame); err != nil {
			return err
		}
		// Inter-chunk delay only between chunks, not after the last one.
		if end < len(runes) {
			time.Sleep(ptyInputChunkDelay)
		}
	}

	// Brief pause before Enter (matches TS: Enter sent as a separate frame).
	// Skipped for an empty message — an Enter-only nudge has no paste to let
	// settle, and the pause would only widen the guard-read→Enter window
	// (mirrors the tmux runtime's enterDelay contract).
	if len(runes) > 0 {
		time.Sleep(ptyInputEnterDelay)
	}
	frame, err := EncodeMessage(MsgTerminalInput, []byte("\r"))
	if err != nil {
		return err
	}
	_, err = conn.Write(frame)
	return err
}

func clientSendInput(addr, input string) error {
	conn, err := dialHost(addr, dialTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	frame, err := EncodeMessage(MsgTerminalInput, []byte(input))
	if err != nil {
		return err
	}
	_, err = conn.Write(frame)
	return err
}

// clientGetOutput sends MsgGetOutputReq and reads frames until MsgGetOutputRes.
// Returns "" on timeout or connection failure (no error), matching the TS.
// lines <= 0 is handled by the caller (runtime.go rejects it before calling).
func clientGetOutput(ctx context.Context, addr string, lines int) (string, error) {
	return clientReadOutput(ctx, addr, lines, MsgGetOutputReq, MsgGetOutputRes)
}

func clientGetStyledOutput(ctx context.Context, addr string, lines int) (string, error) {
	return clientReadOutput(ctx, addr, lines, MsgGetStyledOutputReq, MsgGetStyledOutputRes)
}

func clientReadOutput(ctx context.Context, addr string, lines int, requestType, responseType byte) (string, error) {
	conn, err := dialHostContext(ctx, addr, getOutputTimeout)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", nil // ponytail: connect failure -> "" like the TS
	}
	defer func() { _ = conn.Close() }()
	stopCancellation := armClientDeadline(ctx, conn, getOutputTimeout)
	defer stopCancellation()

	req, _ := json.Marshal(GetOutputReq{Lines: lines})
	reqFrame, _ := EncodeMessage(requestType, req) // req is small JSON, never overflows uint32
	if _, err := conn.Write(reqFrame); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", nil
	}

	resultC := make(chan string, 1)
	parser := NewMessageParser(func(msgType byte, payload []byte) {
		if msgType == responseType {
			select {
			case resultC <- string(payload):
			default:
			}
		}
	})

	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			parser.Feed(buf[:n])
		}
		select {
		case text := <-resultC:
			return text, nil
		default:
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			break
		}
	}
	// Drain the channel one last time after the read loop ends.
	select {
	case text := <-resultC:
		return text, nil
	default:
		return "", nil // timeout or EOF before response
	}
}

// clientIsAlive probes the host with MsgStatusReq and distinguishes three
// outcomes for the reaper (see IsAlive in runtime.go):
//
//   - alive==true,  transientErr==nil: a valid MsgStatusRes was received.
//   - alive==false, transientErr==nil: the host is DEFINITIVELY gone (the dial
//     was refused: nothing is listening on the loopback addr).
//   - alive==false, transientErr!=nil: a TRANSIENT probe failure (network
//     timeout, or any connected-then-failed I/O error). The reaper records this
//     as ProbeFailed and retries instead of reaping a possibly-live session.
//
// When unsure, we prefer transient (return the error) rather than reporting
// death. Mirrors ptyHostIsAlive from pty-client.ts on the alive path: host
// reachable == alive, regardless of the inner agent's alive field.
func clientIsAlive(addr string) (alive bool, transientErr error) {
	_, hostAlive, err := clientStatus(addr)
	return hostAlive, err
}

// clientStatus returns both pty-host reachability and the state of the child
// process managed by that host. A reachable host remains the runtime after its
// child exits.
func clientStatus(addr string) (status StatusPayload, hostAlive bool, transientErr error) {
	return clientStatusContext(context.Background(), addr)
}

func clientStatusContext(ctx context.Context, addr string) (status StatusPayload, hostAlive bool, transientErr error) {
	conn, err := dialHostContext(ctx, addr, isAliveTimeout)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return StatusPayload{}, false, ctxErr
		}
		// A dial timeout is transient (the loopback hiccupped). A refused
		// connection means nothing is listening -> definitively gone. Any
		// other dial failure is treated as transient ("when unsure, retry").
		if isTimeout(err) {
			return StatusPayload{}, false, err
		}
		if isConnRefused(err) {
			return StatusPayload{}, false, nil
		}
		return StatusPayload{}, false, err
	}
	defer func() { _ = conn.Close() }()
	stopCancellation := armClientDeadline(ctx, conn, isAliveTimeout)
	defer stopCancellation()

	statusReqFrame, _ := EncodeMessage(MsgStatusReq, nil) // nil payload, never overflows
	if _, err := conn.Write(statusReqFrame); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return StatusPayload{}, false, ctxErr
		}
		// We connected, then the write failed: connected-then-failed I/O is
		// transient (the host may still be up; the conn was disrupted).
		return StatusPayload{}, false, err
	}

	type statusResult struct {
		payload StatusPayload
		err     error
	}
	statusC := make(chan statusResult, 1)
	parser := NewMessageParser(func(msgType byte, payload []byte) {
		if msgType == MsgStatusRes {
			var sp StatusPayload
			decodeErr := json.Unmarshal(payload, &sp)
			select {
			case statusC <- statusResult{payload: sp, err: decodeErr}:
			default:
			}
		}
	})

	buf := make([]byte, 4096)
	var lastErr error
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			parser.Feed(buf[:n])
		}
		select {
		case result := <-statusC:
			if result.err != nil {
				return StatusPayload{}, false, result.err
			}
			return result.payload, true, nil
		default:
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return StatusPayload{}, false, ctxErr
			}
			lastErr = err
			break
		}
	}
	select {
	case result := <-statusC:
		if result.err != nil {
			return StatusPayload{}, false, result.err
		}
		return result.payload, true, nil
	default:
		// Connected but never got a STATUS_RES: read timeout or mid-read EOF.
		// lastErr is the error that broke the read loop (always non-nil here).
		return StatusPayload{}, false, lastErr
	}
}

// isTimeout reports whether err is a network timeout (dial timeout or
// read-deadline expiry). Cross-platform via the net.Error interface.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// isConnRefused reports whether err is a fast "connection refused" dial
// failure (nothing listening). errors.Is(ECONNREFUSED) covers Unix and modern
// Windows; the explicit WSAECONNREFUSED (10061) guards older Windows runtimes
// where the errno is not mapped to syscall.ECONNREFUSED.
func isConnRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	const wsaeconnrefused = syscall.Errno(10061)
	return errors.Is(err, wsaeconnrefused)
}

// clientKill sends MsgKillReq. Connection refused is idempotent success because
// the host is already absent; other transport failures are preserved so
// Destroy can combine them with its final PID evidence.
func clientKill(addr string) error {
	conn, err := dialHost(addr, isAliveTimeout)
	if err != nil {
		if isConnRefused(err) {
			return nil
		}
		return fmt.Errorf("dial pty-host for kill: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(isAliveTimeout))
	killFrame, _ := EncodeMessage(MsgKillReq, nil) // nil payload, never overflows
	if _, err := conn.Write(killFrame); err != nil {
		return fmt.Errorf("write pty-host kill request: %w", err)
	}
	return nil
}
