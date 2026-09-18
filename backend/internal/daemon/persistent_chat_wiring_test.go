package daemon

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestPersistentChatHostKeepSetUsesDurableOwnership(t *testing.T) {
	records := []domain.SessionRecord{
		{ID: "live-chat", Mode: domain.SessionModeChat, Harness: domain.HarnessOpenCode},
		{ID: "terminated-chat", Mode: domain.SessionModeChat, Harness: domain.HarnessOpenCode, IsTerminated: true},
		{ID: "tui", Mode: domain.SessionModeTUI, Harness: domain.HarnessOpenCode},
		{ID: "other-provider", Mode: domain.SessionModeChat, Harness: domain.HarnessOpenCode},
	}
	keep := persistentChatHostKeepSet(records)
	if len(keep) != 2 {
		t.Fatalf("keep = %v, want both live Chat providers", keep)
	}
	if _, ok := keep["live-chat"]; !ok {
		t.Fatalf("keep = %v, missing live-chat", keep)
	}
	if _, ok := keep["other-provider"]; !ok {
		t.Fatalf("keep = %v, missing other-provider", keep)
	}
}
