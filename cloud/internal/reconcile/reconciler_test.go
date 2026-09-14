package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

type lifecycleStore struct {
	acceptedPause bool
	acceptCalls   int
	observations  []string
}

func (s *lifecycleStore) ClaimSandboxes(context.Context, string, int, time.Duration) ([]domain.Sandbox, error) {
	return nil, nil
}
func (s *lifecycleStore) RenewSandboxClaim(context.Context, string, string, string, time.Duration) error {
	return nil
}
func (s *lifecycleStore) UpdateSandboxObservation(_ context.Context, _, _, _, _, state, _ string, _ time.Time) error {
	s.observations = append(s.observations, state)
	return nil
}
func (s *lifecycleStore) AcceptSandboxProviderPause(context.Context, string, string, string, string, time.Time) (bool, error) {
	s.acceptCalls++
	return s.acceptedPause, nil
}
func (s *lifecycleStore) RecordSandboxFailure(context.Context, string, string, string, string, string) error {
	return nil
}
func (s *lifecycleStore) ReleaseSandboxClaim(context.Context, string, string, string, time.Time) error {
	return nil
}
func (s *lifecycleStore) IssueAccessTicket(context.Context, string, string, string, []string, time.Duration) (string, error) {
	return "ticket", nil
}
func (s *lifecycleStore) AppendSessionEvent(context.Context, string, string, string, json.RawMessage) (domain.ClientEvent, error) {
	return domain.ClientEvent{}, nil
}
func (s *lifecycleStore) MarkSandboxDeletionRequested(context.Context, string, string, string) error {
	return nil
}
func (s *lifecycleStore) CompleteSandboxDeletion(context.Context, string, string, string) error {
	return nil
}
func (s *lifecycleStore) DisconnectSessionWorkers(context.Context, string, string) error {
	return nil
}
func (s *lifecycleStore) RecordSandboxStartupRepair(context.Context, string, string, string) (int, error) {
	return 0, nil
}

type lifecycleProvider struct {
	environment sandbox.Environment
	starts      int
	extensions  []time.Time
}

func (p *lifecycleProvider) Create(context.Context, sandbox.Spec) (sandbox.Environment, error) {
	return p.environment, nil
}
func (p *lifecycleProvider) Get(context.Context, sandbox.ID) (sandbox.Environment, error) {
	return p.environment, nil
}
func (p *lifecycleProvider) FindBySession(context.Context, string) (sandbox.Environment, bool, error) {
	return p.environment, true, nil
}
func (p *lifecycleProvider) Start(context.Context, sandbox.ID) error {
	p.starts++
	return nil
}
func (p *lifecycleProvider) Stop(context.Context, sandbox.ID) error   { return nil }
func (p *lifecycleProvider) Pause(context.Context, sandbox.ID) error  { return nil }
func (p *lifecycleProvider) Resume(context.Context, sandbox.ID) error { return nil }
func (p *lifecycleProvider) Delete(context.Context, sandbox.ID) error { return nil }
func (p *lifecycleProvider) ExtendDeadline(_ context.Context, _ sandbox.ID, deadline time.Time) error {
	p.extensions = append(p.extensions, deadline)
	return nil
}

type lifecycleResolver struct{ provider sandbox.Provider }

func (r lifecycleResolver) Resolve(context.Context, domain.Sandbox) (sandbox.Provider, error) {
	return r.provider, nil
}

func testReconciler(store Store, provider sandbox.Provider) *Reconciler {
	return New(store, lifecycleResolver{provider: provider}, Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func runningRecord(keepAlive bool) domain.Sandbox {
	now := time.Now()
	return domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: "coder",
		ProviderEnvironmentID: "workspace-1",
		DesiredState:          domain.SandboxDesiredRunning,
		ObservedState:         domain.SandboxObservedRunning,
		WorkerLastSeenAt:      &now,
		KeepAlive:             keepAlive,
		UpdatedAt:             now,
	}
}

func TestCoderAutostopBecomesPausedWithoutRestart(t *testing.T) {
	store := &lifecycleStore{acceptedPause: true}
	provider := &lifecycleProvider{environment: sandbox.Environment{
		ID: "workspace-1", State: sandbox.StateStopped, StopCause: sandbox.StopCauseExternalIdle,
	}}
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), runningRecord(false)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if store.acceptCalls != 1 || provider.starts != 0 {
		t.Fatalf("accept calls = %d, starts = %d; want 1, 0", store.acceptCalls, provider.starts)
	}
}

func TestActiveWorkRestoresStoppedCoderWorkspace(t *testing.T) {
	store := &lifecycleStore{acceptedPause: true}
	provider := &lifecycleProvider{environment: sandbox.Environment{
		ID: "workspace-1", State: sandbox.StateStopped, StopCause: sandbox.StopCauseExternalIdle,
	}}
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), runningRecord(true)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if store.acceptCalls != 0 || provider.starts != 1 {
		t.Fatalf("accept calls = %d, starts = %d; want 0, 1", store.acceptCalls, provider.starts)
	}
	if len(store.observations) != 1 || store.observations[0] != domain.SandboxObservedRestoring {
		t.Fatalf("observations = %v, want restoring", store.observations)
	}
}

func TestAmbiguousProviderStopPreservesExistingRestoreBehavior(t *testing.T) {
	store := &lifecycleStore{acceptedPause: true}
	provider := &lifecycleProvider{environment: sandbox.Environment{
		ID: "workspace-1", State: sandbox.StateStopped,
	}}
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), runningRecord(false)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if store.acceptCalls != 0 || provider.starts != 1 {
		t.Fatalf("accept calls = %d, starts = %d; want 0, 1", store.acceptCalls, provider.starts)
	}
}

func TestActiveWorkExtendsNearCoderDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	store := &lifecycleStore{}
	provider := &lifecycleProvider{environment: sandbox.Environment{
		ID: "workspace-1", State: sandbox.StateRunning, Deadline: &deadline,
	}}
	started := time.Now()
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), runningRecord(true)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(provider.extensions) != 1 {
		t.Fatalf("extensions = %v, want one", provider.extensions)
	}
	if provider.extensions[0].Before(started.Add(activeDeadlineExtension - time.Second)) {
		t.Fatalf("extended deadline = %s, want about %s from now", provider.extensions[0], activeDeadlineExtension)
	}
	if provider.extensions[0].After(started.Add(activeDeadlineExtension + time.Second)) {
		t.Fatalf("extended deadline = %s, want provider-neutral request about %s from now", provider.extensions[0], activeDeadlineExtension)
	}
}

func TestIdleWorkDoesNotExtendCoderDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	store := &lifecycleStore{}
	provider := &lifecycleProvider{environment: sandbox.Environment{
		ID: "workspace-1", State: sandbox.StateRunning, Deadline: &deadline,
	}}
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), runningRecord(false)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(provider.extensions) != 0 {
		t.Fatalf("extensions = %v, want none", provider.extensions)
	}
}

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

// pausePathStore spies on the two store calls the pause path makes.
type pausePathStore struct {
	Store
	disconnected int
	observed     string
}

func (s *pausePathStore) DisconnectSessionWorkers(context.Context, string, string) error {
	s.disconnected++
	return nil
}

func (s *pausePathStore) UpdateSandboxObservation(
	_ context.Context, _, _, _, _, observedState, _ string, _ time.Time,
) error {
	s.observed = observedState
	return nil
}

// stopSpyProvider fakes just the two provider calls the pause path uses.
type stopSpyProvider struct {
	sandbox.Provider
	state   string
	stopped int
}

func (p *stopSpyProvider) Get(context.Context, sandbox.ID) (sandbox.Environment, error) {
	return sandbox.Environment{ID: "env-1", State: p.state}, nil
}

func (p *stopSpyProvider) Stop(context.Context, sandbox.ID) error {
	p.stopped++
	return nil
}

type fixedResolver struct{ provider sandbox.Provider }

func (r fixedResolver) Resolve(context.Context, domain.Sandbox) (sandbox.Provider, error) {
	return r.provider, nil
}

// Pausing a running sandbox must stop the provider AND disconnect its worker,
// so a subsequent terminal keystroke sees "no worker" and wakes the box instead
// of enqueuing input to a dead worker that expires unclaimed.
func TestReconcilePauseDisconnectsWorker(t *testing.T) {
	t.Parallel()
	store := &pausePathStore{}
	provider := &stopSpyProvider{state: sandbox.StateRunning}
	reconciler := New(store, fixedResolver{provider}, Options{})
	if err := reconciler.reconcileSandbox(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderNodeOps,
		DesiredState:          domain.SandboxDesiredPaused,
		ObservedState:         domain.SandboxObservedRunning,
		ProviderEnvironmentID: "env-1",
	}); err != nil {
		t.Fatalf("reconcileSandbox: %v", err)
	}
	if provider.stopped != 1 {
		t.Fatalf("provider.Stop called %d times, want 1", provider.stopped)
	}
	if store.disconnected != 1 {
		t.Fatalf("DisconnectSessionWorkers called %d times, want 1", store.disconnected)
	}
	if store.observed != domain.SandboxObservedStopped {
		t.Fatalf("observed = %q, want %q", store.observed, domain.SandboxObservedStopped)
	}
}

// A sandbox already stopped provider-side must not re-stop or re-disconnect on
// every reconcile tick while it stays paused.
func TestReconcilePauseAlreadyStoppedSkipsDisconnect(t *testing.T) {
	t.Parallel()
	store := &pausePathStore{}
	provider := &stopSpyProvider{state: sandbox.StateStopped}
	reconciler := New(store, fixedResolver{provider}, Options{})
	if err := reconciler.reconcileSandbox(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderNodeOps,
		DesiredState:          domain.SandboxDesiredPaused,
		ObservedState:         domain.SandboxObservedStopped,
		ProviderEnvironmentID: "env-1",
	}); err != nil {
		t.Fatalf("reconcileSandbox: %v", err)
	}
	if provider.stopped != 0 {
		t.Fatalf("provider.Stop called %d times on an already-stopped env, want 0", provider.stopped)
	}
	if store.disconnected != 0 {
		t.Fatalf("DisconnectSessionWorkers called %d times on an already-stopped env, want 0", store.disconnected)
	}
}
