package sessionmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestImportedHistoryResumeFailurePreservesHistoryAndReusesWorkspace(t *testing.T) {
	launcher := &recordingLauncher{startErr: errors.New("provider unavailable")}
	m, st, _ := newChatManager(launcher)
	ws := m.workspace.(*fakeWorkspace)
	rec := domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity: domain.Activity{State: domain.ActivityExited},
		Metadata: domain.SessionMetadata{
			ProviderConversationID: "native-1",
			NativeTranscriptPath:   "/home/user/.codex/archived_sessions/history.jsonl",
		},
	}
	st.sessions[rec.ID] = rec
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); err == nil {
		t.Fatal("expected provider startup failure")
	}
	got := st.sessions[rec.ID]
	if got.IsTerminated || got.Metadata.NativeTranscriptPath != rec.Metadata.NativeTranscriptPath || got.Metadata.ProviderConversationID != "native-1" {
		t.Fatalf("failed resume lost imported history: %+v", got)
	}
	if len(ws.createBranches) != 1 || got.Metadata.WorkspacePath == "" {
		t.Fatalf("explicit resume must prepare one owned workspace: %+v, %v", got.Metadata, ws.createBranches)
	}
	launcher.startErr = nil
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); err != nil {
		t.Fatalf("retry resume: %v", err)
	}
	if len(ws.createBranches) != 1 || len(launcher.turns) != 0 {
		t.Fatalf("retry duplicated workspace or sent a prompt: branches=%v, turns=%v", ws.createBranches, launcher.turns)
	}
}
