package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRegisterImportDoesNotLaunchOrRequireAuthentication(t *testing.T) {
	st := newFakeStore()
	st.projects["p"] = domain.ProjectRecord{ID: "p", Path: "/project"}
	readiness := &fakeAgentReadiness{err: fmt.Errorf("no account signed in")}
	// No manager is supplied: creating a worktree or controller would panic.
	svc := NewWithDeps(Deps{Store: st, AgentReadiness: readiness})
	for i := range 180 {
		id := fmt.Sprintf("native-%d", i)
		session, _, _, err := svc.RegisterImport(context.Background(), ports.SpawnConfig{
			ProjectID: "p", Harness: domain.HarnessCodex, DisplayName: id,
			ResumeNativeSession: &ports.ResumeNativeSession{NativeSessionID: id, TranscriptPath: "/history/" + id + ".jsonl"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if session.Metadata.WorkspacePath != "" || session.Metadata.RuntimeHandleID != "" || session.Metadata.ControllerGeneration != "" {
			t.Fatal("import launched resources")
		}
		if session.Metadata.ProviderConversationID != id || session.Metadata.NativeTranscriptPath == "" {
			t.Fatal("history binding was not preserved")
		}
	}
	if readiness.calls != 0 || len(st.sessions) != 180 {
		t.Fatalf("auth calls=%d sessions=%d", readiness.calls, len(st.sessions))
	}
}

func TestRegisterImportRejectsMissingProjectBeforeWriting(t *testing.T) {
	st := newFakeStore()
	svc := NewWithDeps(Deps{Store: st, Clock: time.Now})
	_, _, _, err := svc.RegisterImport(context.Background(), ports.SpawnConfig{ProjectID: "missing"})
	if err == nil || len(st.sessions) != 0 {
		t.Fatal("invalid import created a row")
	}
}

func TestRegisterImportAllowsDormantStandaloneHistory(t *testing.T) {
	st := newFakeStore()
	svc := NewWithDeps(Deps{Store: st, Clock: time.Now})
	session, _, _, err := svc.RegisterImport(context.Background(), ports.SpawnConfig{
		Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode, DisplayName: "Imported history",
		ResumeNativeSession: &ports.ResumeNativeSession{
			Provider: domain.HarnessClaudeCode, NativeSessionID: "native-standalone", TranscriptPath: "/history/standalone.jsonl",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ProjectID != "" || !session.IsStandalone() || session.Metadata.WorkspacePath != "" {
		t.Fatalf("standalone dormant import = %+v", session.SessionRecord)
	}
	if session.Metadata.ProviderConversationID != "native-standalone" || len(st.sessions) != 1 {
		t.Fatalf("standalone import identity was not preserved: %+v", session.SessionRecord)
	}
}

func TestRegisterImportRejectsStandaloneOrchestrator(t *testing.T) {
	st := newFakeStore()
	svc := NewWithDeps(Deps{Store: st, Clock: time.Now})
	_, _, _, err := svc.RegisterImport(context.Background(), ports.SpawnConfig{
		Kind:                domain.KindOrchestrator,
		ResumeNativeSession: &ports.ResumeNativeSession{NativeSessionID: "native", TranscriptPath: "/history/native.jsonl"},
	})
	if err == nil || len(st.sessions) != 0 {
		t.Fatalf("standalone orchestrator import err=%v sessions=%d", err, len(st.sessions))
	}
}
