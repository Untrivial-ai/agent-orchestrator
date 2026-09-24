package chat_test

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type backgroundTestConversation struct {
	*terminatingConversation
}

func (c *backgroundTestConversation) StartDeferredTurn(providerTurnID string) error {
	c.emit(
		ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: providerTurnID, Text: "Fix renderer"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: providerTurnID, TurnState: domain.TurnStateCompleted},
	)
	return nil
}
func (*backgroundTestConversation) DiscardDeferredTurn(string) {}

func TestRunBackgroundTaskUsesNativeDriverAndTerminatesIt(t *testing.T) {
	conversation := &backgroundTestConversation{terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()}}
	var started ports.ChatStartConfig
	driver := fakeDriver{conv: conversation, startCfg: &started}
	ids := []string{"task-id", "scope-id"}
	service := chatsvc.New(chatsvc.Options{
		Drivers: fakeRegistry{driver: driver},
		NewID: func() string {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})

	title, err := service.RunBackgroundTask(context.Background(), domain.HarnessCodex, ports.ChatStartConfig{
		DataDir: "/data", WorkspacePath: "/workspace",
		Env: map[string]string{"CODEX_HOME": "/account"}, Model: "small", Effort: "low",
		Permissions: ports.PermissionModeAuto, SystemPrompt: "title only",
	}, "Fix the renderer")
	if err != nil || title != "Fix renderer" {
		t.Fatalf("RunBackgroundTask = %q, %v", title, err)
	}
	if started.SessionID != "background-task-id" || started.ProviderScopeID != "scope-id" || !started.ProviderIDsScoped || !started.Ephemeral {
		t.Fatalf("start identity = %#v", started)
	}
	if started.Model != "small" || started.Effort != "low" || started.Env["CODEX_HOME"] != "/account" {
		t.Fatalf("start config = %#v", started)
	}
	messages := conversation.sentMessages()
	if len(messages) != 1 || messages[0].Text != "Fix the renderer" || messages[0].Origin != domain.MessageOriginAutomation {
		t.Fatalf("messages = %#v", messages)
	}
	if !conversation.terminated.Load() {
		t.Fatal("background provider was not terminated")
	}
}

type approvingBackgroundConversation struct {
	*backgroundTestConversation
	mode     string
	decision ports.ChatDecision
}

func (c *approvingBackgroundConversation) ListConfigOptions(context.Context) ([]ports.ChatConfigOption, error) {
	return nil, nil
}
func (c *approvingBackgroundConversation) SetConfigOption(_ context.Context, id string, value ports.ChatConfigOptionValue) ([]ports.ChatConfigOption, error) {
	c.mode = id + ":" + value.Select
	return nil, nil
}
func (c *approvingBackgroundConversation) StartDeferredTurn(id string) error {
	c.emit(ports.ChatEvent{Kind: ports.ChatEventApprovalRequested, ProviderTurnID: id, RequestID: "request-1", Decisions: []ports.ChatDecisionOption{{ID: "allow"}}})
	return nil
}
func (c *approvingBackgroundConversation) ResolveRequest(_ context.Context, _ string, decision ports.ChatDecision) error {
	c.decision = decision
	return c.backgroundTestConversation.StartDeferredTurn("")
}

func TestResearchBackgroundAppliesModeAndResolvesApproval(t *testing.T) {
	conversation := &approvingBackgroundConversation{backgroundTestConversation: &backgroundTestConversation{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
	}}
	var started ports.ChatStartConfig
	service := chatsvc.New(chatsvc.Options{Drivers: fakeRegistry{driver: fakeDriver{conv: conversation, startCfg: &started}}, NewID: func() string { return "scope-1" }})
	result, err := service.RunBackgroundTask(context.Background(), domain.HarnessClaudeCode, ports.ChatStartConfig{
		SessionID: "research-run-1", Mode: "plan", Permissions: ports.PermissionModeDefault,
		OnApproval: func(_ context.Context, event ports.ChatEvent) (ports.ChatDecision, error) {
			if event.RequestID != "request-1" {
				t.Fatalf("approval = %+v", event)
			}
			return ports.ChatDecision{ID: "allow", Raw: []byte(`"allow"`)}, nil
		},
	}, "Find the entry point")
	if err != nil || result != "Fix renderer" || started.SessionID != "research-run-1" || conversation.mode != "mode:plan" || conversation.decision.ID != "allow" || !conversation.terminated.Load() {
		t.Fatalf("research result=%q err=%v start=%+v mode=%q decision=%+v", result, err, started, conversation.mode, conversation.decision)
	}
}
