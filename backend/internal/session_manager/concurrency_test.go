package sessionmanager

import (
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// newCappedManager mirrors newManager but with a daemon-wide concurrency cap.
func newCappedManager(limit int) (*Manager, *fakeStore) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	lookPath := func(string) (string, error) { return "/bin/true", nil }
	m := New(Deps{Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: &fakeWorkspace{}, Store: st, Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: st}, LookPath: lookPath, MaxConcurrentSessions: limit})
	return m, st
}

func TestSpawnRefusesWorkerAtGlobalConcurrencyCap(t *testing.T) {
	m, st := newCappedManager(2)

	for i := 0; i < 2; i++ {
		if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
			t.Fatalf("spawn %d under cap: %v", i, err)
		}
	}
	_, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("spawn over cap: err = %v, want ErrConcurrencyLimit", err)
	}
	// No durable residue: the refused spawn must not have created a seed row.
	if got := len(st.sessions); got != 2 {
		t.Fatalf("sessions after refused spawn = %d, want 2", got)
	}

	// A terminated session frees its slot: the cap counts live sessions only.
	for id, rec := range st.sessions {
		rec.IsTerminated = true
		st.sessions[id] = rec
		break
	}
	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn after slot freed: %v", err)
	}
}

func TestSpawnOrchestratorExemptFromConcurrencyCap(t *testing.T) {
	m, st := newCappedManager(1)

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	// The board is at the cap, but recovery must stay possible: an orchestrator
	// spawn is never refused by the concurrency cap.
	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindOrchestrator}); err != nil {
		t.Fatalf("spawn orchestrator at cap: %v", err)
	}
	if got := len(st.sessions); got != 2 {
		t.Fatalf("sessions = %d, want 2", got)
	}
}

func TestSpawnEnforcesProjectConcurrencyCap(t *testing.T) {
	m, st := newCappedManager(0) // no global cap
	capped := testRoleAgents()
	capped.MaxConcurrentSessions = 1
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: capped}
	st.projects["other"] = domain.ProjectRecord{ID: "other", Config: testRoleAgents()}

	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn under project cap: %v", err)
	}
	_, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("spawn over project cap: err = %v, want ErrConcurrencyLimit", err)
	}
	// The cap is per-project: another project spawns freely.
	if _, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "other", Kind: domain.KindWorker}); err != nil {
		t.Fatalf("spawn in uncapped project: %v", err)
	}
}

func TestWorkerAdmissionCountsStartsNotYetDurable(t *testing.T) {
	m, st := newCappedManager(1)
	release, err := m.beginWorkerAdmission(ctx, domain.KindWorker, "mer", 0)
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}
	if _, err := m.beginWorkerAdmission(ctx, domain.KindWorker, "mer", 0); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("racing admission err = %v, want ErrConcurrencyLimit", err)
	}
	if len(st.sessions) != 0 {
		t.Fatalf("admission created %d durable sessions, want 0", len(st.sessions))
	}
	release()
	if release2, err := m.beginWorkerAdmission(ctx, domain.KindWorker, "mer", 0); err != nil {
		t.Fatalf("admission after release: %v", err)
	} else {
		release2()
	}
}

func TestWorkerAdmissionAppliesProjectCapToRestoreStarts(t *testing.T) {
	m, st := newCappedManager(0)
	rec := domain.SessionRecord{ID: "existing", ProjectID: "mer", Kind: domain.KindWorker}
	st.sessions[rec.ID] = rec
	if _, err := m.beginWorkerAdmission(ctx, domain.KindWorker, "mer", 1); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("restore admission err = %v, want ErrConcurrencyLimit", err)
	}
	if release, err := m.beginWorkerAdmission(ctx, domain.KindOrchestrator, "mer", 1); err != nil {
		t.Fatalf("orchestrator recovery admission: %v", err)
	} else {
		release()
	}
}
