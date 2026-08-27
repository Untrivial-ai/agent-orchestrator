package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type agentSwitchFixtureUpdater interface {
	UpdateAgentSwitch(context.Context, domain.AgentSwitch, domain.AgentSwitchState, domain.AgentGenerationID, domain.AgentGenerationID) (bool, error)
}

func advanceAgentSwitchFixture(ctx context.Context, t *testing.T, s agentSwitchFixtureUpdater, sw *domain.AgentSwitch, next domain.AgentSwitchState, at time.Time) {
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, sw, next, at, nil)
}

func advanceAgentSwitchFixtureWithMutation(ctx context.Context, t *testing.T, s agentSwitchFixtureUpdater, sw *domain.AgentSwitch, next domain.AgentSwitchState, at time.Time, mutate func(*domain.AgentSwitch)) {
	t.Helper()
	expectedState := sw.State
	expectedTarget := sw.TargetGenerationID
	if mutate != nil {
		mutate(sw)
	}
	sw.State = next
	sw.UpdatedAt = at
	if ok, err := s.UpdateAgentSwitch(ctx, *sw, expectedState, sw.SourceGenerationID, expectedTarget); err != nil || !ok {
		t.Fatalf("advance switch %s -> %s: ok=%v err=%v", expectedState, next, ok, err)
	}
}

func TestActivateChatAgentSwitchTargetKeepsRuntimeEmptyAcrossNativeSwitchBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "chat-switch")
	rec := sampleRecord("chat-switch")
	now := rec.CreatedAt
	rec.Mode = domain.SessionModeChat
	rec.Harness = domain.HarnessClaudeCode
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	rec.Metadata.RuntimeHandleID = ""
	rec.Metadata.RuntimeLaunchID = ""
	rec.Metadata.ProviderConversationID = "source-chat-native"
	rec.Metadata.ControllerGeneration = "source-chat-generation"
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create Chat session: %v", err)
	}
	conversation, err := s.CreateConversation(ctx, "chat-switch-conversation",
		domain.ConversationScopeSession, "chat-switch", session.ID, now)
	if err != nil {
		t.Fatalf("create Chat conversation: %v", err)
	}
	if err := s.SetConversationSettings(ctx, conversation.ID, domain.ConversationSettings{
		Model: "source-provider-model", ReasoningEffort: "high",
		ApprovalMode: domain.PermissionModeAcceptEdits,
	}, now); err != nil {
		t.Fatalf("seed source conversation settings: %v", err)
	}
	created, err := s.AppendUserMessage(ctx, conversation.ID, session.ID, "source-chat-generation",
		domain.ConversationMessage{
			ID: "source-message", Origin: domain.MessageOriginHuman, Text: "original source prompt",
			ClientMessageID: "source-client-message",
		}, "source-turn", now)
	if err != nil || !created {
		t.Fatalf("append source prompt: created=%v err=%v", created, err)
	}
	if err := s.BindTurnToProvider(ctx, "source-turn", "source-provider-turn", now); err != nil {
		t.Fatalf("bind source prompt: %v", err)
	}
	sourceEditBranch := domain.ConversationBranch{
		ID: "source-edit-branch", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "source-chat-edited-native",
		ParentBranchID:         conversation.ActiveBranchID, ReplacedTurnID: "source-turn",
		ForkAfterSequence: 0, CreatedAt: now.Add(500 * time.Millisecond),
	}
	if err := s.CreateAndActivateConversationBranch(
		ctx, session.ID, sourceEditBranch, "source-chat-generation", sourceEditBranch.CreatedAt,
	); err != nil {
		t.Fatalf("create source edit branch: %v", err)
	}
	created, err = s.AppendUserMessage(ctx, conversation.ID, session.ID, "source-chat-generation",
		domain.ConversationMessage{
			ID: "source-edit-message", Origin: domain.MessageOriginHuman, Text: "edited source prompt",
			ClientMessageID: "source-edit-client-message",
		}, "source-edit-turn", now.Add(600*time.Millisecond))
	if err != nil || !created {
		t.Fatalf("append source edit prompt: created=%v err=%v", created, err)
	}
	if err := s.BindTurnToProvider(ctx, "source-edit-turn", "source-edit-provider-turn", now.Add(700*time.Millisecond)); err != nil {
		t.Fatalf("bind source edit prompt: %v", err)
	}
	if err := s.UpdateConversationBranchReplacement(ctx, sourceEditBranch.ID, "source-edit-turn"); err != nil {
		t.Fatalf("record source branch replacement: %v", err)
	}
	sw, created, err := s.CreateAgentSwitch(ctx, domain.AgentSwitch{
		ID: "switch-chat", SessionID: session.ID, IdempotencyKey: "switch-chat",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, TargetStartMode: domain.AgentSwitchTargetStartPending,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted,
		SourceGenerationID: "source-chat-generation", RequestedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("create Chat switch: created=%v err=%v", created, err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &sw, domain.AgentSwitchStoppingSource, now.Add(time.Second), func(next *domain.AgentSwitch) {
		next.TargetStartMode = domain.AgentSwitchTargetStartFresh
		next.TargetGenerationID = "target-chat-generation"
	})
	if ok, err := s.ConfirmAgentSwitchSourceStopped(ctx, domain.AgentSwitchSourceStopConfirmation{
		SwitchID: sw.ID, SessionID: session.ID, SourceMode: domain.SessionModeChat,
		SourceHarness: domain.HarnessClaudeCode, SourceGenerationID: "source-chat-generation",
		ExpectedSourceControllerGeneration: "source-chat-generation",
		TargetGenerationID:                 "target-chat-generation", StoppedAt: now.Add(2 * time.Second),
	}); err != nil || !ok {
		t.Fatalf("confirm Chat source stopped: ok=%v err=%v", ok, err)
	}
	target := domain.AgentNativeSession{
		ID: "target-chat-native-ref", AOSessionID: session.ID, Harness: domain.HarnessCodex,
		NativeSessionID: "target-chat-native", LastGenerationID: "target-chat-generation",
		CreatedAt: now.Add(3 * time.Second), LastUsedAt: now.Add(3 * time.Second),
	}
	if _, created, err := s.CreateAgentNativeSession(ctx, target); err != nil || !created {
		t.Fatalf("create target Chat native session: created=%v err=%v", created, err)
	}
	sw, _, _ = s.GetAgentSwitch(ctx, sw.ID)
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &sw, domain.AgentSwitchStartingTarget, now.Add(3*time.Second), func(next *domain.AgentSwitch) {
		next.TargetNativeSessionRef = &target.ID
	})
	if err := s.ClaimChatControllerGeneration(ctx, session.ID, "target-chat-generation", now.Add(4*time.Second)); err != nil {
		t.Fatalf("claim target Chat generation: %v", err)
	}
	activatedAt := now.Add(5 * time.Second)
	if ok, err := s.ActivateChatAgentSwitchTarget(ctx, domain.AgentSwitchChatTargetActivation{
		SwitchID: sw.ID, SessionID: session.ID,
		SourceHarness: domain.HarnessClaudeCode, SourceGenerationID: "source-chat-generation",
		TargetHarness: domain.HarnessCodex, TargetNativeSessionRef: target.ID,
		TargetGenerationID:     "target-chat-generation",
		ProviderConversationID: "target-chat-native", ControllerGeneration: "target-chat-generation",
		ActivatedAt: activatedAt,
	}); err != nil || !ok {
		t.Fatalf("activate Chat target: ok=%v err=%v", ok, err)
	}
	got, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("get activated Chat session: ok=%v err=%v", ok, err)
	}
	if got.Mode != domain.SessionModeChat || got.Harness != domain.HarnessCodex ||
		got.Metadata.RuntimeHandleID != "" || got.Metadata.RuntimeLaunchID != "" ||
		got.Metadata.ProviderConversationID != "target-chat-native" ||
		got.Metadata.ControllerGeneration != "target-chat-generation" {
		t.Fatalf("activated Chat session = %+v", got)
	}
	conversation, err = s.ConversationForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload activated conversation: %v", err)
	}
	if conversation.Settings.Model != "" || conversation.Settings.ReasoningEffort != "" ||
		conversation.Settings.ApprovalMode != domain.PermissionModeAcceptEdits {
		t.Fatalf("activated conversation settings = %+v, want target defaults with preserved approval",
			conversation.Settings)
	}
	branches, err := s.ConversationBranches(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("list activated conversation branches: %v", err)
	}
	if len(branches) != 3 {
		t.Fatalf("activated conversation branches = %+v, want source edit lineage and target provider boundary", branches)
	}
	var targetBranch domain.ConversationBranch
	for _, branch := range branches {
		if branch.Active {
			targetBranch = branch
			break
		}
	}
	if targetBranch.ProviderConversationID != "target-chat-native" ||
		targetBranch.ParentBranchID != sourceEditBranch.ID ||
		targetBranch.ReplacedTurnID != "" ||
		targetBranch.ProviderScopeID != targetBranch.ID ||
		targetBranch.ForkAfterSequence != conversation.LatestSequence {
		t.Fatalf("target provider boundary = %+v, conversation = %+v", targetBranch, conversation)
	}
	created, err = s.AppendUserMessage(ctx, conversation.ID, session.ID, "target-chat-generation",
		domain.ConversationMessage{
			ID: "target-message", Origin: domain.MessageOriginAutomation, Text: "continue target",
			ClientMessageID: "target-client-message",
		}, "target-turn", now.Add(6*time.Second))
	if err != nil || !created {
		t.Fatalf("append target prompt: created=%v err=%v", created, err)
	}
	if err := s.BindTurnToProvider(ctx, "target-turn", "target-provider-turn", now.Add(7*time.Second)); err != nil {
		t.Fatalf("bind target prompt: %v", err)
	}
	snapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("load provider-boundary snapshot: %v", err)
	}
	page, err := s.LoadConversationSnapshotPage(ctx, conversation.ID, 0, 200)
	if err != nil {
		t.Fatalf("load provider-boundary snapshot page: %v", err)
	}
	for _, projection := range []struct {
		name     string
		snapshot store.ConversationSnapshot
	}{
		{name: "full", snapshot: snapshot},
		{name: "page", snapshot: page},
	} {
		if projection.snapshot.BranchedFromEarlierMessage {
			t.Fatalf("%s provider ownership boundary was exposed as an edited conversation branch", projection.name)
		}
		if len(projection.snapshot.BranchPoints) != 0 {
			t.Fatalf("%s source-provider branch navigation leaked across boundary: %+v",
				projection.name, projection.snapshot.BranchPoints)
		}
		for _, turn := range projection.snapshot.Turns {
			if turn.ID == "target-turn" && turn.ProviderTurnID != "target-provider-turn" {
				t.Fatalf("%s active target turn lost its history affordance: %+v", projection.name, turn)
			}
			if turn.ID != "target-turn" && turn.ProviderTurnID != "" {
				t.Fatalf("%s source-provider turn %q retained a live history affordance via %q",
					projection.name, turn.ID, turn.ProviderTurnID)
			}
		}
	}
	if _, err := s.ConversationEditAnchor(ctx, conversation.ID, "source-turn"); !errors.Is(err, store.ErrConversationTurnNotFound) {
		t.Fatalf("source-provider edit anchor error = %v, want ErrConversationTurnNotFound", err)
	}

	// Complete the first switch, then return to Claude's retained native
	// conversation. The second activation must create a new provider ownership
	// epoch even though that provider conversation id already exists earlier in
	// the lineage.
	sw, ok, err = s.GetAgentSwitch(ctx, sw.ID)
	if err != nil || !ok {
		t.Fatalf("reload first Chat switch: ok=%v err=%v", ok, err)
	}
	advanceAgentSwitchFixture(ctx, t, s, &sw, domain.AgentSwitchDelivering, now.Add(8*time.Second))
	advanceAgentSwitchFixture(ctx, t, s, &sw, domain.AgentSwitchCompleted, now.Add(9*time.Second))

	returnTarget := domain.AgentNativeSession{
		ID: "source-chat-native-ref", AOSessionID: session.ID, Harness: domain.HarnessClaudeCode,
		NativeSessionID: "source-chat-edited-native", LastGenerationID: "return-chat-generation",
		CreatedAt: now.Add(10 * time.Second), LastUsedAt: now.Add(10 * time.Second),
	}
	if _, created, err := s.CreateAgentNativeSession(ctx, returnTarget); err != nil || !created {
		t.Fatalf("create retained source Chat native session: created=%v err=%v", created, err)
	}
	returnSwitch, created, err := s.CreateAgentSwitch(ctx, domain.AgentSwitch{
		ID: "switch-chat-return", SessionID: session.ID, IdempotencyKey: "switch-chat-return",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessClaudeCode, ""),
		FromHarness:        domain.HarnessCodex, TargetHarness: domain.HarnessClaudeCode,
		State: domain.AgentSwitchPreparingHandoff, TargetStartMode: domain.AgentSwitchTargetStartPending,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted,
		SourceGenerationID: "target-chat-generation", RequestedAt: now.Add(10 * time.Second), UpdatedAt: now.Add(10 * time.Second),
	})
	if err != nil || !created {
		t.Fatalf("create return Chat switch: created=%v err=%v", created, err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &returnSwitch, domain.AgentSwitchStoppingSource, now.Add(11*time.Second), func(next *domain.AgentSwitch) {
		next.TargetStartMode = domain.AgentSwitchTargetStartResumed
		next.TargetGenerationID = "return-chat-generation"
	})
	if ok, err := s.ConfirmAgentSwitchSourceStopped(ctx, domain.AgentSwitchSourceStopConfirmation{
		SwitchID: returnSwitch.ID, SessionID: session.ID, SourceMode: domain.SessionModeChat,
		SourceHarness: domain.HarnessCodex, SourceGenerationID: "target-chat-generation",
		ExpectedSourceControllerGeneration: "target-chat-generation",
		TargetGenerationID:                 "return-chat-generation", StoppedAt: now.Add(12 * time.Second),
	}); err != nil || !ok {
		t.Fatalf("confirm return Chat source stopped: ok=%v err=%v", ok, err)
	}
	returnSwitch, ok, err = s.GetAgentSwitch(ctx, returnSwitch.ID)
	if err != nil || !ok {
		t.Fatalf("reload return Chat switch: ok=%v err=%v", ok, err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &returnSwitch, domain.AgentSwitchStartingTarget, now.Add(13*time.Second), func(next *domain.AgentSwitch) {
		next.TargetNativeSessionRef = &returnTarget.ID
	})
	if err := s.ClaimChatControllerGeneration(ctx, session.ID, "return-chat-generation", now.Add(14*time.Second)); err != nil {
		t.Fatalf("claim return Chat generation: %v", err)
	}
	if ok, err := s.ActivateChatAgentSwitchTarget(ctx, domain.AgentSwitchChatTargetActivation{
		SwitchID: returnSwitch.ID, SessionID: session.ID,
		SourceHarness: domain.HarnessCodex, SourceGenerationID: "target-chat-generation",
		TargetHarness: domain.HarnessClaudeCode, TargetNativeSessionRef: returnTarget.ID,
		TargetGenerationID:     "return-chat-generation",
		ProviderConversationID: "source-chat-edited-native", ControllerGeneration: "return-chat-generation",
		ActivatedAt: now.Add(15 * time.Second),
	}); err != nil || !ok {
		t.Fatalf("activate retained Chat target: ok=%v err=%v", ok, err)
	}
	conversation, err = s.ConversationForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload returned conversation: %v", err)
	}
	branches, err = s.ConversationBranches(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("list returned conversation branches: %v", err)
	}
	if len(branches) != 4 {
		t.Fatalf("returned conversation branches = %+v, want a distinct return ownership epoch", branches)
	}
	var returnBranch domain.ConversationBranch
	for _, branch := range branches {
		if branch.Active {
			returnBranch = branch
			break
		}
	}
	if returnBranch.ID != "switch-chat-return:provider" ||
		returnBranch.ProviderConversationID != "source-chat-edited-native" ||
		returnBranch.ParentBranchID != targetBranch.ID || returnBranch.ReplacedTurnID != "" {
		t.Fatalf("return provider ownership boundary = %+v, prior target = %+v", returnBranch, targetBranch)
	}
}

func TestListActiveAgentSwitchesExcludesTerminalRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	makeSwitch := func(projectID string, switchID domain.AgentSwitchID) domain.AgentSwitch {
		t.Helper()
		seedProject(t, s, projectID)
		session, err := s.CreateSession(ctx, sampleRecord(projectID))
		if err != nil {
			t.Fatalf("create %s session: %v", projectID, err)
		}
		rec := domain.AgentSwitch{
			ID: switchID, SessionID: session.ID, IdempotencyKey: string(switchID),
			RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
			FromHarness:        domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
			State: domain.AgentSwitchPreparingHandoff, TargetStartMode: domain.AgentSwitchTargetStartPending,
			AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: domain.AgentGenerationID("generation-" + projectID),
			RequestedAt: now, UpdatedAt: now,
		}
		stored, created, err := s.CreateAgentSwitch(ctx, rec)
		if err != nil || !created {
			t.Fatalf("create %s switch: created=%v err=%v", projectID, created, err)
		}
		return stored
	}

	active := makeSwitch("active-switch", "switch-active")
	failed := makeSwitch("terminal-switch", "switch-failed")
	failed.State = domain.AgentSwitchFailed
	failed.ErrorCode = domain.AgentSwitchErrorSwitchFailed
	failed.UpdatedAt = now.Add(time.Second)
	if updated, err := s.UpdateAgentSwitch(ctx, failed, domain.AgentSwitchPreparingHandoff, failed.SourceGenerationID, ""); err != nil || !updated {
		t.Fatalf("fail terminal switch: updated=%v err=%v", updated, err)
	}

	got, err := s.ListActiveAgentSwitches(ctx)
	if err != nil {
		t.Fatalf("list active agent switches: %v", err)
	}
	if len(got) != 1 || got[0].ID != active.ID {
		t.Fatalf("active switches = %+v, want only %s", got, active.ID)
	}
}

func TestAgentNativeSessionsRetainMultipleConversationsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-native")
	session, err := s.CreateSession(ctx, sampleRecord("switch-native"))
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	first := domain.AgentNativeSession{
		ID: "native-codex-1", AOSessionID: session.ID, Harness: domain.HarnessCodex,
		ConfigDir: "/ao/codex/a", NativeSessionID: "codex-thread-1",
		TranscriptPath:   "/transcripts/codex-1.jsonl",
		LastGenerationID: "generation-1",
		CreatedAt:        now, LastUsedAt: now,
	}
	stored, created, err := s.CreateAgentNativeSession(ctx, first)
	if err != nil || !created || stored.ID != first.ID {
		t.Fatalf("create first native session: created=%v stored=%+v err=%v", created, stored, err)
	}

	second := first
	second.ID = "native-codex-2"
	second.NativeSessionID = "codex-thread-2"
	second.TranscriptPath = "/transcripts/codex-2.jsonl"
	second.LastGenerationID = "generation-2"
	second.CreatedAt = now.Add(time.Minute)
	second.LastUsedAt = second.CreatedAt
	if _, created, err := s.CreateAgentNativeSession(ctx, second); err != nil || !created {
		t.Fatalf("create second same-harness conversation: created=%v err=%v", created, err)
	}

	// A retry with a new AO id but the same concrete native identity resolves
	// to the durable original instead of adding a duplicate conversation.
	duplicate := second
	duplicate.ID = "retry-generated-id"
	got, created, err := s.CreateAgentNativeSession(ctx, duplicate)
	if err != nil || created || got.ID != second.ID {
		t.Fatalf("idempotent native identity: created=%v got=%+v err=%v", created, got, err)
	}

	// Unknown native ids are not deduplicated: these are separate pending
	// conversations waiting for provider discovery.
	for _, id := range []domain.AgentNativeSessionID{"pending-1", "pending-2"} {
		pending := first
		pending.ID, pending.NativeSessionID = id, ""
		pending.CreatedAt = now.Add(2 * time.Minute)
		pending.LastUsedAt = pending.CreatedAt
		if _, created, err := s.CreateAgentNativeSession(ctx, pending); err != nil || !created {
			t.Fatalf("create pending native session %s: created=%v err=%v", id, created, err)
		}
	}

	all, err := s.ListAgentNativeSessions(ctx, session.ID)
	if err != nil || len(all) != 4 {
		t.Fatalf("list native sessions: len=%d err=%v rows=%+v", len(all), err, all)
	}
	if all[0].LastUsedAt.Before(all[1].LastUsedAt) || all[1].LastUsedAt.Before(all[2].LastUsedAt) {
		t.Fatalf("native sessions are not newest-first: %+v", all)
	}
}

func TestAgentNativeSessionGenerationFence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "native-fence")
	session, err := s.CreateSession(ctx, sampleRecord("native-fence"))
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.AgentNativeSession{
		ID: "native-claude", AOSessionID: session.ID, Harness: domain.HarnessClaudeCode,
		ConfigDir: "/ao/claude", LastGenerationID: "source-generation",
		CreatedAt: now, LastUsedAt: now,
	}
	if _, _, err := s.CreateAgentNativeSession(ctx, rec); err != nil {
		t.Fatalf("create native session: %v", err)
	}

	rec.NativeSessionID = "claude-session-id"
	rec.TranscriptPath = "/claude/session.jsonl"
	rec.LastGenerationID = "next-generation"
	rec.LastUsedAt = now.Add(time.Minute)
	if ok, err := s.UpdateAgentNativeSession(ctx, rec, "stale-generation"); err != nil || ok {
		t.Fatalf("stale native update: ok=%v err=%v", ok, err)
	}
	if ok, err := s.UpdateAgentNativeSession(ctx, rec, "source-generation"); err != nil || !ok {
		t.Fatalf("fenced native update: ok=%v err=%v", ok, err)
	}
	if ok, err := s.UpdateAgentNativeSession(ctx, rec, "source-generation"); err != nil || ok {
		t.Fatalf("replayed old-generation update: ok=%v err=%v", ok, err)
	}

	got, ok, err := s.GetAgentNativeSession(ctx, rec.ID)
	if err != nil || !ok || got.LastGenerationID != "next-generation" || got.TranscriptPath != rec.TranscriptPath {
		t.Fatalf("updated native session = %+v, ok=%v err=%v", got, ok, err)
	}
}

func TestAgentSwitchIdempotencySingleActiveSagaAndGenerationFences(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-saga")
	session, err := s.CreateSession(ctx, sampleRecord("switch-saga"))
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	switchRec := domain.AgentSwitch{
		ID: "switch-1", SessionID: session.ID, IdempotencyKey: "request-1",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode,
		TargetHarness:      domain.HarnessCodex,
		State:              domain.AgentSwitchPreparingHandoff, TargetStartMode: domain.AgentSwitchTargetStartPending,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted,
		SourceGenerationID: "source-generation", RequestedAt: now, UpdatedAt: now,
	}
	for _, status := range []domain.AgentHandoffStatus{domain.AgentHandoffRequested, domain.AgentHandoffReceived} {
		prepopulated := switchRec
		prepopulated.ID = domain.AgentSwitchID("prepopulated-" + string(status))
		prepopulated.IdempotencyKey = string(prepopulated.ID)
		prepopulated.AgentHandoffStatus = status
		if status == domain.AgentHandoffReceived {
			prepopulated.AgentHandoffPath = "/ao/handoffs/prepopulated/agent-handoff.json"
			prepopulated.AgentHandoffHash = strings.Repeat("a", 64)
		}
		if _, created, err := s.CreateAgentSwitch(ctx, prepopulated); err == nil || created || !strings.Contains(err.Error(), "without target launch or handoff facts") {
			t.Fatalf("create switch with prepopulated handoff %q: created=%v err=%v", status, created, err)
		}
	}
	stored, created, err := s.CreateAgentSwitch(ctx, switchRec)
	if err != nil || !created || stored.ID != switchRec.ID {
		t.Fatalf("create switch: created=%v stored=%+v err=%v", created, stored, err)
	}
	if stored.SourceTranscriptStatus != domain.AgentSwitchSourceTranscriptNotAttempted {
		t.Fatalf("new switch transcript status = %q, want not_attempted", stored.SourceTranscriptStatus)
	}

	retry := switchRec
	retry.ID = "new-retry-id"
	stored, created, err = s.CreateAgentSwitch(ctx, retry)
	if err != nil || created || stored.ID != switchRec.ID {
		t.Fatalf("idempotent switch retry: created=%v stored=%+v err=%v", created, stored, err)
	}

	conflictingRetry := retry
	conflictingRetry.RequestFingerprint = domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, "different-model")
	if _, _, err := s.CreateAgentSwitch(ctx, conflictingRetry); !errors.Is(err, domain.ErrAgentSwitchIdempotencyConflict) {
		t.Fatalf("conflicting idempotency key err=%v", err)
	}

	secondSwitch := switchRec
	secondSwitch.ID, secondSwitch.IdempotencyKey = "switch-2", "request-2"
	active, created, err := s.CreateAgentSwitch(ctx, secondSwitch)
	if !errors.Is(err, domain.ErrAgentSwitchInProgress) || created || active.ID != switchRec.ID {
		t.Fatalf("second active switch: active=%+v created=%v err=%v", active, created, err)
	}

	handoffPath := "/ao/handoffs/switch-1/agent-handoff.json"
	handoffHash := strings.Repeat("a", 64)
	if ok, err := s.RecordAgentHandoff(ctx, switchRec.ID, "stale-source", domain.AgentHandoffReceived, handoffPath, handoffHash, now.Add(time.Second)); err != nil || ok {
		t.Fatalf("stale semantic handoff: ok=%v err=%v", ok, err)
	}
	current := stored
	staleBeforeHandoff := current
	if ok, err := s.RecordAgentHandoff(ctx, switchRec.ID, "source-generation", domain.AgentHandoffRequested, "", "", now.Add(time.Second)); err != nil || !ok {
		t.Fatalf("request semantic handoff: ok=%v err=%v", ok, err)
	}
	if ok, err := s.RecordAgentHandoff(ctx, switchRec.ID, "source-generation", domain.AgentHandoffReceived, handoffPath, handoffHash, now.Add(time.Second)); err != nil || !ok {
		t.Fatalf("record semantic handoff: ok=%v err=%v", ok, err)
	}
	if ok, err := s.RecordAgentHandoff(ctx, switchRec.ID, "source-generation", domain.AgentHandoffTimedOut, "", "", now.Add(2*time.Second)); err != nil || ok {
		t.Fatalf("timeout overwrote received handoff: ok=%v err=%v", ok, err)
	}

	// Amend from a value captured before semantic collection. The general saga
	// update must not erase the received tuple written by RecordAgentHandoff.
	staleBeforeHandoff.UpdatedAt = now.Add(1500 * time.Millisecond)
	if ok, err := s.UpdateAgentSwitch(ctx, staleBeforeHandoff, domain.AgentSwitchPreparingHandoff, "source-generation", ""); err != nil || !ok {
		t.Fatalf("amend switch using stale pre-handoff value: ok=%v err=%v", ok, err)
	}
	current, ok, err := s.GetActiveAgentSwitch(ctx, session.ID)
	if err != nil || !ok || current.State != domain.AgentSwitchPreparingHandoff || current.AgentHandoffStatus != domain.AgentHandoffReceived || current.AgentHandoffPath != handoffPath || current.AgentHandoffHash != handoffHash {
		t.Fatalf("active switch after handoff = %+v, ok=%v err=%v", current, ok, err)
	}
	advanceAgentSwitchFixture(ctx, t, s, &current, domain.AgentSwitchStoppingSource, now.Add(2*time.Second))
	if ok, err := s.UpdateAgentSwitch(ctx, current, domain.AgentSwitchPreparingHandoff, "source-generation", ""); err != nil || ok {
		t.Fatalf("replay stale state transition: ok=%v err=%v", ok, err)
	}
	if ok, err := s.RecordAgentHandoff(ctx, switchRec.ID, "source-generation", domain.AgentHandoffReceived, handoffPath, handoffHash, now.Add(3*time.Second)); err != nil || ok {
		t.Fatalf("late handoff after artifact closed: ok=%v err=%v", ok, err)
	}

	targetNative := domain.AgentNativeSession{
		ID: "native-target", AOSessionID: session.ID, Harness: domain.HarnessCodex,
		NativeSessionID:  "codex-1",
		LastGenerationID: "target-generation", CreatedAt: now, LastUsedAt: now,
	}
	if _, _, err := s.CreateAgentNativeSession(ctx, targetNative); err != nil {
		t.Fatalf("create target native session: %v", err)
	}
	targetRef := targetNative.ID
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &current, domain.AgentSwitchStoppingSource, now.Add(3*time.Second), func(sw *domain.AgentSwitch) {
		sw.TargetNativeSessionRef = &targetRef
		sw.TargetStartMode = domain.AgentSwitchTargetStartResumed
		sw.TargetGenerationID = "target-generation"
	})
	advanceAgentSwitchFixture(ctx, t, s, &current, domain.AgentSwitchSourceStopped, now.Add(4*time.Second))
	finalPath := "/ao/handoffs/switch-1/handoff.json"
	finalHash := strings.Repeat("b", 64)
	if finalized, err := s.FinalizeAgentSwitchHandoff(
		ctx, current.ID, current.SessionID, current.SourceGenerationID, current.TargetGenerationID,
		finalPath, finalHash, true, domain.AgentSwitchSourceTranscriptUnavailable, now.Add(4500*time.Millisecond),
	); err != nil || !finalized {
		t.Fatalf("finalize semantic handoff: finalized=%v err=%v", finalized, err)
	}
	current, ok, err = s.GetAgentSwitch(ctx, current.ID)
	if err != nil || !ok || !current.SemanticHandoffIncluded || current.AgentHandoffPath != finalPath || current.AgentHandoffHash != finalHash {
		t.Fatalf("semantic inclusion fact = %+v, ok=%v err=%v", current, ok, err)
	}
	advanceAgentSwitchFixture(ctx, t, s, &current, domain.AgentSwitchStartingTarget, now.Add(5*time.Second))
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &current, domain.AgentSwitchStartingTarget, now.Add(5500*time.Millisecond), func(sw *domain.AgentSwitch) {
		sw.TargetRuntimeHandleID = "opaque-target-handle"
	})
	roundTripped, ok, err := s.GetAgentSwitch(ctx, current.ID)
	if err != nil || !ok || roundTripped.TargetRuntimeHandleID != "opaque-target-handle" {
		t.Fatalf("round-trip target runtime handle = %q, ok=%v err=%v", roundTripped.TargetRuntimeHandleID, ok, err)
	}
	staleHandle := current
	staleHandle.TargetRuntimeHandleID = "different-target-handle"
	staleHandle.UpdatedAt = now.Add(5750 * time.Millisecond)
	if ok, err := s.UpdateAgentSwitch(ctx, staleHandle, domain.AgentSwitchStartingTarget, "source-generation", "target-generation"); err != nil || ok {
		t.Fatalf("overwrite durable target runtime handle: ok=%v err=%v", ok, err)
	}
	advanceAgentSwitchFixture(ctx, t, s, &current, domain.AgentSwitchTargetReady, now.Add(6*time.Second))
	advanceAgentSwitchFixture(ctx, t, s, &current, domain.AgentSwitchDelivering, now.Add(7*time.Second))

	completedAt := now.Add(8 * time.Second)
	current.State = domain.AgentSwitchCompleted
	current.UpdatedAt = completedAt
	if ok, err := s.UpdateAgentSwitch(ctx, current, domain.AgentSwitchDelivering, "source-generation", "stale-target"); err != nil || ok {
		t.Fatalf("stale target completion: ok=%v err=%v", ok, err)
	}
	if ok, err := s.UpdateAgentSwitch(ctx, current, domain.AgentSwitchDelivering, "source-generation", "target-generation"); err != nil || !ok {
		t.Fatalf("complete target generation: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetActiveAgentSwitch(ctx, session.ID); err != nil || ok {
		t.Fatalf("completed switch remained active: ok=%v err=%v", ok, err)
	}

	// Terminal completion releases the durable one-active-saga fence.
	secondSwitch.UpdatedAt = completedAt.Add(time.Second)
	secondSwitch.RequestedAt = secondSwitch.UpdatedAt
	if _, created, err := s.CreateAgentSwitch(ctx, secondSwitch); err != nil || !created {
		t.Fatalf("create switch after completion: created=%v err=%v", created, err)
	}
}

func TestAgentSwitchTargetStartUnconfirmedMarkerIsNonTerminalAndMonotonic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-recovery")
	rec := sampleRecord("switch-recovery")
	now := rec.CreatedAt
	rec.Metadata.RuntimeHandleID = "source-handle"
	rec.Metadata.RuntimeLaunchID = "source-runtime"
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	target := domain.AgentNativeSession{
		ID: "recovery-target", AOSessionID: session.ID, Harness: domain.HarnessCodex,
		NativeSessionID: "codex-recovery", LastGenerationID: "target-generation",
		CreatedAt: now, LastUsedAt: now,
	}
	if _, _, err := s.CreateAgentNativeSession(ctx, target); err != nil {
		t.Fatalf("create target native session: %v", err)
	}
	targetRef := target.ID
	sw, created, err := s.CreateAgentSwitch(ctx, domain.AgentSwitch{
		ID: "switch-recovery", SessionID: session.ID, IdempotencyKey: "switch-recovery",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, TargetStartMode: domain.AgentSwitchTargetStartPending,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &sw, domain.AgentSwitchStoppingSource, now.Add(time.Second), func(sw *domain.AgentSwitch) {
		sw.TargetNativeSessionRef = &targetRef
		sw.TargetStartMode = domain.AgentSwitchTargetStartFresh
		sw.TargetGenerationID = "target-generation"
	})
	if ok, err := s.ConfirmAgentSwitchSourceStopped(ctx, domain.AgentSwitchSourceStopConfirmation{
		SwitchID: sw.ID, SessionID: session.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID: "source-generation", ExpectedSourceRuntimeLaunchID: "source-runtime",
		TargetGenerationID: "target-generation", StoppedAt: now.Add(2 * time.Second),
	}); err != nil || !ok {
		t.Fatalf("confirm source stopped: ok=%v err=%v", ok, err)
	}
	sw, _, _ = s.GetAgentSwitch(ctx, sw.ID)
	advanceAgentSwitchFixture(ctx, t, s, &sw, domain.AgentSwitchStartingTarget, now.Add(3*time.Second))

	sw.ErrorCode = domain.AgentSwitchErrorTargetStartUnconfirmed
	sw.UpdatedAt = now.Add(4 * time.Second)
	if ok, err := s.UpdateAgentSwitch(ctx, sw, domain.AgentSwitchStartingTarget, "source-generation", "target-generation"); err != nil || !ok {
		t.Fatalf("persist recovery marker: ok=%v err=%v", ok, err)
	}
	marked, ok, err := s.GetActiveAgentSwitch(ctx, session.ID)
	if err != nil || !ok || !marked.RequiresRecovery() || marked.State.Terminal() {
		t.Fatalf("marked switch = %+v, ok=%v err=%v", marked, ok, err)
	}

	staleClear := marked
	staleClear.ErrorCode = ""
	staleClear.UpdatedAt = now.Add(5 * time.Second)
	if changed, err := s.UpdateAgentSwitch(ctx, staleClear, domain.AgentSwitchStartingTarget, "source-generation", "target-generation"); err != nil || changed {
		t.Fatalf("stale recovery-marker clear: changed=%v err=%v", changed, err)
	}
	invalidHandle := marked
	invalidHandle.TargetRuntimeHandleID = "target-handle"
	invalidHandle.UpdatedAt = now.Add(5 * time.Second)
	if changed, err := s.UpdateAgentSwitch(ctx, invalidHandle, domain.AgentSwitchStartingTarget, "source-generation", "target-generation"); err == nil || changed {
		t.Fatalf("recovery marker with handle: changed=%v err=%v", changed, err)
	}
	if changed, err := s.ActivateAgentSwitchTarget(ctx, domain.AgentSwitchTargetActivation{
		SwitchID: marked.ID, SessionID: session.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID: "source-generation", ExpectedSourceRuntimeLaunchID: "source-runtime",
		TargetHarness: domain.HarnessCodex, TargetNativeSessionRef: target.ID,
		TargetGenerationID: "target-generation", RuntimeHandleID: "target-handle",
		ActivatedAt: now.Add(6 * time.Second),
	}); err != nil || changed {
		t.Fatalf("activate recovery-marked target: changed=%v err=%v", changed, err)
	}
	owner, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok || owner.Harness != domain.HarnessClaudeCode || owner.Activity.State != domain.ActivityExited {
		t.Fatalf("recovery activation changed source ownership: owner=%+v ok=%v err=%v", owner, ok, err)
	}
}

func TestAgentSwitchSourceStopMarkerCanAdvanceOnlyThroughConfirmedBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "source-stop-recovery")
	rec := sampleRecord("source-stop-recovery")
	now := rec.CreatedAt
	rec.Metadata.RuntimeHandleID = "source-handle"
	rec.Metadata.RuntimeLaunchID = "source-runtime"
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	sw, created, err := s.CreateAgentSwitch(ctx, domain.AgentSwitch{
		ID: "switch-source-stop", SessionID: session.ID, IdempotencyKey: "switch-source-stop",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, TargetStartMode: domain.AgentSwitchTargetStartPending,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &sw, domain.AgentSwitchStoppingSource, now.Add(time.Second), func(sw *domain.AgentSwitch) {
		sw.TargetGenerationID = "target-generation"
	})
	sw.ErrorCode = domain.AgentSwitchErrorSourceStopUnconfirmed
	sw.UpdatedAt = now.Add(2 * time.Second)
	if ok, err := s.UpdateAgentSwitch(ctx, sw, domain.AgentSwitchStoppingSource, "source-generation", "target-generation"); err != nil || !ok {
		t.Fatalf("persist source-stop marker: ok=%v err=%v", ok, err)
	}
	marked, ok, err := s.GetActiveAgentSwitch(ctx, session.ID)
	if err != nil || !ok || !marked.RequiresRecovery() || marked.State.Terminal() {
		t.Fatalf("marked switch = %+v, ok=%v err=%v", marked, ok, err)
	}

	staleClear := marked
	staleClear.ErrorCode = ""
	staleClear.UpdatedAt = now.Add(3 * time.Second)
	if changed, err := s.UpdateAgentSwitch(ctx, staleClear, domain.AgentSwitchStoppingSource, "source-generation", "target-generation"); err != nil || changed {
		t.Fatalf("stale source-stop clear: changed=%v err=%v", changed, err)
	}
	if ok, err := s.ConfirmAgentSwitchSourceStopped(ctx, domain.AgentSwitchSourceStopConfirmation{
		SwitchID: marked.ID, SessionID: session.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID: "source-generation", ExpectedSourceRuntimeLaunchID: "source-runtime", TargetGenerationID: "target-generation",
		StoppedAt: now.Add(4 * time.Second),
	}); err != nil || !ok {
		t.Fatalf("confirm marked source stopped: ok=%v err=%v", ok, err)
	}
	confirmed, ok, err := s.GetAgentSwitch(ctx, marked.ID)
	if err != nil || !ok || confirmed.State != domain.AgentSwitchSourceStopped || confirmed.ErrorCode != "" {
		t.Fatalf("confirmed switch = %+v, ok=%v err=%v", confirmed, ok, err)
	}
}

func TestAgentSwitchSourceRestoreMarkerCanSettleOnlyAsTerminalFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "source-restore-recovery")
	rec := sampleRecord("source-restore-recovery")
	now := rec.CreatedAt
	rec.Metadata.RuntimeHandleID = "source-handle"
	rec.Metadata.RuntimeLaunchID = "source-runtime"
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	sw, created, err := s.CreateAgentSwitch(ctx, domain.AgentSwitch{
		ID: "switch-source-restore", SessionID: session.ID, IdempotencyKey: "switch-source-restore",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, TargetStartMode: domain.AgentSwitchTargetStartPending,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &sw, domain.AgentSwitchStoppingSource, now.Add(time.Second), func(sw *domain.AgentSwitch) {
		sw.TargetGenerationID = "target-generation"
	})
	if ok, err := s.ConfirmAgentSwitchSourceStopped(ctx, domain.AgentSwitchSourceStopConfirmation{
		SwitchID: sw.ID, SessionID: session.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID: "source-generation", ExpectedSourceRuntimeLaunchID: "source-runtime", TargetGenerationID: "target-generation",
		StoppedAt: now.Add(2 * time.Second),
	}); err != nil || !ok {
		t.Fatalf("confirm source stopped: ok=%v err=%v", ok, err)
	}
	sw, _, _ = s.GetAgentSwitch(ctx, sw.ID)
	sw.ErrorCode = domain.AgentSwitchErrorSourceRestoreUnconfirmed
	sw.UpdatedAt = now.Add(3 * time.Second)
	if ok, err := s.UpdateAgentSwitch(ctx, sw, domain.AgentSwitchSourceStopped, "source-generation", "target-generation"); err != nil || !ok {
		t.Fatalf("persist source-restore marker: ok=%v err=%v", ok, err)
	}
	marked, ok, err := s.GetActiveAgentSwitch(ctx, session.ID)
	if err != nil || !ok || !marked.RequiresSourceRestore() || marked.State.Terminal() {
		t.Fatalf("marked switch = %+v, ok=%v err=%v", marked, ok, err)
	}

	staleClear := marked
	staleClear.ErrorCode = ""
	staleClear.UpdatedAt = now.Add(4 * time.Second)
	if changed, err := s.UpdateAgentSwitch(ctx, staleClear, domain.AgentSwitchSourceStopped, "source-generation", "target-generation"); err != nil || changed {
		t.Fatalf("stale source-restore clear: changed=%v err=%v", changed, err)
	}
	failed := marked
	failed.State = domain.AgentSwitchFailed
	failed.ErrorCode = domain.AgentSwitchErrorDaemonRestartPostStop
	failed.UpdatedAt = now.Add(5 * time.Second)
	if changed, err := s.UpdateAgentSwitch(ctx, failed, domain.AgentSwitchSourceStopped, "source-generation", "target-generation"); err != nil || !changed {
		t.Fatalf("terminal source-restore settlement: changed=%v err=%v", changed, err)
	}
}

func TestAgentHandoffOutcomeIsMonotonicWhenTimeoutWins(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "handoff-timeout")
	session, err := s.CreateSession(ctx, sampleRecord("handoff-timeout"))
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	switchRec := domain.AgentSwitch{
		ID: "switch-timeout", SessionID: session.ID, IdempotencyKey: "request-timeout",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode,
		TargetHarness:      domain.HarnessCodex, State: domain.AgentSwitchPreparingHandoff,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted,
		SourceGenerationID: "source-generation", RequestedAt: now, UpdatedAt: now,
	}
	if _, created, err := s.CreateAgentSwitch(ctx, switchRec); err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	if ok, err := s.RecordAgentHandoff(ctx, switchRec.ID, "source-generation", domain.AgentHandoffRequested, "", "", now.Add(time.Second)); err != nil || !ok {
		t.Fatalf("request handoff: ok=%v err=%v", ok, err)
	}
	if ok, err := s.RecordAgentHandoff(ctx, switchRec.ID, "source-generation", domain.AgentHandoffTimedOut, "", "", now.Add(2*time.Second)); err != nil || !ok {
		t.Fatalf("record timeout: ok=%v err=%v", ok, err)
	}
	if ok, err := s.RecordAgentHandoff(ctx, switchRec.ID, "source-generation", domain.AgentHandoffReceived, "/ao/handoffs/switch-timeout/agent-handoff.json", strings.Repeat("b", 64), now.Add(3*time.Second)); err != nil || ok {
		t.Fatalf("late response overwrote timeout: ok=%v err=%v", ok, err)
	}
	got, ok, err := s.GetAgentSwitch(ctx, switchRec.ID)
	if err != nil || !ok {
		t.Fatalf("get switch: ok=%v err=%v", ok, err)
	}
	if got.AgentHandoffStatus != domain.AgentHandoffTimedOut || got.AgentHandoffPath != "" || got.AgentHandoffHash != "" {
		t.Fatalf("handoff outcome = status %q path %q hash %q, want timed_out and empty reference", got.AgentHandoffStatus, got.AgentHandoffPath, got.AgentHandoffHash)
	}
}

func TestAgentHandoffUnavailableClosesRequestedLaneDuringRecovery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "handoff-recovery")
	session, err := s.CreateSession(ctx, sampleRecord("handoff-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	sw := domain.AgentSwitch{
		ID: "switch-recovery-handoff", SessionID: session.ID, IdempotencyKey: "request-recovery-handoff",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
		State: domain.AgentSwitchPreparingHandoff, AgentHandoffStatus: domain.AgentHandoffNotAttempted,
		SourceGenerationID: "source-generation", RequestedAt: now, UpdatedAt: now,
	}
	if _, created, err := s.CreateAgentSwitch(ctx, sw); err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	current, _, err := s.GetAgentSwitch(ctx, sw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RecordAgentHandoff(ctx, sw.ID, sw.SourceGenerationID, domain.AgentHandoffRequested, "", "", now.Add(2*time.Second)); err != nil || !ok {
		t.Fatalf("request handoff: ok=%v err=%v", ok, err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &current, domain.AgentSwitchStoppingSource, now.Add(4*time.Second), func(next *domain.AgentSwitch) {
		next.TargetGenerationID = "target-generation"
	})
	if ok, err := s.RecordAgentHandoff(ctx, sw.ID, sw.SourceGenerationID, domain.AgentHandoffUnavailable, "", "", now.Add(5*time.Second)); err != nil || !ok {
		t.Fatalf("close leaked requested handoff: ok=%v err=%v", ok, err)
	}
	got, _, err := s.GetAgentSwitch(ctx, sw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentHandoffStatus != domain.AgentHandoffUnavailable || got.AgentHandoffPath != "" || got.AgentHandoffHash != "" {
		t.Fatalf("recovered handoff = status %q path %q hash %q", got.AgentHandoffStatus, got.AgentHandoffPath, got.AgentHandoffHash)
	}
}

func TestAgentHandoffReferenceMatchesOutcome(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	tests := []struct {
		name   string
		status domain.AgentHandoffStatus
		path   string
		hash   string
	}{
		{name: "received without path", status: domain.AgentHandoffReceived, hash: strings.Repeat("a", 64)},
		{name: "received without hash", status: domain.AgentHandoffReceived, path: "/ao/handoffs/switch/agent-handoff.json"},
		{name: "received with padded path", status: domain.AgentHandoffReceived, path: " /ao/handoffs/switch/agent-handoff.json", hash: strings.Repeat("a", 64)},
		{name: "received with prefixed hash", status: domain.AgentHandoffReceived, path: "/ao/handoffs/switch/agent-handoff.json", hash: "sha256:" + strings.Repeat("a", 64)},
		{name: "received with short hash", status: domain.AgentHandoffReceived, path: "/ao/handoffs/switch/agent-handoff.json", hash: strings.Repeat("a", 63)},
		{name: "received with uppercase hash", status: domain.AgentHandoffReceived, path: "/ao/handoffs/switch/agent-handoff.json", hash: strings.Repeat("A", 64)},
		{name: "received with nonhex hash", status: domain.AgentHandoffReceived, path: "/ao/handoffs/switch/agent-handoff.json", hash: strings.Repeat("a", 63) + "g"},
		{name: "non-received with reference", status: domain.AgentHandoffTimedOut, path: "/ao/handoffs/switch/agent-handoff.json", hash: strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ok, err := s.RecordAgentHandoff(ctx, "switch", "source-generation", tt.status, tt.path, tt.hash, now); err == nil || ok {
				t.Fatalf("RecordAgentHandoff() = ok %v, err %v; want validation error", ok, err)
			}
		})
	}
}

func TestAgentSwitchRejectsNativeReferenceFromAnotherAOSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-owner")
	first, err := s.CreateSession(ctx, sampleRecord("switch-owner"))
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	second, err := s.CreateSession(ctx, sampleRecord("switch-owner"))
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	foreign := domain.AgentNativeSession{
		ID: "foreign-native", AOSessionID: second.ID, Harness: domain.HarnessCodex,
		NativeSessionID:  "codex-foreign",
		LastGenerationID: "foreign-generation", CreatedAt: now, LastUsedAt: now,
	}
	if _, _, err := s.CreateAgentNativeSession(ctx, foreign); err != nil {
		t.Fatalf("create foreign native session: %v", err)
	}
	base, created, err := s.CreateAgentSwitch(ctx, domain.AgentSwitch{
		ID: "cross-session", SessionID: first.ID, IdempotencyKey: "cross-session",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(first.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode,
		TargetHarness:      domain.HarnessCodex,
		State:              domain.AgentSwitchPreparingHandoff,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	advanceAgentSwitchFixture(ctx, t, s, &base, domain.AgentSwitchStoppingSource, now.Add(time.Second))
	advanceAgentSwitchFixture(ctx, t, s, &base, domain.AgentSwitchSourceStopped, now.Add(2*time.Second))
	advanceAgentSwitchFixture(ctx, t, s, &base, domain.AgentSwitchStartingTarget, now.Add(3*time.Second))
	foreignRef := foreign.ID
	base.TargetNativeSessionRef = &foreignRef
	base.TargetStartMode = domain.AgentSwitchTargetStartResumed
	base.TargetGenerationID = "target-generation"
	base.UpdatedAt = now.Add(4 * time.Second)
	if ok, err := s.UpdateAgentSwitch(ctx, base, domain.AgentSwitchStartingTarget, "source-generation", ""); err == nil || ok {
		t.Fatal("cross-session native reference was accepted")
	}
}

func TestAgentSwitchRejectsTargetNativeReferenceWithWrongHarness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-harness")
	session, err := s.CreateSession(ctx, sampleRecord("switch-harness"))
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	base := domain.AgentSwitch{
		ID: "wrong-target-switch", SessionID: session.ID, IdempotencyKey: "wrong-target-switch",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode,
		TargetHarness:      domain.HarnessCodex,
		State:              domain.AgentSwitchPreparingHandoff,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	stored, created, err := s.CreateAgentSwitch(ctx, base)
	if err != nil || !created {
		t.Fatalf("create switch without source reference: created=%v err=%v", created, err)
	}
	wrongTarget := domain.AgentNativeSession{
		ID: "wrong-target-harness", AOSessionID: session.ID, Harness: domain.HarnessClaudeCode,
		NativeSessionID:  "claude-target",
		LastGenerationID: "target-generation", CreatedAt: now, LastUsedAt: now,
	}
	if _, _, err := s.CreateAgentNativeSession(ctx, wrongTarget); err != nil {
		t.Fatalf("create wrong-harness target: %v", err)
	}
	advanceAgentSwitchFixture(ctx, t, s, &stored, domain.AgentSwitchStoppingSource, now.Add(time.Second))
	advanceAgentSwitchFixture(ctx, t, s, &stored, domain.AgentSwitchSourceStopped, now.Add(2*time.Second))
	advanceAgentSwitchFixture(ctx, t, s, &stored, domain.AgentSwitchStartingTarget, now.Add(3*time.Second))
	wrongTargetRef := wrongTarget.ID
	stored.TargetNativeSessionRef = &wrongTargetRef
	stored.TargetStartMode = domain.AgentSwitchTargetStartFresh
	stored.TargetGenerationID = "target-generation"
	stored.UpdatedAt = now.Add(4 * time.Second)
	if ok, err := s.UpdateAgentSwitch(ctx, stored, domain.AgentSwitchStartingTarget, "source-generation", ""); err == nil || ok {
		t.Fatalf("same-session target native reference with the wrong harness: ok=%v err=%v", ok, err)
	}
}

func TestAgentSwitchTargetAcknowledgementIsGenerationFencedAndWriteOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-ack")
	session, err := s.CreateSession(ctx, sampleRecord("switch-ack"))
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	target := domain.AgentNativeSession{
		ID: "ack-target", AOSessionID: session.ID, Harness: domain.HarnessCodex,
		NativeSessionID:  "codex-ack",
		LastGenerationID: "target-generation", CreatedAt: now, LastUsedAt: now,
	}
	if _, _, err := s.CreateAgentNativeSession(ctx, target); err != nil {
		t.Fatalf("create target native session: %v", err)
	}
	targetRef := target.ID
	sw := domain.AgentSwitch{
		ID: "switch-ack", SessionID: session.ID, IdempotencyKey: "switch-ack",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode,
		TargetHarness:      domain.HarnessCodex, State: domain.AgentSwitchPreparingHandoff,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	stored, created, err := s.CreateAgentSwitch(ctx, sw)
	if err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	for step, state := range []domain.AgentSwitchState{
		domain.AgentSwitchStoppingSource,
		domain.AgentSwitchSourceStopped,
		domain.AgentSwitchStartingTarget,
		domain.AgentSwitchTargetReady,
		domain.AgentSwitchDelivering,
	} {
		if state == domain.AgentSwitchStoppingSource {
			advanceAgentSwitchFixtureWithMutation(ctx, t, s, &stored, state, now.Add(time.Duration(step+1)*time.Second), func(sw *domain.AgentSwitch) {
				sw.TargetNativeSessionRef = &targetRef
				sw.TargetStartMode = domain.AgentSwitchTargetStartFresh
				sw.TargetGenerationID = "target-generation"
			})
			continue
		}
		advanceAgentSwitchFixture(ctx, t, s, &stored, state, now.Add(time.Duration(step+1)*time.Second))
	}

	acknowledgedAt := stored.UpdatedAt.Add(time.Second)
	if ok, err := s.AcknowledgeAgentSwitchTarget(ctx, sw.ID, session.ID, "stale-target", acknowledgedAt); err != nil || ok {
		t.Fatalf("stale target acknowledgement: ok=%v err=%v", ok, err)
	}
	if ok, err := s.AcknowledgeAgentSwitchTarget(ctx, sw.ID, session.ID, "target-generation", acknowledgedAt); err != nil || !ok {
		t.Fatalf("target acknowledgement: ok=%v err=%v", ok, err)
	}
	if ok, err := s.AcknowledgeAgentSwitchTarget(ctx, sw.ID, session.ID, "target-generation", acknowledgedAt.Add(time.Second)); err != nil || ok {
		t.Fatalf("duplicate target acknowledgement: ok=%v err=%v", ok, err)
	}
	got, ok, err := s.GetAgentSwitch(ctx, sw.ID)
	if err != nil || !ok {
		t.Fatalf("get acknowledged switch: ok=%v err=%v", ok, err)
	}
	if got.TargetAcknowledgedAt == nil || !got.TargetAcknowledgedAt.Equal(acknowledgedAt) {
		t.Fatalf("target acknowledgement = %v, want %v", got.TargetAcknowledgedAt, acknowledgedAt)
	}
}

func TestAgentSwitchDeliveryFailureIsAtomicWithAcknowledgement(t *testing.T) {
	tests := []struct {
		name              string
		acknowledgesFirst bool
		wantState         domain.AgentSwitchState
		wantFailed        bool
	}{
		{name: "acknowledgement wins", acknowledgesFirst: true, wantState: domain.AgentSwitchDelivering},
		{name: "failure wins", wantState: domain.AgentSwitchFailed, wantFailed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			seedProject(t, s, "switch-delivery-outcome")
			session, err := s.CreateSession(ctx, sampleRecord("switch-delivery-outcome"))
			if err != nil {
				t.Fatalf("create AO session: %v", err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			target := domain.AgentNativeSession{
				ID: "delivery-target", AOSessionID: session.ID, Harness: domain.HarnessCodex,
				NativeSessionID:  "codex-delivery",
				LastGenerationID: "target-generation", CreatedAt: now, LastUsedAt: now,
			}
			if _, _, err := s.CreateAgentNativeSession(ctx, target); err != nil {
				t.Fatalf("create target native session: %v", err)
			}
			targetRef := target.ID
			sw := domain.AgentSwitch{
				ID: "switch-delivery", SessionID: session.ID, IdempotencyKey: "switch-delivery",
				RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
				FromHarness:        domain.HarnessClaudeCode,
				TargetHarness:      domain.HarnessCodex, State: domain.AgentSwitchPreparingHandoff,
				AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
				RequestedAt: now, UpdatedAt: now,
			}
			stored, created, err := s.CreateAgentSwitch(ctx, sw)
			if err != nil || !created {
				t.Fatalf("create switch: created=%v err=%v", created, err)
			}
			for step, nextState := range []domain.AgentSwitchState{
				domain.AgentSwitchStoppingSource,
				domain.AgentSwitchSourceStopped,
				domain.AgentSwitchStartingTarget,
				domain.AgentSwitchTargetReady,
				domain.AgentSwitchDelivering,
			} {
				expectedState := stored.State
				expectedTarget := stored.TargetGenerationID
				if nextState == domain.AgentSwitchStoppingSource {
					stored.TargetNativeSessionRef = &targetRef
					stored.TargetStartMode = domain.AgentSwitchTargetStartFresh
					stored.TargetGenerationID = "target-generation"
				}
				stored.State = nextState
				stored.UpdatedAt = now.Add(time.Duration(step+1) * time.Second)
				if ok, err := s.UpdateAgentSwitch(ctx, stored, expectedState, "source-generation", expectedTarget); err != nil || !ok {
					t.Fatalf("advance context delivery to %s: ok=%v err=%v", nextState, ok, err)
				}
			}

			acknowledgedAt := stored.UpdatedAt.Add(time.Second)
			if tt.acknowledgesFirst {
				if ok, err := s.AcknowledgeAgentSwitchTarget(ctx, sw.ID, session.ID, "target-generation", acknowledgedAt); err != nil || !ok {
					t.Fatalf("acknowledge before failure: ok=%v err=%v", ok, err)
				}
			}
			failedAt := acknowledgedAt.Add(time.Second)
			failure := stored
			failure.State = domain.AgentSwitchFailed
			failure.ErrorCode = "delivery_unconfirmed"
			failure.UpdatedAt = failedAt
			if ok, err := s.UpdateAgentSwitch(ctx, failure, domain.AgentSwitchDelivering, "source-generation", "target-generation"); err == nil || ok {
				t.Fatalf("generic delivery failure bypassed acknowledgement CAS: ok=%v err=%v", ok, err)
			}
			failed, err := s.FailAgentSwitchIfUnacknowledged(ctx, failure)
			if err != nil || failed != tt.wantFailed {
				t.Fatalf("fail unacknowledged delivery: failed=%v want=%v err=%v", failed, tt.wantFailed, err)
			}
			if !tt.acknowledgesFirst {
				if ok, err := s.AcknowledgeAgentSwitchTarget(ctx, sw.ID, session.ID, "target-generation", failedAt.Add(time.Second)); err != nil || ok {
					t.Fatalf("late acknowledgement after failure: ok=%v err=%v", ok, err)
				}
			}

			got, ok, err := s.GetAgentSwitch(ctx, sw.ID)
			if err != nil || !ok {
				t.Fatalf("get settled delivery: ok=%v err=%v", ok, err)
			}
			if got.State != tt.wantState {
				t.Fatalf("delivery state = %q, want %q", got.State, tt.wantState)
			}
			if tt.acknowledgesFirst {
				if got.TargetAcknowledgedAt == nil || !got.TargetAcknowledgedAt.Equal(acknowledgedAt) || got.ErrorCode != "" {
					t.Fatalf("acknowledgement winner was overwritten: %+v", got)
				}
			} else if got.TargetAcknowledgedAt != nil || got.ErrorCode != failure.ErrorCode {
				t.Fatalf("failure winner was overwritten: %+v", got)
			}
		})
	}
}

func TestAgentSwitchSourceStopAndTargetActivationAreAtomicAndNarrow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-activation")
	rec := sampleRecord("switch-activation")
	now := rec.CreatedAt
	rec.DisplayName = "Keep this name"
	rec.FirstSignalAt = now
	rec.TerminateOnPRMerge = true
	rec.CleanupGeneration = 7
	rec.Metadata.RuntimeHandleID = "source-handle"
	rec.Metadata.RuntimeLaunchID = "source-runtime-generation"
	rec.Metadata.AgentSessionID = "claude-native-id"
	rec.Metadata.NativeTranscriptPath = "/claude/source.jsonl"
	rec.Metadata.Prompt = "original task"
	rec.Metadata.LatestUserPrompt = "latest user direction"
	rec.Metadata.LatestAssistantUpdate = "latest assistant update"
	rec.Metadata.PreviewURL = "http://localhost:3000"
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	conversation, err := s.CreateConversation(ctx, "conversation-switch-activation", domain.ConversationScopeSession, session.ProjectID, session.ID, now)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.SetConversationSettings(ctx, conversation.ID, domain.ConversationSettings{
		Model: "source-model", ReasoningEffort: "source-mode", ApprovalMode: domain.PermissionModeAcceptEdits,
	}, now); err != nil {
		t.Fatalf("set source conversation settings: %v", err)
	}
	target := domain.AgentNativeSession{
		ID: "activation-target", AOSessionID: session.ID, Harness: domain.HarnessCodex,
		NativeSessionID: "codex-native-id",
		TranscriptPath:  "/codex/target.jsonl", LastGenerationID: "target-generation",
		CreatedAt: now, LastUsedAt: now,
	}
	if _, _, err := s.CreateAgentNativeSession(ctx, target); err != nil {
		t.Fatalf("create target native session: %v", err)
	}
	targetRef := target.ID
	sw := domain.AgentSwitch{
		ID: "switch-activation", SessionID: session.ID, IdempotencyKey: "switch-activation",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode,
		TargetHarness:      domain.HarnessCodex, State: domain.AgentSwitchPreparingHandoff,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-switch-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	stored, created, err := s.CreateAgentSwitch(ctx, sw)
	if err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &stored, domain.AgentSwitchStoppingSource, now.Add(time.Second), func(sw *domain.AgentSwitch) {
		sw.TargetNativeSessionRef = &targetRef
		sw.TargetStartMode = domain.AgentSwitchTargetStartFresh
		sw.TargetGenerationID = "target-generation"
	})

	confirmation := domain.AgentSwitchSourceStopConfirmation{
		SwitchID: sw.ID, SessionID: session.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID:            "source-switch-generation",
		ExpectedSourceRuntimeLaunchID: "source-runtime-generation",
		TargetGenerationID:            "stale-target", StoppedAt: now.Add(2 * time.Second),
	}
	if ok, err := s.ConfirmAgentSwitchSourceStopped(ctx, confirmation); err != nil || ok {
		t.Fatalf("stale source-stop confirmation: ok=%v err=%v", ok, err)
	}
	unchanged, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok || unchanged.Activity.State != domain.ActivityActive {
		t.Fatalf("stale source stop mutated session: session=%+v ok=%v err=%v", unchanged, ok, err)
	}
	confirmation.TargetGenerationID = "target-generation"
	if ok, err := s.ConfirmAgentSwitchSourceStopped(ctx, confirmation); err != nil || !ok {
		t.Fatalf("confirm source stopped: ok=%v err=%v", ok, err)
	}
	exited, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("get exited source session: ok=%v err=%v", ok, err)
	}
	if exited.Activity.State != domain.ActivityExited || exited.Harness != domain.HarnessClaudeCode ||
		exited.Metadata.RuntimeLaunchID != "source-runtime-generation" || exited.Metadata.RuntimeHandleID != "source-handle" {
		t.Fatalf("source-stop projection changed owner facts: %+v", exited)
	}
	if exited.FirstSignalAt.IsZero() || exited.Metadata.LatestUserPrompt != rec.Metadata.LatestUserPrompt {
		t.Fatalf("source-stop projection changed unrelated facts: %+v", exited)
	}
	lateSource := exited
	lateSource.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: now.Add(2500 * time.Millisecond)}
	lateSource.Metadata.AgentSessionID = "late-source-native"
	lateSource.Metadata.LatestAssistantUpdate = "late source callback"
	lateSource.UpdatedAt = now.Add(2500 * time.Millisecond)
	if applied, err := s.UpdateSessionFromActivitySignal(ctx, lateSource); err != nil || applied {
		t.Fatalf("late source activity after stop confirmation: applied=%v err=%v", applied, err)
	}
	stillExited, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok || stillExited != exited {
		t.Fatalf("late source activity mutated stopped projection: session=%+v ok=%v err=%v", stillExited, ok, err)
	}

	stored, ok, err = s.GetAgentSwitch(ctx, sw.ID)
	if err != nil || !ok || stored.State != domain.AgentSwitchSourceStopped {
		t.Fatalf("switch after source stop = %+v, ok=%v err=%v", stored, ok, err)
	}
	finalPath := "/ao/handoffs/switch-source-stop/handoff.json"
	finalHash := strings.Repeat("a", 64)
	if finalized, finalizeErr := s.FinalizeAgentSwitchHandoff(
		ctx, stored.ID, stored.SessionID, stored.SourceGenerationID, stored.TargetGenerationID,
		finalPath, finalHash, true, domain.AgentSwitchSourceTranscriptAvailable, now.Add(2700*time.Millisecond),
	); finalizeErr != nil || finalized {
		t.Fatalf("finalize nonexistent semantic handoff: finalized=%v err=%v", finalized, finalizeErr)
	}
	if finalized, finalizeErr := s.FinalizeAgentSwitchHandoff(
		ctx, stored.ID, stored.SessionID, stored.SourceGenerationID, stored.TargetGenerationID,
		finalPath, finalHash, false, domain.AgentSwitchSourceTranscriptAvailable, now.Add(2750*time.Millisecond),
	); finalizeErr != nil || !finalized {
		t.Fatalf("finalize handoff: finalized=%v err=%v", finalized, finalizeErr)
	}
	stored, ok, err = s.GetAgentSwitch(ctx, sw.ID)
	if err != nil || !ok || stored.FinalHandoffPath != finalPath || stored.FinalHandoffHash != finalHash || stored.SourceTranscriptStatus != domain.AgentSwitchSourceTranscriptAvailable || stored.SemanticHandoffIncluded {
		t.Fatalf("finalized handoff facts = %+v, ok=%v err=%v", stored, ok, err)
	}
	stored.State = domain.AgentSwitchStartingTarget
	stored.UpdatedAt = now.Add(3 * time.Second)
	if ok, err := s.UpdateAgentSwitch(ctx, stored, domain.AgentSwitchSourceStopped, "source-switch-generation", "target-generation"); err != nil || !ok {
		t.Fatalf("advance to starting target: ok=%v err=%v", ok, err)
	}
	stored.TargetRuntimeHandleID = "target-handle"
	stored.UpdatedAt = now.Add(3500 * time.Millisecond)
	if ok, err := s.UpdateAgentSwitch(ctx, stored, domain.AgentSwitchStartingTarget, "source-switch-generation", "target-generation"); err != nil || !ok {
		t.Fatalf("record target runtime handle: ok=%v err=%v", ok, err)
	}
	activation := domain.AgentSwitchTargetActivation{
		SwitchID: sw.ID, SessionID: session.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID:            "stale-source",
		ExpectedSourceRuntimeLaunchID: "source-runtime-generation",
		TargetHarness:                 domain.HarnessCodex, TargetNativeSessionRef: target.ID,
		TargetGenerationID: "target-generation", RuntimeHandleID: "target-handle",
		ActivatedAt: now.Add(4 * time.Second),
	}
	if ok, err := s.ActivateAgentSwitchTarget(ctx, activation); err != nil || ok {
		t.Fatalf("stale target activation: ok=%v err=%v", ok, err)
	}
	unchanged, ok, err = s.GetSession(ctx, session.ID)
	if err != nil || !ok || unchanged.Harness != domain.HarnessClaudeCode || unchanged.Activity.State != domain.ActivityExited {
		t.Fatalf("stale activation partially mutated session: session=%+v ok=%v err=%v", unchanged, ok, err)
	}
	activation.SourceGenerationID = "source-switch-generation"
	activation.RuntimeHandleID = "stale-target-handle"
	if ok, err := s.ActivateAgentSwitchTarget(ctx, activation); err != nil || ok {
		t.Fatalf("stale runtime handle activation: ok=%v err=%v", ok, err)
	}
	unchangedConversation, err := s.ConversationForSession(ctx, session.ID)
	if err != nil || unchangedConversation.Settings.Model != "source-model" || unchangedConversation.Settings.ReasoningEffort != "source-mode" {
		t.Fatalf("stale activation changed conversation settings: conversation=%+v err=%v", unchangedConversation, err)
	}
	unchanged, ok, err = s.GetSession(ctx, session.ID)
	if err != nil || !ok || unchanged.Harness != domain.HarnessClaudeCode || unchanged.Activity.State != domain.ActivityExited {
		t.Fatalf("stale runtime handle partially mutated session: session=%+v ok=%v err=%v", unchanged, ok, err)
	}
	activation.RuntimeHandleID = "target-handle"
	if ok, err := s.ActivateAgentSwitchTarget(ctx, activation); err != nil || !ok {
		t.Fatalf("activate target: ok=%v err=%v", ok, err)
	}
	activated, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("get activated session: ok=%v err=%v", ok, err)
	}
	if activated.Harness != domain.HarnessCodex || activated.Activity.State != domain.ActivityIdle ||
		activated.Metadata.RuntimeHandleID != "target-handle" || activated.Metadata.RuntimeLaunchID != "target-generation" ||
		activated.Metadata.AgentSessionID != target.NativeSessionID || activated.Metadata.AgentSessionIDLaunchID != "target-generation" ||
		activated.Metadata.NativeTranscriptPath != target.TranscriptPath {
		t.Fatalf("target owner projection = %+v", activated)
	}
	if !activated.FirstSignalAt.IsZero() {
		t.Fatalf("target activation retained old hook receipt: %v", activated.FirstSignalAt)
	}
	activatedConversation, err := s.ConversationForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get activated conversation: %v", err)
	}
	if activatedConversation.Settings.Model != "" || activatedConversation.Settings.ReasoningEffort != "" || activatedConversation.Settings.ApprovalMode != domain.PermissionModeAcceptEdits {
		t.Fatalf("target activation conversation settings = %+v, want target-default model/mode and preserved approval", activatedConversation.Settings)
	}
	if activated.IsTerminated || activated.DisplayName != rec.DisplayName ||
		activated.TerminateOnPRMerge != rec.TerminateOnPRMerge || activated.CleanupGeneration != rec.CleanupGeneration ||
		activated.Metadata.Branch != rec.Metadata.Branch || activated.Metadata.WorkspacePath != rec.Metadata.WorkspacePath ||
		activated.Metadata.Prompt != rec.Metadata.Prompt || activated.Metadata.LatestUserPrompt != rec.Metadata.LatestUserPrompt ||
		activated.Metadata.LatestAssistantUpdate != rec.Metadata.LatestAssistantUpdate || activated.Metadata.PreviewURL != rec.Metadata.PreviewURL {
		t.Fatalf("target activation changed unrelated session facts: %+v", activated)
	}
	lateSource.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: now.Add(5 * time.Second)}
	lateSource.UpdatedAt = now.Add(5 * time.Second)
	if applied, err := s.UpdateSessionFromActivitySignal(ctx, lateSource); err != nil || applied {
		t.Fatalf("late source activity after target activation: applied=%v err=%v", applied, err)
	}
	stillActivated, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok || stillActivated != activated {
		t.Fatalf("late source activity overwrote target owner: session=%+v ok=%v err=%v", stillActivated, ok, err)
	}
	finalSwitch, ok, err := s.GetAgentSwitch(ctx, sw.ID)
	if err != nil || !ok || finalSwitch.State != domain.AgentSwitchTargetReady {
		t.Fatalf("switch after target activation = %+v, ok=%v err=%v", finalSwitch, ok, err)
	}

	// The source-side fence must not suppress the exact target generation or
	// its normal delivery acknowledgement after atomic ownership transfer.
	finalSwitch.State = domain.AgentSwitchDelivering
	finalSwitch.UpdatedAt = now.Add(6 * time.Second)
	if ok, err := s.UpdateAgentSwitch(ctx, finalSwitch, domain.AgentSwitchTargetReady, "source-switch-generation", "target-generation"); err != nil || !ok {
		t.Fatalf("open target delivery: ok=%v err=%v", ok, err)
	}
	targetSignal := activated
	targetSignal.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: now.Add(7 * time.Second)}
	targetSignal.FirstSignalAt = now.Add(7 * time.Second)
	targetSignal.UpdatedAt = now.Add(7 * time.Second)
	if applied, err := s.UpdateSessionFromActivitySignal(ctx, targetSignal); err != nil || !applied {
		t.Fatalf("target activity during delivery: applied=%v err=%v", applied, err)
	}
	acknowledgedAt := now.Add(8 * time.Second)
	if acknowledged, err := s.AcknowledgeAgentSwitchTarget(ctx, sw.ID, session.ID, "target-generation", acknowledgedAt); err != nil || !acknowledged {
		t.Fatalf("target acknowledgement after guarded activity: acknowledged=%v err=%v", acknowledged, err)
	}
}

func TestAgentSwitchOwnershipTransactionsRejectTerminatedSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-terminated")
	rec := sampleRecord("switch-terminated")
	now := rec.CreatedAt
	rec.IsTerminated = true
	rec.Activity.State = domain.ActivityExited
	rec.Metadata.RuntimeHandleID = "source-handle"
	rec.Metadata.RuntimeLaunchID = "source-runtime"
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create terminated AO session: %v", err)
	}
	target := domain.AgentNativeSession{
		ID: "terminated-target", AOSessionID: session.ID, Harness: domain.HarnessCodex,
		NativeSessionID:  "codex-terminated",
		LastGenerationID: "target-generation", CreatedAt: now, LastUsedAt: now,
	}
	if _, _, err := s.CreateAgentNativeSession(ctx, target); err != nil {
		t.Fatalf("create target native session: %v", err)
	}
	targetRef := target.ID
	sw := domain.AgentSwitch{
		ID: "switch-terminated", SessionID: session.ID, IdempotencyKey: "switch-terminated",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode,
		TargetHarness:      domain.HarnessCodex, TargetNativeSessionRef: nil,
		State:              domain.AgentSwitchPreparingHandoff,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	stored, _, err := s.CreateAgentSwitch(ctx, sw)
	if err != nil {
		t.Fatalf("create switch: %v", err)
	}
	advanceAgentSwitchFixtureWithMutation(ctx, t, s, &stored, domain.AgentSwitchStoppingSource, now.Add(time.Second), func(sw *domain.AgentSwitch) {
		sw.TargetNativeSessionRef = &targetRef
		sw.TargetStartMode = domain.AgentSwitchTargetStartFresh
		sw.TargetGenerationID = "target-generation"
	})
	if ok, err := s.ConfirmAgentSwitchSourceStopped(ctx, domain.AgentSwitchSourceStopConfirmation{
		SwitchID: sw.ID, SessionID: session.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID: "source-generation", ExpectedSourceRuntimeLaunchID: "source-runtime",
		TargetGenerationID: "target-generation", StoppedAt: now.Add(2 * time.Second),
	}); err != nil || ok {
		t.Fatalf("terminated source-stop confirmation: ok=%v err=%v", ok, err)
	}
	current, ok, err := s.GetAgentSwitch(ctx, sw.ID)
	if err != nil || !ok || current.State != domain.AgentSwitchStoppingSource {
		t.Fatalf("terminated source stop partially advanced switch: %+v ok=%v err=%v", current, ok, err)
	}
	advanceAgentSwitchFixture(ctx, t, s, &current, domain.AgentSwitchSourceStopped, now.Add(2500*time.Millisecond))
	advanceAgentSwitchFixture(ctx, t, s, &current, domain.AgentSwitchStartingTarget, now.Add(3*time.Second))
	if ok, err := s.ActivateAgentSwitchTarget(ctx, domain.AgentSwitchTargetActivation{
		SwitchID: sw.ID, SessionID: session.ID, SourceHarness: domain.HarnessClaudeCode,
		SourceGenerationID: "source-generation", ExpectedSourceRuntimeLaunchID: "source-runtime",
		TargetHarness: domain.HarnessCodex, TargetNativeSessionRef: target.ID,
		TargetGenerationID: "target-generation", RuntimeHandleID: "target-handle",
		ActivatedAt: now.Add(4 * time.Second),
	}); err != nil || ok {
		t.Fatalf("terminated target activation: ok=%v err=%v", ok, err)
	}
	current, ok, err = s.GetAgentSwitch(ctx, sw.ID)
	if err != nil || !ok || current.State != domain.AgentSwitchStartingTarget {
		t.Fatalf("terminated activation partially advanced switch: %+v ok=%v err=%v", current, ok, err)
	}
	unchanged, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok || !unchanged.IsTerminated || unchanged.Harness != domain.HarnessClaudeCode || unchanged.Metadata.RuntimeHandleID != "source-handle" {
		t.Fatalf("terminated activation resurrected session: %+v ok=%v err=%v", unchanged, ok, err)
	}
}

func TestAgentSwitchAndOwnerChangesEmitSessionInvalidationCDC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "switch-cdc")
	session, err := s.CreateSession(ctx, sampleRecord("switch-cdc"))
	if err != nil {
		t.Fatalf("create AO session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	baseSeq, _ := s.LatestSeq(ctx)
	sw := domain.AgentSwitch{
		ID: "switch-cdc", SessionID: session.ID, IdempotencyKey: "switch-cdc",
		RequestFingerprint: domain.ComputeAgentSwitchRequestFingerprint(session.ID, domain.HarnessCodex, ""),
		FromHarness:        domain.HarnessClaudeCode,
		TargetHarness:      domain.HarnessCodex, State: domain.AgentSwitchPreparingHandoff,
		AgentHandoffStatus: domain.AgentHandoffNotAttempted, SourceGenerationID: "source-generation",
		RequestedAt: now, UpdatedAt: now,
	}
	if _, created, err := s.CreateAgentSwitch(ctx, sw); err != nil || !created {
		t.Fatalf("create switch: created=%v err=%v", created, err)
	}
	events, err := s.EventsAfter(ctx, baseSeq, 100)
	if err != nil {
		t.Fatalf("read switch insert CDC: %v", err)
	}
	if len(events) != 1 || string(events[0].Type) != "session_updated" {
		t.Fatalf("switch insert events = %+v, want one session_updated", events)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode switch CDC payload: %v", err)
	}
	if len(payload) != 1 || payload["id"] != string(session.ID) {
		t.Fatalf("switch CDC payload = %v, want id-only", payload)
	}

	baseSeq, _ = s.LatestSeq(ctx)
	current, ok, err := s.GetSession(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	current.Harness = domain.HarnessCodex
	current.Metadata.RuntimeLaunchID = "target-generation"
	current.Metadata.AgentSessionID = "target-native-id"
	current.Metadata.NativeTranscriptPath = "/target/session.jsonl"
	current.UpdatedAt = now.Add(time.Second)
	if err := s.UpdateSession(ctx, current); err != nil {
		t.Fatalf("update owner facts: %v", err)
	}
	events, err = s.EventsAfter(ctx, baseSeq, 100)
	if err != nil {
		t.Fatalf("read owner CDC: %v", err)
	}
	if len(events) != 1 || string(events[0].Type) != "session_updated" {
		t.Fatalf("owner update events = %+v, want one session_updated", events)
	}
}
