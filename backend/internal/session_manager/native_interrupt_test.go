package sessionmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type escapeInterruptAgent struct{ semanticSignalingAgent }

func (escapeInterruptAgent) InterruptInput() string { return "\x1b" }

type rawInterruptRuntime struct {
	fakeRuntime
	input  string
	handle ports.RuntimeHandle
}

func (r *rawInterruptRuntime) SendInput(_ context.Context, handle ports.RuntimeHandle, input string) error {
	r.input, r.handle = input, handle
	return nil
}

func TestInterruptTUIPreservesNativeCancelKey(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "generic", true: "escape"}[native], func(t *testing.T) {
			st := newFakeStore()
			id := domain.SessionID("native-interrupt")
			st.sessions[id] = domain.SessionRecord{ID: id, Harness: domain.HarnessCodex, Metadata: domain.SessionMetadata{RuntimeHandleID: "pane-1"}}
			rt := &rawInterruptRuntime{}
			var agent ports.Agent = semanticSignalingAgent{}
			if native {
				agent = escapeInterruptAgent{}
			}
			manager := &Manager{store: st, runtime: rt, agents: singleAgent{agent}}
			if err := manager.InterruptTUI(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if native {
				if rt.input != "\x1b" || rt.handle.ID != "pane-1" || len(rt.interrupts) != 0 {
					t.Fatalf("native cancellation = %q, %+v, %v", rt.input, rt.handle, rt.interrupts)
				}
			} else if len(rt.interrupts) != 1 || rt.interrupts[0] != "pane-1" || rt.input != "" {
				t.Fatalf("generic cancellation = %q, %v", rt.input, rt.interrupts)
			}
		})
	}
}

func TestNativeInterruptDoesNotFallbackToWrongControlKey(t *testing.T) {
	rt := &fakeRuntime{}
	manager := &Manager{runtime: rt, agents: singleAgent{escapeInterruptAgent{}}}
	err := manager.interruptTerminal(context.Background(), domain.SessionRecord{Harness: domain.HarnessCodex, Metadata: domain.SessionMetadata{RuntimeHandleID: "pane-1"}})
	if !errors.Is(err, ErrSemanticAcceptanceUnsupported) || len(rt.interrupts) != 0 {
		t.Fatalf("unsupported native cancellation = %v, %v", err, rt.interrupts)
	}
}
