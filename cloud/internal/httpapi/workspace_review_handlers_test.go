package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

type workspaceReviewHandlerStore struct {
	Store
	provider       string
	createdKind    string
	createdPayload json.RawMessage
	eventType      string
	errorCode      string
}

func (s *workspaceReviewHandlerStore) GetSession(_ context.Context, _ domain.Principal, _, _ string) (domain.Session, error) {
	return domain.Session{SandboxProvider: s.provider}, nil
}

func (s *workspaceReviewHandlerStore) ResumeSession(_ context.Context, _ domain.Principal, _, sessionID string) (domain.SandboxLifecycle, error) {
	return domain.SandboxLifecycle{SessionID: sessionID}, nil
}

func (s *workspaceReviewHandlerStore) CreateWorkspaceRequest(_ context.Context, _ domain.Principal, orgID, sessionID, kind string, payload json.RawMessage, _ time.Duration) (domain.WorkerRequest, error) {
	s.createdKind, s.createdPayload = kind, payload
	return domain.WorkerRequest{ID: "00000000-0000-4000-8000-000000000003", OrgID: orgID, SessionID: sessionID}, nil
}

func (s *workspaceReviewHandlerStore) GetWorkspaceRequest(_ context.Context, _ domain.Principal, _, _, _ string) (domain.WorkerRequest, error) {
	if s.errorCode != "" {
		return domain.WorkerRequest{Status: "failed", ErrorCode: s.errorCode, ErrorMessage: "stale"}, nil
	}
	response, _ := json.Marshal(map[string]any{})
	return domain.WorkerRequest{Status: "succeeded", Response: response}, nil
}

func (s *workspaceReviewHandlerStore) AppendSessionEvent(_ context.Context, _, _, eventType string, _ json.RawMessage) (domain.ClientEvent, error) {
	s.eventType = eventType
	return domain.ClientEvent{}, nil
}

func TestWorkspaceReviewHandlersDispatchForEveryProvider(t *testing.T) {
	operations := []struct {
		name    string
		method  string
		target  string
		body    any
		handler func(*Server, http.ResponseWriter, *http.Request)
		kind    string
	}{
		{"summary", http.MethodGet, "/workspace/review", nil, (*Server).getWorkspaceReview, "workspace.review.summary"},
		{"tree", http.MethodGet, "/workspace/tree?path=src", nil, (*Server).getWorkspaceReviewTree, "workspace.review.tree"},
		{"search", http.MethodGet, "/workspace/search?query=app&limit=20", nil, (*Server).getWorkspaceReviewSearch, "workspace.review.search"},
		{"file", http.MethodGet, "/workspace/review/file?path=README.md&scope=staged", nil, (*Server).getWorkspaceReviewFile, "workspace.review.file"},
		{"diffs", http.MethodPost, "/workspace/review/diffs", worker.WorkspaceReviewDiffsRequest{Scope: worker.WorkspaceReviewStaged, Paths: []string{"README.md"}, ContextLines: 3}, (*Server).postWorkspaceReviewDiffs, "workspace.review.diffs"},
		{"revision", http.MethodGet, "/workspace/review/revision?path=README.md&scope=combined&side=after&workspaceVersion=v1", nil, (*Server).getWorkspaceReviewRevision, "workspace.review.revision"},
		{"write", http.MethodPut, "/workspace/review/file", worker.WorkspaceReviewWriteRequest{Path: "README.md", Content: "new\n", ExpectedFileFingerprint: "fp"}, (*Server).putWorkspaceReviewFile, "workspace.review.write"},
	}
	for _, provider := range []string{sandbox.ProviderDocker, sandbox.ProviderNodeOps, sandbox.ProviderCoder} {
		for _, operation := range operations {
			t.Run(provider+"/"+operation.name, func(t *testing.T) {
				store := &workspaceReviewHandlerStore{provider: provider}
				server := &Server{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerRequestTimeout: time.Second}
				request := workspaceReviewRequest(t, operation.method, operation.target, operation.body)
				recorder := httptest.NewRecorder()
				operation.handler(server, recorder, request)
				if recorder.Code != http.StatusOK || store.createdKind != operation.kind {
					t.Fatalf("status=%d kind=%q body=%s", recorder.Code, store.createdKind, recorder.Body.String())
				}
				if operation.name == "write" && store.eventType != "workspace.changed" {
					t.Fatalf("write event = %q", store.eventType)
				}
			})
		}
	}
}

func TestWorkspaceReviewHandlerMapsStaleSnapshotToConflict(t *testing.T) {
	store := &workspaceReviewHandlerStore{provider: sandbox.ProviderDocker, errorCode: "WORKSPACE_SNAPSHOT_STALE"}
	server := &Server{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerRequestTimeout: time.Second}
	request := workspaceReviewRequest(t, http.MethodPost, "/workspace/review/diffs", worker.WorkspaceReviewDiffsRequest{
		Scope: worker.WorkspaceReviewCombined, Paths: []string{"README.md"}, WorkspaceVersion: "stale",
	})
	recorder := httptest.NewRecorder()
	server.postWorkspaceReviewDiffs(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func workspaceReviewRequest(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("orgId", workspaceTestOrgID)
	routeContext.URLParams.Add("sessionId", workspaceTestSessionID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	return request.WithContext(context.WithValue(request.Context(), principalKey, domain.Principal{UserID: "user-1"}))
}
