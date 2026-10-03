package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/processalive"
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

func TestPersistentClaudeACPHibernateAndNativeResume(t *testing.T) {
	callsPath := filepath.Join(t.TempDir(), "calls.log")
	cfg, start := restartFixture(t, map[string]string{"AO_TEST_PERSISTENT_ACP_CALLS": callsPath})
	cfg.Harness = domain.HarnessClaudeCode
	cfg.Capabilities = ports.ChatCapabilities{ports.ChatCapabilityResume: true}
	t.Cleanup(func() { _ = persistenthost.Shutdown(context.Background(), start.DataDir, string(start.SessionID)) })

	first, err := New(cfg, nil).Start(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Capabilities()[ports.ChatCapabilityResume] {
		t.Fatal("Claude ACP did not advertise native resume")
	}
	_ = nextEvent(t, first.Events()) // ready
	providerID := first.ProviderConversationID()
	firstPID := completedProviderPID(t, first)
	if err := first.(ports.ChatProviderHibernator).Hibernate(); err != nil {
		t.Fatalf("Hibernate: %v", err)
	}
	if processalive.Alive(firstPID) {
		t.Fatalf("Claude ACP bridge process %d survived hibernation", firstPID)
	}
	descriptor := filepath.Join(start.DataDir, "chat-hosts", string(start.SessionID), "host.json")
	if _, err := os.Stat(descriptor); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Claude ACP host survived hibernation: %v", err)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil || strings.Contains(string(calls), "session/close\n") {
		t.Fatalf("hibernate closed native session: calls=%q err=%v", calls, err)
	}

	second, err := New(cfg, nil).Resume(context.Background(), ports.ChatResumeConfig{
		SessionID: start.SessionID, DataDir: start.DataDir, WorkspacePath: start.WorkspacePath,
		ProviderConversationID: providerID, ProviderScopeID: start.ProviderScopeID,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = second.(ports.ChatProviderTerminator).Terminate() }()
	if second.ProviderConversationID() != providerID || second.(ports.ChatLiveReconnector).ReconnectedLive() {
		t.Fatal("Claude ACP did not native-resume the same session in a replacement process")
	}
	_ = nextEvent(t, second.Events()) // ready
	if secondPID := completedProviderPID(t, second); secondPID == firstPID {
		t.Fatalf("Claude ACP bridge process did not change: %d", secondPID)
	}
	calls, err = os.ReadFile(callsPath)
	if err != nil || strings.Count(string(calls), "session/resume\n") != 1 {
		t.Fatalf("native resume calls=%q err=%v", calls, err)
	}
}

func completedProviderPID(t *testing.T, conv ports.ChatConversation) int {
	t.Helper()
	ref, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatal(err)
	}
	var pid int
	completed := false
	for {
		event := nextEvent(t, conv.Events())
		if event.Kind == ports.ChatEventMessageDelta {
			_, _ = fmt.Sscanf(event.Delta, "provider-pid=%d", &pid)
		}
		if event.Kind == ports.ChatEventTurnCompleted {
			if event.TurnState != domain.TurnStateCompleted {
				t.Fatalf("turn state = %q", event.TurnState)
			}
			completed = true
			if err := conv.(ports.ChatProviderEventAcknowledger).AcknowledgeProviderEvent(context.Background(), event.ProviderEventID); err != nil {
				t.Fatal(err)
			}
		}
		if completed && event.Kind == ports.ChatEventControllerState && event.ControllerState == ports.ChatControllerReady {
			if pid == 0 {
				t.Fatal("provider did not report its process id")
			}
			return pid
		}
	}
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
		completed, ready := false, false
		for !ready {
			event := nextEvent(t, conv.Events())
			switch event.Kind {
			case ports.ChatEventTurnCompleted:
				if event.TurnState != want || event.ProviderEventID == "" {
					t.Fatalf("completion = %#v, want %s with a durable receipt", event, want)
				}
				if err := conv.(ports.ChatProviderEventAcknowledger).AcknowledgeProviderEvent(context.Background(), event.ProviderEventID); err != nil {
					t.Fatal(err)
				}
				completed = true
			case ports.ChatEventControllerState:
				// TurnCompleted is emitted before the driver releases activeTurn.
				// ControllerReady is the barrier after which the next turn is valid.
				ready = completed && event.ControllerState == ports.ChatControllerReady
			}
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
