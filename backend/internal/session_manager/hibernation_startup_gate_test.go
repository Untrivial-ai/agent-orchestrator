package sessionmanager

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestHibernatedResumeWaitsForPersistentHostReconcile(t *testing.T) {
	started := make(chan struct{}, 1)
	launcher := &recordingLauncher{beforeStart: func(ChatStart) { started <- struct{}{} }}
	mgr, store, _ := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityIdle)
	rec := store.sessions["mer-1"]
	at := time.Now().UTC()
	rec.HibernatedAt = &at
	store.sessions[rec.ID] = rec

	reconciled := make(chan struct{})
	mgr.SetPersistentHostReconcileDone(reconciled)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- mgr.WakeHibernatedChat(ctx, rec.ID) }()

	deadline := time.After(time.Second)
	for {
		mgr.agentOpMu.Lock()
		operation := mgr.agentOperations[rec.ID]
		mgr.agentOpMu.Unlock()
		if operation == agentOperationResume {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("resume finished before host reconciliation: %v", err)
		case <-deadline:
			t.Fatal("resume did not reach startup gate")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-started:
		t.Fatal("provider started before host reconciliation")
	case err := <-result:
		t.Fatalf("resume finished before host reconciliation: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if store.sessions[rec.ID].HibernatedAt == nil {
		t.Fatal("resume cleared the hibernation marker before host reconciliation")
	}

	close(reconciled)
	if err := <-result; err != nil {
		t.Fatalf("resume after host reconciliation: %v", err)
	}
	if len(launcher.started) != 1 || launcher.started[0].ProviderConversationID != rec.Metadata.ProviderConversationID {
		t.Fatalf("native resumes = %+v, want one with stored provider id", launcher.started)
	}
	if store.sessions[rec.ID].HibernatedAt != nil {
		t.Fatal("successful resume left hibernation marker set")
	}
}
