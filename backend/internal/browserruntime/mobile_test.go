package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeMobileConn struct {
	reads  chan mobileResultMessage
	writes chan wireMessage
	once   sync.Once
	closed chan struct{}
}

func newFakeMobileConn() *fakeMobileConn {
	return &fakeMobileConn{reads: make(chan mobileResultMessage, 2), writes: make(chan wireMessage, 2), closed: make(chan struct{})}
}

func (f *fakeMobileConn) ReadJSON(ctx context.Context, value any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.closed:
		return errors.New("closed")
	case message := <-f.reads:
		*(value.(*mobileResultMessage)) = message
		return nil
	}
}
func (f *fakeMobileConn) WriteJSON(ctx context.Context, value any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.closed:
		return errors.New("closed")
	case f.writes <- value.(wireMessage):
		return nil
	}
}
func (f *fakeMobileConn) Close(string) error { f.once.Do(func() { close(f.closed) }); return nil }

func TestMobileHubRoutesAndCorrelatesSessionCommand(t *testing.T) {
	hub := NewMobileHub()
	conn := newFakeMobileConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- hub.Serve(ctx, "s1", "phone-1", conn) }()

	deadline := time.Now().Add(time.Second)
	for !hub.Status("s1").Connected && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !hub.Status("s1").Connected {
		t.Fatal("mobile target did not register")
	}

	resultDone := make(chan Result, 1)
	errDone := make(chan error, 1)
	go func() {
		result, err := hub.Execute(context.Background(), "s1", "snapshot", map[string]interface{}{"interactive": true})
		resultDone <- result
		errDone <- err
	}()
	command := <-conn.writes
	if command.SessionID != domain.SessionID("s1") || command.Action != "snapshot" || command.RequestID == "" {
		t.Fatalf("command = %#v", command)
	}
	payload, _ := json.Marshal(map[string]interface{}{"text": "button Save [ref=e1]"})
	conn.reads <- mobileResultMessage{Type: "result", RequestID: command.RequestID, OK: true, Result: payload}
	if err := <-errDone; err != nil {
		t.Fatal(err)
	}
	result := <-resultDone
	if result.RequestID != command.RequestID || result.Value.(map[string]interface{})["text"] == "" {
		t.Fatalf("result = %#v", result)
	}

	cancel()
	<-serveDone
	if hub.Status("s1").Connected {
		t.Fatal("mobile target remained connected after transport closed")
	}
}

func TestRouterAutoPrefersForegroundMobileTarget(t *testing.T) {
	desktop := New(nil)
	hub := NewMobileHub()
	router := NewRouter(desktop, hub)
	if _, transport := router.Status("s1", "auto"); transport != "electron-webcontents-debugger" {
		t.Fatalf("disconnected auto transport = %q", transport)
	}
	conn := newFakeMobileConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = hub.Serve(ctx, "s1", "phone-1", conn) }()
	deadline := time.Now().Add(time.Second)
	for !hub.Status("s1").Connected && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status, transport := router.Status("s1", "auto")
	if !status.Connected || transport != "mobile-webview" {
		t.Fatalf("auto status = %+v transport=%q", status, transport)
	}
}
