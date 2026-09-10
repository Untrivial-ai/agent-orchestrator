package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func restartFixture(t *testing.T, env map[string]string) (Config, ports.ChatStartConfig) {
	t.Helper()
	env["AO_TEST_PERSISTENT_ACP_PROVIDER"] = "1"
	cfg := Config{
		Harness: domain.HarnessOMP,
		Launch: func(context.Context, LaunchConfig) (Launch, error) {
			return Launch{Command: os.Args[0], Args: []string{"-test.run=TestPersistentACPProviderHelper"}, Env: env}, nil
		},
	}
	start := ports.ChatStartConfig{
		SessionID: "restart-test", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
		ProviderScopeID: "scope", Permissions: ports.PermissionModeDefault,
	}
	return cfg, start
}

func TestPersistentACPFailedPromptAcknowledgementAllowsNextTurn(t *testing.T) {
	cfg, start := restartFixture(t, map[string]string{"AO_TEST_PERSISTENT_ACP_ERROR": "1"})
	conv, err := New(cfg, nil).Start(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.(ports.ChatProviderTerminator).Terminate() }()
	_ = nextEvent(t, conv.Events())
	for _, want := range []domain.TurnState{domain.TurnStateFailed, domain.TurnStateCompleted} {
		ref, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "run"})
		if err != nil {
			t.Fatal(err)
		}
		if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
			t.Fatal(err)
		}
		for {
			event := nextEvent(t, conv.Events())
			if event.Kind != ports.ChatEventTurnCompleted {
				continue
			}
			if event.TurnState != want || event.ProviderEventID == "" {
				t.Fatalf("completion = %#v, want %s with a durable receipt", event, want)
			}
			if err := conv.(ports.ChatProviderEventAcknowledger).AcknowledgeProviderEvent(context.Background(), event.ProviderEventID); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
}

func TestPersistentACPSetupFailureReleasesNewHost(t *testing.T) {
	env := map[string]string{"AO_TEST_PERSISTENT_ACP_BAD_SETUP": "1"}
	cfg, start := restartFixture(t, env)
	if _, err := New(cfg, nil).Start(context.Background(), start); err == nil {
		t.Fatal("invalid session/new response accepted")
	}
	descriptor := filepath.Join(start.DataDir, "chat-hosts", string(start.SessionID), "host.json")
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := os.Stat(descriptor)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed setup left a host descriptor: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	delete(env, "AO_TEST_PERSISTENT_ACP_BAD_SETUP")
	conv, err := New(cfg, nil).Start(context.Background(), start)
	if err != nil {
		t.Fatalf("fresh retry: %v", err)
	}
	defer func() { _ = conv.(ports.ChatProviderTerminator).Terminate() }()
}

func TestPersistentACPAdoptionRetainsLaunchPermissionFence(t *testing.T) {
	cfg, start := restartFixture(t, map[string]string{})
	cfg.ValidateTurnSettings = func(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
		if ports.NormalizePermissionMode(initial) != ports.NormalizePermissionMode(settings.Approval) {
			return errors.New("approval is fixed at launch")
		}
		return nil
	}
	conv, err := New(cfg, nil).Start(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.Close(); err != nil {
		t.Fatal(err)
	}
	resume := ports.ChatResumeConfig{
		SessionID: start.SessionID, DataDir: start.DataDir, WorkspacePath: start.WorkspacePath,
		ProviderScopeID: start.ProviderScopeID, ProviderConversationID: conv.ProviderConversationID(),
		Permissions: ports.PermissionModeBypassPermissions,
	}
	if _, err := New(cfg, nil).Resume(context.Background(), resume); !errors.Is(err, ports.ErrChatRecoveryInconclusive) {
		t.Fatalf("changed launch policy = %v, want preserved but refused", err)
	}
	resume.Permissions = start.Permissions
	adopted, err := New(cfg, nil).Resume(context.Background(), resume)
	if err != nil {
		t.Fatalf("original policy could not adopt preserved host: %v", err)
	}
	defer func() { _ = adopted.(ports.ChatProviderTerminator).Terminate() }()
}

func TestPersistentACPRequiresDurableSessionIdentity(t *testing.T) {
	driver := New(Config{Harness: domain.HarnessClaudeCode, Launch: func(context.Context, LaunchConfig) (Launch, error) {
		t.Fatal("invalid session identity invoked launch side effects")
		return Launch{}, nil
	}}, nil)
	for _, cfg := range []ports.ChatStartConfig{
		{WorkspacePath: t.TempDir()},
		{WorkspacePath: t.TempDir(), DataDir: t.TempDir(), SessionID: "../escape"},
	} {
		if _, err := driver.Start(context.Background(), cfg); err == nil {
			t.Fatal("invalid session identity accepted")
		}
	}
}
