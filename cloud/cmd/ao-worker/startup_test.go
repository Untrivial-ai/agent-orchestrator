package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workertransport"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func workerResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestStartInteractiveAgentStopsOnWorkspaceFailure(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("checkout failed")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	client := &client{baseURL: "http://worker.test", http: httpClient}
	workspacePrepared := make(chan workspacePreparationResult, 1)
	workspacePrepared <- workspacePreparationResult{Err: wantErr}
	supervisor := &workertransport.Supervisor{}

	err := startInteractiveAgent(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		client,
		worker.BootstrapResponse{},
		t.TempDir(),
		t.TempDir(),
		"",
		"",
		"",
		supervisor,
		workspacePrepared,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("startup error = %v, want workspace failure", err)
	}
	if supervisor.AgentTerminalID != "" {
		t.Fatalf("agent terminal %q remained reserved after workspace failure", supervisor.AgentTerminalID)
	}
}

func TestAgentDependenciesPrepareBeforeWorkspace(t *testing.T) {
	t.Parallel()
	credentialStarted := make(chan struct{})
	terminalStarted := make(chan struct{})
	var credentialOnce, terminalOnce sync.Once
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/worker/credential":
			credentialOnce.Do(func() { close(credentialStarted) })
			return workerResponse(http.StatusOK, `{"provider":"test","credentialType":"token","secret":"secret"}`), nil
		case "/worker/terminals/agent":
			terminalOnce.Do(func() { close(terminalStarted) })
			return workerResponse(http.StatusOK, `{"terminalId":"agent-1"}`), nil
		default:
			return workerResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	client := &client{baseURL: "http://worker.test", http: httpClient}
	workspacePrepared := make(chan workspacePreparationResult)
	supervisor := &workertransport.Supervisor{}
	workspace := t.TempDir()
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- startInteractiveAgent(
			ctx,
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			client,
			worker.BootstrapResponse{},
			workspace,
			dataDir,
			"",
			"",
			"",
			supervisor,
			workspacePrepared,
		)
	}()

	for name, started := range map[string]<-chan struct{}{
		"credential": credentialStarted,
		"terminal":   terminalStarted,
	} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("%s preparation did not start while workspace was pending", name)
		}
	}
	select {
	case err := <-done:
		t.Fatalf("startup returned before workspace result: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("startup cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup did not stop after cancellation")
	}
	if supervisor.AgentTerminalID != "" {
		t.Fatalf("agent terminal %q remained reserved after cancellation", supervisor.AgentTerminalID)
	}
}

func TestStartInteractiveAgentPublishesCredentialStartupFailure(t *testing.T) {
	t.Parallel()
	var published worker.EventRequest
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/worker/credential":
			return workerResponse(http.StatusInternalServerError, `{"error":"unavailable"}`), nil
		case "/worker/terminals/agent":
			return workerResponse(http.StatusOK, `{"terminalId":"agent-1"}`), nil
		case "/worker/events":
			if err := json.NewDecoder(request.Body).Decode(&published); err != nil {
				t.Fatalf("decode published event: %v", err)
			}
			return workerResponse(http.StatusAccepted, `{}`), nil
		default:
			return workerResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	client := &client{baseURL: "http://worker.test", http: httpClient}
	workspacePrepared := make(chan workspacePreparationResult, 1)
	workspacePrepared <- workspacePreparationResult{}
	supervisor := &workertransport.Supervisor{}
	bootstrap := worker.BootstrapResponse{WorkerID: "worker-1", Epoch: 4}

	err := startInteractiveAgent(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		client,
		bootstrap,
		t.TempDir(),
		t.TempDir(),
		"",
		"",
		"",
		supervisor,
		workspacePrepared,
	)
	if err == nil || !strings.Contains(err.Error(), "load coding-agent credential") {
		t.Fatalf("startup error = %v, want credential failure", err)
	}
	if published.Type != "startup.failed" {
		t.Fatalf("event type = %q, want startup.failed", published.Type)
	}
	payload, err := json.Marshal(published.Payload)
	if err != nil {
		t.Fatalf("marshal event payload: %v", err)
	}
	var failure worker.StartupFailureEvent
	if err := json.Unmarshal(payload, &failure); err != nil {
		t.Fatalf("decode failure payload: %v", err)
	}
	if failure.WorkerID != "worker-1" || failure.Epoch != 4 ||
		failure.Phase != "starting_agent" || failure.Code != "CREDENTIAL_FAILED" {
		t.Fatalf("failure payload = %+v", failure)
	}
}

func TestPrepareWorkspaceRejectsCheckoutDenial(t *testing.T) {
	t.Setenv("AO_CLOUD_ALLOW_ANONYMOUS_GITHUB_CHECKOUT", "false")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/worker/checkout-grant" {
			t.Fatalf("request path = %q, want checkout grant", request.URL.Path)
		}
		return workerResponse(
			http.StatusForbidden,
			`{"code":"CHECKOUT_NOT_AUTHORIZED","message":"repository grant denied"}`,
		), nil
	})}
	client := &client{baseURL: "http://worker.test", http: httpClient}
	bootstrap := worker.BootstrapResponse{
		SessionID: "session-1",
		Launch: worker.LaunchContext{
			RepositoryURL: "https://github.com/example/private",
		},
	}

	err := prepareWorkspace(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		client,
		bootstrap,
		t.TempDir(),
		t.TempDir(),
		"http://worker.test",
	)
	if !errors.Is(err, errCheckoutForbidden) {
		t.Fatalf("workspace error = %v, want checkout denial", err)
	}
}

func TestRehydrateSessionReturnsFailure(t *testing.T) {
	t.Parallel()
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return workerResponse(http.StatusInternalServerError, `{"error":"restore unavailable"}`), nil
	})}
	client := &client{baseURL: "http://worker.test", http: httpClient}
	_, err := rehydrateSession(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		client,
		worker.BootstrapResponse{},
		t.TempDir(),
		t.TempDir(),
	)
	if err == nil || !strings.Contains(err.Error(), "fetch captured checkpoint") {
		t.Fatalf("rehydrate error = %v, want fetch failure", err)
	}
}
