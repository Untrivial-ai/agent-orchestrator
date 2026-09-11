package reconcile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

type workerSpecStore struct {
	Store
	issued int
}

type observationStore struct {
	Store
	state string
}

func (s *observationStore) UpdateSandboxObservation(
	_ context.Context, _, _, _, _ string, state string, _ string, _ time.Time,
) error {
	s.state = state
	return nil
}

type fixedResolver struct {
	provider sandbox.Provider
}

func (r fixedResolver) Resolve(context.Context, domain.Sandbox) (sandbox.Provider, error) {
	return r.provider, nil
}

type provisioningProvider struct{}

func (provisioningProvider) Create(context.Context, sandbox.Spec) (sandbox.Environment, error) {
	return sandbox.Environment{}, nil
}
func (provisioningProvider) Get(context.Context, sandbox.ID) (sandbox.Environment, error) {
	return sandbox.Environment{ID: "workspace-1", State: sandbox.StateProvisioning}, nil
}
func (provisioningProvider) FindBySession(context.Context, string) (sandbox.Environment, bool, error) {
	return sandbox.Environment{}, false, nil
}
func (provisioningProvider) Start(context.Context, sandbox.ID) error  { return nil }
func (provisioningProvider) Stop(context.Context, sandbox.ID) error   { return nil }
func (provisioningProvider) Pause(context.Context, sandbox.ID) error  { return nil }
func (provisioningProvider) Resume(context.Context, sandbox.ID) error { return nil }
func (provisioningProvider) Delete(context.Context, sandbox.ID) error { return nil }

func (s *workerSpecStore) IssueAccessTicket(
	context.Context, string, string, string, []string, time.Duration,
) (string, error) {
	s.issued++
	return "bootstrap-ticket", nil
}

func TestWorkerSpecUsesPersistedCoderWorkspaceLayout(t *testing.T) {
	t.Parallel()
	store := &workerSpecStore{}
	reconciler := New(store, nil, Options{PublicURL: "https://cloud.example.com"})
	profile := json.RawMessage(`{"coder":{"baseUrl":"https://coder.example.com","owner":"planned-owner","templateId":"2a2e262c-b31c-4202-946d-a19ad45d1fd2","parameters":{"region":"us-west-2"},"durableRoot":"/customer/persistent"}}`)
	spec, err := reconciler.workerSpec(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderCoder,
		ResourceProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"AO_WORKSPACE_DIR":  "/customer/persistent/repository",
		"AO_DATA_DIR":       "/customer/persistent/.ao/worker",
		"HOME":              "/customer/persistent/.ao/home",
		"CLAUDE_CONFIG_DIR": "/customer/persistent/.ao/home/.claude",
		"CODEX_HOME":        "/customer/persistent/.ao/home/.codex",
	}
	for key, expected := range want {
		if spec.Environment[key] != expected {
			t.Errorf("%s = %q, want %q", key, spec.Environment[key], expected)
		}
	}
	if spec.DurableRoot != "/customer/persistent" {
		t.Errorf("DurableRoot = %q", spec.DurableRoot)
	}
}

func TestWorkerSpecPreservesOtherProviderWorkspaceLayout(t *testing.T) {
	t.Parallel()
	store := &workerSpecStore{}
	reconciler := New(store, nil, Options{PublicURL: "https://cloud.example.com"})
	spec, err := reconciler.workerSpec(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderNodeOps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Environment["AO_WORKSPACE_DIR"] != "/workspace/repository" ||
		spec.Environment["AO_DATA_DIR"] != "/workspace/.ao/worker" || spec.DurableRoot != "" {
		t.Fatalf("unexpected non-Coder layout: %+v", spec)
	}
}

func TestWorkerSpecRejectsCoderWithoutDurableContractBeforeIssuingTicket(t *testing.T) {
	t.Parallel()
	store := &workerSpecStore{}
	reconciler := New(store, nil, Options{PublicURL: "https://cloud.example.com"})
	_, err := reconciler.workerSpec(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderCoder,
	})
	if err == nil || !strings.Contains(err.Error(), "session resource profile") {
		t.Fatalf("workerSpec error = %v", err)
	}
	if store.issued != 0 {
		t.Fatalf("issued %d bootstrap tickets for an invalid layout", store.issued)
	}
}

func TestCoderRestoreBootstrapRequiresDurableIdentity(t *testing.T) {
	t.Parallel()
	now := time.Now()
	reconciler := New(&workerSpecStore{}, nil, Options{})
	record := domain.Sandbox{
		SessionID: "session-1", Provider: sandbox.ProviderCoder, WorkerLastSeenAt: &now,
	}
	bootstrap := reconciler.workerBootstrap(record, sandbox.Spec{DurableRoot: "/mnt/ao"}, false)
	if !bootstrap.RequireDurableIdentity || bootstrap.DurableIdentity != "session-1" {
		t.Fatalf("unexpected restore bootstrap: %+v", bootstrap)
	}
	first := reconciler.workerBootstrap(domain.Sandbox{
		SessionID: "session-2", Provider: sandbox.ProviderCoder,
	}, sandbox.Spec{DurableRoot: "/mnt/ao"}, false)
	if first.RequireDurableIdentity {
		t.Fatal("first Coder bootstrap unexpectedly required an existing identity")
	}
}

func TestProvisioningProviderStatePreservesRestoreIntent(t *testing.T) {
	t.Parallel()
	store := &observationStore{}
	reconciler := New(store, fixedResolver{provider: provisioningProvider{}}, Options{})
	now := time.Now()
	err := reconciler.reconcileSandbox(context.Background(), domain.Sandbox{
		SessionID:             "session-1",
		OrgID:                 "org-1",
		ProviderEnvironmentID: "workspace-1",
		DesiredState:          domain.SandboxDesiredRunning,
		ObservedState:         domain.SandboxObservedRestoring,
		WorkerLastSeenAt:      &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.state != domain.SandboxObservedRestoring {
		t.Fatalf("observed state = %q, want %q", store.state, domain.SandboxObservedRestoring)
	}
}
