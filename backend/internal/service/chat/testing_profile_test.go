package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func TestTestingMCPSecretsStayOutOfChatPersistenceSnapshotsAndLogs(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "start"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			st := openStore(t)
			provider := newFakeConversation()
			secret := "launch-only-testing-capability-that-must-not-be-persisted"
			var started ports.ChatStartConfig
			var resumed ports.ChatResumeConfig
			var logs bytes.Buffer
			svc := chatsvc.New(chatsvc.Options{
				Store: st, Sessions: st, Reader: fullSnapshotReader(st),
				Drivers: fakeRegistry{driver: fakeDriver{conv: provider, startCfg: &started, resumeCfg: &resumed}},
				Log:     slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				NewID:   func() string { return "testing-secret-proof" },
			})
			defer func() { _ = svc.Stop(context.Background(), testSession) }()
			workspace := t.TempDir()
			cfg := chatsvc.StartConfig{
				SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessClaudeCode,
				WorkspacePath: workspace, Kind: domain.KindWorker,
				MCPServers: []ports.ChatMCPServerConfig{{
					Name: "ao-testing", Type: "stdio", Command: "/supervisor/ao", Args: []string{"testing", "mcp"},
					Env: map[string]string{"AO_TEST_CAPABILITY": secret, "AO_TEST_ATTEMPT_ID": "attempt", "AO_SESSION_ID": string(testSession), "AO_RUN_FILE": "/supervisor/running.json"},
				}},
			}
			if resume {
				cfg.ProviderConversationID = provider.ProviderConversationID()
			}
			ctrl, err := svc.Start(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			servers := started.MCPServers
			if resume {
				servers = resumed.MCPServers
			}
			if len(servers) != 1 || servers[0].Env["AO_TEST_CAPABILITY"] != secret {
				t.Fatal("chat service failed to pass the MCP secret to the provider")
			}
			rows, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := svc.Snapshot(context.Background(), testSession)
			if err != nil {
				t.Fatal(err)
			}
			rec, exists, err := st.GetSession(context.Background(), testSession)
			if err != nil || !exists {
				t.Fatal("stored session missing", err)
			}
			for _, value := range []any{rows, snapshot, rec} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte("AO_TEST_CAPABILITY")) {
					t.Fatal("chat state persisted or exposed MCP child environment")
				}
			}
			if strings.Contains(logs.String(), secret) {
				t.Fatal("chat launch logs leaked the testing capability")
			}
			// The seed project's path is the isolated database directory. Inspect
			// SQLite and its WAL as well as any files produced in the workspace.
			project, exists, err := st.GetProject(context.Background(), string(testProject))
			if err != nil || !exists {
				t.Fatal("stored project missing", err)
			}
			for _, root := range []string{project.Path, workspace} {
				if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
					if walkErr != nil || entry.IsDir() {
						return walkErr
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if bytes.Contains(data, []byte(secret)) {
						t.Errorf("testing capability written to %s", path)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
