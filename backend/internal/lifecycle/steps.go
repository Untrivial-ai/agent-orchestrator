package lifecycle

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// stepRingSize is how many tool steps a session keeps. The window shows a
// handful; the rest is slack for posts that arrive out of order.
const stepRingSize = 20

// recordStep folds a tool-use hook into the session's step ring: a pre opens
// a step, the matching post closes it. Steps are a live reading like memory,
// held in memory only; a daemon restart starts them empty. Must hold m.mu.
func (m *Manager) recordStepLocked(id domain.SessionID, s ports.ActivitySignal, now time.Time) {
	if s.ToolName == "" {
		return
	}
	switch {
	case s.Event == "pre-tool-use":
		m.steps[id] = append(m.steps[id], domain.SessionStep{
			ToolUseID: s.ToolUseID,
			Tool:      s.ToolName,
			StartedAt: timeOr(s.Timestamp, now),
		})
		if steps := m.steps[id]; len(steps) > stepRingSize {
			m.steps[id] = steps[len(steps)-stepRingSize:]
		}
	case isPostToolUseEvent(s.Event):
		if s.ToolUseID == "" {
			return
		}
		steps := m.steps[id]
		for i := len(steps) - 1; i >= 0; i-- {
			if steps[i].ToolUseID != s.ToolUseID || !steps[i].EndedAt.IsZero() {
				continue
			}
			steps[i].EndedAt = timeOr(s.Timestamp, now)
			steps[i].Failed = s.Event != "post-tool-use"
			return
		}
	}
}

// Steps returns a session's recent tool steps, oldest first. Empty for a
// harness that emits no tool hooks.
func (m *Manager) Steps(id domain.SessionID) []domain.SessionStep {
	m.mu.Lock()
	defer m.mu.Unlock()
	steps := m.steps[id]
	out := make([]domain.SessionStep, len(steps))
	copy(out, steps)
	return out
}
