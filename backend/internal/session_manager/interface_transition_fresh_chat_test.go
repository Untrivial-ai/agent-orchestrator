package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type freshChatTransitionStore struct {
	*transitionStore
	conversation domain.ConversationRecord
	branch       domain.ConversationBranch
	historyErr   error
	hasTurns     bool
}

func (s *freshChatTransitionStore) ConversationForSession(context.Context, domain.SessionID) (domain.ConversationRecord, error) {
	return s.conversation, s.historyErr
}

func (s *freshChatTransitionStore) ConversationBranch(context.Context, string, string) (domain.ConversationBranch, error) {
	return s.branch, s.historyErr
}

func (s *freshChatTransitionStore) HasConversationTurns(context.Context, string) (bool, error) {
	return s.hasTurns, s.historyErr
}

func withFreshChatHistory(manager *Manager, store *transitionStore) *freshChatTransitionStore {
	rec := store.sessions["session-1"]
	history := &freshChatTransitionStore{
		transitionStore: store,
		conversation:    domain.ConversationRecord{ID: "conversation-1", SessionID: rec.ID, ActiveBranchID: "branch-1"},
		branch: domain.ConversationBranch{ID: "branch-1", ConversationID: "conversation-1", SessionID: rec.ID,
			ProviderConversationID: rec.Metadata.ProviderConversationID},
	}
	manager.store = history
	return history
}

func TestInterfaceTransitionUnpromptedChatWithoutNativeID(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeChat)
	// Chat's durable empty-root proof does not need a provider file probe.
	manager.agents = singleAgent{agent: transitionAgent{}}
	rec := store.sessions["session-1"]
	rec.Metadata.AgentSessionID = ""
	rec.Metadata.ProviderConversationID = ""
	store.sessions[rec.ID] = rec
	withFreshChatHistory(manager, store)
	status, err := manager.InterfaceTransitionStatus(context.Background(), rec.ID)
	if err != nil || !status.Supported {
		t.Fatalf("untouched Chat status = %+v, err=%v", status, err)
	}
	transition, err := manager.StartInterfaceTransition(context.Background(), rec.ID,
		domain.SessionModeTUI, domain.SessionInterfaceTransitionInterrupt, domain.SessionInterfaceTransitionHistoryStrict)
	if err != nil {
		t.Fatal(err)
	}
	if settled := awaitTransition(t, store, transition.ID); settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("switch = %s (%s): %s", settled.Phase, settled.ErrorCode, settled.ErrorDetail)
	}
}

func TestInterfaceTransitionChatRequiresUntouchedConversationProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*freshChatTransitionStore)
	}{
		{"accepted message or activity", func(s *freshChatTransitionStore) { s.conversation.LatestSequence = 1 }},
		{"provider turn without text", func(s *freshChatTransitionStore) { s.hasTurns = true }},
		{"history read failed", func(s *freshChatTransitionStore) { s.historyErr = errors.New("database unavailable") }},
		{"different owner", func(s *freshChatTransitionStore) { s.conversation.SessionID = "session-2" }},
		{"different branch owner", func(s *freshChatTransitionStore) { s.branch.SessionID = "session-2" }},
		{"different provider", func(s *freshChatTransitionStore) { s.branch.ProviderConversationID = "other-native" }},
		{"branch with inherited context", func(s *freshChatTransitionStore) { s.branch.ParentBranchID = "parent" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeChat)
			manager.agents = singleAgent{agent: emptyTransitionAgent{}}
			tc.mutate(withFreshChatHistory(manager, store))
			_, err := manager.StartInterfaceTransition(context.Background(), "session-1",
				domain.SessionModeTUI, domain.SessionInterfaceTransitionInterrupt, domain.SessionInterfaceTransitionHistoryStrict)
			if !errors.Is(err, ErrNativeConversationMissing) {
				t.Fatalf("switch without untouched proof = %v", err)
			}
			if len(store.transitions) != 0 || runtime.created != 0 || chat.preparedPolicy != "" || len(*log) != 0 {
				t.Fatalf("refusal mutated source: transitions=%d log=%v", len(store.transitions), *log)
			}
		})
	}
}

func TestInterfaceTransitionChatWithoutAnyConversationStartsFresh(t *testing.T) {
	// A session switched into Chat but never messaged has no conversation row
	// at all: the Chat controller materializes it on the first turn. That is
	// the freshest possible state and must not fail the Terminal handoff with
	// NATIVE_SESSION_MISSING (issue #5482 reproduction).
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeChat)
	manager.agents = singleAgent{agent: emptyTransitionAgent{}}
	rec := store.sessions["session-1"]
	rec.Metadata.ProviderConversationID = "019fc430-1234-7abc-8def-0123456789ab"
	store.sessions[rec.ID] = rec
	withFreshChatHistory(manager, store).historyErr = domain.ErrNoConversation

	status, err := manager.InterfaceTransitionStatus(context.Background(), rec.ID)
	if err != nil || !status.Supported {
		t.Fatalf("never-messaged Chat status = %+v, err=%v", status, err)
	}
	transition, err := manager.StartInterfaceTransition(context.Background(), rec.ID,
		domain.SessionModeTUI, domain.SessionInterfaceTransitionInterrupt, domain.SessionInterfaceTransitionHistoryStrict)
	if err != nil {
		t.Fatalf("Chat-to-terminal switch without a conversation row was refused: %v", err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("switch = %s (%s): %s", settled.Phase, settled.ErrorCode, settled.ErrorDetail)
	}
	if settled.NativeConversationID != "" || chat.start.ProviderConversationID != "" {
		t.Fatalf("reserved id leaked into fresh handoff: %q / %q",
			settled.NativeConversationID, chat.start.ProviderConversationID)
	}
	if runtime.created != 1 || strings.Contains(strings.Join(runtime.lastCfg.Argv, " "), "resume") {
		t.Fatalf("expected one fresh terminal launch: %+v", runtime.lastCfg)
	}
	if got := fmt.Sprint(*log); got != "[prepare:chat:interrupt stop:chat start:tui]" {
		t.Fatalf("controller order = %s", got)
	}
}

type racingFreshChat struct {
	*transitionChat
	beforePrepare func()
}

func (c *racingFreshChat) PrepareChatHandoff(ctx context.Context, id domain.SessionID, policy domain.SessionInterfaceTransitionPolicy) error {
	c.beforePrepare()
	return c.transitionChat.PrepareChatHandoff(ctx, id, policy)
}

func TestInterfaceTransitionFreshChatRechecksAfterFencing(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeChat)
	manager.agents = singleAgent{agent: emptyTransitionAgent{}}
	history := withFreshChatHistory(manager, store)
	manager.chat = &racingFreshChat{transitionChat: chat, beforePrepare: func() {
		// Message intake won the race with ArmChatHandoff. Its durable sequence
		// remains even if interrupt settles it before a native transcript exists.
		history.conversation.LatestSequence = 1
	}}
	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeTUI, domain.SessionInterfaceTransitionInterrupt, domain.SessionInterfaceTransitionHistoryStrict)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "NATIVE_SESSION_MISSING" {
		t.Fatalf("switch after accepted message = %s (%s)", settled.Phase, settled.ErrorCode)
	}
	if runtime.created != 0 || fmt.Sprint(*log) != "[prepare:chat:interrupt]" {
		t.Fatalf("source stopped after losing untouched proof: %v", *log)
	}
}
