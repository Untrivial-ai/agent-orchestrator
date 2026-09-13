package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type promptConflictStore struct {
	*fakeStore
	conflict       bool
	alwaysConflict bool
}

func (s *promptConflictStore) UpdateSessionFromActivitySignal(ctx context.Context, rec domain.SessionRecord, expected int64) (bool, error) {
	if s.conflict || s.alwaysConflict {
		s.conflict = false
		current := s.sessions[rec.ID]
		current.Metadata.ConversationCheckpointState = domain.ConversationCheckpointPrompt
		current.Metadata.LatestUserPrompt = "human prompt"
		current.Revision = expected + 1
		s.sessions[rec.ID] = current
		return false, nil
	}
	return s.fakeStore.UpdateSessionFromActivitySignal(ctx, rec, expected)
}

func TestActivityProjectionExhaustionReturnsError(t *testing.T) {
	s := &promptConflictStore{fakeStore: newFakeStore(), alwaysConflict: true}
	s.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", Mode: domain.SessionModeTUI}
	m := New(s, nil)
	err := m.ApplyActivitySignal(context.Background(), "mer-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "pre-tool", ToolUseID: "tool-1",
	})
	if err == nil || !strings.Contains(err.Error(), "exhausted 4 attempts") {
		t.Fatalf("contention must not acknowledge a lost signal: %v", err)
	}
	if m.flights["mer-1"] != nil {
		t.Fatalf("rejected projection leaked tool state: %+v", m.flights["mer-1"])
	}
}

func TestActivityProjectionRetryUsesOriginalStopSignal(t *testing.T) {
	s := &promptConflictStore{fakeStore: newFakeStore(), conflict: true}
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	s.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now}, UpdatedAt: now,
		Metadata: domain.SessionMetadata{
			RuntimeLaunchID: "launch-1", AgentSessionID: "native-1", AgentSessionIDLaunchID: "launch-1",
			ConversationCheckpointState:      domain.ConversationCheckpointCoordination,
			ConversationCheckpointGeneration: "launch-1", ConversationCheckpointNativeID: "native-1",
		},
	}
	m := New(s, nil)
	if err := m.ApplyActivitySignal(context.Background(), "mer-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityIdle, Event: "stop", Timestamp: now.Add(time.Second),
		LaunchID: "launch-1", AgentSessionID: "native-1", LatestAssistantUpdate: "human answer",
	}); err != nil {
		t.Fatal(err)
	}
	after := s.sessions["mer-1"].Metadata
	if s.conflict || after.ConversationCheckpointState != domain.ConversationCheckpointComplete || after.LatestAssistantUpdate != "human answer" {
		t.Fatalf("retry lost original Stop payload: conflict=%v checkpoint=%+v", s.conflict, after)
	}
}
