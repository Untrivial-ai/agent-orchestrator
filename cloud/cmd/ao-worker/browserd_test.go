package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/vmbrowser"
)

type workerFakeEngine struct {
	mu      sync.Mutex
	closed  int
	execute func(action string, args map[string]any) (map[string]any, error)
}

func (f *workerFakeEngine) Execute(_ context.Context, action string, args map[string]any) (map[string]any, error) {
	if f.execute != nil {
		return f.execute(action, args)
	}
	return map[string]any{"untrustedExternalContent": true}, nil
}

func (f *workerFakeEngine) Screenshot(context.Context) (string, int, int, error) {
	return "cG5n", 10, 20, nil
}

func (f *workerFakeEngine) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *workerFakeEngine) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func TestStartBrowserdInjectsEnvAndServesCommands(t *testing.T) {
	engine := &workerFakeEngine{}
	env, stop, err := startBrowserd(context.Background(), BrowserdOptions{
		DataDir:     t.TempDir(),
		SessionID:   "sess-1",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewEngine:   func(_, _ string) vmbrowser.EngineLike { return engine },
		IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("startBrowserd: %v", err)
	}
	defer func() { _ = stop(context.Background()) }()

	if env["AO_BROWSER_CAPABILITY"] == "" {
		t.Fatal("capability must be injected into the agent env")
	}
	if !strings.HasPrefix(env["AO_BROWSER_API_URL"], "http://127.0.0.1:") {
		t.Fatalf("AO_BROWSER_API_URL = %q", env["AO_BROWSER_API_URL"])
	}

	body := fmt.Sprintf(`{"sessionId":"sess-1","action":"console"}`)
	request, err := http.NewRequest(http.MethodPost, env["AO_BROWSER_API_URL"]+browsercontract.RouteCommands, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(browsercontract.CapabilityHeader, env["AO_BROWSER_CAPABILITY"])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var decoded struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil || decoded.Action != "console" {
		t.Fatalf("decoded = %+v err = %v", decoded, err)
	}
}

func TestStartBrowserdStopsCleanly(t *testing.T) {
	engine := &workerFakeEngine{}
	env, stop, err := startBrowserd(context.Background(), BrowserdOptions{
		DataDir:     t.TempDir(),
		SessionID:   "sess-1",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewEngine:   func(_, _ string) vmbrowser.EngineLike { return engine },
		IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("startBrowserd: %v", err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if engine.closeCount() == 0 {
		t.Fatal("stop must close the engine (stopping chromium)")
	}
	// The listener must be gone.
	request, err := http.NewRequest(http.MethodGet, env["AO_BROWSER_API_URL"]+browsercontract.RouteStatus, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := http.DefaultClient.Do(request); err == nil {
		t.Fatal("browserd listener still serving after stop")
	}
}

func TestStartBrowserdIdleShutdownClosesEngine(t *testing.T) {
	engine := &workerFakeEngine{}
	_, stop, err := startBrowserd(context.Background(), BrowserdOptions{
		DataDir:     t.TempDir(),
		SessionID:   "sess-1",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewEngine:   func(_, _ string) vmbrowser.EngineLike { return engine },
		IdleTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("startBrowserd: %v", err)
	}
	defer func() { _ = stop(context.Background()) }()
	deadline := time.Now().Add(3 * time.Second)
	for engine.closeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if engine.closeCount() == 0 {
		t.Fatal("idle shutdown never closed the engine")
	}
}

func TestStartBrowserdRequiresSessionAndDataDir(t *testing.T) {
	if _, _, err := startBrowserd(context.Background(), BrowserdOptions{}); err == nil {
		t.Fatal("empty options must fail")
	}
	if _, _, err := startBrowserd(context.Background(), BrowserdOptions{DataDir: "x"}); err == nil {
		t.Fatal("missing session id must fail")
	}
}
