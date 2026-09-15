package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

const (
	workspaceTestOrgID     = "00000000-0000-4000-8000-000000000001"
	workspaceTestSessionID = "00000000-0000-4000-8000-000000000002"
)

type workspaceHandlerStore struct {
	Store
	provider       string
	createdKind    string
	createdPayload json.RawMessage
	created        bool
}

func (s *workspaceHandlerStore) GetSession(_ context.Context, _ domain.Principal, _, _ string) (domain.Session, error) {
	return domain.Session{SandboxProvider: s.provider}, nil
}

func (s *workspaceHandlerStore) ResumeSession(_ context.Context, _ domain.Principal, _, sessionID string) (domain.SandboxLifecycle, error) {
	return domain.SandboxLifecycle{SessionID: sessionID}, nil
}

func (s *workspaceHandlerStore) CreateWorkspaceRequest(_ context.Context, _ domain.Principal, orgID, sessionID, kind string, payload json.RawMessage, _ time.Duration) (domain.WorkerRequest, error) {
	s.created, s.createdKind, s.createdPayload = true, kind, payload
	return domain.WorkerRequest{ID: "00000000-0000-4000-8000-000000000003", OrgID: orgID, SessionID: sessionID}, nil
}

func (s *workspaceHandlerStore) GetWorkspaceRequest(_ context.Context, _ domain.Principal, _, _, _ string) (domain.WorkerRequest, error) {
	response, _ := json.Marshal(worker.WorkspaceDiffFile{
		Path: "notes.txt", Status: "untracked", Content: "hello\n",
		Diff: "diff --git a/notes.txt b/notes.txt\n", DiffTruncated: false,
	})
	return domain.WorkerRequest{Status: "succeeded", Response: response}, nil
}

func TestWorkspaceDiffDispatchesForSupportedProviders(t *testing.T) {
	providers := []string{sandbox.ProviderDocker, sandbox.ProviderNodeOps, sandbox.ProviderCoder}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			store := &workspaceHandlerStore{provider: provider}
			server := workspaceHandlerServer(store)
			recorder := httptest.NewRecorder()
			server.readWorkspaceDiffFile(recorder, workspaceHandlerRequest(t, "notes.txt", "unpushed"))
			if recorder.Code != http.StatusOK {
				t.Fatalf("diff-file status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
			if !store.created || store.createdKind != "workspace.diff-file" {
				t.Fatalf("created request = %t %q", store.created, store.createdKind)
			}
			var payload worker.WorkspaceDiffFileRequest
			if err := json.Unmarshal(store.createdPayload, &payload); err != nil || payload.Path != "notes.txt" || payload.Category != "unpushed" {
				t.Fatalf("transport payload = %#v, err=%v", payload, err)
			}

			store.created = false
			recorder = httptest.NewRecorder()
			server.getWorkspaceDiff(recorder, workspaceHandlerRequest(t, "", ""))
			if recorder.Code != http.StatusOK {
				t.Fatalf("diff status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
			if !store.created || store.createdKind != "workspace.diff" {
				t.Fatalf("created request = %t %q", store.created, store.createdKind)
			}
		})
	}
}

func TestWorkspaceDiffRejectsUnknownProviderWithoutDispatch(t *testing.T) {
	store := &workspaceHandlerStore{provider: "unknown"}
	server := workspaceHandlerServer(store)
	recorder := httptest.NewRecorder()
	server.readWorkspaceDiffFile(recorder, workspaceHandlerRequest(t, "notes.txt", ""))
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("diff-file status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if store.created {
		t.Fatal("unknown provider dispatched a diff-file request")
	}
}

func TestReadWorkspaceDiffFileRejectsUnknownCategoryWithoutDispatch(t *testing.T) {
	store := &workspaceHandlerStore{provider: sandbox.ProviderDocker}
	server := workspaceHandlerServer(store)
	recorder := httptest.NewRecorder()
	server.readWorkspaceDiffFile(recorder, workspaceHandlerRequest(t, "notes.txt", "unexpected"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("diff-file status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if store.created {
		t.Fatal("invalid category dispatched a diff-file request")
	}
}

func workspaceHandlerServer(store Store) *Server {
	return &Server{
		store:                store,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		workerRequestTimeout: time.Second,
	}
}

func workspaceHandlerRequest(t *testing.T, path, category string) *http.Request {
	t.Helper()
	query := url.Values{"path": []string{path}}
	if category != "" {
		query.Set("category", category)
	}
	request := httptest.NewRequest(http.MethodGet, "/workspace/file/diff?"+query.Encode(), nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("orgId", workspaceTestOrgID)
	routeContext.URLParams.Add("sessionId", workspaceTestSessionID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	return request.WithContext(context.WithValue(request.Context(), principalKey, domain.Principal{UserID: "user-1"}))
}
