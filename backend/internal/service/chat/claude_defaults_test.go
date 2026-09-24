package chat_test

import (
	"context"
	"errors"
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
	listCalls     int
	listErr       error
	live          bool
}

func (c *claudeDefaultsConversation) ListConfigOptions(context.Context) ([]ports.ChatConfigOption, error) {
	c.listCalls++
	if c.listErr != nil {
		return nil, c.listErr
	}
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

func (c *claudeDefaultsConversation) ReconnectedLive() bool { return c.live }

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

func TestClaudeImplicitDefaultsRemainProviderOwnedAcrossStartSwitchAndResume(t *testing.T) {
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
	newService := func(conv *claudeDefaultsConversation, resumed *ports.ChatResumeConfig) *chatsvc.Service {
		return chatsvc.New(chatsvc.Options{
			Store: st, Reader: fullSnapshotReader(st), Sessions: st,
			Drivers: fakeRegistry{driver: fakeDriver{conv: conv, resumeCfg: resumed}},
			Log:     slog.New(slog.DiscardHandler), NewID: uuid.NewString,
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
	svc := newService(first, nil)
	if _, err := svc.Start(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if len(first.calls) != 0 {
		t.Fatalf("startup provider writes = %v, want none", first.calls)
	}
	stored, err := st.ConversationForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Settings.Model != "" || stored.Settings.ReasoningEffort != "" {
		t.Fatalf("startup durable settings = %+v", stored.Settings)
	}
	if _, err := svc.SetConfigOption(ctx, id, "model", ports.ChatConfigOptionValue{Select: "opus-4.7"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.calls, []string{"model:opus-4.7"}) {
		t.Fatalf("model-switch provider calls = %v", first.calls)
	}
	stored, err = st.ConversationForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Settings.Model != "opus-4.7" || stored.Settings.ReasoningEffort != "" {
		t.Fatalf("model-switch durable settings = %+v", stored.Settings)
	}
	if err := svc.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	second := newConversation()
	second.model = "opus-4.7"
	var resumed ports.ChatResumeConfig
	svc = newService(second, &resumed)
	cfg.ProviderConversationID = "claude-thread"
	if _, err := svc.Start(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Stop(ctx, id) })
	if len(second.calls) != 0 || resumed.Model != "opus-4.7" || resumed.Effort != "" {
		t.Fatalf("resume writes = %v, config model=%q effort=%q", second.calls, resumed.Model, resumed.Effort)
	}
	if _, err := svc.SetConfigOption(ctx, id, "effort", ports.ChatConfigOptionValue{Select: "high"}); err != nil {
		t.Fatal(err)
	}
	stored, err = st.ConversationForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Settings.Model != "opus-4.7" || stored.Settings.ReasoningEffort != "high" {
		t.Fatalf("explicit effort durable settings = %+v", stored.Settings)
	}
	if !reflect.DeepEqual(second.calls, []string{"effort:high"}) {
		t.Fatalf("explicit effort provider calls = %v", second.calls)
	}
	if _, err := svc.SetConfigOption(ctx, id, "model", ports.ChatConfigOptionValue{Select: "default"}); err != nil {
		t.Fatal(err)
	}
	stored, err = st.ConversationForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Settings.Model != "" || stored.Settings.ReasoningEffort != "" {
		t.Fatalf("restored provider defaults = %+v", stored.Settings)
	}
	if !reflect.DeepEqual(second.calls, []string{"effort:high", "model:default"}) {
		t.Fatalf("restored default provider calls = %v", second.calls)
	}
}

func TestClaudeStartIgnoresConfigCatalogFailureOnResumeAndLiveReconnect(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "live reconnect"}[live], func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			created, err := st.CreateSession(ctx, domain.SessionRecord{
				ProjectID: testProject, Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
				Mode: domain.SessionModeChat, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			})
			if err != nil {
				t.Fatal(err)
			}
			newConversation := func(live bool) *claudeDefaultsConversation {
				conv := &claudeDefaultsConversation{
					fakeConversation: newFakeConversation(), model: "default", effort: "default",
					listErr: errors.New("catalog unavailable"), live: live,
				}
				conv.providerConversationID = "claude-thread"
				return conv
			}
			newService := func(conv *claudeDefaultsConversation) *chatsvc.Service {
				return chatsvc.New(chatsvc.Options{
					Store: st, Reader: fullSnapshotReader(st), Sessions: st,
					Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
					Log:     slog.New(slog.DiscardHandler), NewID: uuid.NewString,
				})
			}
			first := newConversation(false)
			svc := newService(first)
			if _, err := svc.Start(ctx, chatsvc.StartConfig{
				SessionID: created.ID, ProjectID: testProject, Harness: domain.HarnessClaudeCode,
				WorkspacePath: t.TempDir(),
			}); err != nil {
				t.Fatal(err)
			}
			if err := svc.Stop(ctx, created.ID); err != nil {
				t.Fatal(err)
			}
			conv := newConversation(live)
			svc = newService(conv)
			if _, err := svc.Start(ctx, chatsvc.StartConfig{
				SessionID: created.ID, ProjectID: testProject, Harness: domain.HarnessClaudeCode,
				WorkspacePath: t.TempDir(), ProviderConversationID: "claude-thread",
			}); err != nil {
				t.Fatalf("resume: %v", err)
			}
			t.Cleanup(func() { _ = svc.Stop(ctx, created.ID) })
			if conv.listCalls != 0 || len(conv.calls) != 0 {
				t.Fatalf("provider config reads=%d writes=%v, want none", conv.listCalls, conv.calls)
			}
		})
	}
}
