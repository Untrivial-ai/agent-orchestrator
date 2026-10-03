package chat_test

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
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type connectedSideForkDriver struct {
	fakeDriver
	mu      sync.Mutex
	hosts   []domain.SessionID
	anchors []string
	forks   []*fakeConversation
	configs []ports.ChatStartConfig
	sources []string
}

func (d *connectedSideForkDriver) ForkIntoHost(_ context.Context, source, anchor string, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	conv := newFakeConversation()
	conv.providerConversationID = "fork-" + string(cfg.SessionID)
	d.hosts = append(d.hosts, cfg.SessionID)
	d.anchors = append(d.anchors, anchor)
	d.forks = append(d.forks, conv)
	d.configs = append(d.configs, cfg)
	d.sources = append(d.sources, source)
	return conv, nil
}

func TestCodexSideReopenUsesConnectedNativeFork(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	main := newFakeConversation()
	driver := &connectedSideForkDriver{}
	driver.start = func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
		if cfg.SessionID != testSession {
			return nil, fmt.Errorf("chat host already has a controller: %s", cfg.SessionID)
		}
		return main, nil
	}
	driver.resume = func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
		return nil, fmt.Errorf("connected fork must not be resumed")
	}
	var ids atomic.Int64
	var stopsMu sync.Mutex
	var stops []domain.SessionID
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st, Reader: fullSnapshotReader(st), Drivers: fakeRegistry{driver: driver},
		AppRunID: "reopen-test", Log: slog.New(slog.DiscardHandler),
		NewID: func() string { return fmt.Sprintf("reopen-%d", ids.Add(1)) },
		StopProviderHost: func(_ context.Context, host domain.SessionID) error {
			stopsMu.Lock()
			defer stopsMu.Unlock()
			stops = append(stops, host)
			return nil
		},
	})
	t.Cleanup(func() { svc.StopAll(ctx) })
	ctrl, err := svc.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(), Model: "original", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.InitializeSideChats(ctx); err != nil {
		t.Fatal(err)
	}
	h := &harness{st: st, svc: svc, ctrl: ctrl, conv: main}
	completeMain := func(client string) string {
		t.Helper()
		turn, err := svc.Send(ctx, testSession, ports.ChatUserMessage{Text: client, ClientMessageID: client, Origin: domain.MessageOriginHuman})
		if err != nil {
			t.Fatal(err)
		}
		main.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, TurnState: domain.TurnStateCompleted})
		h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
			for _, row := range s.Turns {
				if row.ID == turn.ID {
					return row.State == domain.TurnStateCompleted
				}
			}
			return false
		})
		return turn.ProviderTurnID
	}
	awaitSide := func(id string, pred func(domain.SideSnapshot) bool) domain.SideSnapshot {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			snapshot, err := svc.SideSnapshot(ctx, testSession, id, time.Time{}, 100)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Side.State == "failed" {
				t.Fatalf("side failed: %s", snapshot.Side.ErrorMessage)
			}
			if pred(snapshot) {
				return snapshot
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("side did not reach expected state")
		return domain.SideSnapshot{}
	}
	openSide := func(key string) domain.SideConversation {
		t.Helper()
		side, err := svc.CreateIndependentSideChat(ctx, testSession, chatsvc.SideCreateRequest{IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		return awaitSide(side.ID, func(s domain.SideSnapshot) bool { return s.Side.State == "ready" }).Side
	}
	sendSide := func(side domain.SideConversation, client string) {
		t.Helper()
		turn, err := svc.SendSideQuestion(ctx, testSession, side.ID, ports.ChatUserMessage{Text: client, ClientMessageID: client})
		if err != nil {
			t.Fatal(err)
		}
		driver.mu.Lock()
		conv := driver.forks[len(driver.forks)-1]
		driver.mu.Unlock()
		awaitSide(side.ID, func(s domain.SideSnapshot) bool { return conv.sendCallCount() == 1 })
		if got := conv.sentTexts(); len(got) != 1 || got[0] != client {
			t.Fatalf("side received %v", got)
		}
		conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1", TurnState: domain.TurnStateCompleted})
		awaitSide(side.ID, func(s domain.SideSnapshot) bool {
			for _, row := range s.Turns {
				if row.ID == turn.ID {
					return row.State == "completed"
				}
			}
			return false
		})
	}
	firstAnchor := completeMain("first-main")
	first := openSide("first-side")
	sendSide(first, "first-question")
	if _, err := svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{Model: "current-model", ReasoningEffort: "high"}); err != nil {
		t.Fatal(err)
	}
	newAnchor := completeMain("newer-main")
	if err := svc.CloseIndependentSideChat(ctx, testSession, first.ID); err != nil {
		t.Fatal(err)
	}
	second := openSide("second-side")
	sendSide(second, "second-question")
	if first.ProviderHostID == second.ProviderHostID || second.ProviderHostID == string(testSession) {
		t.Fatal("side reused a provider host")
	}
	if got, err := svc.Controller(testSession); err != nil || got != ctrl {
		t.Fatalf("main controller replaced: %v", err)
	}
	completeMain("still-connected")
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if len(driver.hosts) != 2 || driver.anchors[0] != firstAnchor || driver.anchors[1] != newAnchor {
		t.Fatalf("fork anchors = %v", driver.anchors)
	}
	if second.Model != "current-model" || second.Effort != "high" || driver.configs[1].Model != second.Model || driver.configs[1].Effort != second.Effort {
		t.Fatalf("side/provider settings = %+v / %+v", second, driver.configs[1])
	}
	for _, source := range driver.sources {
		if source != main.ProviderConversationID() {
			t.Fatalf("fork source = %q", source)
		}
	}
	stopsMu.Lock()
	defer stopsMu.Unlock()
	if len(stops) != 1 || stops[0] != domain.SessionID(first.ProviderHostID) {
		t.Fatalf("stopped hosts = %v", stops)
	}
}

func TestSideRecoveryFailureRetryAndCloseFence(t *testing.T) {
	for _, closeDuringRecovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("close-%v", closeDuringRecovery), func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			main := newFakeConversation()
			var resumeCalls atomic.Int64
			var ids atomic.Int64
			release := make(chan struct{})
			driver := &fakeDriver{}
			driver.start = func(ports.ChatStartConfig) (ports.ChatConversation, error) { return main, nil }
			driver.resume = func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
				if cfg.SessionID == testSession {
					return main, nil
				}
				n := resumeCalls.Add(1)
				if n == 1 {
					return nil, fmt.Errorf("provider offline")
				}
				if n == 2 {
					<-release
				}
				return newFakeConversation(), nil
			}
			svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Reader: fullSnapshotReader(st), Drivers: fakeRegistry{driver: driver}, AppRunID: "run", NewID: func() string { return fmt.Sprintf("recovery-%d", ids.Add(1)) }, Log: slog.New(slog.DiscardHandler)})
			t.Cleanup(func() { svc.StopAll(ctx) })
			_, err := svc.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.InitializeSideChats(ctx); err != nil {
				t.Fatal(err)
			}
			side := domain.SideConversation{ID: "recovered", SessionID: testSession, MainConversationID: "", State: "ready"}
			mainRecord, err := st.ConversationForSession(ctx, testSession)
			if err != nil {
				t.Fatal(err)
			}
			side.MainConversationID = mainRecord.ID
			records := []chatsvc.SideRecoveryRecord{{Side: side, ProviderHostID: "btw-recovered", ProviderForkID: "side-fork", Generation: "g", Harness: domain.HarnessCodex}}
			if err := svc.RecoverSideChatLaunch(ctx, "run", records); err != nil {
				t.Fatal(err)
			}
			await := func(state string) {
				t.Helper()
				for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
					snap, e := svc.SideSnapshot(ctx, testSession, side.ID, time.Time{}, 10)
					if e == nil && snap.Side.State == state {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("side never reached %s", state)
			}
			await("failed")
			if _, err := svc.SendSideQuestion(ctx, testSession, side.ID, ports.ChatUserMessage{Text: "q", ClientMessageID: "c"}); !errors.Is(err, chatsvc.ErrSideNotReady) {
				t.Fatalf("failed send: %v", err)
			}
			if err := svc.RetrySideConnection(ctx, testSession, side.ID); err != nil {
				t.Fatal(err)
			}
			await("recovering")
			for deadline := time.Now().Add(time.Second); resumeCalls.Load() < 2 && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			if err := svc.RetrySideConnection(ctx, testSession, side.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.SendSideQuestion(ctx, testSession, side.ID, ports.ChatUserMessage{Text: "q", ClientMessageID: "c"}); !errors.Is(err, chatsvc.ErrSideNotReady) {
				t.Fatalf("recovering send: %v", err)
			}
			if closeDuringRecovery {
				if err := svc.CloseIndependentSideChat(ctx, testSession, side.ID); err != nil {
					t.Fatal(err)
				}
				close(release)
				time.Sleep(30 * time.Millisecond)
				if _, err := svc.SideSnapshot(ctx, testSession, side.ID, time.Time{}, 10); err == nil {
					t.Fatal("late recovery resurrected closed side")
				}
			} else {
				close(release)
				await("ready")
				if _, err := svc.SendSideQuestion(ctx, testSession, side.ID, ports.ChatUserMessage{Text: "q", ClientMessageID: "c"}); err != nil {
					t.Fatalf("recovered send: %v", err)
				}
			}
			if resumeCalls.Load() != 2 {
				t.Fatalf("duplicate resume calls: %d", resumeCalls.Load())
			}

		})
	}
}
