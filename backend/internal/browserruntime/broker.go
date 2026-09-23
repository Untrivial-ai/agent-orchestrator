// Package browserruntime brokers browser commands between the loopback daemon
// and the Electron process that owns AO's per-session WebContentsView targets.
package browserruntime

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	// ProtocolVersion identifies the daemon-to-Electron browser bridge contract.
	ProtocolVersion = 2
	// RuntimeTokenStdinEnv tells an app-spawned daemon to read its token once
	// from the inherited private stdin pipe instead of an environment variable.
	RuntimeTokenStdinEnv = "AO_BROWSER_RUNTIME_TOKEN_STDIN" //nolint:gosec // Environment variable name, not a credential.
	// RuntimeAddressEnv carries the exact listener address into running.json so
	// Electron never has to duplicate the backend's platform-specific naming.
	RuntimeAddressEnv    = "AO_BROWSER_RUNTIME_ADDRESS"
	helloTimeout         = 5 * time.Second
	maxRuntimeFrameBytes = 8 << 20
)

// ReadRuntimeToken reads the one-line token handoff used by the desktop app.
// The token is never placed in a file, command line, or daemon environment, and
// the daemon does not pass the consumed stdin handle to worker processes.
func ReadRuntimeToken(r io.Reader) (string, error) {
	const maxTokenBytes = 256
	line, err := bufio.NewReader(io.LimitReader(r, maxTokenBytes+1)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read browser runtime token: %w", err)
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", errors.New("browser runtime token handoff was empty")
	}
	if len(token) > maxTokenBytes {
		return "", errors.New("browser runtime token handoff was too long")
	}
	return token, nil
}

// ErrUnavailable indicates that no Electron browser runtime can accept a command.
var ErrUnavailable = errors.New("browser runtime is unavailable")

// Status describes whether Electron is connected to the browser command broker.
type Status struct {
	Connected   bool
	ConnectedAt time.Time
}

// Command is one session-scoped operation sent to the Electron browser runtime.
type Command struct {
	RequestID string                 `json:"requestId"`
	SessionID domain.SessionID       `json:"sessionId"`
	Action    string                 `json:"action"`
	Args      map[string]interface{} `json:"args,omitempty"`
}

// CommandError is a stable browser failure returned by the Electron runtime.
type CommandError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e CommandError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s (%s)", e.Message, e.Code)
}

// Result contains the correlated response to a browser command.
type Result struct {
	RequestID string
	Value     interface{}
}

type wireMessage struct {
	Type      string                 `json:"type"`
	Version   int                    `json:"version,omitempty"`
	Token     string                 `json:"token,omitempty"`
	RequestID string                 `json:"requestId,omitempty"`
	SessionID domain.SessionID       `json:"sessionId,omitempty"`
	Action    string                 `json:"action,omitempty"`
	Args      map[string]interface{} `json:"args,omitempty"`
	OK        bool                   `json:"ok,omitempty"`
	Result    json.RawMessage        `json:"result,omitempty"`
	Error     *CommandError          `json:"error,omitempty"`
}

type pendingResult struct {
	value interface{}
	err   error
}

// Broker owns the single active Electron runtime connection. Commands are
// correlated by request id, so independent AO sessions may use the bridge
// concurrently without sharing browser targets or results.
type Broker struct {
	log *slog.Logger

	mu          sync.Mutex
	conn        net.Conn
	connectedAt time.Time
	pending     map[string]chan pendingResult
	writeGate   chan struct{}
	token       string
}

// New creates an empty browser command broker.
func New(log *slog.Logger, token ...string) *Broker {
	if log == nil {
		log = slog.Default()
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	runtimeToken := ""
	if len(token) > 0 {
		runtimeToken = token[0]
	}
	return &Broker{log: log, pending: make(map[string]chan pendingResult), writeGate: gate, token: runtimeToken}
}

// NewToken returns a per-daemon-launch secret used to authenticate the desktop
// browser runtime before it can replace the active bridge connection.
func NewToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate browser runtime token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Status returns the current Electron runtime connection state.
func (b *Broker) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Status{Connected: b.conn != nil, ConnectedAt: b.connectedAt}
}

// Execute sends one browser command to the connected Electron runtime.
func (b *Broker) Execute(ctx context.Context, sessionID domain.SessionID, action string, args map[string]interface{}) (Result, error) {
	requestID := uuid.NewString()
	resultCh := make(chan pendingResult, 1)

	b.mu.Lock()
	conn := b.conn
	if conn == nil {
		b.mu.Unlock()
		return Result{}, ErrUnavailable
	}
	b.pending[requestID] = resultCh
	b.mu.Unlock()

	msg := wireMessage{
		Type:      "command",
		RequestID: requestID,
		SessionID: sessionID,
		Action:    action,
		Args:      args,
	}
	if err := b.write(ctx, conn, msg); err != nil {
		b.removePending(requestID)
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		b.disconnect(conn, fmt.Errorf("write browser command: %w", err))
		return Result{}, ErrUnavailable
	}

	select {
	case <-ctx.Done():
		b.removePending(requestID)
		cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = b.write(cancelCtx, conn, wireMessage{Type: "cancel", RequestID: requestID})
		cancel()
		return Result{}, ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			return Result{}, result.err
		}
		return Result{RequestID: requestID, Value: result.value}, nil
	}
}

// DestroySession implements sessionmanager.BrowserLifecycle. It bypasses the
// public browser action allowlist but still uses the authenticated runtime
// transport, so session teardown does not depend on a mounted renderer panel.
func (b *Broker) DestroySession(ctx context.Context, sessionID domain.SessionID) error {
	_, err := b.Execute(ctx, sessionID, "__destroy-session", nil)
	if errors.Is(err, ErrUnavailable) {
		return nil
	}
	return err
}

// Serve accepts Electron runtime connections until ctx is cancelled. A new
// valid runtime replaces an older connection and fails its in-flight commands;
// this makes renderer reload/restart recovery deterministic.
func (b *Broker) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil //nolint:nilerr // listener closure is the expected shutdown path
			}
			return fmt.Errorf("accept browser runtime: %w", err)
		}
		go b.serveConn(ctx, conn)
	}
}

func (b *Broker) serveConn(ctx context.Context, conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), maxRuntimeFrameBytes)
	var hello wireMessage
	if !scanner.Scan() ||
		json.Unmarshal(scanner.Bytes(), &hello) != nil ||
		hello.Type != "hello" ||
		hello.Version != ProtocolVersion ||
		!validRuntimeToken(b.token, hello.Token) {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	b.mu.Lock()
	old := b.conn
	b.conn = conn
	b.connectedAt = time.Now().UTC()
	pending := b.takePendingLocked()
	b.mu.Unlock()
	if old != nil && old != conn {
		_ = old.Close()
	}
	failPending(pending, ErrUnavailable)
	b.log.Info("browser runtime connected")

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for scanner.Scan() {
		var msg wireMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Type != "result" || msg.RequestID == "" {
			continue
		}
		b.resolve(msg)
	}
	b.disconnect(conn, scanner.Err())
}

func (b *Broker) write(ctx context.Context, conn net.Conn, msg wireMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.writeGate:
	}
	defer func() { b.writeGate <- struct{}{} }()

	deadline := time.Now().Add(30 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	interruptDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetWriteDeadline(time.Now())
		close(interruptDone)
	})
	frame, err := json.Marshal(msg)
	if err == nil && len(frame)+1 > maxRuntimeFrameBytes {
		err = fmt.Errorf("browser runtime frame exceeds %d bytes", maxRuntimeFrameBytes)
	}
	if err == nil {
		frame = append(frame, '\n')
		for len(frame) > 0 && err == nil {
			var written int
			written, err = conn.Write(frame)
			if written == 0 && err == nil {
				err = io.ErrShortWrite
				break
			}
			frame = frame[written:]
		}
	}
	writeComplete := err == nil && len(frame) == 0
	if !stop() {
		<-interruptDone
	}
	_ = conn.SetWriteDeadline(time.Time{})
	// Once the complete frame reached the runtime, let Execute observe a
	// simultaneous cancellation and send the matching cancel frame. Returning
	// ctx.Err here would make the delivered command impossible to cancel —
	// Execute would bail out of the error path without writing the cancel
	// frame, hanging any test/client waiting to read it.
	if writeComplete {
		return nil
	}
	// The write failed. Surface a context error when applicable rather than
	// a raw i/o timeout. The write deadline (derived from the context
	// deadline) and the context's own timer are independent — the write
	// deadline can fire microseconds before the context timer, so ctx.Err()
	// may still be nil when we reach here.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if requested, ok := ctx.Deadline(); ok && !time.Now().Before(requested) {
		return context.DeadlineExceeded
	}
	return err
}

func validRuntimeToken(expected, actual string) bool {
	if expected == "" {
		return actual == ""
	}
	return hmac.Equal([]byte(expected), []byte(actual))
}

func (b *Broker) resolve(msg wireMessage) {
	b.mu.Lock()
	ch := b.pending[msg.RequestID]
	delete(b.pending, msg.RequestID)
	b.mu.Unlock()
	if ch == nil {
		return
	}
	if !msg.OK {
		if msg.Error == nil {
			msg.Error = &CommandError{Code: "BROWSER_COMMAND_FAILED", Message: "Browser command failed"}
		}
		ch <- pendingResult{err: *msg.Error}
		return
	}
	var value interface{} = map[string]interface{}{}
	if len(msg.Result) > 0 && string(msg.Result) != "null" {
		if err := json.Unmarshal(msg.Result, &value); err != nil {
			ch <- pendingResult{err: fmt.Errorf("decode browser result: %w", err)}
			return
		}
	}
	ch <- pendingResult{value: value}
}

func (b *Broker) disconnect(conn net.Conn, cause error) {
	b.mu.Lock()
	if b.conn != conn {
		b.mu.Unlock()
		return
	}
	b.conn = nil
	b.connectedAt = time.Time{}
	pending := b.takePendingLocked()
	b.mu.Unlock()
	_ = conn.Close()
	failPending(pending, ErrUnavailable)
	if cause != nil && !errors.Is(cause, io.EOF) && !errors.Is(cause, net.ErrClosed) {
		b.log.Warn("browser runtime disconnected", "err", cause)
	} else {
		b.log.Info("browser runtime disconnected")
	}
}

func (b *Broker) removePending(requestID string) {
	b.mu.Lock()
	delete(b.pending, requestID)
	b.mu.Unlock()
}

func (b *Broker) takePendingLocked() map[string]chan pendingResult {
	pending := b.pending
	b.pending = make(map[string]chan pendingResult)
	return pending
}

func failPending(pending map[string]chan pendingResult, err error) {
	for _, ch := range pending {
		ch <- pendingResult{err: err}
	}
}
