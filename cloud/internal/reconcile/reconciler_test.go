package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

type lifecycleStore struct {
	creationMu     sync.Mutex
	acceptedPause  bool
	acceptCalls    int
	observations   []string
	renewError     error
	creations      []domain.SandboxCreation
	creationStates map[string]string
}

func (s *lifecycleStore) BeginSandboxCreation(_ context.Context, _ string, record domain.Sandbox, id string) error {
	s.creationMu.Lock()
	defer s.creationMu.Unlock()
	s.creations = append(s.creations, domain.SandboxCreation{ID: id, Generation: record.PreparationGeneration})
	return nil
}
func (s *lifecycleStore) RecordSandboxCreationResult(ctx context.Context, _, _, id, environmentID string) error {
	s.creationMu.Lock()
	defer s.creationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for index := range s.creations {
		if s.creations[index].ID == id {
			s.creations[index].EnvironmentID = environmentID
		}
	}
	return nil
}
func (s *lifecycleStore) ListSandboxCreations(context.Context, string, string) ([]domain.SandboxCreation, error) {
	s.creationMu.Lock()
	defer s.creationMu.Unlock()
	return append([]domain.SandboxCreation(nil), s.creations...), nil
}
func (s *lifecycleStore) ResolveSandboxCreation(ctx context.Context, _, _, id, state string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.creationMu.Lock()
	defer s.creationMu.Unlock()
	if s.creationStates == nil {
		s.creationStates = map[string]string{}
	}
	s.creationStates[id] = state
	for index := range s.creations {
		if s.creations[index].ID == id {
			s.creations = append(s.creations[:index], s.creations[index+1:]...)
			break
		}
	}
	return nil
}
func (s *lifecycleStore) ClaimSandboxes(context.Context, string, int, time.Duration) ([]domain.Sandbox, error) {
	return nil, nil
}
func (s *lifecycleStore) RenewSandboxClaim(context.Context, string, string, string, int64, time.Duration) error {
	return s.renewError
}
func (s *lifecycleStore) UpdateSandboxObservation(_ context.Context, _, _, _ string, _ int64, _, state, _ string, _ time.Time) error {
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
	createdSpec sandbox.Spec
	starts      int
	extensions  []time.Time
}

func (p *lifecycleProvider) Create(_ context.Context, spec sandbox.Spec) (sandbox.Environment, error) {
	p.createdSpec = spec
	return p.environment, nil
}

type lateCreateProvider struct {
	*lifecycleProvider
	deleteCalls int
}

func (p *lateCreateProvider) FindBySession(context.Context, string) (sandbox.Environment, bool, error) {
	return sandbox.Environment{}, false, nil
}

func (p *lateCreateProvider) Delete(context.Context, sandbox.ID) error {
	p.deleteCalls++
	return nil
}

func TestProvisionPersistsCreateResultAfterPreparationGenerationIsFenced(t *testing.T) {
	store := &lifecycleStore{renewError: postgres.ErrSandboxLeaseLost}
	provider := &lateCreateProvider{lifecycleProvider: &lifecycleProvider{
		environment: sandbox.Environment{ID: "environment-1", State: sandbox.StateProvisioning},
	}}
	reconciler := testReconciler(store, provider)
	record := domain.Sandbox{
		SessionID:             "session-1",
		OrgID:                 "org-1",
		Provider:              sandbox.ProviderDocker,
		PreparationGeneration: 7,
	}

	err := reconciler.provision(context.Background(), record, provider)

	if !errors.Is(err, postgres.ErrSandboxLeaseLost) {
		t.Fatalf("provision error = %v", err)
	}
	if provider.deleteCalls != 0 || len(store.creations) != 1 || store.creations[0].EnvironmentID != "environment-1" {
		t.Fatalf("late result was not retained: deletes=%d creations=%+v", provider.deleteCalls, store.creations)
	}
	if got := provider.createdSpec.Labels["ao.preparation_generation"]; got != "7" {
		t.Fatalf("preparation generation label = %q", got)
	}
	if len(store.observations) != 0 {
		t.Fatalf("stale observations = %v", store.observations)
	}
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

type wakeStore struct {
	*lifecycleStore
	claims atomic.Int64
	seen   chan int64
}

func (s *wakeStore) ClaimSandboxes(context.Context, string, int, time.Duration) ([]domain.Sandbox, error) {
	claim := s.claims.Add(1)
	select {
	case s.seen <- claim:
	default:
	}
	return nil, nil
}

func TestReconcilerWake(t *testing.T) {
	t.Parallel()
	store := &wakeStore{
		lifecycleStore: &lifecycleStore{},
		seen:           make(chan int64, 4),
	}
	reconciler := New(store, lifecycleResolver{provider: &lifecycleProvider{}}, Options{
		Interval: time.Hour,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for index := 0; index < 10; index++ {
		reconciler.Wake()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- reconciler.Run(ctx) }()
	for want := int64(1); want <= 2; want++ {
		select {
		case got := <-store.seen:
			if got != want {
				t.Fatalf("reconcile pass = %d, want %d", got, want)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("reconcile pass %d did not run", want)
		}
	}
	select {
	case got := <-store.seen:
		t.Fatalf("duplicate wake produced reconcile pass %d", got)
	case <-time.After(100 * time.Millisecond):
	}

	reconciler.Wake()
	select {
	case got := <-store.seen:
		if got != 3 {
			t.Fatalf("reconcile pass after wake = %d, want 3", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("wake did not interrupt the reconcile interval")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run reconciler: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconciler did not stop after cancellation")
	}
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

func TestStartingUpCoderWorkspaceIgnoresIdleStop(t *testing.T) {
	// A user resume is in flight: the short interaction lease has already lapsed
	// (KeepAlive false), but startup_started_at is recent, so the box is still
	// coming up. A provider idle-stop in this window must be refused and the box
	// restored, not accepted as a pause — otherwise the resume flips back to
	// "resuming" as the terminal is about to appear.
	store := &lifecycleStore{acceptedPause: true}
	provider := &lifecycleProvider{environment: sandbox.Environment{
		ID: "workspace-1", State: sandbox.StateStopped, StopCause: sandbox.StopCauseExternalIdle,
	}}
	record := runningRecord(false)
	record.ObservedState = domain.SandboxObservedRestoring
	startedAt := time.Now()
	record.StartupStartedAt = &startedAt
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), record); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if store.acceptCalls != 0 || provider.starts != 1 {
		t.Fatalf("accept calls = %d, starts = %d; want 0, 1", store.acceptCalls, provider.starts)
	}
	if len(store.observations) != 1 || store.observations[0] != domain.SandboxObservedRestoring {
		t.Fatalf("observations = %v, want restoring", store.observations)
	}
}

func TestStaleStartupCoderWorkspaceAcceptsIdleStop(t *testing.T) {
	// A bring-up that never converged has aged past the startup window. The idle
	// guard must no longer hold the box awake: accept the provider pause so
	// compute (and billing) stops instead of looping restores forever.
	store := &lifecycleStore{acceptedPause: true}
	provider := &lifecycleProvider{environment: sandbox.Environment{
		ID: "workspace-1", State: sandbox.StateStopped, StopCause: sandbox.StopCauseExternalIdle,
	}}
	record := runningRecord(false)
	stale := time.Now().Add(-2 * DefaultStartupTimeout)
	record.StartupStartedAt = &stale
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), record); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if store.acceptCalls != 1 || provider.starts != 0 {
		t.Fatalf("accept calls = %d, starts = %d; want 1, 0", store.acceptCalls, provider.starts)
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
	lifecycleStore
	disconnected int
	observed     string
}

func (s *pausePathStore) DisconnectSessionWorkers(context.Context, string, string) error {
	s.disconnected++
	return nil
}

func (s *pausePathStore) UpdateSandboxObservation(
	_ context.Context, _, _, _ string, _ int64, _, observedState, _ string, _ time.Time,
) error {
	s.observed = observedState
	return nil
}

// restoreProvider fakes the provider calls the provision path and the
// terminated-park path use, counting them so a test can assert which branch a
// restored sandbox took.
type restoreProvider struct {
	sandbox.Provider
	env       sandbox.Environment
	found     bool
	findCalls int
	getCalls  int
}

func (p *restoreProvider) FindBySession(context.Context, string) (sandbox.Environment, bool, error) {
	p.findCalls++
	return p.env, p.found, nil
}

func (p *restoreProvider) Get(context.Context, sandbox.ID) (sandbox.Environment, error) {
	p.getCalls++
	return p.env, nil
}

// A deleted-then-restored sandbox (observed 'deleted', no provider environment,
// desired 'running') must re-enter provisioning and build a fresh sandbox.
func TestRestoredDeletedSandboxReprovisions(t *testing.T) {
	t.Parallel()
	store := &pausePathStore{}
	provider := &restoreProvider{found: true, env: sandbox.Environment{ID: "env-new"}}
	reconciler := New(store, fixedResolver{provider}, Options{})
	if err := reconciler.reconcileSandbox(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderNodeOps,
		DesiredState:          domain.SandboxDesiredRunning,
		ObservedState:         domain.SandboxObservedDeleted,
		ProviderEnvironmentID: "",
	}); err != nil {
		t.Fatalf("reconcileSandbox: %v", err)
	}
	if provider.findCalls != 1 {
		t.Fatalf("provision not reached: FindBySession called %d times, want 1", provider.findCalls)
	}
	if store.observed != domain.SandboxObservedProvisioning {
		t.Fatalf("observed = %q, want %q", store.observed, domain.SandboxObservedProvisioning)
	}
}

// A restore that re-asserts a running intent on a sandbox parked as 'terminated'
// (repair-storm ceiling) must un-park it and re-enter provisioning.
func TestRestoredTerminatedSandboxReprovisions(t *testing.T) {
	t.Parallel()
	store := &pausePathStore{}
	provider := &restoreProvider{found: true, env: sandbox.Environment{ID: "env-new"}}
	reconciler := New(store, fixedResolver{provider}, Options{})
	if err := reconciler.reconcileSandbox(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderNodeOps,
		DesiredState:          domain.SandboxDesiredRunning,
		ObservedState:         domain.SandboxObservedTerminated,
		ProviderEnvironmentID: "",
	}); err != nil {
		t.Fatalf("reconcileSandbox: %v", err)
	}
	if provider.findCalls != 1 {
		t.Fatalf("terminated+running not re-provisioned: FindBySession called %d times, want 1", provider.findCalls)
	}
	if store.observed != domain.SandboxObservedProvisioning {
		t.Fatalf("observed = %q, want %q", store.observed, domain.SandboxObservedProvisioning)
	}
}

// The terminated park is preserved for any non-running desired state: a
// terminated sandbox is not probed or resumed while it stays parked.
func TestTerminatedSandboxStaysParkedWhenNotRunning(t *testing.T) {
	t.Parallel()
	store := &pausePathStore{}
	provider := &restoreProvider{env: sandbox.Environment{ID: "env-1"}}
	reconciler := New(store, fixedResolver{provider}, Options{})
	if err := reconciler.reconcileSandbox(context.Background(), domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderNodeOps,
		DesiredState:          domain.SandboxDesiredPaused,
		ObservedState:         domain.SandboxObservedTerminated,
		ProviderEnvironmentID: "env-1",
	}); err != nil {
		t.Fatalf("reconcileSandbox: %v", err)
	}
	if provider.getCalls != 0 || provider.findCalls != 0 {
		t.Fatalf("a parked terminated sandbox must not touch the provider (get=%d find=%d)",
			provider.getCalls, provider.findCalls)
	}
	if store.observed != domain.SandboxObservedTerminated {
		t.Fatalf("observed = %q, want %q (parked)", store.observed, domain.SandboxObservedTerminated)
	}
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

type recoveryProvider struct {
	*lateCreateProvider
	cancel    context.CancelFunc
	deleteErr error
	createErr error
	absent    bool
	found     bool
}

func (p *recoveryProvider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Environment, error) {
	if p.cancel != nil {
		p.cancel()
	}
	if p.createErr != nil {
		return p.environment, p.createErr
	}
	return p.lifecycleProvider.Create(ctx, spec)
}

func TestCanceledCapacityRejectionResolvesCreation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &lifecycleStore{}
	provider := &recoveryProvider{
		lateCreateProvider: &lateCreateProvider{lifecycleProvider: &lifecycleProvider{}},
		cancel:             cancel, createErr: sandbox.ErrAtCapacity,
	}
	record := domain.Sandbox{OrgID: "org", SessionID: "session", Provider: sandbox.ProviderDocker}
	_ = testReconciler(store, provider).provision(ctx, record, provider)
	if len(store.creations) != 0 || len(store.creationStates) != 1 {
		t.Fatalf("definitive rejection remained unresolved: %+v", store.creations)
	}
}
func (p *recoveryProvider) Get(context.Context, sandbox.ID) (sandbox.Environment, error) {
	if p.absent {
		return sandbox.Environment{}, sandbox.ErrNotFound
	}
	return p.environment, nil
}
func (p *recoveryProvider) FindBySession(context.Context, string) (sandbox.Environment, bool, error) {
	return p.environment, p.found, nil
}
func (p *recoveryProvider) Delete(context.Context, sandbox.ID) error {
	p.deleteCalls++
	if p.deleteErr != nil {
		return p.deleteErr
	}
	p.absent = true
	return nil
}

func TestLateCreationSurvivesCancellationAndDeleteFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &lifecycleStore{renewError: context.Canceled}
	provider := &recoveryProvider{lateCreateProvider: &lateCreateProvider{lifecycleProvider: &lifecycleProvider{
		environment: sandbox.Environment{ID: "late", State: sandbox.StateRunning},
	}}, cancel: cancel, deleteErr: errors.New("provider unavailable")}
	record := domain.Sandbox{OrgID: "org", SessionID: "session", Provider: sandbox.ProviderDocker, PreparationGeneration: 1}
	if err := testReconciler(store, provider).provision(ctx, record, provider); !errors.Is(err, context.Canceled) {
		t.Fatalf("provision=%v", err)
	}
	if len(store.creations) != 1 || store.creations[0].EnvironmentID != "late" {
		t.Fatalf("lost late result: %+v", store.creations)
	}
	creationID := store.creations[0].ID
	record.PreparationGeneration++
	record.DesiredState = domain.SandboxDesiredDeleted
	// Each pass may run in a replacement reconciler process.
	_, _ = testReconciler(store, provider).reconcileCreations(context.Background(), record, provider)
	if len(store.creations) != 1 || provider.deleteCalls != 1 {
		t.Fatalf("failed deletion was discarded: %+v", store.creations)
	}
	provider.deleteErr = nil
	_, err := testReconciler(store, provider).reconcileCreations(context.Background(), record, provider)
	if err != nil {
		t.Fatal(err)
	}
	_, err = testReconciler(store, provider).reconcileCreations(context.Background(), record, provider)
	if err != nil || len(store.creations) != 0 || store.creationStates[creationID] != "deleted" {
		t.Fatalf("cleanup=%+v err=%v", store.creations, err)
	}
}

func TestUnresolvedCreationPreventsDeletionAndReplacement(t *testing.T) {
	store := &lifecycleStore{creations: []domain.SandboxCreation{{ID: "in-flight", Generation: 1}}}
	provider := &recoveryProvider{lateCreateProvider: &lateCreateProvider{lifecycleProvider: &lifecycleProvider{}}}
	for _, desired := range []string{domain.SandboxDesiredDeleted, domain.SandboxDesiredRunning} {
		record := domain.Sandbox{SessionID: "session", OrgID: "org", DesiredState: desired, PreparationGeneration: 2}
		handled, err := testReconciler(store, provider).reconcileCreations(context.Background(), record, provider)
		if err != nil || !handled || len(store.creations) != 1 {
			t.Fatalf("desired=%s handled=%v err=%v", desired, handled, err)
		}
	}
}

func TestLateCreationDoesNotReplaceCurrentMachine(t *testing.T) {
	store := &lifecycleStore{creations: []domain.SandboxCreation{{ID: "old-create", Generation: 1, EnvironmentID: "old"}}}
	provider := &recoveryProvider{lateCreateProvider: &lateCreateProvider{lifecycleProvider: &lifecycleProvider{
		environment: sandbox.Environment{ID: "old", State: sandbox.StateRunning},
	}}}
	record := domain.Sandbox{SessionID: "session", OrgID: "org", PreparationGeneration: 2, ProviderEnvironmentID: "current", DesiredState: domain.SandboxDesiredRunning}
	handled, err := testReconciler(store, provider).reconcileCreations(context.Background(), record, provider)
	if err != nil || !handled || provider.deleteCalls != 1 || store.creationStates["old-create"] == "adopted" {
		t.Fatalf("handled=%v err=%v states=%v", handled, err, store.creationStates)
	}
}
