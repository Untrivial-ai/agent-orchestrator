package sessionmanager

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// Use the production lifecycle and reopen SQLite: the fake lifecycle copies
// metadata wholesale and cannot catch fields dropped by the real reducer.
func TestClaudeSessionModelSurvivesRestore(t *testing.T) {
	for _, kind := range []domain.SessionKind{domain.KindWorker, domain.KindOrchestrator} {
		for _, selection := range []struct {
			name         string
			projectModel string
			roleModel    string
			spawnModel   string
			wantModel    string
			legacy       bool
		}{
			{name: "spawn_override", projectModel: "opus", roleModel: "sonnet", spawnModel: "haiku", wantModel: "haiku"},
			{name: "role_default", projectModel: "opus", roleModel: "sonnet", wantModel: "sonnet"},
			{name: "project_default", projectModel: "opus", wantModel: "opus"},
			{name: "custom_provider_model", projectModel: "opus", spawnModel: "provider/model-vNext", wantModel: "provider/model-vNext"},
			{name: "agent_default"},
			{name: "legacy_unknown", spawnModel: "haiku", wantModel: "haiku", legacy: true},
		} {
			for _, operation := range []string{"restore", "native_resume", "restore_all", "saved_prompt", "chat_restore", "chat_resume"} {
				t.Run(string(kind)+"/"+selection.name+"/"+operation, func(t *testing.T) {
					ctx := context.Background()
					dataDir := t.TempDir()
					store, err := sqlitetest.Open(dataDir)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if store != nil {
							if err := store.Close(); err != nil {
								t.Error(err)
							}
						}
					})
					project := domain.ProjectRecord{
						ID: "mer", Path: t.TempDir(), RegisteredAt: time.Now().UTC(),
						Config: domain.ProjectConfig{
							AgentConfig: domain.AgentConfig{Model: selection.projectModel},
							Worker: domain.RoleOverride{
								Harness: domain.HarnessClaudeCode, AgentConfig: domain.AgentConfig{Model: selection.roleModel},
							},
							Orchestrator: domain.RoleOverride{
								Harness: domain.HarnessClaudeCode, AgentConfig: domain.AgentConfig{Model: selection.roleModel},
							},
						},
					}
					if err := store.UpsertProject(ctx, project); err != nil {
						t.Fatal(err)
					}
					agent := &recordingAgent{}
					runtime := &fakeRuntime{}
					workspace := &fakeWorkspace{path: t.TempDir()}
					launcher := &recordingLauncher{}
					newManager := func() *Manager {
						messenger := &fakeMessenger{}
						return New(Deps{
							Runtime: runtime, Agents: singleAgent{agent: agent}, Workspace: workspace,
							Store: store, Messenger: messenger, Lifecycle: lifecycle.New(store, messenger),
							Chat: launcher, DataDir: dataDir,
							LookPath: func(string) (string, error) { return "/bin/true", nil },
						})
					}
					chat := strings.HasPrefix(operation, "chat_")
					mode := domain.SessionModeTUI
					if chat {
						mode = domain.SessionModeChat
					}
					manager := newManager()
					rec, _, _, err := manager.Spawn(ctx, ports.SpawnConfig{
						ProjectID: project.ID, Kind: kind, Prompt: "continue the task", RequestedMode: mode,
						AgentConfig: ports.AgentConfig{Model: selection.spawnModel},
					})
					if err != nil {
						t.Fatalf("spawn: %v", err)
					}
					if rec.Metadata.Model != selection.wantModel {
						t.Fatalf("spawn model = %q, want %q", rec.Metadata.Model, selection.wantModel)
					}

					// Emulate an exited provider or a terminated session, preserving the
					// native conversation id independently of its selected model.
					rec.IsTerminated = operation != "native_resume" && operation != "chat_resume"
					rec.Activity.State = domain.ActivityExited
					rec.Metadata.AgentSessionID = "native-model-test"
					if operation == "saved_prompt" {
						rec.Metadata.AgentSessionID = ""
					}
					wantModel := selection.wantModel
					if selection.legacy {
						// Older rows have no selection snapshot, even if a model was
						// explicitly chosen. Do not infer it from current defaults.
						rec.Metadata.Model = ""
						wantModel = ""
					}
					if err := store.UpdateSession(ctx, rec); err != nil {
						t.Fatal(err)
					}
					if operation == "restore_all" {
						if err := store.UpsertSessionWorktree(ctx, domain.SessionWorktreeRecord{
							SessionID: rec.ID, RepoName: domain.RootWorkspaceRepoName,
							WorktreePath: rec.Metadata.WorkspacePath, Branch: rec.Metadata.Branch, State: "removed",
						}); err != nil {
							t.Fatal(err)
						}
					}
					project.Config.AgentConfig.Model = "changed-project-model"
					project.Config.Worker.AgentConfig.Model = "changed-worker-model"
					project.Config.Orchestrator.AgentConfig.Model = "changed-orchestrator-model"
					if err := store.UpsertProject(ctx, project); err != nil {
						t.Fatal(err)
					}

					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					store = nil
					store, err = sqlite.Open(dataDir)
					if err != nil {
						t.Fatalf("reopen store: %v", err)
					}
					manager = newManager()
					*agent = recordingAgent{}
					launcher.started = nil
					runtime.created = 0
					switch operation {
					case "native_resume", "chat_resume":
						_, err = manager.ResumeAgentWithMode(ctx, rec.ID)
					case "restore_all":
						err = manager.RestoreAll(ctx)
					default:
						_, err = manager.RestoreWithMode(ctx, rec.ID)
					}
					if err != nil {
						t.Fatalf("%s: %v", operation, err)
					}
					if chat {
						if len(launcher.started) != 1 || runtime.created != 0 {
							t.Fatalf("controller starts: Chat=%d TUI=%d", len(launcher.started), runtime.created)
						}
						if got := launcher.started[0].Model; got != wantModel {
							t.Fatalf("Chat model = %q, want %q", got, wantModel)
						}
						if got := launcher.started[0].ProviderConversationID; got != rec.Metadata.ProviderConversationID {
							t.Fatalf("Chat conversation = %q, want %q", got, rec.Metadata.ProviderConversationID)
						}
					} else {
						if runtime.created != 1 {
							t.Fatalf("runtime Create calls = %d, want 1", runtime.created)
						}
						if agent.lastConfig.Model != wantModel {
							t.Fatalf("continued model = %q, want %q", agent.lastConfig.Model, wantModel)
						}
						if operation == "saved_prompt" {
							if agent.launchCalls != 1 {
								t.Fatalf("saved-prompt launches = %d, want 1", agent.launchCalls)
							}
						} else if agent.restoreCalls != 1 || agent.lastRestore.Session.Metadata[ports.MetadataKeyAgentSessionID] != "native-model-test" {
							t.Fatalf("native restore lost conversation identity: %+v", agent.lastRestore)
						}
					}
					stored, ok, err := store.GetSession(ctx, rec.ID)
					if err != nil || !ok {
						t.Fatalf("read restored session: found=%t err=%v", ok, err)
					}
					if stored.IsTerminated || stored.Metadata.Model != wantModel {
						t.Fatalf("restored session: terminated=%t model=%q, want live model=%q", stored.IsTerminated, stored.Metadata.Model, wantModel)
					}
				})
			}
		}
	}
}
