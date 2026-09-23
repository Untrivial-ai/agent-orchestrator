package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	aomcp "github.com/aoagents/agent-orchestrator/backend/internal/mcp"
)

type fakeDaemon struct {
	mu       sync.Mutex
	requests []string
	handler  http.HandlerFunc
}

func (f *fakeDaemon) append(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		entry += "?" + r.URL.RawQuery
	}
	f.requests = append(f.requests, entry)
}

func (f *fakeDaemon) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

type httpAPI struct {
	base   string
	client *http.Client
}

func (a httpAPI) GetJSON(ctx context.Context, path string, out any) error {
	return a.do(ctx, http.MethodGet, path, nil, out)
}

func (a httpAPI) PostJSON(ctx context.Context, path string, body, out any) error {
	return a.do(ctx, http.MethodPost, path, body, out)
}

func (a httpAPI) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+"/api/v1/"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("daemon returned HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	return json.Unmarshal(payload, out)
}

func startFakeDaemon(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *fakeDaemon, aomcp.DaemonAPI) {
	t.Helper()
	fake := &fakeDaemon{handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.append(r)
		w.Header().Set("Content-Type", "application/json")
		fake.handler(w, r)
	}))
	t.Cleanup(srv.Close)
	api := httpAPI{base: srv.URL, client: srv.Client()}
	return srv, fake, api
}

func callTool(t *testing.T, api aomcp.DaemonAPI, name string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server := aomcp.NewServer(api, aomcp.Options{Version: "test"})
	t1, t2 := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		t.Fatalf("CallTool(%s) returned tool error: %s", name, b.String())
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMCPToolsAgainstDaemonAPI(t *testing.T) {
	_, fake, api := startFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"demo","name":"Demo","kind":"git","sessionPrefix":"demo"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			_, _ = io.WriteString(w, `{"sessions":[{
				"id":"demo-1","projectId":"demo","kind":"worker","harness":"claude-code",
				"displayName":"fix","status":"working","isTerminated":false,
				"activity":{"state":"working","lastActivityAt":"2026-09-18T12:00:00Z"},
				"createdAt":"2026-09-18T11:00:00Z","updatedAt":"2026-09-18T12:00:00Z",
				"branch":"ao/demo-1","prs":[{"url":"https://github.com/o/r/pull/9","number":9,"state":"open","ci":"pending","review":"none"}]
			}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1/pr":
			_, _ = io.WriteString(w, `{"prs":[{"number":9,"url":"https://github.com/o/r/pull/9","state":"open","ci":{"state":"pending"},"review":{"decision":"none","unresolvedThreadCount":0}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"project":{"id":"demo","name":"Demo","kind":"git","path":"/repo/demo","config":{"worker":{"agent":"claude-code"}}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/readiness/ensure":
			_, _ = io.WriteString(w, `{"agents":[{"id":"claude-code","installation":{"state":"installed"},"authentication":{"state":"authorized"}}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			_, _ = io.WriteString(w, `{"session":{"id":"demo-2","status":"starting","displayName":"new-task"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/send":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1/conversation":
			_, _ = io.WriteString(w, `{"sessionId":"demo-1","mode":"chat","controller":"ready","messages":[{"id":"m1","role":"assistant","origin":"provider","text":"hello from worker","sequence":1,"createdAt":"2026-09-18T12:00:00Z"}],"activities":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1":
			_, _ = io.WriteString(w, `{"session":{"id":"demo-1","projectId":"demo","kind":"worker","status":"working","isTerminated":false,"activity":{"state":"working","lastActivityAt":"2026-09-18T12:00:00Z"},"createdAt":"2026-09-18T11:00:00Z","updatedAt":"2026-09-18T12:00:00Z","branch":"ao/demo-1","prs":[{"url":"https://github.com/o/r/pull/9","number":9,"state":"open","ci":"pending","review":"none"}]}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/kill":
			_, _ = io.WriteString(w, `{"sessionId":"demo-1","freed":true}`)
		default:
			http.NotFound(w, r)
		}
	})

	if out := callTool(t, api, "list_projects", map[string]any{}); !strings.Contains(out, `"id": "demo"`) {
		t.Fatalf("list_projects output missing project: %s", out)
	}
	if out := callTool(t, api, "list_sessions", map[string]any{"project_id": "demo"}); !strings.Contains(out, `"id": "demo-1"`) || !strings.Contains(out, `"ci"`) {
		t.Fatalf("list_sessions output missing session/PR facts: %s", out)
	}
	if out := callTool(t, api, "spawn_worker", map[string]any{"name": "new-task", "project_id": "demo", "prompt": "do the thing"}); !strings.Contains(out, `"sessionId": "demo-2"`) {
		t.Fatalf("spawn_worker output missing session: %s", out)
	}
	if out := callTool(t, api, "send_message", map[string]any{"session_id": "demo-1", "message": "ping"}); !strings.Contains(out, `"ok": true`) {
		t.Fatalf("send_message output: %s", out)
	}
	if out := callTool(t, api, "read_session_output", map[string]any{"session_id": "demo-1", "limit": 10}); !strings.Contains(out, "hello from worker") {
		t.Fatalf("read_session_output output: %s", out)
	}
	if out := callTool(t, api, "get_pr_status", map[string]any{"session_id": "demo-1"}); !strings.Contains(out, `"number": 9`) {
		t.Fatalf("get_pr_status output: %s", out)
	}
	if out := callTool(t, api, "kill_session", map[string]any{"session_id": "demo-1"}); !strings.Contains(out, `"freed": true`) {
		t.Fatalf("kill_session output: %s", out)
	}

	got := fake.all()
	wantContains := []string{
		"GET /api/v1/projects",
		"GET /api/v1/sessions",
		"POST /api/v1/sessions",
		"POST /api/v1/sessions/demo-1/send",
		"GET /api/v1/sessions/demo-1/conversation",
		"GET /api/v1/sessions/demo-1/pr",
		"POST /api/v1/sessions/demo-1/kill",
	}
	joined := strings.Join(got, "\n")
	for _, want := range wantContains {
		if !strings.Contains(joined, want) {
			t.Fatalf("daemon requests missing %q:\n%s", want, joined)
		}
	}
}

func TestSpawnWorkerValidation(t *testing.T) {
	_, _, api := startFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := aomcp.NewServer(api, aomcp.Options{Version: "test"})
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "spawn_worker",
		Arguments: map[string]any{"name": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for missing project_id")
	}
}

func TestRequireLoopbackHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := aomcp.Run(ctx, httpAPI{}, aomcp.Options{ListenAddr: "0.0.0.0:9"})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback rejection, got %v", err)
	}
}

func TestStreamableHTTPServesTools(t *testing.T) {
	_, _, api := startFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects" {
			_, _ = io.WriteString(w, `{"projects":[{"id":"demo","name":"Demo","kind":"git","sessionPrefix":"demo"}]}`)
			return
		}
		http.NotFound(w, r)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- aomcp.Run(ctx, api, aomcp.Options{Version: "test", ListenAddr: addr})
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCP HTTP server did not become ready: %v", dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "http-client", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: "http://" + addr,
	}, nil)
	if err != nil {
		t.Fatalf("connect streamable HTTP: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_projects", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}
}
