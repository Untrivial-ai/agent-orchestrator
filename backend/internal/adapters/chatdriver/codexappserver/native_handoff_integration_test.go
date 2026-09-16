package codexappserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type handoffDriverRegistry struct{ driver ports.ChatDriver }

func (r handoffDriverRegistry) SupportsChat(harness domain.AgentHarness) bool {
	return harness == domain.HarnessCodex
}

func (r handoffDriverRegistry) Driver(domain.AgentHarness) (ports.ChatDriver, error) {
	return r.driver, nil
}

func TestNativeForkHandoffRetainsEachExchangeOnce(t *testing.T) {
	for _, tc := range []struct {
		name, parent, firstAnswer string
		wantCopies                int
	}{
		{"copied ancestry", "thread-A", "alpha remembered", 1},
		{"retained approval record", "thread-A", "alpha remembered", 1},
		{"persisted history omits item IDs", "thread-A", "alpha remembered", 1},
		{"indirect ancestry", "thread-middle", "alpha remembered", 1},
		{"unrelated identical history", "unrelated", "alpha remembered", 2},
		{"changed ancestor content", "thread-A", "different answer", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := sqlitetest.MustOpenAt(t, t.TempDir())
			now := time.Now()
			if err := st.UpsertProject(ctx, domain.ProjectRecord{ID: "fork", Path: t.TempDir(), RegisteredAt: now}); err != nil {
				t.Fatal(err)
			}
			rec, err := st.CreateSession(ctx, domain.SessionRecord{ID: "fork-1", ProjectID: "fork", Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, Kind: domain.KindWorker, Metadata: domain.SessionMetadata{ProviderConversationID: "thread-A"}, CreatedAt: now, UpdatedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			driver, server := newTestDriver(t)
			reader := chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
				s, err := st.LoadConversationSnapshot(ctx, id)
				return chatsvc.ConversationRows{Conversation: s.Conversation, Turns: s.Turns, Messages: s.Messages, Activities: s.Activities}, err
			})
			svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Reader: reader, Drivers: handoffDriverRegistry{driver}, NewID: uuid.NewString})
			t.Cleanup(func() { svc.StopAll(ctx) })
			server.reply("thread/resume", `{"thread":{"id":"thread-A"}}`)
			server.reply("thread/read", forkHandoffHistory("thread-A", "", "alpha remembered", false))
			if _, err := svc.Start(ctx, chatsvc.StartConfig{SessionID: rec.ID, ProjectID: rec.ProjectID, Harness: rec.Harness, WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-A"}); err != nil {
				t.Fatal(err)
			}
			before, err := svc.Snapshot(ctx, rec.ID)
			if err != nil || len(before.Messages) != 2 {
				t.Fatalf("source snapshot: %+v, %v", before, err)
			}
			if tc.name == "retained approval record" {
				if err := st.UpsertActivity(ctx, before.Conversation.ID, before.Turns[0].ProviderTurnID,
					domain.ConversationActivity{ID: "prior-approval", Kind: domain.ActivityKindApproval, Status: domain.ActivityStatusCompleted, Summary: "Approved pwd"}, now); err != nil {
					t.Fatal(err)
				}
				before, err = svc.Snapshot(ctx, rec.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.StopChat(ctx, rec.ID); err != nil {
				t.Fatal(err)
			}
			// The Terminal's app-server forks A. Exercise the production protocol
			// operation before resuming its full copied transcript through Chat.
			terminalDriver, terminalServer := newTestDriver(t)
			terminalServer.reply("thread/resume", `{"thread":{"id":"thread-A"}}`)
			terminal, err := terminalDriver.Resume(ctx, ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-A"})
			if err != nil {
				t.Fatal(err)
			}
			terminalServer.reply("thread/fork", `{"thread":{"id":"thread-B","forkedFromId":"thread-A"}}`)
			fork, err := terminal.(ports.ChatForker).Fork(ctx, nil)
			if err != nil || fork != "thread-B" {
				t.Fatalf("fork=%q, err=%v", fork, err)
			}
			if err := terminal.Close(); err != nil {
				t.Fatal(err)
			}

			rec, _, err = st.GetSession(ctx, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			rec.Metadata.ProviderConversationID = fork
			rec.IsTerminated = true
			if err := st.UpdateSession(ctx, rec); err != nil {
				t.Fatal(err)
			}
			reservation := &domain.ChatProviderHandoff{BoundaryID: "fork-boundary", ConversationID: before.Conversation.ID, PreviousSessionID: rec.ID, PreviousBranchID: before.Conversation.ActiveBranchID, PreviousSequence: before.Conversation.LatestSequence, ExpectedControllerOwner: rec.ControllerOwner()}
			resumedDriver, resumedServer := newTestDriver(t)
			resumedServer.reply("thread/resume", `{"thread":{"id":"thread-B"}}`)
			replay := forkHandoffHistory("thread-B", tc.parent, tc.firstAnswer, true)
			if tc.name == "persisted history omits item IDs" {
				var decoded struct {
					Thread map[string]any `json:"thread"`
				}
				if err := json.Unmarshal([]byte(replay), &decoded); err != nil {
					t.Fatal(err)
				}
				for _, turn := range decoded.Thread["turns"].([]any) {
					for _, item := range turn.(map[string]any)["items"].([]any) {
						delete(item.(map[string]any), "id")
					}
				}
				encoded, err := json.Marshal(decoded)
				if err != nil {
					t.Fatal(err)
				}
				replay = string(encoded)
			}
			resumedServer.reply("thread/read", replay)
			if tc.parent == "thread-middle" {
				resumedServer.respondSequence("thread/read", forkHandoffHistory("thread-B", tc.parent, tc.firstAnswer, true), `{"thread":{"id":"thread-middle","forkedFromId":"thread-A"}}`)
			}
			svc = chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Reader: reader, Drivers: handoffDriverRegistry{resumedDriver}, NewID: uuid.NewString})
			lcm := lifecycle.New(st, nil)
			cfg := chatsvc.StartConfig{SessionID: rec.ID, ProjectID: rec.ProjectID, Harness: rec.Harness, WorkspacePath: "/tmp/ws", ProviderConversationID: fork, ProviderHandoff: reservation,
				ControllerReady: func(start chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
					metadata := rec.Metadata
					metadata.ControllerGeneration = start.ControllerGeneration
					var err error
					conversation := start.Conversation
					if start.ProviderBoundary != nil {
						err = lcm.MarkChatSpawnedPrepared(ctx, rec.ID, metadata, *start.ProviderBoundary, reservation, start.CommitProviderHistory)
						conversation.ActiveBranchID = start.ProviderBoundary.ID
					} else {
						err = lcm.MarkSpawned(ctx, rec.ID, metadata)
					}
					return chatsvc.ControllerCommit{Conversation: conversation}, err
				},
			}
			if _, err := svc.Start(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			assertSnapshot := func() {
				got, err := svc.Snapshot(ctx, rec.ID)
				if err != nil {
					t.Fatal(err)
				}
				texts := make([]string, 0, len(got.Messages))
				for _, message := range got.Messages {
					texts = append(texts, message.Text)
				}
				want := []string{"remember alpha", "alpha remembered"}
				if tc.wantCopies == 2 {
					want = append(want, "remember alpha", tc.firstAnswer)
				}
				want = append(want, "continue", "beta reply")
				if !reflect.DeepEqual(texts, want) {
					t.Fatalf("visible history = %q, want %q", texts, want)
				}
				var boundaries, commands, approvals int
				for _, activity := range got.Activities {
					switch activity.Kind {
					case domain.ActivityKindCommand:
						commands++
					case domain.ActivityKindApproval:
						approvals++
					default:
						boundaries++
					}
				}
				if boundaries != 1 || commands != tc.wantCopies {
					t.Fatalf("boundaries=%d commands=%d, want 1/%d", boundaries, commands, tc.wantCopies)
				}
				if tc.name == "retained approval record" && approvals != 1 {
					t.Fatalf("retained approvals=%d, want 1", approvals)
				}
			}
			assertSnapshot()
			if err := svc.StopChat(ctx, rec.ID); err != nil {
				t.Fatal(err)
			}
			// A new driver and service exercise a daemon restart, not cached replay.
			restartedDriver, restartedServer := newTestDriver(t)
			restartedServer.reply("thread/resume", `{"thread":{"id":"thread-B"}}`)
			restartedServer.reply("thread/read", replay)
			if tc.parent == "thread-middle" {
				// Restart resolves the chain again; no in-memory ancestry cache.
				restartedServer.respondSequence("thread/read", forkHandoffHistory("thread-B", tc.parent, tc.firstAnswer, true), `{"thread":{"id":"thread-middle","forkedFromId":"thread-A"}}`)
			}
			svc = chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Reader: reader, Drivers: handoffDriverRegistry{restartedDriver}, NewID: uuid.NewString})
			cfg.ProviderHandoff = nil
			if _, err := svc.Start(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			assertSnapshot()
		})
	}
}

func forkHandoffHistory(thread, parent, firstAnswer string, withNewTurn bool) string {
	turn := func(id, prompt, answer string) map[string]any {
		return map[string]any{"id": id, "status": "completed", "items": []any{
			map[string]any{"id": id + "-user", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": prompt}}},
			map[string]any{"id": id + "-answer", "type": "agentMessage", "text": answer},
		}}
	}
	turns := []any{turn("shared", "remember alpha", firstAnswer)}
	shared := turns[0].(map[string]any)
	shared["items"] = append(shared["items"].([]any), map[string]any{
		"id": "shared-command", "type": "commandExecution", "command": "pwd", "cwd": "/tmp/ws",
		"aggregatedOutput": "/tmp/ws\n", "exitCode": 0, "status": "completed",
	})
	if withNewTurn {
		turns = append(turns, turn("new", "continue", "beta reply"))
	}
	data, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": thread, "forkedFromId": parent, "turns": turns}})
	return string(data)
}
