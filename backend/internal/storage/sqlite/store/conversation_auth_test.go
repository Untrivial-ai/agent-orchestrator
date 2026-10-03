package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestPersistedAuthenticationDemandReconcilesOnlyVerifiedCurrentSuccess(t *testing.T) {
	for _, scenario := range []string{"current", "old-generation", "started-before-failure", "child-thread", "missing-authoritative-completion", "newer-failure", "account-change"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			s, err := sqlitetest.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			seedProject(t, s, "auth")
			record := sampleRecord("auth")
			record.Mode = domain.SessionModeChat
			session, err := s.CreateSession(ctx, record)
			if err != nil {
				t.Fatal(err)
			}
			conversation, err := s.CreateConversation(ctx, "auth-conv", domain.ConversationScopeSession, "auth", session.ID, histClock)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ClaimChatControllerGeneration(ctx, session.ID, "gen-1"); err != nil {
				t.Fatal(err)
			}
			failureAt := histClock.Add(time.Second)
			account := domain.ConversationAccount{ReauthRequiredAt: &failureAt, ReauthReason: "expired", PlanLabel: "Pro"}
			if err := s.RecordAccount(ctx, conversation.ID, account, failureAt); err != nil {
				t.Fatal(err)
			}
			startAt := failureAt.Add(time.Second)
			if scenario == "started-before-failure" {
				startAt = histClock
			}
			if err := s.AdoptProviderTurn(ctx, conversation.ID, session.ID, "gen-1", "turn", "provider-turn", startAt); err != nil {
				t.Fatal(err)
			}
			completedAt := startAt.Add(3 * time.Second)
			nativeID := ""
			if scenario == "child-thread" {
				nativeID = "child"
			}
			if scenario == "missing-authoritative-completion" {
				if err := s.SettleTurn(ctx, conversation.ID, "provider-turn", domain.TurnStateCompleted, "", completedAt); err != nil {
					t.Fatal(err)
				}
			} else {
				payload := fmt.Sprintf(`{"providerTurnId":"provider-turn","providerConversationId":%q,"turnState":"completed"}`, nativeID)
				if _, err := s.ProjectProviderEvent(ctx, conversation.ID, session.ID, "gen-1", "completion", "turn.completed", payload, completedAt, func(txCtx context.Context) error {
					return s.SettleTurn(txCtx, conversation.ID, "provider-turn", domain.TurnStateCompleted, "", completedAt)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "old-generation" {
				if err := s.ClaimChatControllerGeneration(ctx, session.ID, "gen-2"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "newer-failure" {
				failureAt = completedAt.Add(time.Second)
				account.ReauthRequiredAt = &failureAt
				if err := s.RecordAccount(ctx, conversation.ID, account, failureAt); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "account-change" {
				account.AuthChangedAt = &completedAt
				if err := s.RecordAccount(ctx, conversation.ID, account, completedAt); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = sqlite.OpenPreMigrated(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			// Page loading must reconcile even if the successful turn is outside the page.
			snapshot, err := s.LoadConversationSnapshotPage(ctx, conversation.ID, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			recovered := snapshot.Conversation.Account.ReauthRequiredAt == nil
			if recovered != (scenario == "current") {
				t.Fatalf("recovered=%v, account=%+v", recovered, snapshot.Conversation.Account)
			}
			if recovered {
				persisted, err := s.ConversationAccount(ctx, conversation.ID)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.AuthenticationState != "authenticated" || persisted.LastAuthFailureReason != "expired" || persisted.PlanLabel != "Pro" {
					t.Fatalf("persisted account = %+v", persisted)
				}
				events, err := s.ProviderEventsSince(ctx, conversation.ID, 0, 10)
				if err != nil || len(events) != 1 {
					t.Fatalf("history lost: %+v, %v", events, err)
				}
			}
		})
	}
}

func TestAccountOnlyChangesInvalidateConversationThroughCDC(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	before, err := s.LatestSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account := domain.ConversationAccount{AuthenticationState: "required", ReauthRequiredAt: &histClock, AuthFailureID: "failure"}
	if err := s.RecordAccount(ctx, conversation, account, histClock); err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsAfter(ctx, before, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("account-only CDC = %+v, %v", events, err)
	}
	if events[0].SessionID != string(session) || string(events[0].Type) != "session_updated" {
		t.Fatalf("account event = %+v", events[0])
	}
	before = events[0].Seq
	if err := s.RecordAccount(ctx, conversation, account, histClock); err != nil {
		t.Fatal(err)
	}
	events, err = s.EventsAfter(ctx, before, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("unchanged account emitted CDC: %+v, %v", events, err)
	}
	account.AuthenticationState = "authenticated"
	account.ReauthRequiredAt = nil
	account.AuthVerifiedAt = &histClock
	if err := s.RecordAccount(ctx, conversation, account, histClock); err != nil {
		t.Fatal(err)
	}
	events, err = s.EventsAfter(ctx, before, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("recovery-only CDC = %+v, %v", events, err)
	}
}

func TestAuthenticationRecoveryRollsBackWithProviderProjection(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	at := histClock.Add(time.Second)
	account := domain.ConversationAccount{AuthenticationState: "required", ReauthRequiredAt: &at, AuthFailureID: "failure"}
	if err := s.RecordAccount(ctx, conversation, account, at); err != nil {
		t.Fatal(err)
	}
	start := at.Add(time.Second)
	if err := s.AdoptProviderTurn(ctx, conversation, session, "gen-1", "turn", "provider-turn", start); err != nil {
		t.Fatal(err)
	}
	completed := start.Add(time.Second)
	_, err := s.ProjectProviderEvent(ctx, conversation, session, "gen-1", "rolled-back-completion", "turn.completed", `{"providerTurnId":"provider-turn","turnState":"completed"}`, completed, func(txCtx context.Context) error {
		if err := s.SettleTurn(txCtx, conversation, "provider-turn", domain.TurnStateCompleted, "", completed); err != nil {
			return err
		}
		a, err := s.ReconcileConversationAuthentication(txCtx, conversation, "gen-1", "provider-turn", completed)
		if err != nil {
			return err
		}
		if a.ReauthRequiredAt != nil {
			t.Fatal("expected recovery within projection")
		}
		return fmt.Errorf("injected projection failure")
	})
	if err == nil {
		t.Fatal("wanted projection error")
	}
	persisted, err := s.ConversationAccount(ctx, conversation)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ReauthRequiredAt == nil || persisted.AuthFailureID != "failure" {
		t.Fatalf("rolled back recovery leaked: %+v", persisted)
	}
	events, err := s.ProviderEventsSince(ctx, conversation, 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("rolled back success archived: %+v, %v", events, err)
	}
}

func TestConcurrentRecoveryCannotClearNewerAuthenticationFailure(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	failureAt := histClock.Add(time.Second)
	if err := s.RecordAccount(ctx, conversation, domain.ConversationAccount{ReauthRequiredAt: &failureAt}, failureAt); err != nil {
		t.Fatal(err)
	}
	startAt := failureAt.Add(time.Second)
	if err := s.AdoptProviderTurn(ctx, conversation, session, "gen-1", "turn", "provider-turn", startAt); err != nil {
		t.Fatal(err)
	}
	completedAt := startAt.Add(time.Second)
	if _, err := s.ProjectProviderEvent(ctx, conversation, session, "gen-1", "completion", "turn.completed", `{"providerTurnId":"provider-turn","turnState":"completed"}`, completedAt, func(txCtx context.Context) error {
		return s.SettleTurn(txCtx, conversation, "provider-turn", domain.TurnStateCompleted, "", completedAt)
	}); err != nil {
		t.Fatal(err)
	}
	newerFailureAt := completedAt.Add(time.Second)
	ready := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-ready
		results <- s.RecordAccount(ctx, conversation, domain.ConversationAccount{AuthenticationState: "required", ReauthRequiredAt: &newerFailureAt, AuthFailureID: "newer"}, newerFailureAt)
	}()
	go func() {
		<-ready
		_, err := s.ReconcileConversationAuthentication(ctx, conversation, "gen-1", "provider-turn", completedAt)
		results <- err
	}()
	close(ready)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	account, err := s.ConversationAccount(ctx, conversation)
	if err != nil {
		t.Fatal(err)
	}
	if account.ReauthRequiredAt == nil || account.AuthFailureID != "newer" {
		t.Fatalf("old success cleared newer concurrent failure: %+v", account)
	}
}
