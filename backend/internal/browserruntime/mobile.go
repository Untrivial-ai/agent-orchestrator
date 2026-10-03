package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// MobileConn is the JSON transport used by a foreground mobile browser target.
// The HTTP layer adapts its WebSocket implementation to this deliberately small
// interface so browser routing remains transport independent.
type MobileConn interface {
	ReadJSON(context.Context, any) error
	WriteJSON(context.Context, any) error
	Close(string) error
}

type mobileTarget struct {
	sessionID   domain.SessionID
	deviceID    string
	connectedAt time.Time
	conn        MobileConn
	writeMu     sync.Mutex
	mu          sync.Mutex
	pending     map[string]chan pendingResult
}

type mobileResultMessage struct {
	Type      string          `json:"type"`
	RequestID string          `json:"requestId"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *CommandError   `json:"error,omitempty"`
}

// MobileHub owns foreground mobile WebView targets. A target exists only while
// its Preview screen is mounted and active; disconnecting immediately makes
// commands unavailable rather than queueing work for a stale page.
type MobileHub struct {
	mu      sync.Mutex
	targets map[domain.SessionID]*mobileTarget
}

// NewMobileHub creates an empty registry of foreground mobile browser targets.
func NewMobileHub() *MobileHub {
	return &MobileHub{targets: make(map[domain.SessionID]*mobileTarget)}
}

// Status returns the foreground mobile runtime state for one session.
func (h *MobileHub) Status(sessionID domain.SessionID) Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	target := h.targets[sessionID]
	if target == nil {
		return Status{}
	}
	return Status{Connected: true, ConnectedAt: target.connectedAt}
}

// Execute sends one browser command to the foreground mobile runtime for a session.
func (h *MobileHub) Execute(ctx context.Context, sessionID domain.SessionID, action string, args map[string]interface{}) (Result, error) {
	h.mu.Lock()
	target := h.targets[sessionID]
	h.mu.Unlock()
	if target == nil {
		return Result{}, ErrUnavailable
	}

	requestID := uuid.NewString()
	resultCh := make(chan pendingResult, 1)
	target.mu.Lock()
	target.pending[requestID] = resultCh
	target.mu.Unlock()

	message := wireMessage{Type: "command", RequestID: requestID, SessionID: sessionID, Action: action, Args: args}
	target.writeMu.Lock()
	err := target.conn.WriteJSON(ctx, message)
	target.writeMu.Unlock()
	if err != nil {
		target.removePending(requestID)
		h.disconnect(target)
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, ErrUnavailable
	}

	select {
	case <-ctx.Done():
		target.removePending(requestID)
		cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		target.writeMu.Lock()
		_ = target.conn.WriteJSON(cancelCtx, wireMessage{Type: "cancel", RequestID: requestID})
		target.writeMu.Unlock()
		cancel()
		return Result{}, ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			return Result{}, result.err
		}
		return Result{RequestID: requestID, Value: result.value}, nil
	}
}

// Serve registers one foreground WebView. A newer connection for the same
// session replaces the old one so a reconnect cannot leave two phones racing
// to answer the same command.
func (h *MobileHub) Serve(ctx context.Context, sessionID domain.SessionID, deviceID string, conn MobileConn) error {
	if sessionID == "" || deviceID == "" {
		return errors.New("mobile browser session and device ids are required")
	}
	target := &mobileTarget{
		sessionID: sessionID, deviceID: deviceID, connectedAt: time.Now().UTC(), conn: conn,
		pending: make(map[string]chan pendingResult),
	}

	h.mu.Lock()
	old := h.targets[sessionID]
	h.targets[sessionID] = target
	h.mu.Unlock()
	if old != nil {
		old.failAll(ErrUnavailable)
		_ = old.conn.Close("replaced")
	}

	defer h.disconnect(target)
	for {
		var message mobileResultMessage
		if err := conn.ReadJSON(ctx, &message); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read mobile browser result: %w", err)
		}
		if message.Type != "result" || message.RequestID == "" {
			continue
		}
		target.resolve(message)
	}
}

func (h *MobileHub) disconnect(target *mobileTarget) {
	h.mu.Lock()
	if h.targets[target.sessionID] == target {
		delete(h.targets, target.sessionID)
	}
	h.mu.Unlock()
	target.failAll(ErrUnavailable)
	_ = target.conn.Close("disconnected")
}

// DestroySession disconnects the mobile runtime and fails its pending commands.
func (h *MobileHub) DestroySession(sessionID domain.SessionID) {
	h.mu.Lock()
	target := h.targets[sessionID]
	if target != nil {
		delete(h.targets, sessionID)
	}
	h.mu.Unlock()
	if target != nil {
		target.failAll(ErrUnavailable)
		_ = target.conn.Close("session ended")
	}
}

func (t *mobileTarget) removePending(requestID string) {
	t.mu.Lock()
	delete(t.pending, requestID)
	t.mu.Unlock()
}

func (t *mobileTarget) failAll(err error) {
	t.mu.Lock()
	pending := t.pending
	t.pending = make(map[string]chan pendingResult)
	t.mu.Unlock()
	failPending(pending, err)
}

func (t *mobileTarget) resolve(message mobileResultMessage) {
	t.mu.Lock()
	ch := t.pending[message.RequestID]
	delete(t.pending, message.RequestID)
	t.mu.Unlock()
	if ch == nil {
		return
	}
	deliverPendingResult(ch, message.OK, message.Result, message.Error, "Mobile browser command failed", "mobile browser")
}

// Router selects a concrete browser surface. Auto prefers a foreground mobile
// target for this session, then falls back to the desktop Electron runtime.
type Router struct {
	desktop *Broker
	mobile  *MobileHub
}

// NewRouter creates a browser runtime router backed by desktop and mobile surfaces.
func NewRouter(desktop *Broker, mobile *MobileHub) *Router {
	return &Router{desktop: desktop, mobile: mobile}
}

// Status returns the selected browser surface state and transport name.
func (r *Router) Status(sessionID domain.SessionID, surface string) (Status, string) {
	switch surface {
	case "mobile":
		return r.mobile.Status(sessionID), "mobile-webview"
	case "desktop":
		return r.desktop.Status(), "electron-webcontents-debugger"
	default:
		if status := r.mobile.Status(sessionID); status.Connected {
			return status, "mobile-webview"
		}
		return r.desktop.Status(), "electron-webcontents-debugger"
	}
}

// Execute dispatches one command to the selected browser surface.
func (r *Router) Execute(ctx context.Context, sessionID domain.SessionID, surface, action string, args map[string]interface{}) (Result, error) {
	switch surface {
	case "mobile":
		return r.mobile.Execute(ctx, sessionID, action, args)
	case "desktop":
		return r.desktop.Execute(ctx, sessionID, action, args)
	default:
		if r.mobile.Status(sessionID).Connected {
			return r.mobile.Execute(ctx, sessionID, action, args)
		}
		return r.desktop.Execute(ctx, sessionID, action, args)
	}
}

// DestroySession tears down both surfaces. Desktop cleanup remains best-effort
// when Electron is absent, matching Broker.DestroySession.
func (r *Router) DestroySession(ctx context.Context, sessionID domain.SessionID) error {
	r.mobile.DestroySession(sessionID)
	return r.desktop.DestroySession(ctx, sessionID)
}
