package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionInterfaceTransitionClaimModeCASAndOutbox(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedProject(t, st, "switch")
	now := time.Now().UTC().Truncate(time.Second)
	rec := sampleRecord("switch")
	rec.ID = "switch-1"
	rec.Mode = domain.SessionModeTUI
	rec.Metadata.RuntimeHandleID = "tmux-switch-1"
	rec.Metadata.RuntimeLaunchID = "tui-generation-1"
	rec.Metadata.AgentSessionID = "native-conversation-1"
	createdSession, err := st.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	transition, created, err := st.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: "transition-1", SessionID: createdSession.ID,
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Policy: domain.SessionInterfaceTransitionDrain, Phase: domain.SessionInterfaceTransitionRequested,
		NativeConversationID: "native-conversation-1", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("create transition: created=%v err=%v", created, err)
	}
	if transition.ID != "transition-1" {
		t.Fatalf("transition id = %q", transition.ID)
	}

	winner, created, err := st.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: "transition-racer", SessionID: createdSession.ID,
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Policy: domain.SessionInterfaceTransitionInterrupt, Phase: domain.SessionInterfaceTransitionRequested,
		NativeConversationID: "native-conversation-1", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	})
	if err != nil || created || winner.ID != transition.ID {
		t.Fatalf("concurrent claim: winner=%q created=%v err=%v", winner.ID, created, err)
	}

	moved, err := st.AdvanceSessionInterfaceTransition(ctx, transition.ID,
		domain.SessionInterfaceTransitionRequested, domain.SessionInterfaceTransitionSourceStopped,
		transition.NativeConversationID, "", "", now.Add(2*time.Second))
	if err != nil || !moved {
		t.Fatalf("advance transition: moved=%v err=%v", moved, err)
	}
	staleMove, err := st.AdvanceSessionInterfaceTransition(ctx, transition.ID,
		domain.SessionInterfaceTransitionRequested, domain.SessionInterfaceTransitionFailed,
		transition.NativeConversationID, "STALE", "stale writer", now.Add(3*time.Second))
	if err != nil || staleMove {
		t.Fatalf("stale phase CAS: moved=%v err=%v", staleMove, err)
	}

	changed, err := st.CommitSessionControllerEpoch(ctx, createdSession.ID,
		domain.SessionModeTUI, domain.SessionModeChat, transition.NativeConversationID, now.Add(4*time.Second))
	if err != nil || !changed {
		t.Fatalf("switch mode: changed=%v err=%v", changed, err)
	}
	changedAgain, err := st.CommitSessionControllerEpoch(ctx, createdSession.ID,
		domain.SessionModeTUI, domain.SessionModeChat, transition.NativeConversationID, now.Add(5*time.Second))
	if err != nil || changedAgain {
		t.Fatalf("stale mode CAS: changed=%v err=%v", changedAgain, err)
	}

	after, ok, err := st.GetSession(ctx, createdSession.ID)
	if err != nil || !ok {
		t.Fatalf("get switched session: ok=%v err=%v", ok, err)
	}
	if after.Mode != domain.SessionModeChat || after.Metadata.RuntimeHandleID != "" || after.Metadata.RuntimeLaunchID != "" {
		t.Fatalf("switched controller facts = mode:%q runtime:%q launch:%q",
			after.Mode, after.Metadata.RuntimeHandleID, after.Metadata.RuntimeLaunchID)
	}
	if after.Metadata.AgentSessionID != transition.NativeConversationID ||
		after.Metadata.ProviderConversationID != transition.NativeConversationID ||
		after.Metadata.ControllerGeneration != "" {
		t.Fatalf("switched native facts = %+v", after.Metadata)
	}
	if after.Activity.State != domain.ActivityIdle {
		t.Fatalf("switched activity = %q, want idle", after.Activity.State)
	}

	if err := st.EnqueueSessionInterfaceTransitionMessage(
		ctx, transition.ID, "transition-message-1", "CI finished", now.Add(6*time.Second),
	); err != nil {
		t.Fatalf("enqueue transition message: %v", err)
	}
	messages, err := st.ListPendingSessionInterfaceTransitionMessages(ctx, transition.ID)
	if err != nil || len(messages) != 1 || messages[0].Message != "CI finished" {
		t.Fatalf("pending messages = %+v err=%v", messages, err)
	}
	if messages[0].ClientMessageID != "transition-message-1" {
		t.Fatalf("client message id = %q", messages[0].ClientMessageID)
	}
	if err := st.MarkSessionInterfaceTransitionMessageDelivered(ctx, messages[0].ID, now.Add(7*time.Second)); err != nil {
		t.Fatalf("mark message delivered: %v", err)
	}
	messages, err = st.ListPendingSessionInterfaceTransitionMessages(ctx, transition.ID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("messages after delivery = %+v err=%v", messages, err)
	}

	moved, err = st.AdvanceSessionInterfaceTransition(ctx, transition.ID,
		domain.SessionInterfaceTransitionSourceStopped, domain.SessionInterfaceTransitionCompleted,
		transition.NativeConversationID, "", "", now.Add(8*time.Second))
	if err != nil || !moved {
		t.Fatalf("complete transition: moved=%v err=%v", moved, err)
	}
	latest, ok, err := st.GetLatestSessionInterfaceTransition(ctx, createdSession.ID)
	if err != nil || !ok || latest.Phase != domain.SessionInterfaceTransitionCompleted || latest.CompletedAt.IsZero() {
		t.Fatalf("latest transition = %+v ok=%v err=%v", latest, ok, err)
	}
	if _, active, err := st.GetActiveSessionInterfaceTransition(ctx, createdSession.ID); err != nil || active {
		t.Fatalf("active after completion = %v err=%v", active, err)
	}
}

func TestSessionInterfaceTransitionNoticeAcknowledgementRoundTripAndCDC(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedProject(t, st, "switch-notice")
	now := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	rec := sampleRecord("switch-notice")
	rec.Mode = domain.SessionModeChat
	createdSession, err := st.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	transition, created, err := st.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: "transition-notice", SessionID: createdSession.ID,
		SourceMode: domain.SessionModeChat, TargetMode: domain.SessionModeTUI,
		Policy: domain.SessionInterfaceTransitionDrain, Phase: domain.SessionInterfaceTransitionRequested,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("create transition: created=%v err=%v", created, err)
	}
	settledAt := now.Add(time.Minute)
	moved, err := st.AdvanceSessionInterfaceTransition(ctx, transition.ID,
		domain.SessionInterfaceTransitionRequested, domain.SessionInterfaceTransitionRecovery,
		"", "DAEMON_RESTARTED", "AO recovered the session.", settledAt)
	if err != nil || !moved {
		t.Fatalf("settle transition: moved=%v err=%v", moved, err)
	}

	baseSeq, _ := st.LatestSeq(ctx)
	acknowledgedAt := settledAt.Add(time.Minute)
	acknowledged, ok, err := st.AcknowledgeSessionInterfaceTransitionNotice(
		ctx, createdSession.ID, transition.ID, acknowledgedAt,
	)
	if err != nil || !ok {
		t.Fatalf("acknowledge transition notice: ok=%v err=%v", ok, err)
	}
	if !acknowledged.NoticeAcknowledgedAt.Equal(acknowledgedAt) {
		t.Fatalf("notice acknowledged at = %v, want %v", acknowledged.NoticeAcknowledgedAt, acknowledgedAt)
	}
	if !acknowledged.UpdatedAt.Equal(settledAt) || !acknowledged.CompletedAt.Equal(settledAt) {
		t.Fatalf("acknowledgement rewrote settlement timestamps: %+v", acknowledged)
	}
	events, err := st.EventsAfter(ctx, baseSeq, 100)
	if err != nil {
		t.Fatalf("read acknowledgement CDC: %v", err)
	}
	if len(events) != 1 || string(events[0].Type) != "session_updated" {
		t.Fatalf("acknowledgement events = %+v, want one session_updated", events)
	}
	if !events[0].CreatedAt.Equal(acknowledgedAt) {
		t.Fatalf("acknowledgement event time = %v, want %v", events[0].CreatedAt, acknowledgedAt)
	}
	var payload struct {
		InterfaceTransitionID    string `json:"interfaceTransitionId"`
		InterfaceTransitionPhase string `json:"interfaceTransitionPhase"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode acknowledgement CDC: %v", err)
	}
	if payload.InterfaceTransitionID != transition.ID ||
		payload.InterfaceTransitionPhase != string(domain.SessionInterfaceTransitionRecovery) {
		t.Fatalf("acknowledgement CDC payload = %+v", payload)
	}

	baseSeq, _ = st.LatestSeq(ctx)
	again, ok, err := st.AcknowledgeSessionInterfaceTransitionNotice(
		ctx, createdSession.ID, transition.ID, acknowledgedAt.Add(time.Minute),
	)
	if err != nil || !ok {
		t.Fatalf("acknowledge transition notice again: ok=%v err=%v", ok, err)
	}
	if !again.NoticeAcknowledgedAt.Equal(acknowledgedAt) {
		t.Fatalf("idempotent acknowledgement changed timestamp to %v", again.NoticeAcknowledgedAt)
	}
	events, err = st.EventsAfter(ctx, baseSeq, 100)
	if err != nil {
		t.Fatalf("read idempotent acknowledgement CDC: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("idempotent acknowledgement emitted duplicate events: %+v", events)
	}
}
