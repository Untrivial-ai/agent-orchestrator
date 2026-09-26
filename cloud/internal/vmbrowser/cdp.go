package vmbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	maxCDPMessageBytes = 2 << 20
	maxCDPQueuedEvents = 256
	cdpCommandTimeout  = 10 * time.Second
)

var errCDPEventOverflow = errors.New("CDP event queue overflow")

type cdpEvent struct {
	Method    string
	SessionID string
	Params    json.RawMessage
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cdpResponse struct {
	Result json.RawMessage
	Error  *cdpError
}

type cdpConnection interface {
	Call(ctx context.Context, sessionID, method string, params any, result any) error
	Events() <-chan cdpEvent
	Close() error
}

type cdpDialer func(context.Context, string) (cdpConnection, error)

type websocketCDP struct {
	conn     *websocket.Conn
	nextID   atomic.Int64
	mu       sync.Mutex
	pending  map[int64]chan cdpResponse
	events   chan cdpEvent
	done     chan struct{}
	once     sync.Once
	closeErr error
}

func dialWebsocketCDP(ctx context.Context, endpoint string) (cdpConnection, error) {
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, fmt.Errorf("dial chromium CDP: %w", err)
	}
	conn.SetReadLimit(maxCDPMessageBytes)
	client := &websocketCDP{
		conn: conn, pending: make(map[int64]chan cdpResponse),
		events: make(chan cdpEvent, maxCDPQueuedEvents), done: make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

func (c *websocketCDP) Events() <-chan cdpEvent { return c.events }

func (c *websocketCDP) Call(ctx context.Context, sessionID, method string, params any, result any) error {
	if method == "" {
		return errors.New("CDP method is required")
	}
	ctx, cancel := context.WithTimeout(ctx, cdpCommandTimeout)
	defer cancel()
	id := c.nextID.Add(1)
	request := map[string]any{"id": id, "method": method}
	if sessionID != "" {
		request["sessionId"] = sessionID
	}
	if params != nil {
		request["params"] = params
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode CDP command: %w", err)
	}
	response := make(chan cdpResponse, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		err := c.closeErr
		c.mu.Unlock()
		return err
	default:
	}
	c.pending[id] = response
	c.mu.Unlock()

	err = c.conn.Write(ctx, websocket.MessageText, payload)
	if err != nil {
		c.removePending(id)
		return fmt.Errorf("write CDP command %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.closeErr
		c.mu.Unlock()
		return fmt.Errorf("CDP connection closed while awaiting %s: %w", method, err)
	case reply := <-response:
		if reply.Error != nil {
			return fmt.Errorf("CDP %s failed (%d): %s", method, reply.Error.Code, reply.Error.Message)
		}
		if result != nil && len(reply.Result) > 0 {
			if err := json.Unmarshal(reply.Result, result); err != nil {
				return fmt.Errorf("decode CDP %s result: %w", method, err)
			}
		}
		return nil
	}
}

func (c *websocketCDP) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *websocketCDP) readLoop() {
	defer close(c.events)
	defer c.shutdown(errors.New("CDP connection closed"))
	for {
		_, payload, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		var envelope struct {
			ID        int64           `json:"id"`
			Method    string          `json:"method"`
			SessionID string          `json:"sessionId"`
			Params    json.RawMessage `json:"params"`
			Result    json.RawMessage `json:"result"`
			Error     *cdpError       `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) != nil {
			continue
		}
		if envelope.ID > 0 {
			c.mu.Lock()
			waiting := c.pending[envelope.ID]
			delete(c.pending, envelope.ID)
			c.mu.Unlock()
			if waiting != nil {
				waiting <- cdpResponse{Result: envelope.Result, Error: envelope.Error}
			}
			continue
		}
		if envelope.Method == "" {
			continue
		}
		select {
		case c.events <- cdpEvent{Method: envelope.Method, SessionID: envelope.SessionID, Params: envelope.Params}:
		case <-c.done:
			return
		default:
			c.shutdown(errCDPEventOverflow)
			return
		}
	}
}

func (c *websocketCDP) shutdown(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.closeErr = err
		c.pending = make(map[int64]chan cdpResponse)
		close(c.done)
		c.mu.Unlock()
		_ = c.conn.CloseNow()
	})
}

func (c *websocketCDP) Close() error {
	c.shutdown(errors.New("CDP connection closed"))
	return nil
}
