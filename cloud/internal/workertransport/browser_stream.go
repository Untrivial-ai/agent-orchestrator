package workertransport

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/aoagents/agent-orchestrator/cloud/internal/vmbrowser"
	"github.com/coder/websocket"
)

const (
	browserStreamWireLimit    = browserstream.MaxFrameBytes + 512
	browserStreamWriteTimeout = 5 * time.Second
	browserFirstFrameTimeout  = 25 * time.Second
)

// BrowserStreamDialer opens the worker side of the browser relay.
type BrowserStreamDialer interface {
	DialBrowserStream(context.Context, string) (*websocket.Conn, error)
}

type browserWireMessage struct {
	kind    websocket.MessageType
	payload []byte
	err     error
}

type browserWireWriter interface {
	Write(context.Context, websocket.MessageType, []byte) error
}

// RunBrowserStream keeps the outbound control-plane socket connected. The
// socket stays idle until a viewer sends attach, which is the browser intent
// that opens browserd's loopback viewer stream and starts Chromium if needed.
func RunBrowserStream(
	ctx context.Context,
	dialer BrowserStreamDialer,
	sessionID, browserAPIURL, capability string,
	logger *slog.Logger,
) error {
	if dialer == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(browserAPIURL) == "" || strings.TrimSpace(capability) == "" {
		return errors.New("browser stream requires dialer, session, browser API, and capability")
	}
	if logger == nil {
		logger = slog.Default()
	}
	backoff := streamRedialFloor
	for ctx.Err() == nil {
		connection, err := dialer.DialBrowserStream(ctx, sessionID)
		if err == nil {
			backoff = streamRedialFloor
			err = bridgeBrowserStream(ctx, connection, browserAPIURL, capability)
			_ = connection.CloseNow()
		}
		if ctx.Err() != nil {
			return nil
		}
		logger.Debug("browser stream reconnecting", "error", err, "delay", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff < streamRedialCeiling {
			backoff *= 2
			if backoff > streamRedialCeiling {
				backoff = streamRedialCeiling
			}
		}
	}
	return nil
}

func bridgeBrowserStream(ctx context.Context, controlPlane *websocket.Conn, browserAPIURL, capability string) error {
	return bridgeBrowserStreamWithFrameTimeout(ctx, controlPlane, browserAPIURL, capability, browserFirstFrameTimeout)
}

func bridgeBrowserStreamWithFrameTimeout(
	ctx context.Context,
	controlPlane *websocket.Conn,
	browserAPIURL, capability string,
	firstFrameTimeout time.Duration,
) error {
	controlPlane.SetReadLimit(browserstream.MaxControlBytes)
	cpMessages := readBrowserWire(ctx, controlPlane)
	var local *websocket.Conn
	var localCancel context.CancelFunc
	var localMessages <-chan browserWireMessage
	var reconnectLocal <-chan time.Time
	var firstFrameTimer *time.Timer
	var firstFrameDeadline <-chan time.Time
	localBackoff := streamRedialFloor

	stopFirstFrameTimer := func() {
		if firstFrameTimer != nil {
			if !firstFrameTimer.Stop() {
				select {
				case <-firstFrameTimer.C:
				default:
				}
			}
		}
		firstFrameTimer = nil
		firstFrameDeadline = nil
	}

	closeLocal := func() {
		stopFirstFrameTimer()
		if localCancel != nil {
			localCancel()
			localCancel = nil
		}
		if local != nil {
			_ = local.CloseNow()
			local = nil
		}
		localMessages = nil
	}
	defer closeLocal()

	writeControlPlane := func(kind websocket.MessageType, payload []byte) error {
		return writeBrowserWire(ctx, controlPlane, browserStreamWriteTimeout, kind, payload)
	}
	writeError := func(code, message string) error {
		payload, _ := json.Marshal(browserstream.Control{
			Type: "error", Version: browserstream.Version, Code: code, Message: message,
		})
		return writeControlPlane(websocket.MessageText, payload)
	}
	attachLocal := func() bool {
		dialCtx, cancel := context.WithTimeout(ctx, browserStreamWriteTimeout)
		connection, err := dialBrowserd(dialCtx, browserAPIURL, capability)
		cancel()
		if err != nil {
			reconnectLocal = time.After(localBackoff)
			if localBackoff < streamRedialCeiling {
				localBackoff *= 2
				if localBackoff > streamRedialCeiling {
					localBackoff = streamRedialCeiling
				}
			}
			return false
		}
		local = connection
		localCtx, cancel := context.WithCancel(ctx)
		localCancel = cancel
		localMessages = readBrowserWire(localCtx, local)
		reconnectLocal = nil
		localBackoff = streamRedialFloor
		if firstFrameTimeout > 0 {
			firstFrameTimer = time.NewTimer(firstFrameTimeout)
			firstFrameDeadline = firstFrameTimer.C
		}
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-localMessages:
			if !ok || message.err != nil {
				closeLocal()
				if err := writeError("BROWSER_RESTARTING", "The session browser is reconnecting."); err != nil {
					return err
				}
				reconnectLocal = time.After(localBackoff)
				continue
			}
			if err := validateBrowserdMessage(message); err != nil {
				closeLocal()
				if writeErr := writeError("BROWSER_STREAM_INVALID", "The session browser sent an invalid message."); writeErr != nil {
					return writeErr
				}
				reconnectLocal = time.After(localBackoff)
				continue
			}
			if message.kind == websocket.MessageBinary {
				stopFirstFrameTimer()
			}
			if err := writeControlPlane(message.kind, message.payload); err != nil {
				return err
			}
		case message, ok := <-cpMessages:
			if !ok || message.err != nil {
				if message.err != nil {
					return message.err
				}
				return errors.New("browser control-plane stream closed")
			}
			if message.kind != websocket.MessageText || len(message.payload) == 0 || len(message.payload) > browserstream.MaxControlBytes {
				return errors.New("invalid browser control-plane message")
			}
			var control browserstream.Control
			if json.Unmarshal(message.payload, &control) != nil || control.Version != browserstream.Version {
				return errors.New("invalid browser control-plane message")
			}
			switch control.Type {
			case "attach":
				closeLocal()
				if !attachLocal() {
					if err := writeError("BROWSER_SESSION_UNAVAILABLE", "The session browser is not available."); err != nil {
						return err
					}
				}
			case "detach":
				closeLocal()
				reconnectLocal = nil
				localBackoff = streamRedialFloor
			default:
				if !viewerControlAllowedForWorker(control.Type) {
					return errors.New("unsupported browser control-plane message")
				}
				if local == nil {
					if err := writeError("BROWSER_SESSION_UNAVAILABLE", "The session browser is not attached."); err != nil {
						return err
					}
					continue
				}
				if err := writeBrowserWire(ctx, local, browserStreamWriteTimeout, websocket.MessageText, message.payload); err != nil {
					closeLocal()
					if writeErr := writeError("BROWSER_RESTARTING", "The session browser is reconnecting."); writeErr != nil {
						return writeErr
					}
					reconnectLocal = time.After(localBackoff)
				}
			}
		case <-firstFrameDeadline:
			closeLocal()
			if err := writeError("BROWSER_RESTARTING", "The session browser did not deliver a frame and is reconnecting."); err != nil {
				return err
			}
			reconnectLocal = time.After(localBackoff)
		case <-reconnectLocal:
			if !attachLocal() {
				if err := writeError("BROWSER_SESSION_UNAVAILABLE", "The session browser is not available."); err != nil {
					return err
				}
			}
		}
	}
}

func writeBrowserWire(
	ctx context.Context,
	writer browserWireWriter,
	timeout time.Duration,
	kind websocket.MessageType,
	payload []byte,
) error {
	writeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return writer.Write(writeCtx, kind, payload)
}

func dialBrowserd(ctx context.Context, browserAPIURL, capability string) (*websocket.Conn, error) {
	streamURL := strings.TrimRight(browserAPIURL, "/") + vmbrowser.ViewerStreamRoute
	if strings.HasPrefix(streamURL, "http") {
		streamURL = "ws" + strings.TrimPrefix(streamURL, "http")
	}
	header := http.Header{}
	header.Set(browsercontract.CapabilityHeader, capability)
	connection, _, err := websocket.Dial(ctx, streamURL, &websocket.DialOptions{
		HTTPHeader: header, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	connection.SetReadLimit(browserStreamWireLimit)
	return connection, nil
}

func readBrowserWire(ctx context.Context, connection *websocket.Conn) <-chan browserWireMessage {
	messages := make(chan browserWireMessage, 1)
	go func() {
		defer close(messages)
		for {
			kind, payload, err := connection.Read(ctx)
			select {
			case messages <- browserWireMessage{kind: kind, payload: payload, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return messages
}

func validateBrowserdMessage(message browserWireMessage) error {
	if message.kind == websocket.MessageBinary {
		if len(message.payload) > browserStreamWireLimit {
			return errors.New("browser frame exceeds worker limit")
		}
		_, err := browserstream.DecodeFrame(message.payload)
		return err
	}
	if message.kind != websocket.MessageText || len(message.payload) == 0 || len(message.payload) > browserstream.MaxControlBytes {
		return errors.New("invalid browserd control message")
	}
	var control browserstream.Control
	if json.Unmarshal(message.payload, &control) != nil || control.Version != browserstream.Version {
		return errors.New("invalid browserd control message")
	}
	return nil
}

func viewerControlAllowedForWorker(messageType string) bool {
	switch messageType {
	case "ping", "viewport", "input", "navigate", "tab", "dialog":
		return true
	default:
		return false
	}
}
