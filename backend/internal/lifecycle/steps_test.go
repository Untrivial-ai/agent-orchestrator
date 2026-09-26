package lifecycle

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestStepsPairPreWithPostByToolUseID(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = working("mer-1")
	ctx := context.Background()
	pre := func(id, tool string) {
		t.Helper()
		if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Event: "pre-tool-use", ToolName: tool, ToolUseID: id}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(id, tool, event string) {
		t.Helper()
		if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Event: event, ToolName: tool, ToolUseID: id}); err != nil {
			t.Fatal(err)
		}
	}
	pre("t1", "Bash")
	pre("t2", "Read")
	post("t2", "Read", "post-tool-use")
	post("t1", "Bash", "post-tool-use-failure")
	post("t9", "Bash", "post-tool-use") // unmatched post: ignored

	steps := m.Steps("mer-1")
	if len(steps) != 2 {
		t.Fatalf("steps = %+v", steps)
	}
	if steps[0].Tool != "Bash" || steps[0].EndedAt.IsZero() || !steps[0].Failed {
		t.Fatalf("first step = %+v", steps[0])
	}
	if steps[1].Tool != "Read" || steps[1].EndedAt.IsZero() || steps[1].Failed {
		t.Fatalf("second step = %+v", steps[1])
	}
	if m.Steps("other") != nil && len(m.Steps("other")) != 0 {
		t.Fatal("unknown session must have no steps")
	}
}

func TestStepsRingKeepsOnlyTheNewest(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = working("mer-1")
	ctx := context.Background()
	for i := 0; i < stepRingSize+5; i++ {
		if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
			Valid: true, State: domain.ActivityActive, Event: "pre-tool-use", ToolName: "Read",
			ToolUseID: fmt.Sprintf("t%d", i), Timestamp: time.Unix(int64(i), 0),
		}); err != nil {
			t.Fatal(err)
		}
	}
	steps := m.Steps("mer-1")
	if len(steps) != stepRingSize || steps[0].ToolUseID != "t5" {
		t.Fatalf("ring = %d entries, first %q", len(steps), steps[0].ToolUseID)
	}
}
