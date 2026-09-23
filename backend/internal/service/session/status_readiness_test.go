package session

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type readinessCommander struct {
	fakeCommander
	revision uint64
}

func (c *readinessCommander) StatusRecoveryRevision() uint64                     { return c.revision }
func (c *readinessCommander) SessionStatusReadiness(domain.SessionRecord) string { return "ready" }

type recoveryDuringReadStore struct {
	*fakeStore
	onRead func()
}

func (s *recoveryDuringReadStore) ListPRFactsForSessions(ctx context.Context, ids []domain.SessionID) (map[domain.SessionID][]domain.PRFacts, error) {
	if s.onRead != nil {
		s.onRead()
		s.onRead = nil
	}
	return s.fakeStore.ListPRFactsForSessions(ctx, ids)
}
func (s *recoveryDuringReadStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	rec, found, err := s.fakeStore.GetSession(ctx, id)
	if s.onRead != nil {
		s.onRead()
		s.onRead = nil
	}
	return rec, found, err
}

func TestRecoveryCannotPublishPreRecoverySnapshotAsReady(t *testing.T) {
	for _, operation := range []string{"list", "get"} {
		t.Run(operation, func(t *testing.T) {
			manager := &readinessCommander{}
			st := &recoveryDuringReadStore{fakeStore: newFakeStore(), onRead: func() { manager.revision++ }}
			st.sessions["s1"] = domain.SessionRecord{ID: "s1", ProjectID: "p1"}
			svc := NewWithDeps(Deps{Manager: manager, Store: st})
			read := func() domain.Session {
				t.Helper()
				if operation == "get" {
					session, err := svc.Get(context.Background(), "s1")
					if err != nil {
						t.Fatal(err)
					}
					return session
				}
				sessions, err := svc.List(context.Background(), ListFilter{})
				if err != nil {
					t.Fatal(err)
				}
				if len(sessions) != 1 {
					t.Fatalf("sessions = %d", len(sessions))
				}
				return sessions[0]
			}
			if got := read().StatusReadiness; got != "checking" {
				t.Fatalf("racing snapshot = %s", got)
			}
			if got := read().StatusReadiness; got != "ready" {
				t.Fatalf("fresh snapshot = %s", got)
			}
		})
	}
}
