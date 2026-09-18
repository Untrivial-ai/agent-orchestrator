package sessionmanager

import (
	"context"
	"os/exec"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type changingBinaryAgent struct {
	ports.Agent
	calls         int
	invalidations int
}

func (a *changingBinaryAgent) ResolveBinary(context.Context) (string, error) {
	a.calls++
	return "/new/agent", nil
}
func (a *changingBinaryAgent) InvalidateBinary(domain.AgentHarness) { a.invalidations++ }

func TestBinaryPreflightReselectsOnceWithoutChangingPrompt(t *testing.T) {
	agent := &changingBinaryAgent{}
	manager := &Manager{lookPath: func(path string) (string, error) {
		if path == "/new/agent" {
			return path, nil
		}
		return "", exec.ErrNotFound
	}}
	original := []string{"env", "MODE=review", "/old/agent", "--prompt", "/old/agent"}
	got, err := manager.validateOrResolveAgentBinary(context.Background(), agent, domain.HarnessCodex, original)
	want := []string{"env", "MODE=review", "/new/agent", "--prompt", "/old/agent"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%v err=%v", got, err)
	}
	if agent.calls != 1 || agent.invalidations != 1 {
		t.Fatalf("resolutions=%d invalidations=%d", agent.calls, agent.invalidations)
	}
	if original[2] != "/old/agent" {
		t.Fatal("mutated original command")
	}
	if _, err = manager.validateOrResolveAgentBinary(context.Background(), agent, domain.HarnessCodex, got); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 {
		t.Fatal("valid executable caused additional resolution")
	}
}
