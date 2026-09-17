package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/go-chi/chi/v5"
)

const autolinkProjectID = "00000000-0000-0000-0000-0000000000d4"
const autolinkOrgID = "00000000-0000-0000-0000-0000000000a1"

// stubAutolinkStore embeds Store (nil) so it satisfies the interface while
// implementing only the two methods createSession reaches on the auto-link
// path. It deliberately does not implement providerConnectionStore, so the
// handler's coding-agent credential check is skipped.
type stubAutolinkStore struct {
	Store
	orchestratorID string
	orchProvider   string
	orchFound      bool
	orchErr        error
	captured       domain.CreateSession
	created        bool
}

func (s *stubAutolinkStore) ProjectActiveOrchestrator(
	_ context.Context, _, _ string,
) (string, string, bool, error) {
	return s.orchestratorID, s.orchProvider, s.orchFound, s.orchErr
}

func (s *stubAutolinkStore) CreateSession(
	_ context.Context, _ domain.Principal, _, _ string, _ int, input domain.CreateSession,
) (domain.Session, error) {
	s.created = true
	s.captured = input
	return domain.Session{ID: "00000000-0000-0000-0000-0000000000e5", Kind: input.Kind}, nil
}

func createSessionRequestHTTP(t *testing.T, kind, provider string) *http.Request {
	t.Helper()
	body := `{"projectId":"` + autolinkProjectID + `","kind":"` + kind +
		`","harness":"claude-code","displayName":"add-logger","prompt":"do the work","mode":"trusted"` +
		func() string {
			if provider == "" {
				return ""
			}
			return `,"provider":"` + provider + `"`
		}() + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "22222222-2222-2222-2222-222222222222")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", autolinkOrgID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: "00000000-0000-0000-0000-0000000000f6"})
	return req.WithContext(ctx)
}

// A worker created for a project that has an active orchestrator is auto-linked
// to it and inherits its provider, even when the client selected (or the
// control plane defaults to) a different provider. This is what makes the
// orchestrator see, drive, and receive reports from a UI-created worker.
func TestCreateSessionAutoLinksWorkerToProjectOrchestrator(t *testing.T) {
	t.Parallel()
	store := &stubAutolinkStore{
		orchestratorID: "00000000-0000-0000-0000-0000000000b2",
		orchProvider:   sandbox.ProviderCoder,
		orchFound:      true,
	}
	// Default provider nodeops, client asks for nodeops; the orchestrator runs
	// on coder, so the worker must come out coder and parented.
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)

	rec := httptest.NewRecorder()
	srv.createSession(rec, createSessionRequestHTTP(t, "worker", sandbox.ProviderNodeOps))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if !store.created {
		t.Fatal("CreateSession was not called")
	}
	if store.captured.ParentSessionID != store.orchestratorID {
		t.Fatalf("ParentSessionID = %q, want %q", store.captured.ParentSessionID, store.orchestratorID)
	}
	if store.captured.Provider != sandbox.ProviderCoder {
		t.Fatalf("worker provider = %q, want %q (inherited from orchestrator)", store.captured.Provider, sandbox.ProviderCoder)
	}
}

// With no active orchestrator in the project, the worker stays standalone: no
// parent, and it keeps the client-selected provider.
func TestCreateSessionLeavesWorkerStandaloneWithoutOrchestrator(t *testing.T) {
	t.Parallel()
	store := &stubAutolinkStore{orchFound: false}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderCoder)

	rec := httptest.NewRecorder()
	srv.createSession(rec, createSessionRequestHTTP(t, "worker", sandbox.ProviderNodeOps))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if store.captured.ParentSessionID != "" {
		t.Fatalf("ParentSessionID = %q, want empty (standalone)", store.captured.ParentSessionID)
	}
	if store.captured.Provider != sandbox.ProviderNodeOps {
		t.Fatalf("worker provider = %q, want %q (client selection)", store.captured.Provider, sandbox.ProviderNodeOps)
	}
}

// An orchestrator is never auto-linked to itself or another orchestrator: the
// project lookup must not run for kind=orchestrator.
func TestCreateSessionDoesNotAutoLinkOrchestrator(t *testing.T) {
	t.Parallel()
	// orchFound=true would link if the handler wrongly consulted it for an
	// orchestrator; assert it stays empty regardless.
	store := &stubAutolinkStore{
		orchestratorID: "00000000-0000-0000-0000-0000000000b2",
		orchProvider:   sandbox.ProviderCoder,
		orchFound:      true,
	}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)

	rec := httptest.NewRecorder()
	srv.createSession(rec, createSessionRequestHTTP(t, "orchestrator", sandbox.ProviderNodeOps))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if store.captured.ParentSessionID != "" {
		t.Fatalf("orchestrator ParentSessionID = %q, want empty", store.captured.ParentSessionID)
	}
	if store.captured.Provider != sandbox.ProviderNodeOps {
		t.Fatalf("orchestrator provider = %q, want %q", store.captured.Provider, sandbox.ProviderNodeOps)
	}
}
