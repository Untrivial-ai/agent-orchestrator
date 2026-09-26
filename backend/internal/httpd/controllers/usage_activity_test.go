package controllers

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionActivityResponseSplitsCurrentFromRecentNewestFirst(t *testing.T) {
	at := func(sec int) time.Time { return time.Date(2026, 9, 22, 12, 0, sec, 0, time.UTC) }
	var steps []domain.SessionStep
	for i := 0; i < 8; i++ {
		steps = append(steps, domain.SessionStep{Tool: string(rune('a' + i)), StartedAt: at(i), EndedAt: at(i + 1)})
	}
	steps = append(steps, domain.SessionStep{Tool: "Bash", StartedAt: at(20)})

	out := sessionActivityResponse(steps)
	if out == nil || out.Current == nil || out.Current.Tool != "Bash" || out.Current.EndedAt != nil {
		t.Fatalf("current = %+v", out)
	}
	if len(out.Recent) != recentStepsShown || out.Recent[0].Tool != "h" || out.Recent[4].Tool != "d" {
		t.Fatalf("recent = %+v", out.Recent)
	}
	if out.Recent[0].EndedAt == nil || !out.Recent[0].EndedAt.Equal(at(8)) {
		t.Fatalf("recent[0].endedAt = %v", out.Recent[0].EndedAt)
	}
	if sessionActivityResponse(nil) != nil {
		t.Fatal("no steps must yield no activity, not an empty one")
	}
}
