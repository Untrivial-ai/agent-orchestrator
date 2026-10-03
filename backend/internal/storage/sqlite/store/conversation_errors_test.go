package store_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	sqlitedriver "modernc.org/sqlite"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type conversationQueryFailure struct {
	query string
	err   error
}

type conversationQueryFailureKey struct{}

// Fail only the named query, including inside a real SQLite transaction. Other
// queries still reach the migrated database so swallowed failures can commit
// orphan projections on the broken implementation.
type conversationErrorConnector struct{ path string }

func (c conversationErrorConnector) Driver() driver.Driver { return &sqlitedriver.Driver{} }

func (c conversationErrorConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.path)
	if err != nil {
		return nil, err
	}
	return conversationErrorConn{Conn: conn}, nil
}

type conversationErrorConn struct{ driver.Conn }

func (c conversationErrorConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if failure, ok := ctx.Value(conversationQueryFailureKey{}).(conversationQueryFailure); ok &&
		strings.HasPrefix(query, "-- name: "+failure.query+" ") {
		return nil, failure.err
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func conversationErrorFixture(t *testing.T) (*store.Store, domain.SessionID, string) {
	t.Helper()
	dir := t.TempDir()
	migrated, err := sqlitetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	connector := conversationErrorConnector{path: filepath.Join(dir, "ao.db") + "?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)"}
	writer, reader := sql.OpenDB(connector), sql.OpenDB(connector)
	writer.SetMaxOpenConns(1)
	s := store.NewStore(writer, reader)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	seedProject(t, s, "query-errors")
	record := sampleRecord("query-errors")
	record.Mode = domain.SessionModeChat
	record.Metadata.ControllerGeneration = "generation"
	session, err := s.CreateSession(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := s.CreateConversation(context.Background(), "conversation", domain.ConversationScopeSession,
		"query-errors", session.ID, histClock)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.AppendUserMessage(context.Background(), conversation.ID, session.ID, "generation",
		domain.ConversationMessage{ID: "prompt", Text: "hello", Origin: domain.MessageOriginHuman}, "turn", histClock)
	if err != nil || !created {
		t.Fatalf("seed prompt: created=%v err=%v", created, err)
	}
	if err := s.BindTurnToProvider(context.Background(), "turn", "provider-turn", histClock); err != nil {
		t.Fatal(err)
	}
	return s, session.ID, conversation.ID
}

func TestConversationSnapshotPropagatesHistoryQueryErrors(t *testing.T) {
	for _, query := range []string{"SelectConversationBranches", "SelectConversationNativeForkAvailableAfterSequence"} {
		for _, paged := range []bool{false, true} {
			name := "whole/"
			if paged {
				name = "page/"
			}
			t.Run(name+query, func(t *testing.T) {
				s, _, conversation := conversationErrorFixture(t)
				for _, queryErr := range []error{errors.New("injected SQLite query failure"), context.Canceled, context.DeadlineExceeded} {
					ctx := context.WithValue(context.Background(), conversationQueryFailureKey{}, conversationQueryFailure{query, queryErr})
					var snapshot store.ConversationSnapshot
					var err error
					if paged {
						snapshot, err = s.LoadConversationSnapshotPage(ctx, conversation, 0, 1)
					} else {
						snapshot, err = s.LoadConversationSnapshot(ctx, conversation)
					}
					if !errors.Is(err, queryErr) {
						t.Errorf("snapshot error = %v, want %v", err, queryErr)
					}
					if snapshot.Conversation.ID != "" || len(snapshot.Messages) != 0 {
						t.Errorf("query failure returned conversation %q with %d messages", snapshot.Conversation.ID, len(snapshot.Messages))
					}
				}
			})
		}
	}
}

func writeConversationErrorProjection(ctx context.Context, s *store.Store, conversation, providerTurn, kind string) error {
	switch kind {
	case "delta":
		return s.AppendAssistantDelta(ctx, conversation, "provider-item", providerTurn, "answer", "answer", histClock)
	case "settled":
		return s.SettleAssistantMessage(ctx, conversation, "provider-item", providerTurn, "answer", "answer", histClock)
	default:
		return s.UpsertActivity(ctx, conversation, providerTurn, domain.ConversationActivity{
			ID: "activity", ProviderItemID: "provider-item", Kind: domain.ActivityKindCommand,
			Status: domain.ActivityStatusRunning, Summary: "go test", Detail: []byte(`{}`),
		}, histClock)
	}
}

func TestConversationProjectionPropagatesTurnQueryErrors(t *testing.T) {
	for _, kind := range []string{"delta", "settled", "activity"} {
		for _, archived := range []bool{false, true} {
			name := kind + "/direct"
			if archived {
				name = kind + "/archived"
			}
			t.Run(name, func(t *testing.T) {
				for _, queryErr := range []error{errors.New("injected SQLite turn lookup failure"), context.Canceled, context.DeadlineExceeded} {
					s, session, conversation := conversationErrorFixture(t)
					ctx := context.WithValue(context.Background(), conversationQueryFailureKey{}, conversationQueryFailure{
						query: "SelectConversationTurnByProviderID", err: queryErr,
					})
					project := func(ctx context.Context) error {
						return writeConversationErrorProjection(ctx, s, conversation, "provider-turn", kind)
					}
					var err error
					if archived {
						var applied bool
						applied, err = s.ProjectProviderEvent(ctx, conversation, session, "generation", "event", "item", `{}`, histClock, project)
						if applied {
							t.Error("failed projection was reported as applied")
						}
					} else {
						err = project(ctx)
					}
					if !errors.Is(err, queryErr) {
						t.Errorf("projection error = %v, want %v", err, queryErr)
					}
					snapshot, err := s.LoadConversationSnapshot(context.Background(), conversation)
					if err != nil {
						t.Fatal(err)
					}
					if len(snapshot.Messages) != 1 || len(snapshot.Activities) != 0 || snapshot.Conversation.LatestSequence != 1 {
						t.Errorf("failed lookup left %d messages, %d activities, sequence %d; want 1/0/1",
							len(snapshot.Messages), len(snapshot.Activities), snapshot.Conversation.LatestSequence)
					}
					events, err := s.ProviderEventsSince(context.Background(), conversation, 0, 10)
					if err != nil || len(events) != 0 {
						t.Errorf("failed projection archive = %+v, %v; want empty", events, err)
					}
				}
			})
		}
	}
}

func TestConversationProjectionPreservesTurnAssociation(t *testing.T) {
	for _, kind := range []string{"delta", "settled", "activity"} {
		for _, tc := range []struct{ name, providerTurn, wantTurn string }{
			{"known", "provider-turn", "turn"},
			{"unknown", "unknown-provider-turn", ""},
			{"empty", "", ""},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				s, session, conversation := conversationErrorFixture(t)
				ctx := context.Background()
				if tc.providerTurn == "" {
					// Empty IDs need no lookup, even if that query is unavailable.
					ctx = context.WithValue(ctx, conversationQueryFailureKey{}, conversationQueryFailure{
						query: "SelectConversationTurnByProviderID", err: errors.New("unexpected turn lookup"),
					})
				}
				applied, err := s.ProjectProviderEvent(ctx, conversation, session, "generation", "event", "item", `{}`, histClock,
					func(ctx context.Context) error {
						return writeConversationErrorProjection(ctx, s, conversation, tc.providerTurn, kind)
					})
				if err != nil || !applied {
					t.Fatalf("projection: applied=%v err=%v", applied, err)
				}
				snapshot, err := s.LoadConversationSnapshot(context.Background(), conversation)
				if err != nil {
					t.Fatal(err)
				}
				var turnID string
				if kind == "activity" {
					if len(snapshot.Activities) != 1 {
						t.Fatalf("activities = %+v, want one", snapshot.Activities)
					}
					turnID = snapshot.Activities[0].TurnID
				} else {
					if len(snapshot.Messages) != 2 {
						t.Fatalf("messages = %+v, want prompt and answer", snapshot.Messages)
					}
					turnID = snapshot.Messages[1].TurnID
				}
				if turnID != tc.wantTurn {
					t.Errorf("projection turn = %q, want %q", turnID, tc.wantTurn)
				}
				events, err := s.ProviderEventsSince(context.Background(), conversation, 0, 10)
				if err != nil || len(events) != 1 {
					t.Errorf("projection archive = %+v, %v; want one", events, err)
				}
			})
		}
	}
}
