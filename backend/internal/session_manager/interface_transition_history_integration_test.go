package sessionmanager

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	claudeagent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// Exercise production launch selection, lifecycle, Chat and SQLite together.
// Only external runtime and provider I/O are controlled.
func TestInterfaceTransitionNativeHistoryOwnership(t *testing.T) {
	for _, harness := range []domain.AgentHarness{domain.HarnessClaudeCode, domain.HarnessQwen} {
		t.Run(string(harness), func(t *testing.T) {
			for _, tc := range []struct {
				name                   string
				replaceOrchestrator    bool
				removeTranscript       bool
				changeTerminalIdentity bool
			}{
				{name: "transcript_preserved"},
				{name: "transcript_unavailable_on_terminal_restart", removeTranscript: true},
				{name: "project_orchestrator_replacement", replaceOrchestrator: true},
				{name: "existing_terminal_identity_changed", changeTerminalIdentity: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := context.Background()
					dir := t.TempDir()
					workspace := filepath.Join(dir, "workspace")
					configDir := filepath.Join(dir, "claude")
					binDir := filepath.Join(dir, "bin")
					for _, path := range []string{workspace, filepath.Join(configDir, "projects", "scratch"), binDir} {
						if err := os.MkdirAll(path, 0700); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("test executable"), 0700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
					t.Setenv("CLAUDE_CONFIG_DIR", configDir)
					st := sqlitetest.MustOpenAt(t, dir)
					project := domain.ProjectRecord{ID: "history", Path: workspace, RegisteredAt: time.Now(), Config: testRoleAgents()}
					project.Config.Orchestrator.Harness = harness
					project.Config.Env = map[string]string{"CLAUDE_CONFIG_DIR": configDir}
					if err := st.UpsertProject(ctx, project); err != nil {
						t.Fatal(err)
					}
					original := "bb786e64-3b86-44cb-8d34-eafae933d8e4"
					freshID := claudeagent.SessionUUID
					var agent ports.Agent = claudeagent.New()
					if harness != domain.HarnessClaudeCode {
						original = "opaque-native-original"
						freshID = func(id string) string { return "opaque-native-" + id }
						agent = nativeOwnershipAgent{configDir: configDir}
					}
					originalTranscript := filepath.Join(configDir, "projects", "scratch", original+".jsonl")
					if err := os.WriteFile(originalTranscript, []byte("{\"type\":\"user\",\"message\":\"terminal question\"}\n"), 0600); err != nil {
						t.Fatal(err)
					}
					sess, err := st.CreateSession(ctx, domain.SessionRecord{
						ProjectID: domain.ProjectID(project.ID), Kind: domain.KindOrchestrator, Harness: harness,
						Mode:      domain.SessionModeChat,
						Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
						Metadata:  domain.SessionMetadata{WorkspacePath: workspace, Branch: "main", ProviderConversationID: original},
						CreatedAt: time.Now(), UpdatedAt: time.Now(),
					})
					if err != nil {
						t.Fatal(err)
					}
					conv, err := st.CreateConversation(ctx, "history-conversation", domain.ConversationScopeProject, domain.ProjectID(project.ID), sess.ID, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					initial, err := st.ConversationBranch(ctx, conv.ID, conv.ActiveBranchID)
					if err != nil {
						t.Fatal(err)
					}
					if initial.ProviderConversationID != original || conv.LatestSequence != 0 {
						t.Fatalf("initial ownership inconsistent: %+v %+v", conv, initial)
					}

					resumeCalls := 0
					expectedNativeID := original
					svc := chatsvc.New(chatsvc.Options{
						Store: st, Sessions: st, Log: slog.New(slog.DiscardHandler), NewID: uuid.NewString,
						Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
							r, err := st.LoadConversationSnapshot(ctx, id)
							return chatsvc.ConversationRows{Conversation: r.Conversation, Turns: r.Turns, Messages: r.Messages, Activities: r.Activities}, err
						}),
						Drivers: integrationChatRegistry{harness: nativeOwnershipDriver{integrationChatDriver{harness: harness,
							resume: func() ports.ChatConversation {
								resumeCalls++
								return &nativeOwnershipHistory{integrationChatConversation: newIntegrationChatConversation(expectedNativeID)}
							},
							start: func() ports.ChatConversation {
								t.Error("unexpected fresh Chat")
								return newIntegrationChatConversation(original)
							},
						}}},
					})
					t.Cleanup(func() { svc.StopAll(context.Background()) })
					log := &[]string{}
					runtime := &transitionRuntime{fakeRuntime: &fakeRuntime{}, log: log}
					lcm := lifecycle.New(st, nil)
					m := New(Deps{
						Store: st, Lifecycle: lcm, Chat: integrationChatLauncher{svc},
						Agents: singleAgent{agent: agent}, Runtime: runtime,
						Workspace: &fakeWorkspace{path: workspace}, Messenger: &fakeMessenger{}, DataDir: dir,
						LookPath:   func(string) (string, error) { return "/bin/true", nil },
						Executable: func() (string, error) { return filepath.Join(dir, "bin", "ao"), nil }, Logger: slog.New(slog.DiscardHandler),
					})
					useFastInterfaceTransitionTimings(m)
					if _, err := m.resumeChatController(ctx, "initial Chat", sess, project,
						ports.WorkspaceInfo{Path: workspace, Branch: "main"}, false, ""); err != nil {
						t.Fatal(err)
					}
					getSession := func() domain.SessionRecord {
						rec, found, err := st.GetSession(ctx, sess.ID)
						if err != nil || !found {
							t.Fatalf("get session: found=%v err=%v", found, err)
						}
						return rec
					}
					wait := func(id string) domain.SessionInterfaceTransition {
						for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
							tr, found, err := st.GetSessionInterfaceTransition(ctx, id)
							if err != nil || !found {
								t.Fatalf("get transition: found=%v err=%v", found, err)
							}
							if tr.Phase.Terminal() {
								return tr
							}
							time.Sleep(10 * time.Millisecond)
						}
						t.Fatal("transition timeout")
						return domain.SessionInterfaceTransition{}
					}

					if tc.replaceOrchestrator {
						if err := svc.StopChat(ctx, sess.ID); err != nil {
							t.Fatal(err)
						}
						if err := lcm.MarkTerminated(ctx, sess.ID); err != nil {
							t.Fatal(err)
						}
						sess, _, _, err = m.Spawn(ctx, ports.SpawnConfig{ProjectID: domain.ProjectID(project.ID), Kind: domain.KindOrchestrator, RequestedMode: domain.SessionModeTUI})
						if err != nil {
							t.Fatal(err)
						}
						if sess.ID == initial.SessionID {
							t.Fatal("replacement reused the previous session")
						}
					} else {
						toTerminal, err := m.StartInterfaceTransition(ctx, sess.ID, domain.SessionModeTUI, domain.SessionInterfaceTransitionInterrupt)
						if err != nil {
							t.Fatal(err)
						}
						first := wait(toTerminal.ID)
						if first.Phase != domain.SessionInterfaceTransitionCompleted {
							t.Fatalf("first switch: %s %s", first.Phase, first.ErrorDetail)
						}
						if first.NativeConversationID != original {
							t.Fatalf("expected native continuity, got id=%q", first.NativeConversationID)
						}
						terminal := getSession()
						if err := lcm.ApplyActivitySignal(ctx, sess.ID, ports.ActivitySignal{
							Valid: true, State: domain.ActivityExited, LaunchID: terminal.Metadata.RuntimeLaunchID,
						}); err != nil {
							t.Fatal(err)
						}
						runtime.aliveByHandle[terminal.Metadata.RuntimeHandleID] = false
						runtime.runtimeOccupied = false
						if tc.removeTranscript {
							if err := os.Rename(originalTranscript, originalTranscript+".unavailable"); err != nil {
								t.Fatal(err)
							}
						}
						restored, err := m.ResumeAgentWithMode(ctx, sess.ID)
						if tc.removeTranscript {
							if !errors.Is(err, ErrNotResumable) {
								t.Fatalf("missing established history must refuse fresh fallback: result=%+v err=%v", restored, err)
							}
							if getSession().Metadata.RuntimeLaunchID != terminal.Metadata.RuntimeLaunchID {
								t.Fatal("refused restore launched a new runtime")
							}
							return
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					// Exercise native identity selection through the generated launch command.
					argv := runtime.lastCfg.Argv
					terminalID := ""
					for i, arg := range argv {
						if (arg == "--session-id" || arg == "--resume") && i+1 < len(argv) {
							terminalID = argv[i+1]
						}
					}
					if terminalID == "" {
						t.Fatalf("fresh launch has no native ID: %v", argv)
					}
					if tc.changeTerminalIdentity {
						// Simulate a native context change after launch.
						terminalID = freshID(string(sess.ID))
					}
					if tc.replaceOrchestrator || tc.changeTerminalIdentity {
						expectedNativeID = freshID(string(sess.ID))
					}
					if terminalID != expectedNativeID {
						t.Fatalf("native identity = %q, want %q", terminalID, expectedNativeID)
					}
					terminal := getSession()
					transcript := filepath.Join(configDir, "projects", "scratch", terminalID+".jsonl")
					if err := os.WriteFile(transcript, []byte("{\"type\":\"user\",\"message\":\"terminal question\"}\n"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := lcm.ApplyActivitySignal(ctx, sess.ID, ports.ActivitySignal{
						Valid: true, State: domain.ActivityIdle, AgentSessionID: terminalID, LaunchID: terminal.Metadata.RuntimeLaunchID,
						LatestUserPrompt: "terminal question", LatestAssistantUpdate: "terminal answer", TranscriptPath: transcript,
					}); err != nil {
						t.Fatal(err)
					}
					terminal = getSession()
					if terminal.Metadata.AgentSessionID != terminalID || terminal.Metadata.AgentSessionIDLaunchID != terminal.Metadata.RuntimeLaunchID {
						t.Fatalf("hook not accepted: %+v", terminal.ControllerOwner())
					}
					toChat, err := m.StartInterfaceTransition(ctx, sess.ID, domain.SessionModeChat, domain.SessionInterfaceTransitionInterrupt)
					if err != nil {
						t.Fatal(err)
					}
					last := wait(toChat.ID)
					if expectedNativeID == original {
						if last.Phase != domain.SessionInterfaceTransitionCompleted || resumeCalls != 2 {
							t.Fatalf("unchanged identity failed: phase=%s detail=%s resumeCalls=%d", last.Phase, last.ErrorDetail, resumeCalls)
						}
						return
					}
					if last.Phase != domain.SessionInterfaceTransitionCompleted {
						t.Fatalf("ordinary Terminal resume left Chat unusable: %s", last.ErrorDetail)
					}
					assertHistory := func() {
						t.Helper()
						rows, err := st.LoadConversationSnapshot(ctx, conv.ID)
						if err != nil {
							t.Fatal(err)
						}
						if len(rows.Turns) != 2 || len(rows.Messages) != 4 {
							t.Fatalf("independent native histories were conflated: turns=%d messages=%d", len(rows.Turns), len(rows.Messages))
						}
						if rows.Turns[0].BranchID != initial.ID || rows.Turns[1].BranchID != interfaceTransitionProviderBoundaryID(last.ID) {
							t.Fatalf("wrong history ownership: %+v", rows.Turns)
						}
						if len(rows.Activities) != 1 || rows.Activities[0].ProviderItemID != interfaceTransitionProviderBoundaryID(last.ID)+":context-boundary" {
							t.Fatalf("missing or duplicate context boundary: %+v", rows.Activities)
						}
						old, err := st.ConversationBranch(ctx, conv.ID, initial.ID)
						if err != nil || old.ProviderConversationID != original || old.SessionID != initial.SessionID {
							t.Fatalf("old native ownership was rewritten: %+v err=%v", old, err)
						}
					}
					assertHistory()
					if err := svc.StopChat(ctx, sess.ID); err != nil {
						t.Fatal(err)
					}
					if _, err := m.resumeChatController(ctx, "retry", getSession(), project,
						ports.WorkspaceInfo{Path: workspace, Branch: "main"}, false, ""); err != nil {
						t.Fatal(err)
					}
					assertHistory()
				})
			}
		})
	}
}

// A non-Claude handoff-capable adapter with opaque IDs. It deliberately uses
// no Claude identity, UUID derivation, or production adapter implementation.
type nativeOwnershipAgent struct {
	transitionAgent
	configDir string
}

func (a nativeOwnershipAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	return []string{"launch", "--session-id", "opaque-native-" + cfg.SessionID}, nil
}

func (a nativeOwnershipAgent) GetRestoreCommand(_ context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	id := cfg.Session.Metadata[ports.MetadataKeyAgentSessionID]
	return []string{"launch", "--resume", id}, id != "", nil
}

func (a nativeOwnershipAgent) NativeConversationExists(_ context.Context, _ ports.SessionRef, id string, _ map[string]string) (bool, error) {
	_, err := os.Stat(filepath.Join(a.configDir, "projects", "scratch", id+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

type nativeOwnershipHistory struct {
	*integrationChatConversation
	scope string
}

type nativeOwnershipDriver struct{ integrationChatDriver }

func (d nativeOwnershipDriver) Resume(ctx context.Context, cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
	conversation, err := d.integrationChatDriver.Resume(ctx, cfg)
	if history, ok := conversation.(*nativeOwnershipHistory); ok {
		history.scope = cfg.ProviderScopeID
	}
	return conversation, err
}

func (c *nativeOwnershipHistory) ReadHistory(context.Context) ([]ports.ChatEvent, error) {
	events := []ports.ChatEvent{
		{Kind: ports.ChatEventTurnStarted, ProviderConversationID: c.providerID, ProviderTurnID: "terminal-turn", ProviderEventID: "turn-start"},
		{Kind: ports.ChatEventUserMessageCompleted, ProviderConversationID: c.providerID, ProviderTurnID: "terminal-turn", ProviderItemID: "user", ProviderEventID: "user-event", Text: "terminal question"},
		{Kind: ports.ChatEventMessageCompleted, ProviderConversationID: c.providerID, ProviderTurnID: "terminal-turn", ProviderItemID: "assistant", ProviderEventID: "assistant-event", Text: "terminal answer"},
		{Kind: ports.ChatEventTurnCompleted, ProviderConversationID: c.providerID, ProviderTurnID: "terminal-turn", ProviderEventID: "turn-end"},
	}
	// Like the real ACP driver, namespace opaque IDs using the reserved scope.
	// Native A and B deliberately reuse both opaque IDs and identical text.
	for i := range events {
		events[i].ProviderTurnID = c.scope + ":" + events[i].ProviderTurnID
		events[i].ProviderEventID = c.scope + ":" + events[i].ProviderEventID
		if events[i].ProviderItemID != "" {
			events[i].ProviderItemAliases = []string{events[i].ProviderItemID}
			events[i].ProviderItemID = c.scope + ":" + events[i].ProviderItemID
		}
	}
	return events, nil
}
