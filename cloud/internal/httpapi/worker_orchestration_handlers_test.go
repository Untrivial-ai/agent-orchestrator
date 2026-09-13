package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

// parentSessionID is a valid UUID because validSessionInput requires the
// child's projectId (which the handler sets to the orchestrator's session id)
// to be a UUID.
const (
	parentSessionID = "00000000-0000-0000-0000-0000000000b2"
	childOrgID      = "00000000-0000-0000-0000-0000000000a1"
)

// stubChildStore embeds the Store interface (a nil value) so it satisfies the
// full interface at compile time while implementing only the methods the child
// spawn path calls. Any other method would panic if reached, which keeps the
// test honest about what the handler touches.
type stubChildStore struct {
	Store
	credentialAvailable bool
	credentialErr       error
	parentProvider      string
	parentProviderErr   error
	created             bool
	captured            domain.CreateSession
	createErr           error
}

func (s *stubChildStore) AgentCredentialAvailable(
	_ context.Context, _, _ string,
) (bool, error) {
	return s.credentialAvailable, s.credentialErr
}

func (s *stubChildStore) OrchestratorAgentCredentialAvailable(
	_ context.Context, _, _, _ string,
) (bool, error) {
	return s.credentialAvailable, s.credentialErr
}

func (s *stubChildStore) OrchestratorSandboxProvider(
	_ context.Context, _, _ string,
) (string, error) {
	return s.parentProvider, s.parentProviderErr
}

func (s *stubChildStore) CreateOrchestratorChild(
	_ context.Context, _, _, _ string, _ int, input domain.CreateSession,
) (domain.Session, error) {
	s.created = true
	s.captured = input
	if s.createErr != nil {
		return domain.Session{}, s.createErr
	}
	return domain.Session{ID: "00000000-0000-0000-0000-0000000000c3", Kind: "worker"}, nil
}

// bothProviderProvisioning is a ProvisioningDefaults whose NodeOps and Coder
// configs both validate, so a plan can be built for either provider regardless
// of which one is the deployment default.
func bothProviderProvisioning(defaultProvider string) sandbox.ProvisioningDefaults {
	return sandbox.ProvisioningDefaults{
		Provider: defaultProvider,
		Release:  "test",
		NodeOps: sandbox.NodeOpsConfig{
			BaseURL:        "https://api.sb.createos.sh",
			APIKey:         "test-key",
			DefaultShape:   "s-1vcpu-1gb",
			DefaultRootFS:  "devbox:1",
			WorkerTokenTTL: 15 * time.Minute,
		},
		Coder: sandbox.CoderConfig{
			BaseURL:        "https://coder.example.com",
			Owner:          "ao-integration",
			TemplateID:     "2a2e262c-b31c-4202-946d-a19ad45d1fd2",
			AgentName:      "dev",
			DurableRoot:    "/home/coder",
			WorkerTokenTTL: 15 * time.Minute,
		},
	}
}

func newChildServer(store Store, provisioning sandbox.ProvisioningDefaults, defaultProvider string) *Server {
	return New(Options{
		Store:                     store,
		SandboxProvider:           defaultProvider,
		AvailableSandboxProviders: []string{sandbox.ProviderNodeOps, sandbox.ProviderCoder},
		Provisioning:              provisioning,
		Logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func childRequest(t *testing.T, scopes []string) *http.Request {
	t.Helper()
	body := `{"harness":"claude-code","displayName":"add-logger","prompt":"do the work","mode":"trusted"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloud/v1/worker/children", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "11111111-1111-1111-1111-111111111111")
	claims := worker.Claims{OrgID: childOrgID, SessionID: parentSessionID, Scopes: scopes}
	return req.WithContext(context.WithValue(req.Context(), workerContextKey{}, claims))
}

func resourceProfileProvider(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var profile struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("decode resource profile: %v", err)
	}
	return profile.Provider
}

// A NodeOps orchestrator must spawn a NodeOps worker even when the control
// plane default is Coder: the child inherits its parent's provider, not the
// deployment default. This is the exact regression the fix addresses.
func TestCreateWorkerChildInheritsNodeOpsProviderOverDefault(t *testing.T) {
	t.Parallel()
	store := &stubChildStore{credentialAvailable: true, parentProvider: sandbox.ProviderNodeOps}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderCoder), sandbox.ProviderCoder)

	rec := httptest.NewRecorder()
	srv.createWorkerChild(rec, childRequest(t, []string{"worker:orchestrate"}))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if !store.created {
		t.Fatal("CreateOrchestratorChild was not called")
	}
	if store.captured.Provider != sandbox.ProviderNodeOps {
		t.Fatalf("child provider = %q, want %q", store.captured.Provider, sandbox.ProviderNodeOps)
	}
	if got := resourceProfileProvider(t, store.captured.ResourceProfile); got != sandbox.ProviderNodeOps {
		t.Fatalf("resource profile provider = %q, want %q", got, sandbox.ProviderNodeOps)
	}
	if got := resourceProfileProvider(t, store.captured.BootstrapContext); got != sandbox.ProviderNodeOps {
		t.Fatalf("bootstrap context provider = %q, want %q", got, sandbox.ProviderNodeOps)
	}
}

// A Coder orchestrator (eleven_x) must spawn a Coder worker even when the
// control plane default is NodeOps.
func TestCreateWorkerChildInheritsCoderProviderOverDefault(t *testing.T) {
	t.Parallel()
	store := &stubChildStore{credentialAvailable: true, parentProvider: sandbox.ProviderCoder}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)

	rec := httptest.NewRecorder()
	srv.createWorkerChild(rec, childRequest(t, []string{"worker:orchestrate"}))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if store.captured.Provider != sandbox.ProviderCoder {
		t.Fatalf("child provider = %q, want %q", store.captured.Provider, sandbox.ProviderCoder)
	}
	if got := resourceProfileProvider(t, store.captured.ResourceProfile); got != sandbox.ProviderCoder {
		t.Fatalf("resource profile provider = %q, want %q", got, sandbox.ProviderCoder)
	}
}

// Without the worker:orchestrate scope the handler must reject the request and
// never reach the store.
func TestCreateWorkerChildRequiresOrchestratorScope(t *testing.T) {
	t.Parallel()
	store := &stubChildStore{credentialAvailable: true, parentProvider: sandbox.ProviderNodeOps}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)

	rec := httptest.NewRecorder()
	srv.createWorkerChild(rec, childRequest(t, []string{"worker:heartbeat"}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if store.created {
		t.Fatal("CreateOrchestratorChild ran despite missing scope")
	}
}

// A parent that is not an active orchestrator (terminated or missing) surfaces
// ErrForbidden from the provider lookup, which the handler maps to 403 and does
// not provision a child.
func TestCreateWorkerChildForbiddenWhenParentNotActiveOrchestrator(t *testing.T) {
	t.Parallel()
	store := &stubChildStore{credentialAvailable: true, parentProviderErr: postgres.ErrForbidden}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)

	rec := httptest.NewRecorder()
	srv.createWorkerChild(rec, childRequest(t, []string{"worker:orchestrate"}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if store.created {
		t.Fatal("CreateOrchestratorChild ran despite a forbidden parent")
	}
}

// When the inherited provider has no valid configuration on this control plane
// the plan cannot be built, and the handler returns 500 rather than silently
// falling back to another provider.
func TestCreateWorkerChildMisconfiguredInheritedProvider(t *testing.T) {
	t.Parallel()
	// Provisioning has no Coder config, so building a Coder plan fails validation.
	provisioning := sandbox.ProvisioningDefaults{
		Provider: sandbox.ProviderNodeOps,
		Release:  "test",
		NodeOps: sandbox.NodeOpsConfig{
			BaseURL: "https://api.sb.createos.sh", APIKey: "test-key",
			DefaultShape: "s-1vcpu-1gb", DefaultRootFS: "devbox:1",
			WorkerTokenTTL: 15 * time.Minute,
		},
	}
	store := &stubChildStore{credentialAvailable: true, parentProvider: sandbox.ProviderCoder}
	srv := newChildServer(store, provisioning, sandbox.ProviderNodeOps)

	rec := httptest.NewRecorder()
	srv.createWorkerChild(rec, childRequest(t, []string{"worker:orchestrate"}))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if store.created {
		t.Fatal("CreateOrchestratorChild ran despite an unbuildable plan")
	}
}

const (
	testOrgID          = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testOrchestratorID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	testChildID        = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// stubStore embeds the Store interface so only the methods a test exercises
// need implementations; anything else panics, which is the desired failure.
type stubStore struct {
	Store

	listChildren        func(ctx context.Context, orgID, sessionID string, includeTerminated bool, cursor *domain.Cursor, limit int) ([]domain.Session, bool, error)
	listSessionChildren func(ctx context.Context, principal domain.Principal, orgID, sessionID string, cursor *domain.Cursor, limit int) ([]domain.Session, bool, error)
	report              func(ctx context.Context, orgID, childID, key, text string) (domain.ClientEvent, error)
	prFacts             func(ctx context.Context, orgID string, sessionIDs []string) (map[string][]contract.PRFacts, error)
	pullRequests        func(ctx context.Context, orgID string, sessionIDs []string) (map[string][]domain.PullRequest, error)
}

func (s *stubStore) ListOrchestratorChildren(
	ctx context.Context, orgID, sessionID string, includeTerminated bool, cursor *domain.Cursor, limit int,
) ([]domain.Session, bool, error) {
	return s.listChildren(ctx, orgID, sessionID, includeTerminated, cursor, limit)
}

func (s *stubStore) ListSessionChildren(
	ctx context.Context, principal domain.Principal, orgID, sessionID string, cursor *domain.Cursor, limit int,
) ([]domain.Session, bool, error) {
	return s.listSessionChildren(ctx, principal, orgID, sessionID, cursor, limit)
}

func (s *stubStore) ReportToOrchestrator(
	ctx context.Context, orgID, childID, key, text string,
) (domain.ClientEvent, error) {
	return s.report(ctx, orgID, childID, key, text)
}

func (s *stubStore) PRFactsBySession(
	ctx context.Context, orgID string, sessionIDs []string,
) (map[string][]contract.PRFacts, error) {
	if s.prFacts == nil {
		return map[string][]contract.PRFacts{}, nil
	}
	return s.prFacts(ctx, orgID, sessionIDs)
}

func (s *stubStore) PullRequestsBySessions(
	ctx context.Context, orgID string, sessionIDs []string,
) (map[string][]domain.PullRequest, error) {
	if s.pullRequests == nil {
		return map[string][]domain.PullRequest{}, nil
	}
	return s.pullRequests(ctx, orgID, sessionIDs)
}

func testServer(store Store) *Server {
	return &Server{
		store:  store,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func workerRequest(t *testing.T, method, target string, body string, scopes ...string) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, reader)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "test-key")
	}
	claims := worker.Claims{
		OrgID: testOrgID, SessionID: testOrchestratorID,
		WorkerID: "w1", Epoch: 1, Scopes: scopes,
	}
	return r.WithContext(context.WithValue(r.Context(), workerContextKey{}, claims))
}

func TestListWorkerChildrenRequiresOrchestrateScope(t *testing.T) {
	s := testServer(&stubStore{})
	w := httptest.NewRecorder()
	s.listWorkerChildren(w, workerRequest(t, http.MethodGet, "/worker/children", "", "worker:connect"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "SCOPE_REQUIRED") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestListWorkerChildrenPlumbsIncludeTerminatedAndPRs(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	var gotIncludeTerminated bool
	store := &stubStore{
		listChildren: func(_ context.Context, orgID, sessionID string, includeTerminated bool, _ *domain.Cursor, _ int) ([]domain.Session, bool, error) {
			if orgID != testOrgID || sessionID != testOrchestratorID {
				t.Fatalf("identity not plumbed: %s %s", orgID, sessionID)
			}
			gotIncludeTerminated = includeTerminated
			return []domain.Session{{
				ID: testChildID, OrgID: orgID, Kind: "worker", Harness: "claude-code",
				DisplayName: "Fix CI", Branch: "ao/cccccccc", Mode: "trusted",
				ActivityState: "idle", UpdatedAt: now, CreatedAt: now,
			}}, false, nil
		},
		pullRequests: func(_ context.Context, _ string, _ []string) (map[string][]domain.PullRequest, error) {
			return map[string][]domain.PullRequest{
				testChildID: {{
					SessionID: testChildID, Number: 42, URL: "https://github.com/o/r/pull/42",
					State: contract.PRStateOpen, Draft: true,
					CIState: contract.CIState("failing"), UpdatedAt: now,
				}},
			}, nil
		},
	}
	s := testServer(store)
	w := httptest.NewRecorder()
	s.listWorkerChildren(w, workerRequest(
		t, http.MethodGet, "/worker/children?includeTerminated=true", "", "worker:orchestrate",
	))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !gotIncludeTerminated {
		t.Fatal("includeTerminated=true not plumbed to the store")
	}
	var response struct {
		Items []struct {
			ID     string `json:"id"`
			Branch string `json:"branch"`
			PRs    []struct {
				Number int    `json:"number"`
				State  string `json:"state"`
				CI     string `json:"ci"`
			} `json:"prs"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || len(response.Items[0].PRs) != 1 {
		t.Fatalf("unexpected payload: %s", w.Body.String())
	}
	pr := response.Items[0].PRs[0]
	if pr.Number != 42 || pr.CI != "failing" {
		t.Fatalf("pr not rendered: %+v", pr)
	}
	if pr.State != "draft" {
		t.Fatalf("open draft PR must render state=draft, got %q", pr.State)
	}
	if response.Items[0].Branch == "" {
		t.Fatal("branch missing from child item")
	}
}

func TestReportToParentRequiresReportScope(t *testing.T) {
	s := testServer(&stubStore{})
	w := httptest.NewRecorder()
	s.reportToParent(w, workerRequest(
		t, http.MethodPost, "/worker/parent/messages", `{"text":"done"}`, "worker:orchestrate",
	))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestReportToParentDelivers(t *testing.T) {
	var gotChild, gotText string
	store := &stubStore{
		report: func(_ context.Context, orgID, childID, key, text string) (domain.ClientEvent, error) {
			if orgID != testOrgID || key != "test-key" {
				t.Fatalf("identity not plumbed: %s %s", orgID, key)
			}
			gotChild, gotText = childID, text
			return domain.ClientEvent{SessionID: testOrchestratorID, Sequence: 7, Type: "message.user"}, nil
		},
	}
	s := testServer(store)
	w := httptest.NewRecorder()
	s.reportToParent(w, workerRequest(
		t, http.MethodPost, "/worker/parent/messages", `{"text":"PR #42 is green"}`, "worker:report",
	))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if gotChild != testOrchestratorID || gotText != "PR #42 is green" {
		t.Fatalf("report not plumbed: child=%s text=%q", gotChild, gotText)
	}
}

func TestReportToParentForbiddenWithoutParent(t *testing.T) {
	store := &stubStore{
		report: func(context.Context, string, string, string, string) (domain.ClientEvent, error) {
			return domain.ClientEvent{}, postgres.ErrForbidden
		},
	}
	s := testServer(store)
	w := httptest.NewRecorder()
	s.reportToParent(w, workerRequest(
		t, http.MethodPost, "/worker/parent/messages", `{"text":"hello"}`, "worker:report",
	))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestReportToParentRejectsEmptyText(t *testing.T) {
	s := testServer(&stubStore{})
	w := httptest.NewRecorder()
	s.reportToParent(w, workerRequest(
		t, http.MethodPost, "/worker/parent/messages", `{"text":"  "}`, "worker:report",
	))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}
}

// userRequest builds a request through the user-auth surface: principal in
// context and chi URL params, exactly what the authenticate middleware and
// router would provide.
func userRequest(t *testing.T, target string, params map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: "user-1"})
	return r.WithContext(ctx)
}

func TestListSessionChildrenHappyPath(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	store := &stubStore{
		listSessionChildren: func(_ context.Context, principal domain.Principal, orgID, sessionID string, _ *domain.Cursor, limit int) ([]domain.Session, bool, error) {
			if principal.UserID != "user-1" || orgID != testOrgID || sessionID != testOrchestratorID {
				t.Fatalf("identity not plumbed: %+v %s %s", principal, orgID, sessionID)
			}
			if limit <= 0 {
				t.Fatalf("limit not defaulted: %d", limit)
			}
			return []domain.Session{
				{ID: testChildID, OrgID: orgID, Kind: "worker", DisplayName: "Fix CI", IsTerminated: true, UpdatedAt: now},
			}, true, nil
		},
	}
	s := testServer(store)
	w := httptest.NewRecorder()
	s.listSessionChildren(w, userRequest(
		t,
		"/orgs/"+testOrgID+"/sessions/"+testOrchestratorID+"/children",
		map[string]string{"orgId": testOrgID, "sessionId": testOrchestratorID},
	))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Items []struct {
			ID           string `json:"id"`
			IsTerminated bool   `json:"isTerminated"`
			PRs          []any  `json:"prs"`
		} `json:"items"`
		Page struct {
			HasMore    bool   `json:"hasMore"`
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || !response.Items[0].IsTerminated {
		t.Fatalf("terminated child must pass through: %s", w.Body.String())
	}
	if response.Items[0].PRs == nil {
		t.Fatal("prs must serialize as an array, not null")
	}
	if !response.Page.HasMore || response.Page.NextCursor == "" {
		t.Fatalf("pagination not rendered: %+v", response.Page)
	}
}

func TestListSessionChildrenRejectsBadUUID(t *testing.T) {
	s := testServer(&stubStore{})
	w := httptest.NewRecorder()
	s.listSessionChildren(w, userRequest(
		t, "/orgs/nope/sessions/nope/children",
		map[string]string{"orgId": "nope", "sessionId": "nope"},
	))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
