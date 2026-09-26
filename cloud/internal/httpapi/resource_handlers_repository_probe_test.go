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

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/go-chi/chi/v5"
)

type repositoryProbeFakeStore struct {
	Store
	created   int
	encrypted []byte
	nonce     []byte
}

func (s *repositoryProbeFakeStore) CreateProject(
	context.Context, domain.Principal, string, string, domain.CreateProject,
) (domain.Project, error) {
	s.created++
	return domain.Project{ID: "proj-1", DisplayName: "widgets"}, nil
}

func (s *repositoryProbeFakeStore) ListUserProviderConnections(context.Context, domain.Principal) ([]domain.UserProviderConnection, error) {
	return nil, nil
}
func (s *repositoryProbeFakeStore) UpsertUserProviderConnection(context.Context, domain.Principal, string, string, []byte, []byte, json.RawMessage) (domain.UserProviderConnection, error) {
	return domain.UserProviderConnection{}, nil
}
func (s *repositoryProbeFakeStore) DeleteUserProviderConnection(context.Context, domain.Principal, string, string) error {
	return nil
}
func (s *repositoryProbeFakeStore) UserProviderConnectionSecret(context.Context, domain.Principal, string, string) ([]byte, []byte, error) {
	return s.encrypted, s.nonce, nil
}

const repositoryProbeOrgID = "00000000-0000-0000-0000-0000000000aa"

type mockRoundTripper struct {
	handler func(req *http.Request) *http.Response
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.handler(req), nil
}

func newRepositoryProbeTestServer(t *testing.T, roundTripper http.RoundTripper) (*Server, *repositoryProbeFakeStore) {
	t.Helper()
	cipher, _ := secrets.New(make([]byte, 32))
	encrypted, nonce, _ := cipher.Encrypt([]byte("fake-token"), "user:00000000-0000-0000-0000-000000000001|github|default")
	store := &repositoryProbeFakeStore{encrypted: encrypted, nonce: nonce}

	probeClient := &http.Client{Transport: roundTripper}

	srv := New(Options{
		Store:                 store,
		RepositoryProbeClient: probeClient,
		SecretCipher:          cipher,
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return srv, store
}

func createProjectRequestFor(t *testing.T, repositoryURL string) *http.Request {
	t.Helper()
	body, err := json.Marshal(createProjectRequest{
		DisplayName: "widgets", RepositoryURL: repositoryURL, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/orgs/"+repositoryProbeOrgID+"/projects", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "test-key-1")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", repositoryProbeOrgID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: "00000000-0000-0000-0000-000000000001"})
	return req.WithContext(ctx)
}

func githubAPIMock(reachable bool) http.RoundTripper {
	return &mockRoundTripper{
		handler: func(req *http.Request) *http.Response {
			if reachable {
				body := `{"permissions":{"push":true}}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(body)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewBufferString(`{"message":"Not Found"}`)),
				Header:     make(http.Header),
			}
		},
	}
}

func TestCreateProjectAcceptsAReachableRepository(t *testing.T) {
	srv, store := newRepositoryProbeTestServer(t, githubAPIMock(true))

	w := httptest.NewRecorder()
	srv.createProject(w, createProjectRequestFor(t, "https://github.com/octo/widgets.git"))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if store.created != 1 {
		t.Fatalf("CreateProject called %d times, want 1", store.created)
	}
}

func TestCreateProjectRejectsAnUnreachableRepositoryBeforeCreating(t *testing.T) {
	srv, store := newRepositoryProbeTestServer(t, githubAPIMock(false))

	w := httptest.NewRecorder()
	srv.createProject(w, createProjectRequestFor(t, "https://github.com/octo/private-or-typo.git"))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	if store.created != 0 {
		t.Fatalf("CreateProject called %d times, want 0 — an unreachable repository must not be created", store.created)
	}
}

func TestProbeRepositoryAccessFindsPublicRepositoryOutsideTokenScope(t *testing.T) {
	requests := 0
	srv, _ := newRepositoryProbeTestServer(t, &mockRoundTripper{
		handler: func(req *http.Request) *http.Response {
			requests++
			if requests == 1 {
				if got := req.Header.Get("Authorization"); got != "Bearer token" {
					t.Fatalf("authenticated probe Authorization = %q, want Bearer token", got)
				}
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewBufferString(`{"message":"Not Found"}`)),
					Header:     make(http.Header),
				}
			}
			if got := req.Header.Get("Authorization"); got != "" {
				t.Fatalf("public probe Authorization = %q, want empty", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"permissions":{"push":false}}`)),
				Header:     make(http.Header),
			}
		},
	})

	reachable, writeAccess, err := srv.probeRepositoryAccess(
		context.Background(), "https://github.com/octo/widgets.git", "token",
	)
	if err != nil {
		t.Fatalf("probeRepositoryAccess: %v", err)
	}
	if !reachable {
		t.Fatal("probeRepositoryAccess reachable = false, want true")
	}
	if writeAccess {
		t.Fatal("probeRepositoryAccess writeAccess = true, want false")
	}
	if requests != 2 {
		t.Fatalf("probe requests = %d, want 2", requests)
	}
}

func TestCreateProjectRejectsNonGitHubURLs(t *testing.T) {
	srv, store := newRepositoryProbeTestServer(t, githubAPIMock(true)) // Even if API would work, it shouldn't be called

	w := httptest.NewRecorder()
	srv.createProject(w, createProjectRequestFor(t, "https://attacker.example.com/repo.git"))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	if store.created != 0 {
		t.Fatalf("CreateProject called %d times, want 0", store.created)
	}
}

func TestProbeRepositoryAccessFailsClosedOnConnectionError(t *testing.T) {
	mockErr := &mockRoundTripper{
		handler: func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(bytes.NewBufferString(`error`)),
			}
		},
	}
	srv, _ := newRepositoryProbeTestServer(t, mockErr)

	reachable, _, err := srv.probeRepositoryAccess(context.Background(), "https://github.com/octo/widgets.git", "token")
	if err == nil {
		t.Fatal("probeRepositoryAccess = nil error on a connection error, want err != nil")
	}
	if reachable {
		t.Fatal("probeRepositoryAccess = true on a connection error, want false (fail closed for security)")
	}
}
