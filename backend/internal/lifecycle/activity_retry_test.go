package lifecycle

import (
	"context"
	"errors"
	"reflect"
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
	writeErr       error
}

func (s *promptConflictStore) UpdateSessionFromActivitySignal(ctx context.Context, rec domain.SessionRecord, expected int64) (bool, error) {
	if s.writeErr != nil {
		return false, s.writeErr
	}
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

func TestActivityProjectionFailurePreservesBlockedToolCorrelation(t *testing.T) {
	s := &promptConflictStore{fakeStore: newFakeStore(), writeErr: errors.New("write failed")}
	s.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityBlocked}}
	m := New(s, nil)
	m.flights["mer-1"] = &toolFlight{inflight: map[string]string{"tool-1": "Bash"}, blockedCandidate: "tool-1"}
	before := cloneToolFlight(m.flights["mer-1"])
	err := m.ApplyActivitySignal(context.Background(), "mer-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "post-tool-use", ToolName: "Bash", ToolUseID: "tool-1",
	})
	if !errors.Is(err, s.writeErr) || !reflect.DeepEqual(before, m.flights["mer-1"]) {
		t.Fatalf("failed projection lost blocked correlation: error=%v flight=%+v", err, m.flights["mer-1"])
	}
}

func TestActivityProjectionExhaustionReturnsError(t *testing.T) {
	s := &promptConflictStore{fakeStore: newFakeStore(), alwaysConflict: true}
	s.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", Mode: domain.SessionModeTUI}
	m := New(s, nil)
	err := m.ApplyActivitySignal(context.Background(), "mer-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "pre-tool-use", ToolName: "Bash", ToolUseID: "tool-1",
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
