// Package conpty - host.go implements the serve engine for the pty-host
// detached process. It owns the agent's PTY (via the ptyConn seam), exposes
// it over a loopback TCP socket using the B1 binary protocol, replays
// scrollback to new clients, fans output to all connected clients, and shuts
// down gracefully (ConPTY dispose first, then clients, then listener).
//
// This file is cross-platform; only the real conptyConn impl is Windows-tagged.
package conpty

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"
)

const (
	initialConPTYColumns = 220
	initialConPTYRows    = 50
)

// ptyConn is the host's handle to the running agent's pseudo-terminal.
// The real impl (conptyConn) lives in host_conpty_windows.go; tests use a fake.
type ptyConn interface {
	io.Reader // PTY output (raw bytes from the terminal)
	io.Writer // PTY input (keystrokes to the terminal)
	Resize(cols, rows int) error
	Close() error          // dispose the ConPTY
	Done() <-chan struct{} // closed when the child process exits
	ExitCode() (int, bool) // (code, true) once exited; (0, false) while running
	PID() int
}

// ServeConfig carries everything the host needs.
type ServeConfig struct {
	SessionID string
	Listener  net.Listener // caller provides (loopback); engine owns Accept loop
	PTY       ptyConn
	Ring      *Ring
}

// Serve runs the host event loop until the listener closes or Shutdown is
// invoked via the returned ShutdownFunc. It pumps PTY output into the ring
// and broadcasts to all clients, accepts new clients (replaying ring snapshot),
// and dispatches client messages. On PTY exit it broadcasts a status update
// but stays alive (keep-alive, mirroring tmux behavior). Returns when shut down.
func Serve(ctx context.Context, cfg ServeConfig) error {
	h := &host{
		cfg:       cfg,
		clients:   make(map[net.Conn]*clientState),
		surface:   newRenderedSurface(initialConPTYColumns, initialConPTYRows),
		shutdownC: make(chan struct{}),
	}
	return h.run(ctx)
}

// clientState is the host's per-connection bookkeeping. cols/rows record the
// grid this client last asked for (sized reports whether it ever asked), so the
// host can size the shared PTY to the largest attached client (see
// applyLargestLocked). A connection that never sends a resize stays sized=false
// and never influences the shared grid.
type clientState struct {
	cols, rows int
	sized      bool
}

// host holds the mutable state for a single pty-host session.
type host struct {
	cfg     ServeConfig
	mu      sync.Mutex
	clients map[net.Conn]*clientState
	surface *renderedSurface

	// curCols/curRows are the grid the host last applied to the shared PTY (0,0
	// = none applied yet). Guarded by mu; used to skip redundant resizes.
	curCols, curRows int

	shutdownOnce sync.Once
	shutdownC    chan struct{} // closed when Shutdown is called
}

// applyLargestLocked sizes the shared PTY to a SINGLE client's grid — the
// largest by area — and resizes only when that choice changes. There is one PTY
// with one grid, so when several clients view it at once (e.g. the desktop app
// and the phone) the largest wins: a small viewer can never shrink the grid a
// larger one needs, which is what produced the "stripped-down" desktop view when
// a phone attached.
//
// Crucially this matches ONE client's cols AND rows as a pair, rather than taking
// an independent max of each axis. A per-axis max would synthesize a grid no
// client actually has — a wide-but-short desktop (120x30) plus a narrow-but-tall
// phone (55x48) would yield 120x48 — and that phantom grid mis-renders for every
// client (the desktop draws its footer at a row it can't show; the phone gets
// columns it can't fit). Matching one client exactly keeps that client (the
// largest — normally the desktop) pixel-correct; only smaller clients scale.
//
// Called on every client resize and on every disconnect, so the grid follows a
// newly-attached larger client and falls back to the remaining largest one when
// it leaves. Callers must hold h.mu.
func (h *host) applyLargestLocked() {
	bestCols, bestRows, bestArea := 0, 0, 0
	for _, cs := range h.clients {
		if !cs.sized {
			continue
		}
		if area := cs.cols * cs.rows; area > bestArea {
			bestArea, bestCols, bestRows = area, cs.cols, cs.rows
		}
	}
	// No client has reported a size yet: leave the PTY at its current grid (the
	// initial size set when the ConPTY was created).
	if bestCols == 0 || bestRows == 0 {
		return
	}
	if bestCols == h.curCols && bestRows == h.curRows {
		return
	}
	h.curCols, h.curRows = bestCols, bestRows
	_ = h.cfg.PTY.Resize(bestCols, bestRows)
	h.surface.Resize(bestCols, bestRows)
}

// run is the main event loop.
func (h *host) run(ctx context.Context) error {
	// Pump PTY output to ring + broadcast.
	go h.pumpPTY()

	// Watch for ctx cancellation and trigger shutdown.
	go func() {
		select {
		case <-ctx.Done():
			h.shutdown()
		case <-h.shutdownC:
		}
	}()

	// runAcceptLoop accepts connections until the listener closes. A listener
	// close is normal (shutdown or external) and is treated as success.
	h.runAcceptLoop()
	return nil
}

// runAcceptLoop runs the Accept loop until the listener closes or returns an
// error. Listener-close errors are swallowed; they signal normal shutdown.
func (h *host) runAcceptLoop() {
	for {
		conn, err := h.cfg.Listener.Accept()
		if err != nil {
			return
		}
		go h.handleConn(conn)
	}
}

// shutdown is idempotent: disposes the ConPTY, closes clients, closes the
// listener. Mirrors the pty-host.ts shutdown() function.
// ponytail: 50ms sleep after pty.Close() gives the OS ConPTY helper
// (conpty_console_list_agent.exe) time to release cleanly; avoids the
// 0x800700e8 error dialog on Windows.
func (h *host) shutdown() {
	h.shutdownOnce.Do(func() {
		close(h.shutdownC)

		// 1. Dispose the ConPTY first (critical ordering).
		_ = h.cfg.PTY.Close()

		// 2. Brief grace so the OS ConPTY helper can clean up.
		time.Sleep(50 * time.Millisecond)

		// 3. Close all client connections.
		h.mu.Lock()
		for c := range h.clients {
			_ = c.Close()
		}
		h.clients = make(map[net.Conn]*clientState)
		h.mu.Unlock()

		// 4. Close the listener to unblock Accept.
		_ = h.cfg.Listener.Close()
	})
}

// pumpPTY reads PTY output continuously, appends to the ring, and broadcasts
// to clients. On PTY exit it flushes the partial line and sends a status
// update but does NOT close the listener (keep-alive).
func (h *host) pumpPTY() {
	buf := make([]byte, 32*1024)
	for {
		n, err := h.cfg.PTY.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			h.cfg.Ring.Append(chunk)
			h.surface.Write(chunk)
			if frame, err := EncodeMessage(MsgTerminalData, chunk); err == nil {
				h.broadcast(frame)
			}
		}
		if err != nil {
			break
		}
	}

	// PTY reader is done (process exited or PTY closed). Wait for the Done
	// signal so ExitCode is populated before we send the status broadcast.
	<-h.cfg.PTY.Done()

	h.cfg.Ring.FlushPartial()

	code, _ := h.cfg.PTY.ExitCode()
	pid := h.cfg.PTY.PID()
	h.broadcast(statusFrame(false, pid, &code))
	// Keep-alive: do NOT shutdown here. The host stays up so clients can
	// still connect and read scrollback.
}

// broadcast sends msg to all connected clients, removing any that error.
func (h *host) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	removed := false
	for c := range h.clients {
		if _, err := c.Write(msg); err != nil {
			_ = c.Close()
			delete(h.clients, c)
			removed = true
		}
	}
	// A dropped client may have been the largest viewer; recompute the shared
	// grid so it follows the remaining clients.
	if removed {
		h.applyLargestLocked()
	}
}

// sendTo sends msg to a single conn (best-effort; removes on error).
func (h *host) sendTo(conn net.Conn, msg []byte) {
	if _, err := conn.Write(msg); err != nil {
		h.mu.Lock()
		_ = conn.Close()
		delete(h.clients, conn)
		h.applyLargestLocked()
		h.mu.Unlock()
	}
}

// handleConn manages the lifecycle of a single client connection.
func (h *host) handleConn(conn net.Conn) {
	// Scrollback replay: take the ring snapshot, write it to the conn, and add
	// the conn to the broadcast set all under a SINGLE h.mu hold. broadcast()
	// also takes h.mu, so it cannot interleave: any PTY chunk that arrives is
	// either already in this snapshot, or is broadcast strictly after the conn
	// joins the set. Doing this in two separate locks would let a chunk slip
	// into the gap (in neither the snapshot nor this client's broadcast) and be
	// silently dropped.
	// ponytail: the snapshot write happens while holding h.mu. It is bounded by
	// MaxOutputLines (the ring cap), so the lock hold is bounded; upgrade path
	// is a per-client send queue if a slow client ever stalls broadcast.
	h.mu.Lock()
	snap := h.cfg.Ring.Snapshot()
	if len(snap) > 0 {
		snapFrame, err := EncodeMessage(MsgTerminalData, snap)
		if err == nil {
			_, err = conn.Write(snapFrame)
		}
		if err != nil {
			h.mu.Unlock()
			_ = conn.Close()
			return
		}
	}
	h.clients[conn] = &clientState{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		// This client is gone; if it was the largest, let the grid shrink back to
		// the remaining largest client.
		h.applyLargestLocked()
		h.mu.Unlock()
		_ = conn.Close()
	}()

	parser := NewMessageParser(func(msgType byte, payload []byte) {
		h.handleClientMsg(conn, msgType, payload)
	})

	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			parser.Feed(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// handleClientMsg dispatches a decoded client message. Mirrors handleClientMessage
// from pty-host.ts.
func (h *host) handleClientMsg(conn net.Conn, msgType byte, payload []byte) {
	switch msgType {
	case MsgTerminalInput:
		if _, alive := h.cfg.PTY.ExitCode(); !alive {
			_, _ = h.cfg.PTY.Write(payload)
		}

	case MsgResize:
		if _, alive := h.cfg.PTY.ExitCode(); !alive {
			var rp ResizePayload
			if err := json.Unmarshal(payload, &rp); err == nil && rp.Cols > 0 && rp.Rows > 0 {
				// Record this client's requested grid, then size the shared PTY to
				// the largest client (see applyLargestLocked) rather than blindly
				// applying this one — otherwise a small viewer shrinks every viewer.
				h.mu.Lock()
				if cs := h.clients[conn]; cs != nil {
					cs.cols, cs.rows, cs.sized = rp.Cols, rp.Rows, true
				}
				h.applyLargestLocked()
				h.mu.Unlock()
			}
			// Malformed resize: ignore (matches TS behavior).
		}

	case MsgGetOutputReq:
		lines := 50 // default matches TS
		var req GetOutputReq
		if err := json.Unmarshal(payload, &req); err == nil && req.Lines > 0 {
			lines = req.Lines
		}
		text := h.cfg.Ring.Tail(lines)
		if frame, err := EncodeMessage(MsgGetOutputRes, []byte(text)); err == nil {
			h.sendTo(conn, frame)
		}

	case MsgGetStyledOutputReq:
		lines := 50
		var req GetOutputReq
		if err := json.Unmarshal(payload, &req); err == nil && req.Lines > 0 {
			lines = req.Lines
		}
		text := h.surface.Tail(lines)
		if frame, err := EncodeMessage(MsgGetStyledOutputRes, []byte(text)); err == nil {
			h.sendTo(conn, frame)
		}

	case MsgStatusReq:
		code, exited := h.cfg.PTY.ExitCode()
		alive := !exited
		pid := h.cfg.PTY.PID()
		var codePtr *int
		if exited {
			codePtr = &code
		}
		h.sendTo(conn, statusFrame(alive, pid, codePtr))

	case MsgKillReq:
		// Trigger graceful shutdown; returns immediately (idempotent).
		go h.shutdown()
	}
}

// statusFrame builds a MsgStatusRes frame.
func statusFrame(alive bool, pid int, exitCode *int) []byte {
	sp := StatusPayload{
		Alive: alive, PID: pid, ExitCode: exitCode,
		ProtocolVersion: conPTYHostProtocolVersion,
	}
	b, _ := json.Marshal(sp)
	frame, _ := EncodeMessage(MsgStatusRes, b) // b is small JSON, never overflows uint32
	return frame
}
