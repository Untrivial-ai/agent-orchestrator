package terminal

import (
	"context"
	"encoding/base64"
	"log/slog"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionguard"
)

// EventSource is the session-state feed the "sessions" channel forwards. The CDC
// broadcaster satisfies it; the interface lives next to its consumer so terminal
// does not depend on CDC internals beyond the Event shape.
type EventSource interface {
	Subscribe(fn func(cdc.Event)) (unsubscribe func())
}

// wsConn is the transport seam: a JSON-framed, single-reader/single-writer
// WebSocket connection. internal/httpd adapts coder/websocket to this; tests
// supply an in-memory fake. WriteJSON is only ever called from the per-conn
// writer goroutine; Ping may be called concurrently (it is a control frame).
type wsConn interface {
	ReadJSON(ctx context.Context, v any) error
	WriteJSON(ctx context.Context, v any) error
	Ping(ctx context.Context) error
	Close(reason string) error
}

const (
	defaultHeartbeat   = 15 * time.Second
	defaultWriteBuffer = 1024
)

// Manager serves WebSocket clients, opening one attach Stream per opened pane
// per connection. There is no shared per-pane state to outlive a connection:
// the runtime owns the session (screen, scrollback, modes), and every fresh
// attach gets its full handshake + repaint. A client reconnect simply attaches
// again.
type Manager struct {
	src       Source
	events    EventSource
	log       *slog.Logger
	heartbeat time.Duration

	// ctx scopes every attachment's PTY lifetime; cancelled by Close.
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	attachments map[*attachment]struct{}
	closed      bool

	// sharedMu guards shared, the per-terminal-id view of every attached client.
	// It arbitrates the single PTY's grid across clients (see reconcileLocked).
	sharedMu sync.Mutex
	shared   map[string]*sharedTerm

	inputLeaseMu sync.RWMutex
	inputLease   sessionguard.InputLease

	// inputMu makes BeginInputDrain atomic with pane writes. Once a drain returns,
	// every write that began before it has finished and every later write is
	// refused until the matching release runs.
	inputMu      sync.Mutex
	inputBlocked map[string]int
	lastInputAt  map[string]time.Time
}

// sharedTerm tracks every client currently viewing one terminal id (one PTY) so
// the daemon can pick a single authoritative grid for it. A PTY has exactly one
// size; when several clients view it at once the largest eligible client wins and
// the rest render that grid scaled — this is what keeps a small phone from
// stripping down the desktop, and keeps every client's grid matched to the PTY so
// full-screen TUIs don't mis-render.
type sharedTerm struct {
	members            map[*connState]*termMember
	authCols, authRows uint16 // last authoritative grid broadcast/applied
}

// termMember is one client's contribution to a shared terminal: its own attach
// Stream, the grid it last asked for, and whether it drives the size (primary).
type termMember struct {
	att     *attachment
	cols    uint16
	rows    uint16
	primary bool
}

// Option configures a Manager.
type Option func(*Manager)

// WithHeartbeat overrides the ping interval.
func WithHeartbeat(d time.Duration) Option { return func(m *Manager) { m.heartbeat = d } }

// SetSessionInputLease late-binds the mutation-aware input authority. The
// terminal manager is created before the session manager during daemon boot.
func (m *Manager) SetSessionInputLease(lease sessionguard.InputLease) {
	m.inputLeaseMu.Lock()
	m.inputLease = lease
	m.inputLeaseMu.Unlock()
}

func (m *Manager) acquireSessionInput(id string) (func(), bool) {
	m.inputLeaseMu.RLock()
	lease := m.inputLease
	m.inputLeaseMu.RUnlock()
	if lease == nil {
		return func() {}, true
	}
	return lease.AcquireSessionInput(domain.SessionID(id))
}

// NewManager builds a Manager. src opens attach Streams; events feeds the session
// channel (may be nil to disable it). A nil logger falls back to slog.Default.
func NewManager(src Source, events EventSource, log *slog.Logger, opts ...Option) *Manager {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		src:          src,
		events:       events,
		log:          log,
		heartbeat:    defaultHeartbeat,
		ctx:          ctx,
		cancel:       cancel,
		attachments:  map[*attachment]struct{}{},
		shared:       map[string]*sharedTerm{},
		inputBlocked: map[string]int{},
		lastInputAt:  map[string]time.Time{},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// BeginInputDrain blocks new user input for one terminal while Session Manager
// hands its controller to another interface. The returned release is idempotent.
func (m *Manager) BeginInputDrain(terminalID string) (lastInputAt time.Time, release func()) {
	m.inputMu.Lock()
	m.inputBlocked[terminalID]++
	lastInputAt = m.lastInputAt[terminalID]
	m.inputMu.Unlock()

	var once sync.Once
	return lastInputAt, func() {
		once.Do(func() {
			m.inputMu.Lock()
			defer m.inputMu.Unlock()
			if m.inputBlocked[terminalID] <= 1 {
				delete(m.inputBlocked, terminalID)
				return
			}
			m.inputBlocked[terminalID]--
		})
	}
}

func (m *Manager) writeInput(terminalID string, a *attachment, raw []byte, release func()) {
	m.inputMu.Lock()
	defer m.inputMu.Unlock()
	if m.inputBlocked[terminalID] > 0 {
		release()
		return
	}
	m.lastInputAt[terminalID] = time.Now()
	_ = a.writeLeased(raw, release)
}

// Close tears down every live attachment and stops re-attach loops. Safe to
// call once on daemon shutdown.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	attachments := make([]*attachment, 0, len(m.attachments))
	for a := range m.attachments {
		attachments = append(attachments, a)
	}
	m.attachments = map[*attachment]struct{}{}
	m.mu.Unlock()

	m.cancel()
	for _, a := range attachments {
		a.close()
	}
}

// track registers a live attachment so Close can tear it down; it refuses new
// attachments once the manager is closed.
func (m *Manager) track(a *attachment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	m.attachments[a] = struct{}{}
	return nil
}

func (m *Manager) forget(a *attachment) {
	m.mu.Lock()
	delete(m.attachments, a)
	m.mu.Unlock()
}

// joinTerminal registers a client (its connection + attach Stream + requested
// grid + role) as a viewer of terminal id, then reconciles the shared grid.
func (m *Manager) joinTerminal(id string, c *connState, att *attachment, cols, rows uint16, primary bool) {
	m.sharedMu.Lock()
	defer m.sharedMu.Unlock()
	s := m.shared[id]
	if s == nil {
		s = &sharedTerm{members: map[*connState]*termMember{}}
		m.shared[id] = s
	}
	previousCols, previousRows := s.authCols, s.authRows
	s.members[c] = &termMember{att: att, cols: cols, rows: rows, primary: primary}
	m.reconcileLocked(id, s, false)
	// A follower joining a PTY that is already at its authoritative size wouldn't
	// be touched by reconciliation. Apply the grid only to its new attachment and
	// tell that client directly, without re-signalling every existing viewer.
	if s.authCols > 0 && s.authRows > 0 &&
		previousCols == s.authCols && previousRows == s.authRows {
		_ = att.resize(s.authRows, s.authCols)
		c.enqueue(serverMsg{Ch: chTerminal, ID: id, Type: msgResize, Cols: s.authCols, Rows: s.authRows})
	}
}

// updateTerminalSize records a client's newly requested grid and reconciles.
// Identical normal updates are no-ops; force is reserved for an explicit
// recovery re-signal when an attach client may have missed the prior SIGWINCH.
func (m *Manager) updateTerminalSize(id string, c *connState, cols, rows uint16, force bool) {
	m.sharedMu.Lock()
	defer m.sharedMu.Unlock()
	s := m.shared[id]
	if s == nil {
		return
	}
	mem := s.members[c]
	if mem == nil {
		return
	}
	if !force && mem.cols == cols && mem.rows == rows {
		return
	}
	mem.cols, mem.rows = cols, rows
	m.reconcileLocked(id, s, force)
}

// leaveTerminal drops a client from a shared terminal and reconciles so the grid
// follows the remaining clients (or the entry is dropped when the last leaves).
func (m *Manager) leaveTerminal(id string, c *connState) {
	m.sharedMu.Lock()
	defer m.sharedMu.Unlock()
	s := m.shared[id]
	if s == nil {
		return
	}
	if _, ok := s.members[c]; !ok {
		return
	}
	delete(s.members, c)
	if len(s.members) == 0 {
		delete(m.shared, id)
		return
	}
	m.reconcileLocked(id, s, false)
}

// reconcileLocked picks the authoritative grid for a shared terminal and applies
// it only when it changed (or explicit recovery forces a re-signal). This keeps
// a non-authoritative viewer's local resize from repainting the shared TUI at an
// unchanged grid. Caller holds m.sharedMu.
func (m *Manager) reconcileLocked(id string, s *sharedTerm, force bool) {
	cols, rows := largestGrid(s.members)
	if cols == 0 || rows == 0 {
		return // no client has reported a usable size yet
	}
	changed := cols != s.authCols || rows != s.authRows
	if !changed && !force {
		return
	}
	s.authCols, s.authRows = cols, rows
	for conn, mem := range s.members {
		_ = mem.att.resize(rows, cols)
		if changed {
			conn.enqueue(serverMsg{Ch: chTerminal, ID: id, Type: msgResize, Cols: cols, Rows: rows})
		}
	}
}

// largestGrid chooses the authoritative grid for a set of viewers: the largest
// (by area) among the PRIMARY clients if any primary has reported a size, else
// the largest among all. Choosing one client's cols AND rows as a pair (never an
// independent per-axis max) guarantees the grid matches a real client exactly, so
// that client renders pixel-correct and only smaller ones scale.
func largestGrid(members map[*connState]*termMember) (cols, rows uint16) {
	anyPrimary := false
	for _, mem := range members {
		if mem.primary && mem.cols > 0 && mem.rows > 0 {
			anyPrimary = true
			break
		}
	}
	bestArea := 0
	for _, mem := range members {
		if anyPrimary && !mem.primary {
			continue
		}
		if mem.cols == 0 || mem.rows == 0 {
			continue
		}
		if a := int(mem.cols) * int(mem.rows); a > bestArea {
			bestArea, cols, rows = a, mem.cols, mem.rows
		}
	}
	return cols, rows
}

// Serve runs the protocol loop for one client connection until it errors, the
// client disconnects, or ctx/the manager is cancelled. It owns the single writer
// goroutine and the heartbeat.
func (m *Manager) Serve(ctx context.Context, conn wsConn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c := &connState{
		mgr:    m,
		conn:   conn,
		cancel: cancel,
		out:    make(chan serverMsg, defaultWriteBuffer),
		terms:  map[string]*attachment{},
	}
	defer c.cleanup()

	go c.writeLoop(ctx)
	go c.heartbeatLoop(ctx, m.heartbeat)

	for {
		var msg clientMsg
		if err := conn.ReadJSON(ctx, &msg); err != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		c.handle(msg)
	}
}

// connState is the per-connection mutable state.
type connState struct {
	mgr    *Manager
	conn   wsConn
	cancel context.CancelFunc
	out    chan serverMsg

	mu        sync.Mutex
	terms     map[string]*attachment // terminal id -> this conn's own attach PTY
	unsubEvts func()
	closed    bool
}

func (c *connState) handle(msg clientMsg) {
	switch msg.Ch {
	case chTerminal:
		c.handleTerminal(msg)
	case chSubscribe:
		c.handleSubscribe(msg)
	case chSystem:
		if msg.Type == msgPing {
			c.enqueue(serverMsg{Ch: chSystem, Type: msgPong})
		}
	}
}

func (c *connState) handleTerminal(msg clientMsg) {
	switch msg.Type {
	case msgOpen:
		c.openTerminal(msg.ID, msg.Rows, msg.Cols, msg.Role)
	case msgData:
		raw, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			return
		}
		release, ok := c.mgr.acquireSessionInput(msg.ID)
		if !ok {
			c.enqueue(serverMsg{Ch: chTerminal, ID: msg.ID, Type: msgError, Error: "session input is disabled while an exclusive session operation is in progress"})
			return
		}
		if a := c.lookup(msg.ID); a != nil {
			c.mgr.writeInput(msg.ID, a, raw, release)
		} else {
			release()
		}
	case msgResize:
		// The client reports the grid it fits to; the manager arbitrates the shared
		// PTY's size across all viewers and resizes the attach Stream itself (see
		// reconcileLocked), so we do not resize this attachment directly here.
		c.mgr.updateTerminalSize(msg.ID, c, msg.Cols, msg.Rows, msg.Force)
	case msgClose:
		c.closeTerminal(msg.ID)
	}
}

// openTerminal opens this connection's own attach Stream for the pane. rows/cols
// are the client's grid from the open frame; the manager arbitrates the shared
// PTY size from it (see joinTerminal) and applies it to the Stream. role marks
// the client primary/secondary for that arbitration (empty = primary).
func (c *connState) openTerminal(id string, rows, cols uint16, role string) {
	if id == "" {
		c.enqueue(serverMsg{Ch: chTerminal, Type: msgError, Error: "missing terminal id"})
		return
	}
	c.mu.Lock()
	if _, ok := c.terms[id]; ok {
		c.mu.Unlock()
		return // already open on this conn; avoid a duplicate attach
	}
	c.mu.Unlock()

	// a is captured by onExit before assignment; safe because the attach loop —
	// the only thing that fires onExit — starts after the registration below.
	var a *attachment
	a = newAttachment(id, ports.RuntimeHandle{ID: id}, c.mgr.src,
		func() {
			c.enqueue(serverMsg{Ch: chTerminal, ID: id, Type: msgOpened})
		},
		func(data []byte) {
			c.enqueue(serverMsg{
				Ch:   chTerminal,
				ID:   id,
				Type: msgData,
				Data: base64.StdEncoding.EncodeToString(data),
			})
		},
		func() {
			// Clear the connection's entry for this id before sending exited so
			// a client that reopens the moment it sees exited finds no stale
			// entry and is served instead of dropped by the open guard. Guard on
			// identity: that reopen may already have installed a fresh
			// attachment under the same id, which must not be evicted.
			c.mu.Lock()
			if c.terms[id] == a {
				delete(c.terms, id)
			}
			c.mu.Unlock()
			c.mgr.leaveTerminal(id, c)
			c.enqueue(serverMsg{Ch: chTerminal, ID: id, Type: msgExited})
		},
		c.mgr.log)
	if err := c.mgr.track(a); err != nil {
		c.enqueue(serverMsg{Ch: chTerminal, ID: id, Type: msgError, Error: err.Error()})
		return
	}
	c.mu.Lock()
	c.terms[id] = a
	c.mu.Unlock()

	// Register with the shared-terminal arbiter, which sizes the attach Stream to
	// the authoritative grid (the open frame's rows/cols become this client's
	// requested size). An empty role means primary — the size-driving client.
	c.mgr.joinTerminal(id, c, a, cols, rows, role != roleSecondary)

	go func() {
		a.run(c.mgr.ctx)
		c.mgr.forget(a)
	}()
}

func (c *connState) closeTerminal(id string) {
	c.mu.Lock()
	a := c.terms[id]
	delete(c.terms, id)
	c.mu.Unlock()
	c.mgr.leaveTerminal(id, c)
	if a != nil {
		a.close()
	}
}

func (c *connState) lookup(id string) *attachment {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terms[id]
}

func (c *connState) handleSubscribe(msg clientMsg) {
	if msg.Type != msgSubscribe || c.mgr.events == nil {
		return
	}
	c.mu.Lock()
	if c.unsubEvts != nil {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	unsub := c.mgr.events.Subscribe(func(e cdc.Event) {
		c.enqueue(serverMsg{
			Ch:   chSessions,
			Type: msgSnapshot,
			Session: &sessionUpdate{
				Seq:       e.Seq,
				ProjectID: e.ProjectID,
				SessionID: e.SessionID,
				EventType: string(e.Type),
			},
		})
	})
	c.mu.Lock()
	c.unsubEvts = unsub
	c.mu.Unlock()
}

// enqueue pushes a frame to the writer. If the buffer is full the client is too
// slow to keep up; tear the connection down rather than block the attachment's
// PTY read loop behind it.
func (c *connState) enqueue(msg serverMsg) {
	select {
	case c.out <- msg:
	default:
		c.cancel()
	}
}

func (c *connState) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.out:
			if err := c.conn.WriteJSON(ctx, msg); err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *connState) heartbeatLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, interval)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *connState) cleanup() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	attachments := make([]*attachment, 0, len(c.terms))
	ids := make([]string, 0, len(c.terms))
	for id, a := range c.terms {
		attachments = append(attachments, a)
		ids = append(ids, id)
	}
	c.terms = map[string]*attachment{}
	unsubEvts := c.unsubEvts
	c.unsubEvts = nil
	c.mu.Unlock()

	// Drop this connection from every shared terminal so the grid follows the
	// clients that remain (a disconnecting large client must let the PTY shrink
	// back to the smaller ones still attached).
	for _, id := range ids {
		c.mgr.leaveTerminal(id, c)
	}
	for _, a := range attachments {
		a.close()
	}
	if unsubEvts != nil {
		unsubEvts()
	}
	_ = c.conn.Close("server: connection closed")
}
