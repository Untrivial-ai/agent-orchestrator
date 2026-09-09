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

func TestWorkerSpecAdvertisesWorkerBinaryHashes(t *testing.T) {
	t.Parallel()
	workerBin := []byte("fake ao-worker binary")
	helperBin := []byte("fake ao helper binary")
	reconciler := New(&workerSpecStore{}, nil, Options{
		PublicURL:          "https://cloud.example.com",
		WorkerBinary:       workerBin,
		WorkerHelperBinary: helperBin,
	})
	spec, err := reconciler.workerSpec(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderNodeOps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Environment["AO_WORKER_EXPECTED_SHA256"]; got != sha256HexOf(workerBin) {
		t.Fatalf("AO_WORKER_EXPECTED_SHA256 = %q, want %q", got, sha256HexOf(workerBin))
	}
	if got := spec.Environment["AO_WORKER_HELPER_EXPECTED_SHA256"]; got != sha256HexOf(helperBin) {
		t.Fatalf("AO_WORKER_HELPER_EXPECTED_SHA256 = %q, want %q", got, sha256HexOf(helperBin))
	}
	if spec.Environment["AO_WORKER_HELPER_PATH"] == "" {
		t.Fatal("AO_WORKER_HELPER_PATH must be advertised so the helper self-update can shadow the baked copy")
	}
}

func TestWorkerSpecOmitsHashesWithoutBinary(t *testing.T) {
	t.Parallel()
	reconciler := New(&workerSpecStore{}, nil, Options{PublicURL: "https://cloud.example.com"})
	spec, err := reconciler.workerSpec(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderNodeOps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spec.Environment["AO_WORKER_EXPECTED_SHA256"]; ok {
		t.Fatal("no worker binary configured: the self-update env must be absent so self-update stays inert")
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
