package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/reaper"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type watchdogAuditRegistry struct{ driver ports.ChatDriver }

func (r watchdogAuditRegistry) Driver(harness domain.AgentHarness) (ports.ChatDriver, error) {
	if r.driver != nil && r.driver.Harness() == harness {
		return r.driver, nil
	}
	return nil, ports.ErrChatUnsupported
}

func (r watchdogAuditRegistry) SupportsChat(harness domain.AgentHarness) bool {
	return r.driver != nil && r.driver.Harness() == harness
}

type watchdogAuditDriver struct {
	mu            sync.Mutex
	conversations []*watchdogAuditConversation
}

func (*watchdogAuditDriver) Harness() domain.AgentHarness { return domain.HarnessClaudeCode }

func (*watchdogAuditDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return (&watchdogAuditConversation{}).Capabilities(), nil
}

func (d *watchdogAuditDriver) next() (ports.ChatConversation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conversations) == 0 {
		return nil, errors.New("watchdog audit driver has no replacement conversation")
	}
	conversation := d.conversations[0]
	d.conversations = d.conversations[1:]
	return conversation, nil
}

func (d *watchdogAuditDriver) Start(context.Context, ports.ChatStartConfig) (ports.ChatConversation, error) {
	return d.next()
}

func (d *watchdogAuditDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	return d.next()
}

type watchdogAuditConversation struct {
	events    chan ports.ChatEvent
	closeOnce sync.Once
	turns     atomic.Int64
}

func newWatchdogAuditConversation() *watchdogAuditConversation {
	return &watchdogAuditConversation{events: make(chan ports.ChatEvent, 8)}
}

func (*watchdogAuditConversation) ProviderConversationID() string { return "watchdog-audit-thread" }

func (*watchdogAuditConversation) Capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming: true,
		ports.ChatCapabilityApprovals: true,
		ports.ChatCapabilityInterrupt: true,
		ports.ChatCapabilityResume:    true,
	}
}

func (c *watchdogAuditConversation) SendTurn(context.Context, ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	return ports.ChatTurnRef{ProviderTurnID: fmt.Sprintf("watchdog-audit-turn-%d", c.turns.Add(1))}, nil
}

func (*watchdogAuditConversation) Interrupt(context.Context, string) error { return nil }

func (*watchdogAuditConversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return nil
}

func (c *watchdogAuditConversation) Events() <-chan ports.ChatEvent { return c.events }

func (c *watchdogAuditConversation) Close() error {
	c.closeOnce.Do(func() { close(c.events) })
	return nil
}

func (c *watchdogAuditConversation) Terminate() error { return c.Close() }

type watchdogAuditRuntime struct{}

func (watchdogAuditRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}

func TestPeriodicReaperSettlesUnresolvedProviderReconnect(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clockNow := base
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clockNow
	}
	advance := func(duration time.Duration) {
		clockMu.Lock()
		clockNow = clockNow.Add(duration)
		clockMu.Unlock()
	}

	st := sqlitetest.MustOpenAt(t, t.TempDir())
	projectID := domain.ProjectID("watchdog-audit-project")
	if err := st.UpsertProject(ctx, domain.ProjectRecord{
		ID: string(projectID), Path: t.TempDir(), RegisteredAt: base,
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	createdSession, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: projectID, Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: base},
		CreatedAt: base, UpdatedAt: base,
	})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	sessionID := createdSession.ID

	provider := newWatchdogAuditConversation()
	replacement := newWatchdogAuditConversation()
	replacement.turns.Store(100)
	driver := &watchdogAuditDriver{conversations: []*watchdogAuditConversation{provider, replacement}}
	lcm := lifecycle.New(st, nil)
	var nextID atomic.Int64
	chat := chatsvc.New(chatsvc.Options{
		Store: st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			rows, err := st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation: rows.Conversation, ActiveBranch: rows.ActiveBranch,
				Turns: rows.Turns, Messages: rows.Messages, Activities: rows.Activities,
			}, nil
		}),
		Sessions: st,
		Drivers:  watchdogAuditRegistry{driver: driver},
		Activity: lcm,
		Log:      slog.New(slog.DiscardHandler),
		NewID:    func() string { return fmt.Sprintf("watchdog-audit-id-%d", nextID.Add(1)) },
		Now:      clock,
	})
	t.Cleanup(func() { chat.StopAll(context.Background()) })
	controller, err := chat.Start(ctx, chatsvc.StartConfig{
		SessionID: sessionID, ProjectID: projectID, Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start Chat controller: %v", err)
	}
	turn, err := controller.Send(ctx, ports.ChatUserMessage{Text: "finish the task"})
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}

	provider.events <- ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderEventID: "watchdog-audit-started",
		ProviderTurnID: turn.ProviderTurnID, ProviderConversationID: provider.ProviderConversationID(),
	}
	provider.events <- ports.ChatEvent{
		Kind: ports.ChatEventMessageDelta, ProviderEventID: "watchdog-audit-message",
		ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "watchdog-audit-assistant",
		Delta: "Work completed, but the terminal frame will never arrive.",
	}
	provider.events <- ports.ChatEvent{
		Kind: ports.ChatEventActivityStarted, ProviderEventID: "watchdog-audit-reconnect",
		ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "watchdog-audit-reconnect",
		ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
		Summary: "Reconnecting to Claude, attempt 7 of 10",
		Detail:  []byte(`{"event":"provider.failure","category":"connection"}`),
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, snapshotErr := st.LoadConversationSnapshot(ctx, controller.ConversationID())
		session, found, sessionErr := st.GetSession(ctx, sessionID)
		if snapshotErr == nil && sessionErr == nil && found && len(snapshot.Turns) == 1 &&
			len(snapshot.Messages) == 2 && len(snapshot.Activities) == 1 &&
			snapshot.Turns[0].State == domain.TurnStateRunning &&
			snapshot.Messages[1].Streaming &&
			snapshot.Activities[0].Status == domain.ActivityStatusRunning &&
			session.Activity.State == domain.ActivityActive &&
			controller.State() == ports.ChatControllerBusy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("wedged facts were not projected: snapshot=%+v session=%+v snapshotErr=%v sessionErr=%v",
				snapshot, session, snapshotErr, sessionErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	advance(chatsvc.StaleProviderFailureTimeout + time.Minute)
	reaperCtx, cancelReaper := context.WithCancel(ctx)
	rp := reaper.New(lcm, st, watchdogAuditRuntime{}, reaper.Config{
		Tick: 10 * time.Millisecond, Clock: clock, Logger: slog.New(slog.DiscardHandler),
	})
	rp.SetChatTurnRecovery(chat)
	reaperDone := rp.Start(reaperCtx)
	t.Cleanup(func() {
		cancelReaper()
		<-reaperDone
	})

	deadline = time.Now().Add(5 * time.Second)
	for {
		snapshot, snapshotErr := st.LoadConversationSnapshot(ctx, controller.ConversationID())
		session, found, sessionErr := st.GetSession(ctx, sessionID)
		current, controllerErr := chat.Controller(sessionID)
		if snapshotErr == nil && sessionErr == nil && found && controllerErr == nil && len(snapshot.Turns) == 1 &&
			len(snapshot.Messages) == 2 && len(snapshot.Activities) == 1 &&
			snapshot.Turns[0].State == domain.TurnStateFailed &&
			snapshot.Turns[0].CompletedAt != nil &&
			snapshot.Turns[0].ErrorMessage == "Connection to Claude was interrupted and never confirmed completion after reconnecting. Please retry." &&
			!snapshot.Messages[1].Streaming &&
			snapshot.Activities[0].Status == domain.ActivityStatusFailed &&
			session.Activity.State == domain.ActivityIdle &&
			current.State() == ports.ChatControllerReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("periodic watchdog did not settle and replace wedge: snapshot=%+v session=%+v snapshotErr=%v sessionErr=%v controllerErr=%v",
				snapshot, session, snapshotErr, sessionErr, controllerErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	next, err := chat.Send(ctx, sessionID, ports.ChatUserMessage{Text: "start the next task"})
	if err != nil {
		t.Fatalf("send after periodic host replacement: %v", err)
	}
	if next.State != domain.TurnStateRunning || replacement.turns.Load() != 101 {
		t.Fatalf("next turn after periodic replacement = %+v, replacement sends=%d", next, replacement.turns.Load())
	}
}
