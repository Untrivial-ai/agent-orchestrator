package sessionmanager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type synchronizedSessionStore struct {
	*fakeStore
	mu sync.RWMutex
}

func (s *synchronizedSessionStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fakeStore.GetSession(ctx, id)
}

func (s *synchronizedSessionStore) markFirstSignal(id domain.SessionID, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.sessions[id]
	rec.FirstSignalAt = at
	s.sessions[id] = rec
}

type terminalReadyAgent struct{ fakeAgent }

func (terminalReadyAgent) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	if output == "ready" {
		return domain.ActivityIdle, true
	}
	return "", false
}

type emptyComposerReadyAgent struct{ fakeAgent }

func (emptyComposerReadyAgent) DetectTerminalActivity(string) (domain.ActivityState, bool) {
	return "", false
}

func (emptyComposerReadyAgent) ComposerIsEmpty(output string) bool {
	return output == "empty-composer"
}

type waitingInputComposerReadyAgent struct{ emptyComposerReadyAgent }

func (waitingInputComposerReadyAgent) EmptyComposerProvesWaitingInputReady() bool { return true }

func TestWaitForMessageDeliveryReadyWaitsForTerminalIdleMarker(t *testing.T) {
	st := newFakeStore()
	st.sessions["orch"] = domain.SessionRecord{
		ID:        "orch",
		ProjectID: "ao",
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeTUI,
		Activity:  domain.Activity{State: domain.ActivityIdle},
		Metadata:  domain.SessionMetadata{RuntimeHandleID: "orch"},
	}
	runtime := &fakeRuntime{outputs: []string{"starting", "ready"}}
	m := New(Deps{Runtime: runtime, Agents: singleAgent{agent: terminalReadyAgent{}}, Store: st})

	if err := m.WaitForMessageDeliveryReady(context.Background(), "orch"); err != nil {
		t.Fatalf("WaitForMessageDeliveryReady: %v", err)
	}
	if runtime.outputCalls < 2 {
		t.Fatalf("terminal output calls = %d, want readiness polling", runtime.outputCalls)
	}
}

func TestWaitForMessageDeliveryReadyHonorsContextWhileTerminalStarts(t *testing.T) {
	st := newFakeStore()
	st.sessions["orch"] = domain.SessionRecord{
		ID:        "orch",
		ProjectID: "ao",
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeTUI,
		Activity:  domain.Activity{State: domain.ActivityIdle},
		Metadata:  domain.SessionMetadata{RuntimeHandleID: "orch"},
	}
	m := New(Deps{Runtime: &fakeRuntime{outputs: []string{"starting"}}, Agents: singleAgent{agent: terminalReadyAgent{}}, Store: st})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := m.WaitForMessageDeliveryReady(ctx, "orch")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForMessageDeliveryReady error = %v, want context deadline", err)
	}
}

func TestWaitForMessageDeliveryReadyAcceptsProvenEmptyComposer(t *testing.T) {
	st := newFakeStore()
	st.sessions["orch"] = domain.SessionRecord{
		ID:        "orch",
		ProjectID: "ao",
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessContinue,
		Mode:      domain.SessionModeTUI,
		Activity:  domain.Activity{State: domain.ActivityIdle},
		Metadata:  domain.SessionMetadata{RuntimeHandleID: "orch"},
	}
	runtime := &fakeRuntime{outputs: []string{"empty-composer"}}
	m := New(Deps{Runtime: runtime, Agents: singleAgent{agent: emptyComposerReadyAgent{}}, Store: st})

	if err := m.WaitForMessageDeliveryReady(context.Background(), "orch"); err != nil {
		t.Fatalf("WaitForMessageDeliveryReady: %v", err)
	}
}

func TestWaitForMessageDeliveryReadyAcceptsProvenEmptyComposerWhileWaitingInput(t *testing.T) {
	st := newFakeStore()
	st.sessions["orch"] = domain.SessionRecord{
		ID:        "orch",
		ProjectID: "ao",
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessAider,
		Mode:      domain.SessionModeTUI,
		Activity:  domain.Activity{State: domain.ActivityWaitingInput},
		Metadata:  domain.SessionMetadata{RuntimeHandleID: "orch"},
	}
	runtime := &fakeRuntime{outputs: []string{"empty-composer"}}
	m := New(Deps{Runtime: runtime, Agents: singleAgent{agent: waitingInputComposerReadyAgent{}}, Store: st})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := m.WaitForMessageDeliveryReady(ctx, "orch"); err != nil {
		t.Fatalf("WaitForMessageDeliveryReady: %v", err)
	}
}

func TestWaitForMessageDeliveryReadyRejectsWaitingInputWithoutExplicitCapability(t *testing.T) {
	st := newFakeStore()
	st.sessions["orch"] = domain.SessionRecord{
		ID:        "orch",
		ProjectID: "ao",
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeTUI,
		Activity:  domain.Activity{State: domain.ActivityWaitingInput},
		Metadata:  domain.SessionMetadata{RuntimeHandleID: "orch"},
	}
	m := New(Deps{
		Runtime: &fakeRuntime{outputs: []string{"empty-composer"}},
		Agents:  singleAgent{agent: emptyComposerReadyAgent{}},
		Store:   st,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := m.WaitForMessageDeliveryReady(ctx, "orch")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForMessageDeliveryReady error = %v, want context deadline", err)
	}
}

func TestWaitForMessageDeliveryReadyWaitsForFirstHookSignal(t *testing.T) {
	st := &synchronizedSessionStore{fakeStore: newFakeStore()}
	st.sessions["cursor-1"] = domain.SessionRecord{
		ID:        "cursor-1",
		ProjectID: "phoenix",
		Harness:   domain.HarnessCursor,
		Mode:      domain.SessionModeTUI,
		Activity:  domain.Activity{State: domain.ActivityIdle},
		Metadata:  domain.SessionMetadata{RuntimeHandleID: "cursor-1"},
	}
	m := New(Deps{Runtime: &fakeRuntime{}, Agents: singleAgent{agent: startupReadySignalingAgent{}}, Store: st})

	done := make(chan error, 1)
	go func() {
		done <- m.WaitForMessageDeliveryReady(context.Background(), "cursor-1")
	}()

	time.Sleep(30 * time.Millisecond)
	st.markFirstSignal("cursor-1", time.Now())

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForMessageDeliveryReady: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for readiness after first hook signal")
	}
}
