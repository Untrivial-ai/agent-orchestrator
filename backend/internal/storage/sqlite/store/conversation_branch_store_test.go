package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

var testNow = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

func seededChatConversation(t *testing.T) (*sqlite.Store, domain.SessionRecord, domain.ConversationRecord) {
	t.Helper()
	ctx := context.Background()
	s := newTestStore(t)
	seedProject(t, s, "branches")

	rec := sampleRecord("branches")
	rec.Mode = domain.SessionModeChat
	rec.Metadata.ProviderConversationID = "thread-root"
	rec.Metadata.ControllerGeneration = "generation-root"
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	conversation, err := s.CreateConversation(
		ctx, "conversation-branches", domain.ConversationScopeSession,
		"branches", session.ID, testNow,
	)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	return s, session, conversation
}

func seedBranchTurns(t *testing.T, s *sqlite.Store, session domain.SessionRecord, conversation domain.ConversationRecord) {
	t.Helper()
	ctx := context.Background()
	created, err := s.AppendUserMessage(ctx, conversation.ID, session.ID, "generation-root", domain.ConversationMessage{
		ID: "message-1", Origin: domain.MessageOriginHuman, Text: "first prompt",
		ClientMessageID: "client-1", DeliveryContentJSON: `[{"type":"text","text":"first prompt"}]`,
	}, "turn-1", testNow)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage turn-1: created=%v err=%v", created, err)
	}
	if err := s.BindTurnToProvider(ctx, "turn-1", "provider-turn-1", testNow); err != nil {
		t.Fatalf("BindTurnToProvider turn-1: %v", err)
	}
	if err := s.SettleAssistantMessage(ctx, conversation.ID, "assistant-1", "provider-turn-1", "first answer", "message-assistant-1", testNow); err != nil {
		t.Fatalf("SettleAssistantMessage turn-1: %v", err)
	}
	created, err = s.AppendUserMessage(ctx, conversation.ID, session.ID, "generation-root", domain.ConversationMessage{
		ID: "message-2", Origin: domain.MessageOriginHuman, Text: "second prompt",
		ClientMessageID: "client-2", DeliveryContentJSON: `[{"type":"text","text":"second prompt"},{"type":"image","url":"data:image/png;base64,AA=="}]`,
	}, "turn-2", testNow.Add(time.Minute))
	if err != nil || !created {
		t.Fatalf("AppendUserMessage turn-2: created=%v err=%v", created, err)
	}
}

func TestConversationBranchRootAndEditAnchorPersist(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	if conversation.ActiveBranchID != conversation.ID+":root" {
		t.Fatalf("active branch = %q, want root", conversation.ActiveBranchID)
	}

	root, err := s.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		t.Fatalf("ConversationBranch: %v", err)
	}
	if !root.Active || root.SessionID != session.ID || root.ProviderConversationID != "thread-root" {
		t.Fatalf("root branch = %+v", root)
	}

	seedBranchTurns(t, s, session, conversation)
	page, err := s.LoadConversationSnapshotPage(ctx, conversation.ID, 0, 1)
	if err != nil {
		t.Fatalf("LoadConversationSnapshotPage: %v", err)
	}
	if !page.HasMoreBefore || page.NativeForkAvailableAfterSequence != 1 {
		t.Fatalf("bounded page native fork threshold = %d hasMore=%v, want 1/true",
			page.NativeForkAvailableAfterSequence, page.HasMoreBefore)
	}
	anchor, err := s.ConversationEditAnchor(ctx, conversation.ID, "turn-2")
	if err != nil {
		t.Fatalf("ConversationEditAnchor: %v", err)
	}
	if anchor.ConversationID != conversation.ID || anchor.SourceBranchID != conversation.ActiveBranchID ||
		anchor.ReplacedTurnID != "turn-2" || anchor.PreviousProviderTurnID != "provider-turn-1" ||
		anchor.ForkAfterSequence != 2 {
		t.Fatalf("edit anchor = %+v", anchor)
	}
	wantDelivery := `[{"type":"text","text":"second prompt"},{"type":"image","url":"data:image/png;base64,AA=="}]`
	if anchor.OriginalDeliveryContentJSON != wantDelivery {
		t.Fatalf("delivery content = %q, want %q", anchor.OriginalDeliveryContentJSON, wantDelivery)
	}

	first, err := s.ConversationEditAnchor(ctx, conversation.ID, "turn-1")
	if err != nil {
		t.Fatalf("ConversationEditAnchor first prompt: %v", err)
	}
	if first.PreviousProviderTurnID != "" || first.ForkAfterSequence != 0 {
		t.Fatalf("first prompt anchor = %+v", first)
	}
}

func TestConversationBranchZeroStrategyPersistsAsNative(t *testing.T) {
	ctx := context.Background()
	s, _, conversation := seededChatConversation(t)
	root, err := s.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		t.Fatalf("ConversationBranch root: %v", err)
	}
	if root.Strategy != domain.ConversationBranchStrategyNative {
		t.Fatalf("root strategy = %q, want native", root.Strategy)
	}
	child := domain.ConversationBranch{
		ID:                     "branch-zero-strategy",
		ConversationID:         conversation.ID,
		ProviderConversationID: "thread-zero-strategy",
		ParentBranchID:         conversation.ActiveBranchID,
	}
	if err := s.CreateConversationBranch(ctx, child, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch: %v", err)
	}
	got, err := s.ConversationBranch(ctx, conversation.ID, child.ID)
	if err != nil {
		t.Fatalf("ConversationBranch child: %v", err)
	}
	if got.Strategy != domain.ConversationBranchStrategyNative {
		t.Fatalf("child strategy = %q, want native", got.Strategy)
	}
}

func TestConversationBranchPreservesEmptyLegacyProviderScope(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	legacy := domain.ConversationBranch{
		ID: "legacy-root", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "legacy-thread", CreatedAt: testNow,
	}
	if err := s.CreateConversationBranch(ctx, legacy, testNow); err != nil {
		t.Fatalf("CreateConversationBranch legacy root: %v", err)
	}
	got, err := s.ConversationBranch(ctx, conversation.ID, legacy.ID)
	if err != nil {
		t.Fatalf("ConversationBranch legacy root: %v", err)
	}
	if got.ProviderScopeID != "" {
		t.Fatalf("legacy provider scope = %q, want empty compatibility namespace", got.ProviderScopeID)
	}
}

func TestConversationBranchInheritsNearestExplicitProviderScope(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	seedBranchTurns(t, s, session, conversation)
	approximate := domain.ConversationBranch{
		ID:                     "branch-approximate",
		ConversationID:         conversation.ID,
		SessionID:              session.ID,
		ProviderConversationID: "thread-approximate",
		ParentBranchID:         conversation.ActiveBranchID,
		ReplacedTurnID:         "turn-2",
		ForkAfterSequence:      2,
		ProviderScopeID:        "scope-approximate",
	}
	if err := s.CreateConversationBranch(ctx, approximate, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch approximate: %v", err)
	}
	nested := domain.ConversationBranch{
		ID:                     "branch-native-under-approximate",
		ConversationID:         conversation.ID,
		SessionID:              session.ID,
		ProviderConversationID: "thread-native-under-approximate",
		ParentBranchID:         approximate.ID,
		ReplacedTurnID:         "turn-2",
		ForkAfterSequence:      2,
	}
	if err := s.CreateConversationBranch(ctx, nested, testNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch nested: %v", err)
	}

	got, err := s.ConversationBranch(ctx, conversation.ID, nested.ID)
	if err != nil {
		t.Fatalf("ConversationBranch nested: %v", err)
	}
	if got.ProviderScopeID != approximate.ProviderScopeID {
		t.Fatalf("nested provider scope = %q, want inherited %q", got.ProviderScopeID, approximate.ProviderScopeID)
	}
	if got.ProviderBindingID != conversation.ActiveBranchID {
		t.Fatalf("nested provider binding = %q, want root %q", got.ProviderBindingID, conversation.ActiveBranchID)
	}
}

func TestConversationEditAnchorSeparatesBindingReplayFloorFromOpaqueScope(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	seedBranchTurns(t, s, session, conversation)
	approximate := domain.ConversationBranch{
		ID:                     "branch-approximate-anchor",
		ConversationID:         conversation.ID,
		SessionID:              session.ID,
		ProviderConversationID: "thread-approximate-anchor",
		ParentBranchID:         conversation.ActiveBranchID,
		ReplacedTurnID:         "turn-2",
		ForkAfterSequence:      2,
		ProviderScopeID:        "scope-approximate-anchor",
	}
	if err := s.CreateConversationBranch(ctx, approximate, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch: %v", err)
	}
	activateTestBranch(t, s, session, conversation, approximate.ID, approximate.ProviderConversationID, "generation-approximate")
	appendBranchPrompt(t, s, session, conversation, "generation-approximate", "b-edited", "B edited")
	if err := s.BindTurnToProvider(ctx, "turn-b-edited", "provider-turn-b-edited", testNow); err != nil {
		t.Fatalf("BindTurnToProvider edited B: %v", err)
	}
	appendBranchPrompt(t, s, session, conversation, "generation-approximate", "c", "C")

	editedAnchor, err := s.ConversationEditAnchor(ctx, conversation.ID, "turn-b-edited")
	if err != nil {
		t.Fatalf("ConversationEditAnchor edited B: %v", err)
	}
	if editedAnchor.PreviousProviderTurnID != "" {
		t.Fatalf("edited-B previous provider turn = %q, want none from before fresh scope", editedAnchor.PreviousProviderTurnID)
	}
	if editedAnchor.ReplayFloorSequence != 0 {
		t.Fatalf("edited-B replay floor = %d, want root binding floor 0", editedAnchor.ReplayFloorSequence)
	}
	laterAnchor, err := s.ConversationEditAnchor(ctx, conversation.ID, "turn-c")
	if err != nil {
		t.Fatalf("ConversationEditAnchor C: %v", err)
	}
	if laterAnchor.PreviousProviderTurnID != "provider-turn-b-edited" {
		t.Fatalf("C previous provider turn = %q, want provider-turn-b-edited", laterAnchor.PreviousProviderTurnID)
	}
	if laterAnchor.ReplayFloorSequence != 0 {
		t.Fatalf("C replay floor = %d, want root binding floor 0", laterAnchor.ReplayFloorSequence)
	}
}

func TestConversationBranchRootLearnsFreshProviderConversationID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedProject(t, s, "fresh-branch")
	rec := sampleRecord("fresh-branch")
	rec.Mode = domain.SessionModeChat
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	conversation, err := s.CreateConversation(ctx, "fresh-conversation", domain.ConversationScopeSession,
		"fresh-branch", session.ID, testNow)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	session.Metadata.ProviderConversationID = "thread-created-after-conversation"
	session.Metadata.ControllerGeneration = "generation-fresh"
	if err := s.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession controller result: %v", err)
	}
	root, err := s.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		t.Fatalf("ConversationBranch: %v", err)
	}
	if root.ProviderConversationID != session.Metadata.ProviderConversationID {
		t.Fatalf("root provider conversation = %q, want %q",
			root.ProviderConversationID, session.Metadata.ProviderConversationID)
	}
}

func TestActivateConversationBranchMovesProviderAndGenerationTogether(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	seedBranchTurns(t, s, session, conversation)
	branch := domain.ConversationBranch{
		ID:                     "branch-child",
		ConversationID:         conversation.ID,
		ProviderConversationID: "thread-child",
		ParentBranchID:         conversation.ActiveBranchID,
		ForkAfterTurnID:        "turn-1",
		ReplacedTurnID:         "turn-2",
		ForkAfterSequence:      2,
	}
	if err := s.CreateConversationBranch(ctx, branch, testNow); err != nil {
		t.Fatalf("CreateConversationBranch: %v", err)
	}
	if err := s.ActivateConversationBranch(ctx, session.ID, conversation.ID, branch.ID, "thread-child", "generation-child", testNow); err != nil {
		t.Fatalf("ActivateConversationBranch: %v", err)
	}
	got, found, err := s.GetSession(ctx, session.ID)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	if got.Metadata.ProviderConversationID != "thread-child" || got.Metadata.ControllerGeneration != "generation-child" {
		t.Fatalf("session controller metadata = %+v", got.Metadata)
	}
	branches, err := s.ConversationBranches(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("ConversationBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %+v", branches)
	}
	byID := map[string]domain.ConversationBranch{}
	for _, gotBranch := range branches {
		byID[gotBranch.ID] = gotBranch
	}
	if byID[conversation.ActiveBranchID].Active || !byID[branch.ID].Active || byID[branch.ID].SessionID != session.ID {
		t.Fatalf("branches = %+v", branches)
	}

	created, err := s.AppendUserMessage(ctx, conversation.ID, session.ID, "generation-child", domain.ConversationMessage{
		ID: "message-replacement", Origin: domain.MessageOriginHuman, Text: "edited second prompt",
		ClientMessageID: "client-replacement",
	}, "turn-replacement", testNow.Add(2*time.Minute))
	if err != nil || !created {
		t.Fatalf("AppendUserMessage replacement: created=%v err=%v", created, err)
	}
	if err := s.UpdateConversationBranchReplacement(ctx, branch.ID, "turn-replacement"); err != nil {
		t.Fatalf("UpdateConversationBranchReplacement: %v", err)
	}
	child, err := s.ConversationBranch(ctx, conversation.ID, branch.ID)
	if err != nil || child.ReplacementTurnID != "turn-replacement" {
		t.Fatalf("child branch = %+v err=%v", child, err)
	}
}

func TestActivateConversationBranchRollsBackWhenSessionCannotMove(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	seedBranchTurns(t, s, session, conversation)
	branch := domain.ConversationBranch{
		ID: "branch-child", ConversationID: conversation.ID,
		ProviderConversationID: "thread-child", ParentBranchID: conversation.ActiveBranchID,
		ForkAfterTurnID: "turn-1", ReplacedTurnID: "turn-2", ForkAfterSequence: 2,
	}
	if err := s.CreateConversationBranch(ctx, branch, testNow); err != nil {
		t.Fatalf("CreateConversationBranch: %v", err)
	}
	session.IsTerminated = true
	if err := s.UpdateSession(ctx, session); err != nil {
		t.Fatalf("terminate session: %v", err)
	}
	if err := s.ActivateConversationBranch(ctx, session.ID, conversation.ID, branch.ID, "thread-child", "generation-child", testNow); err == nil {
		t.Fatal("ActivateConversationBranch succeeded for a terminated session")
	}
	root, err := s.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil || !root.Active {
		t.Fatalf("root after refused activation = %+v err=%v", root, err)
	}
	child, err := s.ConversationBranch(ctx, conversation.ID, branch.ID)
	if err != nil || child.Active {
		t.Fatalf("child after refused activation = %+v err=%v", child, err)
	}
}

func TestConversationBranchRejectsCrossConversationReferences(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	seedBranchTurns(t, s, session, conversation)

	otherRecord := sampleRecord("branches")
	otherRecord.Mode = domain.SessionModeChat
	otherRecord.Metadata.ProviderConversationID = "thread-other"
	otherSession, err := s.CreateSession(ctx, otherRecord)
	if err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}
	otherConversation, err := s.CreateConversation(ctx, "conversation-other", domain.ConversationScopeSession,
		"branches", otherSession.ID, testNow)
	if err != nil {
		t.Fatalf("CreateConversation other: %v", err)
	}
	created, err := s.AppendUserMessage(ctx, otherConversation.ID, otherSession.ID, "generation-other",
		domain.ConversationMessage{ID: "message-other", Origin: domain.MessageOriginHuman, Text: "other"},
		"turn-other", testNow)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage other: created=%v err=%v", created, err)
	}

	for _, tc := range []struct {
		name   string
		branch domain.ConversationBranch
	}{
		{
			name: "parent",
			branch: domain.ConversationBranch{
				ID: "branch-wrong-parent", ConversationID: conversation.ID,
				ProviderConversationID: "thread-wrong-parent", ParentBranchID: otherConversation.ActiveBranchID,
			},
		},
		{
			name: "fork turn",
			branch: domain.ConversationBranch{
				ID: "branch-wrong-fork", ConversationID: conversation.ID,
				ProviderConversationID: "thread-wrong-fork", ParentBranchID: conversation.ActiveBranchID,
				ForkAfterTurnID: "turn-other", ReplacedTurnID: "turn-2",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.CreateConversationBranch(ctx, tc.branch, testNow); err == nil {
				t.Fatalf("CreateConversationBranch accepted cross-conversation %s", tc.name)
			}
		})
	}

	valid := domain.ConversationBranch{
		ID: "branch-valid", ConversationID: conversation.ID,
		ProviderConversationID: "thread-valid", ParentBranchID: conversation.ActiveBranchID,
		ForkAfterTurnID: "turn-1", ReplacedTurnID: "turn-2", ForkAfterSequence: 2,
	}
	if err := s.CreateConversationBranch(ctx, valid, testNow); err != nil {
		t.Fatalf("CreateConversationBranch valid: %v", err)
	}
	if err := s.UpdateConversationBranchReplacement(ctx, valid.ID, "turn-other"); err == nil {
		t.Fatal("UpdateConversationBranchReplacement accepted a turn from another conversation")
	}
	got, err := s.ConversationBranch(ctx, conversation.ID, valid.ID)
	if err != nil || got.ReplacementTurnID != "" {
		t.Fatalf("branch after refused replacement = %+v err=%v", got, err)
	}
}

func TestConversationEditAnchorRejectsMissingOrNonHumanTurn(t *testing.T) {
	ctx := context.Background()
	s, _, conversation := seededChatConversation(t)
	if _, err := s.ConversationEditAnchor(ctx, conversation.ID, "missing"); !errors.Is(err, store.ErrConversationTurnNotFound) {
		t.Fatalf("missing edit anchor error = %v", err)
	}
}

func appendBranchPrompt(
	t *testing.T,
	s *sqlite.Store,
	session domain.SessionRecord,
	conversation domain.ConversationRecord,
	generation, suffix, text string,
) {
	t.Helper()
	created, err := s.AppendUserMessage(context.Background(), conversation.ID, session.ID, generation,
		domain.ConversationMessage{
			ID: "message-" + suffix, Origin: domain.MessageOriginHuman, Text: text,
			ClientMessageID: "client-" + suffix,
		}, "turn-"+suffix, testNow)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage %s: created=%v err=%v", suffix, created, err)
	}
}

func activateTestBranch(
	t *testing.T,
	s *sqlite.Store,
	session domain.SessionRecord,
	conversation domain.ConversationRecord,
	branchID, providerID, generation string,
) {
	t.Helper()
	if err := s.ActivateConversationBranch(context.Background(), session.ID, conversation.ID,
		branchID, providerID, generation, testNow); err != nil {
		t.Fatalf("ActivateConversationBranch %s: %v", branchID, err)
	}
}

func assertMessageTexts(t *testing.T, messages []domain.ConversationMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(messages))
	for _, message := range messages {
		got = append(got, message.Text)
	}
	if len(got) != len(want) {
		t.Fatalf("message texts = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message texts = %#v, want %#v", got, want)
		}
	}
}

func TestConversationBranchSnapshotFollowsOnlyActiveLineage(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	rootID := conversation.ActiveBranchID
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "c", "C")

	child := domain.ConversationBranch{
		ID: "branch-child", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-child", ParentBranchID: rootID,
		ForkAfterTurnID: "turn-a", ReplacedTurnID: "turn-b", ForkAfterSequence: 1,
		Strategy: domain.ConversationBranchStrategyApproximateContext, ReplayTruncated: true,
	}
	if err := s.CreateConversationBranch(ctx, child, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch child: %v", err)
	}
	activateTestBranch(t, s, session, conversation, child.ID, child.ProviderConversationID, "generation-child")
	appendBranchPrompt(t, s, session, conversation, "generation-child", "b-edited", "B edited")
	appendBranchPrompt(t, s, session, conversation, "generation-child", "c-edited", "C edited")

	childSnapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot child: %v", err)
	}
	assertMessageTexts(t, childSnapshot.Messages, []string{"A", "B edited", "C edited"})

	activateTestBranch(t, s, session, conversation, rootID, "thread-root", "generation-root-2")
	rootSnapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot root: %v", err)
	}
	assertMessageTexts(t, rootSnapshot.Messages, []string{"A", "B", "C"})
}

func TestConversationBranchSnapshotSupportsNestedLineage(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	rootID := conversation.ActiveBranchID
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")

	child := domain.ConversationBranch{
		ID: "branch-child", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-child", ParentBranchID: rootID,
		ForkAfterTurnID: "turn-a", ReplacedTurnID: "turn-b", ForkAfterSequence: 1,
	}
	if err := s.CreateConversationBranch(ctx, child, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch child: %v", err)
	}
	activateTestBranch(t, s, session, conversation, child.ID, child.ProviderConversationID, "generation-child")
	appendBranchPrompt(t, s, session, conversation, "generation-child", "b-edited", "B edited")
	appendBranchPrompt(t, s, session, conversation, "generation-child", "c-edited", "C edited")

	nested := domain.ConversationBranch{
		ID: "branch-nested", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-nested", ParentBranchID: child.ID,
		ForkAfterTurnID: "turn-b-edited", ReplacedTurnID: "turn-c-edited", ForkAfterSequence: 3,
	}
	if err := s.CreateConversationBranch(ctx, nested, testNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch nested: %v", err)
	}
	activateTestBranch(t, s, session, conversation, nested.ID, nested.ProviderConversationID, "generation-nested")
	appendBranchPrompt(t, s, session, conversation, "generation-nested", "c-nested", "C nested")

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot nested: %v", err)
	}
	assertMessageTexts(t, snapshot.Messages, []string{"A", "B edited", "C nested"})
}

func TestConversationBranchSnapshotKeepsTheNarrowestNestedCutoff(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	rootID := conversation.ActiveBranchID
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")

	child := domain.ConversationBranch{
		ID: "branch-child", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-child", ParentBranchID: rootID,
		ForkAfterTurnID: "turn-a", ReplacedTurnID: "turn-b", ForkAfterSequence: 1,
	}
	if err := s.CreateConversationBranch(ctx, child, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch child: %v", err)
	}
	activateTestBranch(t, s, session, conversation, child.ID, child.ProviderConversationID, "generation-child")
	appendBranchPrompt(t, s, session, conversation, "generation-child", "b-edited", "B edited")

	// Editing A from the child branch cuts the entire lineage at sequence zero.
	// The child's older root cutoff must not widen that boundary and leak A back in.
	nested := domain.ConversationBranch{
		ID: "branch-nested", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-nested", ParentBranchID: child.ID,
		ReplacedTurnID: "turn-a", ForkAfterSequence: 0,
	}
	if err := s.CreateConversationBranch(ctx, nested, testNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch nested: %v", err)
	}
	activateTestBranch(t, s, session, conversation, nested.ID, nested.ProviderConversationID, "generation-nested")
	appendBranchPrompt(t, s, session, conversation, "generation-nested", "a-nested", "A nested")

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot nested: %v", err)
	}
	assertMessageTexts(t, snapshot.Messages, []string{"A nested"})
}

func TestConversationBranchPageDoesNotLeakSiblingAtBoundary(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	rootID := conversation.ActiveBranchID
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "c", "C")
	child := domain.ConversationBranch{
		ID: "branch-child", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-child", ParentBranchID: rootID,
		ForkAfterTurnID: "turn-a", ReplacedTurnID: "turn-b", ForkAfterSequence: 1,
		Strategy: domain.ConversationBranchStrategyApproximateContext, ReplayTruncated: true,
	}
	if err := s.CreateConversationBranch(ctx, child, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch child: %v", err)
	}
	activateTestBranch(t, s, session, conversation, child.ID, child.ProviderConversationID, "generation-child")
	appendBranchPrompt(t, s, session, conversation, "generation-child", "b-edited", "B edited")
	appendBranchPrompt(t, s, session, conversation, "generation-child", "c-edited", "C edited")

	page, err := s.LoadConversationSnapshotPage(ctx, conversation.ID, 0, 2)
	if err != nil {
		t.Fatalf("LoadConversationSnapshotPage: %v", err)
	}
	assertMessageTexts(t, page.Messages, []string{"B edited", "C edited"})
	if page.ActiveBranch.ID != child.ID ||
		page.ActiveBranch.Strategy != domain.ConversationBranchStrategyApproximateContext ||
		!page.ActiveBranch.ReplayTruncated {
		t.Fatalf("page active branch = %+v", page.ActiveBranch)
	}
	older, err := s.LoadConversationSnapshotPage(ctx, conversation.ID, page.OldestSequence, 2)
	if err != nil {
		t.Fatalf("LoadConversationSnapshotPage older: %v", err)
	}
	assertMessageTexts(t, older.Messages, []string{"A"})
	if older.ActiveBranch.ID != child.ID || older.ActiveBranch.Strategy != child.Strategy {
		t.Fatalf("older page active branch = %+v", older.ActiveBranch)
	}
}

func TestConversationSnapshotEditFloorFollowsActiveProviderBinding(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	rootID := conversation.ActiveBranchID
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")

	conversation, err := s.ConversationForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("ConversationForSession before boundary: %v", err)
	}
	boundary := domain.ConversationBranch{
		ID: "provider-boundary", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-next", ParentBranchID: rootID,
		ForkAfterSequence: conversation.LatestSequence, ProviderScopeID: "provider-boundary",
	}
	if err := s.CreateConversationBranch(ctx, boundary, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch boundary: %v", err)
	}
	activateTestBranch(t, s, session, conversation, boundary.ID, boundary.ProviderConversationID, "generation-next")
	appendBranchPrompt(t, s, session, conversation, "generation-next", "c", "C")

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot boundary: %v", err)
	}
	if snapshot.EditFloorSequence != boundary.ForkAfterSequence {
		t.Fatalf("full edit floor = %d, want provider boundary %d",
			snapshot.EditFloorSequence, boundary.ForkAfterSequence)
	}
	page, err := s.LoadConversationSnapshotPage(ctx, conversation.ID, 0, 2)
	if err != nil {
		t.Fatalf("LoadConversationSnapshotPage boundary: %v", err)
	}
	if page.EditFloorSequence != boundary.ForkAfterSequence {
		t.Fatalf("page edit floor = %d, want provider boundary %d",
			page.EditFloorSequence, boundary.ForkAfterSequence)
	}

	activateTestBranch(t, s, session, conversation, rootID, "thread-root", "generation-root-2")
	root, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot root: %v", err)
	}
	if root.EditFloorSequence != 0 {
		t.Fatalf("root edit floor = %d, want 0", root.EditFloorSequence)
	}
}

func TestConversationBranchPointDescribesSiblingNavigation(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	rootID := conversation.ActiveBranchID
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")

	for index, branchID := range []string{"branch-child-1", "branch-child-2"} {
		providerID := "thread-child-" + fmt.Sprint(index+1)
		branch := domain.ConversationBranch{
			ID: branchID, ConversationID: conversation.ID, SessionID: session.ID,
			ProviderConversationID: providerID, ParentBranchID: rootID,
			ForkAfterTurnID: "turn-a", ReplacedTurnID: "turn-b", ForkAfterSequence: 1,
		}
		if err := s.CreateConversationBranch(ctx, branch, testNow.Add(time.Duration(index+1)*time.Minute)); err != nil {
			t.Fatalf("CreateConversationBranch %s: %v", branchID, err)
		}
		activateTestBranch(t, s, session, conversation, branchID, providerID, "generation-"+branchID)
		replacement := "replacement-" + fmt.Sprint(index+1)
		appendBranchPrompt(t, s, session, conversation, "generation-"+branchID, replacement, "B edited")
		if err := s.UpdateConversationBranchReplacement(ctx, branchID, "turn-"+replacement); err != nil {
			t.Fatalf("UpdateConversationBranchReplacement %s: %v", branchID, err)
		}
	}

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	var point *domain.ConversationBranchPoint
	for i := range snapshot.BranchPoints {
		if snapshot.BranchPoints[i].TurnID == "turn-replacement-2" {
			point = &snapshot.BranchPoints[i]
			break
		}
	}
	if point == nil {
		t.Fatalf("branch points = %+v", snapshot.BranchPoints)
	}
	if point.Position != 3 || point.Total != 3 || point.PreviousBranchID != "branch-child-1" || point.NextBranchID != "" {
		t.Fatalf("active child branch point = %+v", *point)
	}

	activateTestBranch(t, s, session, conversation, rootID, "thread-root", "generation-root-2")
	snapshot, err = s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot root: %v", err)
	}
	point = nil
	for i := range snapshot.BranchPoints {
		if snapshot.BranchPoints[i].TurnID == "turn-b" {
			point = &snapshot.BranchPoints[i]
			break
		}
	}
	if point == nil || point.Position != 1 || point.Total != 3 || point.PreviousBranchID != "" || point.NextBranchID != "branch-child-1" {
		t.Fatalf("root branch point = %+v, all = %+v", point, snapshot.BranchPoints)
	}
}

func TestConversationBranchPointFlattensRepeatedEditsOfAReplacement(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	rootID := conversation.ActiveBranchID
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")

	first := domain.ConversationBranch{
		ID: "branch-first", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-first", ParentBranchID: rootID,
		ForkAfterTurnID: "turn-a", ReplacedTurnID: "turn-b", ForkAfterSequence: 1,
	}
	if err := s.CreateConversationBranch(ctx, first, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch first: %v", err)
	}
	activateTestBranch(t, s, session, conversation, first.ID, first.ProviderConversationID, "generation-first")
	appendBranchPrompt(t, s, session, conversation, "generation-first", "b-first", "B first edit")
	if err := s.UpdateConversationBranchReplacement(ctx, first.ID, "turn-b-first"); err != nil {
		t.Fatalf("UpdateConversationBranchReplacement first: %v", err)
	}

	second := domain.ConversationBranch{
		ID: "branch-second", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-second", ParentBranchID: first.ID,
		ForkAfterTurnID: "turn-a", ReplacedTurnID: "turn-b-first", ForkAfterSequence: 1,
	}
	if err := s.CreateConversationBranch(ctx, second, testNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("CreateConversationBranch second: %v", err)
	}
	activateTestBranch(t, s, session, conversation, second.ID, second.ProviderConversationID, "generation-second")
	appendBranchPrompt(t, s, session, conversation, "generation-second", "b-second", "B second edit")
	if err := s.UpdateConversationBranchReplacement(ctx, second.ID, "turn-b-second"); err != nil {
		t.Fatalf("UpdateConversationBranchReplacement second: %v", err)
	}

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	points := make(map[string]domain.ConversationBranchPoint, len(snapshot.BranchPoints))
	for _, point := range snapshot.BranchPoints {
		points[point.TurnID] = point
	}
	if len(points) != 3 {
		t.Fatalf("branch points = %+v, want one three-way edit group", snapshot.BranchPoints)
	}
	if point := points["turn-b"]; point.Position != 1 || point.Total != 3 || point.NextBranchID != first.ID {
		t.Fatalf("original branch point = %+v", point)
	}
	if point := points["turn-b-first"]; point.Position != 2 || point.Total != 3 ||
		point.PreviousBranchID != rootID || point.NextBranchID != second.ID {
		t.Fatalf("first edit branch point = %+v", point)
	}
	if point := points["turn-b-second"]; point.Position != 3 || point.Total != 3 || point.PreviousBranchID != first.ID {
		t.Fatalf("second edit branch point = %+v", point)
	}
}

func TestCommitChatSpawnAtomicallyPublishesProviderBoundaryAndOwner(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	committedAt := testNow.Add(time.Minute)
	session.Metadata.ProviderConversationID = "thread-fresh"
	session.Metadata.ControllerGeneration = "generation-fresh"
	session.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: committedAt}
	session.UpdatedAt = committedAt
	boundary := domain.ConversationBranch{
		ID: "fresh-provider-boundary", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-fresh", ParentBranchID: conversation.ActiveBranchID,
		ForkAfterSequence: conversation.LatestSequence, ProviderScopeID: "fresh-provider-boundary",
		CreatedAt: committedAt,
	}

	if err := s.CommitChatSpawn(ctx, session, boundary); err != nil {
		t.Fatalf("CommitChatSpawn: %v", err)
	}
	gotConversation, err := s.ConversationForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	if gotConversation.ActiveBranchID != boundary.ID {
		t.Fatalf("active branch = %q, want %q", gotConversation.ActiveBranchID, boundary.ID)
	}
	gotBoundary, err := s.ConversationBranch(ctx, conversation.ID, boundary.ID)
	if err != nil {
		t.Fatalf("ConversationBranch: %v", err)
	}
	if !gotBoundary.Active || gotBoundary.ProviderScopeID != boundary.ID ||
		gotBoundary.ProviderConversationID != boundary.ProviderConversationID {
		t.Fatalf("committed provider boundary = %+v", gotBoundary)
	}
	gotSession, found, err := s.GetSession(ctx, session.ID)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	if gotSession.Metadata.ProviderConversationID != boundary.ProviderConversationID ||
		gotSession.Metadata.ControllerGeneration != session.Metadata.ControllerGeneration {
		t.Fatalf("committed provider owner = handle %q generation %q",
			gotSession.Metadata.ProviderConversationID, gotSession.Metadata.ControllerGeneration)
	}
}

func TestCommitChatSpawnRollsBackProviderBoundaryWhenOwnerWriteFails(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	committedAt := testNow.Add(time.Minute)
	sourceHandle := session.Metadata.ProviderConversationID
	sourceGeneration := session.Metadata.ControllerGeneration
	session.Metadata.ProviderConversationID = "thread-fresh"
	session.Metadata.ControllerGeneration = "generation-fresh"
	// The sessions table rejects this during the final write, after the branch
	// insert and head move have run inside the same transaction.
	session.Activity = domain.Activity{State: domain.ActivityState("not-a-state"), LastActivityAt: committedAt}
	session.UpdatedAt = committedAt
	boundary := domain.ConversationBranch{
		ID: "failed-provider-boundary", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-fresh", ParentBranchID: conversation.ActiveBranchID,
		ForkAfterSequence: conversation.LatestSequence, ProviderScopeID: "failed-provider-boundary",
		CreatedAt: committedAt,
	}

	if err := s.CommitChatSpawn(ctx, session, boundary); err == nil {
		t.Fatal("CommitChatSpawn succeeded with an invalid lifecycle owner write")
	}
	gotConversation, err := s.ConversationForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	if gotConversation.ActiveBranchID != conversation.ActiveBranchID {
		t.Fatalf("active branch after rollback = %q, want source %q",
			gotConversation.ActiveBranchID, conversation.ActiveBranchID)
	}
	if _, err := s.ConversationBranch(ctx, conversation.ID, boundary.ID); !errors.Is(err, domain.ErrNoConversationBranch) {
		t.Fatalf("failed provider boundary lookup error = %v, want ErrNoConversationBranch", err)
	}
	gotSession, found, err := s.GetSession(ctx, session.ID)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	if gotSession.Metadata.ProviderConversationID != sourceHandle ||
		gotSession.Metadata.ControllerGeneration != sourceGeneration {
		t.Fatalf("provider owner after rollback = handle %q generation %q, want %q/%q",
			gotSession.Metadata.ProviderConversationID, gotSession.Metadata.ControllerGeneration,
			sourceHandle, sourceGeneration)
	}
}

func TestCommitChatSpawnRejectsProjectConversationReboundToAnotherSession(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedProject(t, s, "chat-rebind")
	source := sampleRecord("chat-rebind")
	source.Mode = domain.SessionModeChat
	source.Metadata.ProviderConversationID = "thread-source"
	source.Metadata.ControllerGeneration = "generation-source"
	source, err := s.CreateSession(ctx, source)
	if err != nil {
		t.Fatalf("Create source session: %v", err)
	}
	conversation, err := s.CreateConversation(ctx, "project-conversation",
		domain.ConversationScopeProject, source.ProjectID, source.ID, testNow)
	if err != nil {
		t.Fatalf("CreateConversation source: %v", err)
	}
	target := sampleRecord("chat-rebind")
	target.Mode = domain.SessionModeChat
	target, err = s.CreateSession(ctx, target)
	if err != nil {
		t.Fatalf("Create target session: %v", err)
	}
	if _, err := s.CreateConversation(ctx, "unused-project-conversation",
		domain.ConversationScopeProject, target.ProjectID, target.ID, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("rebind project conversation: %v", err)
	}

	source.Metadata.ProviderConversationID = "thread-stale-target"
	source.Metadata.ControllerGeneration = "generation-stale-target"
	source.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: testNow.Add(2 * time.Minute)}
	source.UpdatedAt = testNow.Add(2 * time.Minute)
	boundary := domain.ConversationBranch{
		ID: "stale-provider-boundary", ConversationID: conversation.ID, SessionID: source.ID,
		ProviderConversationID: "thread-stale-target", ParentBranchID: conversation.ActiveBranchID,
		ProviderScopeID: "stale-provider-boundary", CreatedAt: testNow.Add(2 * time.Minute),
	}

	err = s.CommitChatSpawn(ctx, source, boundary)
	if err == nil || !strings.Contains(err.Error(), "no longer owned") {
		t.Fatalf("CommitChatSpawn error = %v, want project conversation owner mismatch", err)
	}
	gotConversation, err := s.ConversationForSession(ctx, target.ID)
	if err != nil {
		t.Fatalf("ConversationForSession target: %v", err)
	}
	if gotConversation.ActiveBranchID != conversation.ActiveBranchID {
		t.Fatalf("active branch after stale commit = %q, want source %q",
			gotConversation.ActiveBranchID, conversation.ActiveBranchID)
	}
	if _, err := s.ConversationBranch(ctx, conversation.ID, boundary.ID); !errors.Is(err, domain.ErrNoConversationBranch) {
		t.Fatalf("stale boundary lookup error = %v, want ErrNoConversationBranch", err)
	}
	gotSource, found, err := s.GetSession(ctx, source.ID)
	if err != nil || !found {
		t.Fatalf("GetSession source: found=%v err=%v", found, err)
	}
	if gotSource.Metadata.ProviderConversationID != "thread-source" ||
		gotSource.Metadata.ControllerGeneration != "generation-source" {
		t.Fatalf("stale source owner changed to %q/%q",
			gotSource.Metadata.ProviderConversationID, gotSource.Metadata.ControllerGeneration)
	}
}

func TestRepairIncompleteProjectEditDoesNotTransferProviderOwnerToReboundSession(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedProject(t, s, "edit-rebind")

	sourceRecord := sampleRecord("edit-rebind")
	sourceRecord.Mode = domain.SessionModeChat
	sourceRecord.Kind = domain.KindOrchestrator
	sourceRecord.Harness = domain.HarnessClaudeCode
	sourceRecord.Metadata.ProviderConversationID = "claude-source-thread"
	sourceRecord.Metadata.ControllerGeneration = "claude-source-generation"
	source, err := s.CreateSession(ctx, sourceRecord)
	if err != nil {
		t.Fatalf("Create source session: %v", err)
	}
	conversation, err := s.CreateConversation(ctx, "edit-rebind-conversation",
		domain.ConversationScopeProject, source.ProjectID, source.ID, testNow)
	if err != nil {
		t.Fatalf("Create source conversation: %v", err)
	}
	seedBranchTurns(t, s, source, conversation)
	child := domain.ConversationBranch{
		ID: "abandoned-claude-edit", ConversationID: conversation.ID, SessionID: source.ID,
		ProviderConversationID: "claude-child-thread", ParentBranchID: conversation.ActiveBranchID,
		ReplacedTurnID: "turn-2", ForkAfterSequence: 2,
		ProviderScopeID: "claude-child-scope", Strategy: domain.ConversationBranchStrategyApproximateContext,
		CreatedAt: testNow.Add(2 * time.Minute),
	}
	if err := s.CreateAndActivateConversationBranch(ctx, source.ID, child,
		"claude-child-generation", child.CreatedAt); err != nil {
		t.Fatalf("CreateAndActivateConversationBranch: %v", err)
	}

	targetRecord := sampleRecord("edit-rebind")
	targetRecord.Mode = domain.SessionModeChat
	targetRecord.Kind = domain.KindOrchestrator
	targetRecord.Harness = domain.HarnessCodex
	target, err := s.CreateSession(ctx, targetRecord)
	if err != nil {
		t.Fatalf("Create target session: %v", err)
	}
	rebound, err := s.CreateConversation(ctx, "unused-edit-rebind-conversation",
		domain.ConversationScopeProject, target.ProjectID, target.ID, testNow.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("rebind project conversation: %v", err)
	}
	if rebound.ID != conversation.ID {
		t.Fatalf("rebound conversation = %q, want %q", rebound.ID, conversation.ID)
	}

	repaired, restoredOwner, err := s.RepairIncompleteConversationEdit(
		ctx, target.ID, conversation.ID, testNow.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("RepairIncompleteConversationEdit: %v", err)
	}
	if repaired.ID != conversation.ActiveBranchID || restoredOwner {
		t.Fatalf("repair = %+v restoredOwner=%v, want source branch without owner transfer",
			repaired, restoredOwner)
	}
	gotTarget, found, err := s.GetSession(ctx, target.ID)
	if err != nil || !found {
		t.Fatalf("GetSession target: found=%v err=%v", found, err)
	}
	if gotTarget.Metadata.ProviderConversationID != "" || gotTarget.Metadata.ControllerGeneration != "" {
		t.Fatalf("target inherited prior provider owner %q/%q",
			gotTarget.Metadata.ProviderConversationID, gotTarget.Metadata.ControllerGeneration)
	}
	gotConversation, err := s.ConversationForSession(ctx, target.ID)
	if err != nil || gotConversation.ActiveBranchID != conversation.ActiveBranchID {
		t.Fatalf("active branch after repair = %q, want source %q, err=%v",
			gotConversation.ActiveBranchID, conversation.ActiveBranchID, err)
	}
}
