package chat_test

import (
	"context"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type claudeDefaultsConversation struct {
	*fakeConversation
	model, effort string
	calls         []string
}

func (c *claudeDefaultsConversation) ListConfigOptions(context.Context) ([]ports.ChatConfigOption, error) {
	return []ports.ChatConfigOption{
		{
			ID: "model", Category: "model", Type: ports.ChatConfigOptionSelect,
			Current: ports.ChatConfigOptionValue{Select: c.model},
			Choices: []ports.ChatConfigOptionChoice{
				{Value: "default", Name: "Default (recommended)", Description: "Opus (1M context)"},
				{Value: "opus[1m]", Name: "Opus (1M context)", Description: "Opus 5.5 with 1M context"},
				{Value: "opus-4.7", Name: "Opus 4.7", Description: "Opus 4.7"},
			},
		},
		{
			ID: "effort", Category: "thought_level", Type: ports.ChatConfigOptionSelect,
			Current: ports.ChatConfigOptionValue{Select: c.effort},
			Choices: []ports.ChatConfigOptionChoice{
				{Value: "default", Name: "Default"}, {Value: "low", Name: "Low"},
				{Value: "medium", Name: "Medium"}, {Value: "high", Name: "High"},
				{Value: "xhigh", Name: "Xhigh"},
			},
		},
	}, nil
}

func (c *claudeDefaultsConversation) SetConfigOption(ctx context.Context, id string, value ports.ChatConfigOptionValue) ([]ports.ChatConfigOption, error) {
	c.calls = append(c.calls, id+":"+value.Select)
	switch id {
	case "model":
		c.model, c.effort = value.Select, "default"
	case "effort":
		c.effort = value.Select
	}
	return c.ListConfigOptions(ctx)
}

func TestClaudeDefaultsAreAppliedAndPersistedAcrossStartSwitchAndResume(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	created, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		Mode: domain.SessionModeChat, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID
	newService := func(conv *claudeDefaultsConversation) *chatsvc.Service {
		return chatsvc.New(chatsvc.Options{
			Store: st, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
			Log: slog.New(slog.DiscardHandler), NewID: uuid.NewString,
		})
	}
	newConversation := func() *claudeDefaultsConversation {
		conv := &claudeDefaultsConversation{fakeConversation: newFakeConversation(), model: "default", effort: "default"}
		conv.providerConversationID = "claude-thread"
		return conv
	}
	cfg := chatsvc.StartConfig{
		SessionID: id, ProjectID: testProject, Harness: domain.HarnessClaudeCode,
		WorkspacePath: t.TempDir(),
	}
	first := newConversation()
	svc := newService(first)
	if _, err := svc.Start(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.calls, []string{"model:opus[1m]", "effort:medium"}) {
		t.Fatalf("startup provider calls = %v", first.calls)
	}
	stored, err := st.ConversationForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Settings.Model != "opus[1m]" || stored.Settings.ReasoningEffort != "medium" {
		t.Fatalf("startup durable settings = %+v", stored.Settings)
	}
	if _, err := svc.SetConfigOption(ctx, id, "model", ports.ChatConfigOptionValue{Select: "opus-4.7"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.calls, []string{"model:opus[1m]", "effort:medium", "model:opus-4.7", "effort:xhigh"}) {
		t.Fatalf("model-switch provider calls = %v", first.calls)
	}
	stored, err = st.ConversationForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Settings.Model != "opus-4.7" || stored.Settings.ReasoningEffort != "xhigh" {
		t.Fatalf("model-switch durable settings = %+v", stored.Settings)
	}
	if err := svc.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	second := newConversation()
	svc = newService(second)
	cfg.ProviderConversationID = "claude-thread"
	if _, err := svc.Start(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Stop(ctx, id) })
	if !reflect.DeepEqual(second.calls, []string{"model:opus-4.7", "effort:xhigh"}) {
		t.Fatalf("resumed provider calls = %v", second.calls)
	}
}
