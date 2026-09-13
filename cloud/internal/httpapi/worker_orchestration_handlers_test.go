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

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
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
