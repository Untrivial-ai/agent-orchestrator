package httpd

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/browserstream"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
)

const (
	browserLiveReadLimit = 64 << 10
	browserFrameVersion  = byte(1)
	browserFrameJPEG     = byte(1)
)

type browserLiveBroker interface {
	Subscribe(context.Context, string) (*browserstream.Subscription, error)
	Send(context.Context, string, string, any) error
}

type browserLiveSessionReader interface {
	Get(context.Context, domain.SessionID) (domain.Session, error)
}

// BrowserLiveHub bridges authenticated mobile WebSockets to the private
// daemon↔Electron media link. It also owns the exclusive controller lease for
// each session and can synchronously revoke all streams when the desktop gate
// is disabled.
type BrowserLiveHub struct {
	broker   browserLiveBroker
	sessions browserLiveSessionReader
	enabled  func() bool
	log      *slog.Logger

	mu     sync.Mutex
	leases map[string]context.CancelFunc
}

// NewBrowserLiveHub creates the authenticated LAN WebSocket bridge.
func NewBrowserLiveHub(broker browserLiveBroker, sessions browserLiveSessionReader, enabled func() bool, log *slog.Logger) *BrowserLiveHub {
	return &BrowserLiveHub{broker: broker, sessions: sessions, enabled: enabled, log: loggerOrDefault(log), leases: map[string]context.CancelFunc{}}
}

// CloseAll revokes every active mobile browser lease.
func (h *BrowserLiveHub) CloseAll() {
	h.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(h.leases))
	for _, cancel := range h.leases {
		cancels = append(cancels, cancel)
	}
	h.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (h *BrowserLiveHub) reserve(sessionID string) (context.Context, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.leases[sessionID]; exists {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.leases[sessionID] = cancel
	release := func() {
		h.mu.Lock()
		if current := h.leases[sessionID]; current != nil {
			delete(h.leases, sessionID)
			current()
		}
		h.mu.Unlock()
	}
	return ctx, release, true
}

func mountBrowserLive(r chi.Router, hub *BrowserLiveHub) {
	if hub == nil {
		return
	}
	r.Get("/api/v1/sessions/{id}/browser/live", hub.serve)
}

func (h *BrowserLiveHub) serve(w http.ResponseWriter, r *http.Request) {
	if !requestscope.IsLAN(r.Context()) {
		notFoundJSON(w, r)
		return
	}
	if h.enabled == nil || !h.enabled() {
		envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "BROWSER_REMOTE_DISABLED", "Browser viewing from mobile is disabled on the desktop", nil)
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	if sessionID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SESSION_ID_REQUIRED", "session id is required", nil)
		return
	}
	session, err := h.sessions.Get(r.Context(), domain.SessionID(sessionID))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if session.IsTerminated {
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "SESSION_TERMINATED", "Session is terminated", nil)
		return
	}
	leaseCtx, release, ok := h.reserve(sessionID)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "BROWSER_CONTROL_IN_USE", "Another mobile client controls this browser", nil)
		return
	}
	defer release()

	sub, err := h.broker.Subscribe(r.Context(), sessionID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, browserstream.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		envelope.WriteAPIError(w, r, status, "unavailable", "BROWSER_HOST_UNAVAILABLE", "Desktop browser host is unavailable", nil)
		return
	}
	defer sub.Close()

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		h.log.Warn("browser live: websocket upgrade failed", "err", err)
		return
	}
	defer func() {
		if closeErr := c.Close(websocket.StatusNormalClosure, "browser stream closed"); closeErr != nil {
			h.log.Debug("browser live: websocket close failed", "err", closeErr)
		}
	}()
	c.SetReadLimit(browserLiveReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-leaseCtx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	writeDone := make(chan error, 1)
	go func() { writeDone <- h.writeBrowserEvents(ctx, c, sub.Events) }()
	readDone := make(chan error, 1)
	go func() { readDone <- h.readBrowserCommands(ctx, c, sessionID) }()
	select {
	case err = <-writeDone:
	case err = <-readDone:
	case <-ctx.Done():
		err = ctx.Err()
	}
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) && websocket.CloseStatus(err) == -1 {
		h.log.Debug("browser live connection ended", "session", sessionID, "err", err)
	}
}

func (h *BrowserLiveHub) writeBrowserEvents(ctx context.Context, c *websocket.Conn, events <-chan browserstream.Event) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if event.Frame != nil {
				frame := event.Frame
				packet := make([]byte, 22+len(frame.JPEG))
				packet[0], packet[1] = browserFrameVersion, browserFrameJPEG
				binary.BigEndian.PutUint64(packet[2:10], frame.Sequence)
				binary.BigEndian.PutUint64(packet[10:18], uint64(frame.Captured.UnixMilli()))
				binary.BigEndian.PutUint16(packet[18:20], frame.Width)
				binary.BigEndian.PutUint16(packet[20:22], frame.Height)
				copy(packet[22:], frame.JPEG)
				if err := c.Write(ctx, websocket.MessageBinary, packet); err != nil {
					return err
				}
				continue
			}
			message := map[string]any{"type": event.Type}
			if len(event.Payload) > 0 {
				message["payload"] = event.Payload
			}
			if event.Code != "" {
				message["code"] = event.Code
			}
			if event.Message != "" {
				message["message"] = event.Message
			}
			payload, marshalErr := json.Marshal(message)
			if marshalErr != nil {
				return marshalErr
			}
			if err := c.Write(ctx, websocket.MessageText, payload); err != nil {
				return err
			}
		}
	}
}

type browserLiveCommand struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func (h *BrowserLiveHub) readBrowserCommands(ctx context.Context, c *websocket.Conn, sessionID string) error {
	for {
		messageType, payload, err := c.Read(ctx)
		if err != nil {
			return err
		}
		if messageType != websocket.MessageText {
			return c.Close(websocket.StatusUnsupportedData, "commands must be JSON text")
		}
		var command browserLiveCommand
		if err := json.Unmarshal(payload, &command); err != nil {
			return c.Close(websocket.StatusInvalidFramePayloadData, "invalid browser command")
		}
		if command.Type != "input" && command.Type != "navigate" && command.Type != "tab" {
			return c.Close(websocket.StatusPolicyViolation, "unsupported browser command")
		}
		commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = h.broker.Send(commandCtx, sessionID, command.Type, command.Payload)
		cancel()
		if err != nil {
			return err
		}
	}
}
