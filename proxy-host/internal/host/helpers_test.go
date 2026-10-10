package host

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

var control, inference = strings.Repeat("c", 32), strings.Repeat("i", 32)

const testModel = "probe-model"

// fakeProvider stands in for a provider: it records which account ran each
// model request and answers the calls made as an account.
type fakeProvider struct {
	name    string
	failure int           // every model request is refused with this status
	started chan string   // when set, a model request announces its account and waits
	release chan struct{} // closed to let waiting model requests finish
	answer  func(r *http.Request, body string) (int, string)
	refuse  error // what a sign-in renewal fails with

	mu        sync.Mutex
	calls     []string
	requests  []*http.Request
	refreshes int
}

func (e *fakeProvider) Identifier() string { return e.name }
func (e *fakeProvider) ran() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}
func (e *fakeProvider) admit(ctx context.Context, a *coreauth.Auth, kind string) error {
	e.mu.Lock()
	e.calls = append(e.calls, a.ID+" "+kind)
	e.mu.Unlock()
	if e.failure != 0 {
		return &coreauth.Error{Code: "refused", Message: "fake provider refusal", HTTPStatus: e.failure}
	}
	if e.started != nil {
		select {
		case e.started <- a.ID:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (e *fakeProvider) wait(ctx context.Context) error {
	if e.release == nil {
		return nil
	}
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *fakeProvider) Execute(ctx context.Context, a *coreauth.Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	if err := e.admit(ctx, a, "plain"); err != nil {
		return executor.Response{}, err
	}
	if err := e.wait(ctx); err != nil {
		return executor.Response{}, err
	}
	if e.name == "claude" {
		return executor.Response{Payload: []byte(fmt.Sprintf(`{"id":%q,"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, a.ID))}, nil
	}
	return executor.Response{Payload: []byte(fmt.Sprintf(`{"id":%q,"object":"response","output":[]}`, a.ID))}, nil
}
func (e *fakeProvider) ExecuteStream(ctx context.Context, a *coreauth.Auth, _ executor.Request, _ executor.Options) (*executor.StreamResult, error) {
	if err := e.admit(ctx, a, "stream"); err != nil {
		return nil, err
	}
	first := `data: {"type":"response.created","response":{"id":"pending","object":"response","status":"in_progress","output":[]}}` + "\n\n"
	last := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n", a.ID)
	if e.name == "claude" {
		first = fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":%q,\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n", a.ID)
		last = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	chunks := make(chan executor.StreamChunk, 2)
	chunks <- executor.StreamChunk{Payload: []byte(first)}
	go func() {
		defer close(chunks)
		if e.wait(ctx) == nil {
			chunks <- executor.StreamChunk{Payload: []byte(last)}
		}
	}()
	return &executor.StreamResult{Chunks: chunks}, nil
}
func (e *fakeProvider) CountTokens(ctx context.Context, a *coreauth.Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	if err := e.admit(ctx, a, "count"); err != nil {
		return executor.Response{}, err
	}
	return executor.Response{Payload: []byte(`{"input_tokens":3}`)}, nil
}
func (e *fakeProvider) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refreshes++
	if e.refuse != nil {
		return nil, e.refuse
	}
	return a, nil
}
func (*fakeProvider) PrepareRequest(*http.Request, *coreauth.Auth) error { return nil }
func (e *fakeProvider) HttpRequest(_ context.Context, _ *coreauth.Auth, r *http.Request) (*http.Response, error) {
	body := ""
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
	}
	e.mu.Lock()
	e.requests = append(e.requests, r)
	e.mu.Unlock()
	if e.answer == nil {
		return nil, fmt.Errorf("provider is out of reach")
	}
	status, answer := e.answer(r, body)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(answer))}, nil
}

// fixture is the boundary in front of the SDK's real inference handlers and a
// manager whose accounts the test chooses.
type fixture struct {
	t       *testing.T
	engine  *gin.Engine
	routes  *Routes
	manager *coreauth.Manager
	logins  *Logins
	counted *Activity
	apiKey  bool // sessions present their ticket as X-Api-Key, as Claude Code does
}

func newFixture(t *testing.T, providers ...*fakeProvider) *fixture {
	t.Helper()
	f, base := bareFixture(t, providers...)
	codex, anthropic := openai.NewOpenAIResponsesAPIHandler(base), claude.NewClaudeCodeAPIHandler(base)
	f.engine.POST("/v1/responses", codex.Responses)
	f.engine.POST("/v1/responses/compact", codex.Compact)
	f.engine.POST("/v1/messages", anthropic.ClaudeMessages)
	f.engine.POST("/v1/messages/count_tokens", anthropic.ClaudeCountTokens)
	return f
}

// bareFixture is the boundary and its own /ao routes with nothing behind them.
func bareFixture(t *testing.T, providers ...*fakeProvider) (*fixture, *handlers.BaseAPIHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, exactSelector{}, nil)
	manager.SetConfig(&config.Config{})
	manager.SetRetryConfig(2, time.Millisecond, 2)
	for _, provider := range providers {
		manager.RegisterExecutor(provider)
	}
	f := &fixture{t: t, engine: gin.New(), manager: manager, logins: newLogins(t.TempDir())}
	f.routes = OpenRoutes(filepath.Join(t.TempDir(), "run", "routes.json"))
	f.counted = OpenActivity(filepath.Join(t.TempDir(), "run", "activity.json"))
	boundary := Boundary{Routes: f.routes, ControlKey: control, InferenceKey: inference, Logins: f.logins, Activity: f.counted}
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
	f.engine.Use(boundary.Middleware)
	boundary.Configure(f.engine, base, &config.Config{})
	return f, base
}

// account adds a signed-in account that can run the test model.
func (f *fixture) account(auth *coreauth.Auth) {
	f.t.Helper()
	if auth.Status == "" {
		auth.Status = coreauth.StatusActive
	}
	if _, err := f.manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
		f.t.Fatal(err)
	}
	cliproxy.GlobalModelRegistry().RegisterClient(auth.ID, auth.Provider, []*cliproxy.ModelInfo{{ID: testModel}})
	f.t.Cleanup(func() { cliproxy.GlobalModelRegistry().UnregisterClient(auth.ID) })
}

// route builds the table from "ticket provider account" triples; every account
// they name is signed in.
func (f *fixture) route(triples ...string) {
	f.t.Helper()
	routes, ids := []Route{}, []string{}
	for _, triple := range triples {
		part := strings.Split(triple, " ")
		routes = append(routes, Route{TicketHash: TicketHash(part[0]), Provider: part[1], AuthID: part[2]})
		ids = append(ids, part[2])
	}
	if err := f.routes.Apply(routes, ids); err != nil {
		f.t.Fatal(err)
	}
}

// send makes one request; headers are "Name: value" pairs.
func (f *fixture) send(method, path, token, body string, headers ...string) *httptest.ResponseRecorder {
	return send(f.engine, method, path, token, body, headers...)
}
func send(engine http.Handler, method, path, token, body string, headers ...string) *httptest.ResponseRecorder {
	return sendContext(context.Background(), engine, method, path, token, body, headers...)
}
func sendContext(ctx context.Context, engine http.Handler, method, path, token, body string, headers ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for _, header := range headers {
		name, value, _ := strings.Cut(header, ": ")
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

// model sends a model request as a session, with selection headers a session
// must not be able to set.
func (f *fixture) model(path, ticket string, stream bool) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"model":%q,"input":"hi","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, testModel, stream)
	if f.apiKey {
		return f.send(http.MethodPost, path, "", body, "X-Api-Key: "+ticket, accountHeader+": other-account", providerHeader+": other-provider")
	}
	return f.send(http.MethodPost, path, ticket, body, accountHeader+": other-account", providerHeader+": other-provider")
}
