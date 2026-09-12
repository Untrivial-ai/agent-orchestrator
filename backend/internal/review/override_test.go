package review

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestTriggerRejectsAnUnknownHarnessOverride(t *testing.T) {
	eng := New(Deps{})

	_, err := eng.Trigger(context.Background(), "mer-1", "not-a-reviewer", domain.AgentConfig{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}
