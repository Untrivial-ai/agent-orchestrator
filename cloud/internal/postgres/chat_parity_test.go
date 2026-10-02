package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCloudChatAutomationProvenanceSurvivesRetriesAndLegacyReads(t *testing.T) {
	store, admin, f := openNotificationTestStore(t)
	ctx := context.Background()
	p := domain.Principal{UserID: f.userID, Provider: "local"}
	if _, err := admin.Exec(ctx, `UPDATE ao_sessions SET kind='orchestrator' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateOrchestratorChild(ctx, f.orgID, f.sessionID, "child", 100, domain.CreateSession{Harness: "codex", DisplayName: "Home search", Provider: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := store.ReportToOrchestrator(ctx, f.orgID, child.ID, "report", "Found two homes.")
	if err != nil {
		t.Fatal(err)
	}
	assertAutomation := func(event domain.ClientEvent) {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["origin"] != "automation" || payload["senderSessionId"] != child.ID {
			t.Fatalf("missing attribution: %s", event.Payload)
		}
	}
	assertAutomation(report)
	retry, err := store.ReportToOrchestrator(ctx, f.orgID, child.ID, "report", "Found two homes.")
	if err != nil || retry.Sequence != report.Sequence {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	assertAutomation(retry)
	human, err := store.SendMessage(ctx, p, f.orgID, f.sessionID, "human", "[from worker someone] I pasted a report", domain.ChatTurnSettings{})
	if err != nil {
		t.Fatal(err)
	}
	trace := &chatAuditQueryTrace{}
	config := store.pool.Config()
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	tracedStore := &Store{pool: pool}
	t.Cleanup(tracedStore.Close)
	if _, _, err := tracedStore.ListClientEvents(ctx, p, f.orgID, f.sessionID, 0, 100); err != nil {
		t.Fatal(err)
	}
	if got := trace.queries.Load(); got != 0 {
		t.Fatalf("fully attributed page queried audit %d times", got)
	}
	// Emulate a pre-parity message. Its server-owned audit attribution remains.
	if _, err := admin.Exec(ctx, `UPDATE ao_events SET payload=payload-'origin'-'senderSessionId' WHERE org_id=$1 AND session_id=$2 AND sequence=$3`, f.orgID, f.sessionID, report.Sequence); err != nil {
		t.Fatal(err)
	}
	events, _, err := tracedStore.ListClientEvents(ctx, p, f.orgID, f.sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := trace.queries.Load(); got != 1 {
		t.Fatalf("legacy page queried audit %d times, want once", got)
	}
	found := false
	for _, event := range events {
		if event.Sequence == report.Sequence {
			assertAutomation(event)
			found = true
		}
		if event.Sequence == human.Sequence {
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["origin"] != "human" {
				t.Fatal("human message lost durable origin")
			}
		}
	}
	if !found {
		t.Fatal("report missing from chat history")
	}
}

func TestCloudChatPersistsProviderMessageIDUnderTurnFence(t *testing.T) {
	store, _, f := openNotificationTestStore(t)
	ctx := context.Background()
	p := domain.Principal{UserID: f.userID, Provider: "local"}
	if _, err := store.SendMessage(ctx, p, f.orgID, f.sessionID, "prompt", "Find homes", domain.ChatTurnSettings{}); err != nil {
		t.Fatal(err)
	}
	turn, claimed, err := store.ClaimWorkerTurn(ctx, f.orgID, f.sessionID, f.workerID, f.epoch)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if err := store.AppendWorkerTurnOutput(ctx, f.orgID, f.sessionID, f.workerID, turn.ID, f.epoch, turn.Attempt, "stdout", "\n\n", "answer"); err != nil {
		t.Fatal(err)
	}
	events, _, err := store.ListClientEvents(ctx, p, f.orgID, f.sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != "chat.assistant_delta" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["itemId"] != "answer" || payload["text"] != "\n\n" || payload["turnId"] != turn.ID {
			t.Fatalf("lost message boundary: %s", event.Payload)
		}
		return
	}
	t.Fatal("output missing from history")
}

// Observe the actual store boundary so new pages cannot silently reintroduce
// legacy audit recovery work.
type chatAuditQueryTrace struct {
	queries atomic.Int64
}

func (trace *chatAuditQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM ao_audit_events") {
		trace.queries.Add(1)
	}
	return ctx
}

func (*chatAuditQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
