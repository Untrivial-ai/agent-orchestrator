package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/go-chi/chi/v5"
)

type preparationHandlerStore struct {
	Store
	createdInput     domain.CreateSession
	commitInput      domain.CommitSessionPreparation
	commitSession    string
	commitError      error
	renewSession     string
	renewClient      string
	renewGeneration  int64
	renewLease       time.Duration
	renewExpires     time.Time
	renewError       error
	detachSession    string
	detachClient     string
	detachGeneration int64
	detachCalls      int
	createCalls      int
	commitCalls      int
	renewCalls       int
}

const preparationClientID = "00000000-0000-0000-0000-0000000000c1"

type concurrentPreparationStore struct {
	*preparationHandlerStore
	credentialStarted   chan struct{}
	orchestratorStarted chan struct{}
}

func (s *concurrentPreparationStore) ListProviderConnections(
	ctx context.Context,
	_ domain.Principal,
	_ string,
) ([]domain.ProviderConnection, error) {
	close(s.credentialStarted)
	select {
	case <-s.orchestratorStarted:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []domain.ProviderConnection{{
		Provider: "codex", Label: defaultAgentConnectionLabel, ValidationState: "valid",
	}}, nil
}

func (s *concurrentPreparationStore) UpsertProviderConnection(
	context.Context,
	domain.Principal,
	string,
	string,
	string,
	[]byte,
	[]byte,
	json.RawMessage,
) (domain.ProviderConnection, error) {
	return domain.ProviderConnection{}, nil
}

func (s *concurrentPreparationStore) DeleteProviderConnection(
	context.Context,
	domain.Principal,
	string,
	string,
	string,
) error {
	return nil
}

func (s *concurrentPreparationStore) ProjectActiveOrchestrator(
	ctx context.Context,
	_, _ string,
) (string, string, bool, error) {
	close(s.orchestratorStarted)
	select {
	case <-s.credentialStarted:
	case <-ctx.Done():
		return "", "", false, ctx.Err()
	}
	return "", "", false, nil
}

func (s *preparationHandlerStore) CreateSession(
	_ context.Context,
	_ domain.Principal,
	_, _ string,
	_ int,
	input domain.CreateSession,
) (domain.Session, error) {
	s.createCalls++
	s.createdInput = input
	session := domain.Session{ID: "00000000-0000-0000-0000-0000000000e5", Kind: input.Kind}
	if input.PreparationExpiresAfter > 0 {
		expiresAt := time.Date(2026, time.September, 23, 12, 2, 0, 0, time.UTC)
		session.PreparationExpiresAt = &expiresAt
		session.PreparationGeneration = 1
		session.PreparationDisposition = "created"
	}
	return session, nil
}

func (s *preparationHandlerStore) RenewSessionPreparation(
	_ context.Context,
	_ domain.Principal,
	_, sessionID, clientInstanceID string,
	generation int64,
	lease time.Duration,
) (domain.SessionPreparationLease, error) {
	s.renewCalls++
	s.renewSession = sessionID
	s.renewClient = clientInstanceID
	s.renewGeneration = generation
	s.renewLease = lease
	return domain.SessionPreparationLease{ExpiresAt: s.renewExpires, Generation: generation}, s.renewError
}

func (s *preparationHandlerStore) DetachSessionPreparation(
	_ context.Context,
	_ domain.Principal,
	_, sessionID, clientInstanceID string,
	generation int64,
	_ time.Duration,
) (domain.SessionPreparationLease, error) {
	s.detachCalls++
	s.detachSession = sessionID
	s.detachClient = clientInstanceID
	s.detachGeneration = generation
	return domain.SessionPreparationLease{ExpiresAt: s.renewExpires, Generation: generation}, nil
}

func (s *preparationHandlerStore) CommitSessionPreparation(
	_ context.Context,
	_ domain.Principal,
	_, sessionID, _ string,
	input domain.CommitSessionPreparation,
) (domain.Session, error) {
	s.commitCalls++
	s.commitSession = sessionID
	s.commitInput = input
	if s.commitError != nil {
		return domain.Session{}, s.commitError
	}
	return domain.Session{ID: sessionID, DisplayName: input.DisplayName, Kind: "worker"}, nil
}

func preparationRequest(t *testing.T, method, path, body, sessionID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "22222222-2222-2222-2222-222222222222")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", autolinkOrgID)
	if sessionID != "" {
		rctx.URLParams.Add("sessionId", sessionID)
		rctx.URLParams.Add("clientInstanceId", preparationClientID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: "00000000-0000-0000-0000-0000000000f6"})
	return req.WithContext(ctx)
}

func TestPrepareSessionCreatesHiddenExpiringWorker(t *testing.T) {
	store := &preparationHandlerStore{}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	srv.prepareSession(recorder, preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/session-preparations",
		`{"projectId":"`+autolinkProjectID+`","harness":"codex","provider":"nodeops","clientInstanceId":"`+preparationClientID+`"}`,
		"",
	))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", recorder.Code, recorder.Body.String())
	}
	if store.createCalls != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", store.createCalls)
	}
	if store.createdInput.Kind != "worker" || store.createdInput.Prompt != "" {
		t.Fatalf("prepared session = %+v", store.createdInput)
	}
	if store.createdInput.PreparationExpiresAfter != sessionPreparationTTL {
		t.Fatalf("preparation TTL = %v, want %v", store.createdInput.PreparationExpiresAfter, sessionPreparationTTL)
	}
	if store.createdInput.PreparationClientInstanceID != preparationClientID {
		t.Fatalf("preparation client = %q", store.createdInput.PreparationClientInstanceID)
	}
	var response struct {
		Preparation sessionPreparationLeaseResponse `json:"preparation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Preparation.LeaseSeconds != 120 || response.Preparation.ExpiresAt.IsZero() ||
		response.Preparation.Generation != 1 {
		t.Fatalf("preparation lease = %+v", response.Preparation)
	}
}

func TestPrepareSessionRunsIndependentPreflightsConcurrently(t *testing.T) {
	store := &concurrentPreparationStore{
		preparationHandlerStore: &preparationHandlerStore{},
		credentialStarted:       make(chan struct{}),
		orchestratorStarted:     make(chan struct{}),
	}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	request := preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/session-preparations",
		`{"projectId":"`+autolinkProjectID+`","harness":"codex","provider":"nodeops","clientInstanceId":"`+preparationClientID+`"}`,
		"",
	)
	ctx, cancel := context.WithTimeout(request.Context(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		srv.prepareSession(recorder, request.WithContext(ctx))
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("preflight checks did not overlap")
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCommitSessionPreparationQueuesFirstTask(t *testing.T) {
	store := &preparationHandlerStore{}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	sessionID := "00000000-0000-0000-0000-0000000000e5"

	srv.commitSessionPreparation(recorder, preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions/"+sessionID+"/commit-preparation",
		`{"displayName":"Fix startup","prompt":"Run the checks","clientInstanceId":"`+preparationClientID+`","generation":1}`,
		sessionID,
	))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if store.commitCalls != 1 || store.commitSession != sessionID {
		t.Fatalf("commit = (%d, %q), want (1, %q)", store.commitCalls, store.commitSession, sessionID)
	}
	if store.commitInput.DisplayName != "Fix startup" || store.commitInput.Prompt != "Run the checks" ||
		store.commitInput.ClientInstanceID != preparationClientID || store.commitInput.Generation != 1 {
		t.Fatalf("commit input = %+v", store.commitInput)
	}
}

func TestCommitSessionPreparationRejectsEmptyPrompt(t *testing.T) {
	store := &preparationHandlerStore{}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	sessionID := "00000000-0000-0000-0000-0000000000e5"

	srv.commitSessionPreparation(recorder, preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions/"+sessionID+"/commit-preparation",
		`{"displayName":"New task","prompt":"  ","clientInstanceId":"`+preparationClientID+`","generation":1}`,
		sessionID,
	))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	if store.commitCalls != 0 {
		t.Fatalf("CommitSessionPreparation calls = %d, want 0", store.commitCalls)
	}
}

func TestCommitSessionPreparationReturnsStableStateErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "expired", err: postgres.ErrPreparationExpired, status: http.StatusGone, code: "PREPARATION_EXPIRED"},
		{name: "committed", err: postgres.ErrPreparationCommitted, status: http.StatusConflict, code: "PREPARATION_COMMITTED"},
		{name: "stale", err: postgres.ErrPreparationStale, status: http.StatusConflict, code: "PREPARATION_STALE"},
		{name: "unavailable", err: postgres.ErrPreparationUnavailable, status: http.StatusConflict, code: "PREPARATION_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &preparationHandlerStore{commitError: test.err}
			srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
			recorder := httptest.NewRecorder()
			sessionID := "00000000-0000-0000-0000-0000000000e5"

			srv.commitSessionPreparation(recorder, preparationRequest(
				t,
				http.MethodPost,
				"/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions/"+sessionID+"/commit-preparation",
				`{"displayName":"New task","prompt":"Run checks","clientInstanceId":"`+preparationClientID+`","generation":1}`,
				sessionID,
			))

			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			var response errorEnvelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Code != test.code {
				t.Fatalf("code = %q, want %q", response.Code, test.code)
			}
		})
	}
}

func TestRenewSessionPreparationExtendsLease(t *testing.T) {
	expiresAt := time.Date(2026, time.September, 23, 12, 4, 0, 0, time.UTC)
	store := &preparationHandlerStore{renewExpires: expiresAt}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	sessionID := "00000000-0000-0000-0000-0000000000e5"

	srv.renewSessionPreparation(recorder, preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions/"+sessionID+"/renew-preparation",
		`{"clientInstanceId":"`+preparationClientID+`","generation":1}`,
		sessionID,
	))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if store.renewCalls != 1 || store.renewSession != sessionID || store.renewLease != sessionPreparationTTL ||
		store.renewClient != preparationClientID || store.renewGeneration != 1 {
		t.Fatalf("renew = (%d, %q, %v)", store.renewCalls, store.renewSession, store.renewLease)
	}
	var response struct {
		Preparation sessionPreparationLeaseResponse `json:"preparation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Preparation.ExpiresAt.Equal(expiresAt) || response.Preparation.LeaseSeconds != 120 ||
		response.Preparation.Generation != 1 {
		t.Fatalf("preparation lease = %+v", response.Preparation)
	}
}

func TestRenewSessionPreparationReturnsStableStateErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "expired", err: postgres.ErrPreparationExpired, status: http.StatusGone, code: "PREPARATION_EXPIRED"},
		{name: "committed", err: postgres.ErrPreparationCommitted, status: http.StatusConflict, code: "PREPARATION_COMMITTED"},
		{name: "stale", err: postgres.ErrPreparationStale, status: http.StatusConflict, code: "PREPARATION_STALE"},
		{name: "unavailable", err: postgres.ErrPreparationUnavailable, status: http.StatusConflict, code: "PREPARATION_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &preparationHandlerStore{renewError: test.err}
			srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
			recorder := httptest.NewRecorder()
			sessionID := "00000000-0000-0000-0000-0000000000e5"

			srv.renewSessionPreparation(recorder, preparationRequest(
				t,
				http.MethodPost,
				"/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions/"+sessionID+"/renew-preparation",
				`{"clientInstanceId":"`+preparationClientID+`","generation":1}`,
				sessionID,
			))

			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			var response errorEnvelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Code != test.code {
				t.Fatalf("code = %q, want %q", response.Code, test.code)
			}
		})
	}
}

func TestDetachSessionPreparationStartsGraceWithoutDeletingSharedSession(t *testing.T) {
	expiresAt := time.Date(2026, time.September, 23, 12, 4, 0, 0, time.UTC)
	store := &preparationHandlerStore{renewExpires: expiresAt}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	sessionID := "00000000-0000-0000-0000-0000000000e5"
	request := preparationRequest(
		t,
		http.MethodDelete,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions/"+sessionID+
			"/preparation-attachments/"+preparationClientID+"?generation=1",
		"",
		sessionID,
	)

	srv.detachSessionPreparation(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if store.detachCalls != 1 || store.detachSession != sessionID ||
		store.detachClient != preparationClientID || store.detachGeneration != 1 {
		t.Fatalf("detach = (%d, %q, %q, %d)", store.detachCalls, store.detachSession, store.detachClient, store.detachGeneration)
	}
}
