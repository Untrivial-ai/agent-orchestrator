package chat_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type issue4657Store struct {
	*sqlite.Store
	calls      atomic.Int64
	delay      time.Duration
	completed  chan struct{}
	started    time.Time
	deltaBytes int
}

func (s *issue4657Store) ProjectProviderEvent(ctx context.Context, conversationID string, session domain.SessionID, generation, providerEventID, method, payloadJSON string, now time.Time, project func(context.Context) error) (bool, error) {
	s.calls.Add(1)
	ok, err := s.Store.ProjectProviderEvent(ctx, conversationID, session, generation, providerEventID, method, payloadJSON, now, func(txCtx context.Context) error {
		// Synthetic transaction latency, explicitly NOT a model of NFS locking.
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		return project(txCtx)
	})
	if ok && err == nil {
		var event ports.ChatEvent
		if decodeErr := json.Unmarshal([]byte(payloadJSON), &event); decodeErr != nil {
			panic(decodeErr)
		}
		if event.Kind == ports.ChatEventMessageDelta {
			s.deltaBytes += len(event.Delta)
		}
		evidenceEmit(map[string]any{"stage": "commit", "transactions": s.calls.Load(), "deltaBytes": s.deltaBytes, "method": method, "elapsedMs": float64(time.Since(s.started).Microseconds()) / 1000})
	}
	if method == string(ports.ChatEventTurnCompleted) {
		s.completed <- struct{}{}
	}
	return ok, err
}

type issue4657Measurement struct {
	calls, rows, cdc, changes int64
	elapsed                   time.Duration
}

func issue4657Replay(t *testing.T, chunks int, delay time.Duration) issue4657Measurement {
	t.Helper()
	ctx := context.Background()
	st := openStore(t)
	observed := &issue4657Store{Store: st, delay: delay, completed: make(chan struct{}, 1)}
	conv := newFakeConversation()
	conv.events = make(chan ports.ChatEvent, 256)
	var ids atomic.Int64
	svc := chatsvc.New(chatsvc.Options{
		Store: observed, Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: conv}},
		Activity: lifecycle.New(st, nil),
		Log:      slog.New(slog.DiscardHandler),
		NewID:    func() string { return fmt.Sprintf("repro-%d", ids.Add(1)) },
	})
	ctrl, err := svc.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessClaudeCode, WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Stop(ctx, testSession) })
	project, found, err := st.GetProject(ctx, string(testProject))
	if err != nil || !found {
		t.Fatalf("project: found=%t err=%v", found, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(project.Path, "ao.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scalar := func(query string) int64 {
		t.Helper()
		var n int64
		if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	beforeCDC := scalar(`SELECT count(*) FROM change_log WHERE event_type = 'session_updated'`)
	beforeEvents := scalar(`SELECT count(*) FROM conversation_provider_events`)
	beforeChanges, err := st.Issue4657TotalChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	evidenceEmit(map[string]any{"stage": "ready", "chunks": chunks, "replyBytes": 1320, "delayMs": delay.Milliseconds()})
	barrier := os.Getenv("AO_EVIDENCE_START_FILE")
	if barrier == "" {
		t.Fatal("AO_EVIDENCE_START_FILE is required")
	}
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	observed.started = start
	evidenceEmit(map[string]any{"stage": "running"})
	turn, err := svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "Write two short paragraphs.", ClientMessageID: "repro-client", Origin: domain.MessageOriginHuman})
	if err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("hello world ", 110)
	events := []ports.ChatEvent{{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID}}
	for i := 0; i < chunks; i++ {
		events = append(events, ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "repro-message", Delta: full[len(full)*i/chunks : len(full)*(i+1)/chunks]})
	}
	events = append(events,
		ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "repro-message", Text: full},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, TurnState: domain.TurnStateCompleted},
	)
	conv.emit(events...)
	select {
	case <-observed.completed:
	case <-time.After(15 * time.Second):
		t.Fatal("turn persistence did not complete")
	}
	elapsed := time.Since(start)
	// Close drains the controller and its post-projection lifecycle work.
	if err := svc.Stop(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	ctrl.Wait()
	snapshot, err := st.LoadConversationSnapshot(ctx, ctrl.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[1].Text != full || snapshot.Messages[1].Streaming {
		t.Fatal("reply text incorrect or not settled")
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateCompleted {
		t.Fatal("turn not completed")
	}
	afterChanges, err := st.Issue4657TotalChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := issue4657Measurement{
		calls:   observed.calls.Load(),
		rows:    scalar(`SELECT count(*) FROM conversation_provider_events`) - beforeEvents,
		cdc:     scalar(`SELECT count(*) FROM change_log WHERE event_type = 'session_updated'`) - beforeCDC,
		changes: afterChanges - beforeChanges,
		elapsed: elapsed,
	}
	t.Logf("chunks=%d injected_delay=%s reply_chars=%d projection_calls=%d archive_rows=%d session_updated_rows=%d sqlite_row_changes=%d turn_elapsed=%s", chunks, delay, len(full), m.calls, m.rows, m.cdc, m.changes, m.elapsed)
	evidenceEmit(map[string]any{"stage": "result", "transactions": m.calls, "archiveRows": m.rows, "cdcRows": m.cdc, "rowChanges": m.changes, "elapsedMs": float64(m.elapsed.Microseconds()) / 1000, "replyBytes": len(snapshot.Messages[1].Text), "replySHA256": fmt.Sprintf("%x", sha256.Sum256([]byte(snapshot.Messages[1].Text))), "textVerified": true, "turnState": snapshot.Turns[0].State})
	return m
}

func evidenceEmit(value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	fmt.Printf("EVIDENCE %s\n", raw)
}

func TestIssue5176Evidence(t *testing.T) {
	delay, err := strconv.Atoi(os.Getenv("AO_EVIDENCE_DELAY_MS"))
	if err != nil {
		t.Fatal(err)
	}
	issue4657Replay(t, 110, time.Duration(delay)*time.Millisecond)
}
