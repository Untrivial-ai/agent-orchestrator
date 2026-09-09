package controllers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionStartupViewExposesRecoveryReasonOnly(t *testing.T) {
	session := domain.Session{SessionRecord: domain.SessionRecord{Metadata: domain.SessionMetadata{
		Startup: &domain.SessionStartup{
			ID: "attempt-1", Stage: "cleanup_pending", StartedAt: time.Now(), LastError: "runtime exit not confirmed",
			RuntimePossible: true, Worktrees: []domain.StartupWorktree{{Path: "/private/worktree"}},
		},
	}}}
	view := sessionView(session)
	if view.Startup == nil || view.Startup.ID != "attempt-1" || view.Startup.LastError != "runtime exit not confirmed" {
		t.Fatalf("startup = %+v", view.Startup)
	}
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "runtimePossible") || strings.Contains(string(data), "/private/worktree") {
		t.Fatalf("internal startup ownership exposed: %s", data)
	}
	session.Metadata.Startup = nil
	data, err = json.Marshal(sessionView(session))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"startup"`) {
		t.Fatalf("finished startup not omitted: %s", data)
	}
}
