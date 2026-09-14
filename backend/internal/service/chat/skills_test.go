package chat_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// skillfulConversation is a provider double that can enumerate skills.
type skillfulConversation struct {
	*fakeConversation
	skills []ports.ChatSkill
	err    error
	calls  int
}

func (c *skillfulConversation) ListSkills(context.Context) ([]ports.ChatSkill, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.skills, nil
}

func TestSkillsComeFromTheLiveConversation(t *testing.T) {
	conv := &skillfulConversation{
		fakeConversation: newFakeConversation(),
		skills: []ports.ChatSkill{
			{Name: "review", DisplayName: "review", Description: "Look at the diff", Source: "repo"},
		},
	}
	h := newHarnessWithConversation(t, conv)

	skills, err := h.svc.Skills(context.Background(), testSession)
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "review" {
		t.Fatalf("got %+v, want the provider's own list", skills)
	}
	if conv.calls != 1 {
		t.Errorf("provider asked %d times, want 1: the list must not be served from a table in AO", conv.calls)
	}
}

// A driver that cannot enumerate skills has to be distinguishable from one that
// reported none, because only the first is permanent.
func TestSkillsReportsUnsupportedForADriverThatCannotList(t *testing.T) {
	h := newHarness(t)

	_, err := h.svc.Skills(context.Background(), testSession)
	if !errorsIs(err, chatsvc.ErrSkillsUnsupported) {
		t.Fatalf("err = %v, want ErrSkillsUnsupported", err)
	}
}

// Without a controller there is no provider to ask. Reporting that plainly is what
// lets a client explain the state instead of showing an empty menu.
func TestSkillsRequiresALiveController(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.Stop(context.Background(), testSession); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	_, err := h.svc.Skills(context.Background(), testSession)
	if !errorsIs(err, chatsvc.ErrNoController) {
		t.Fatalf("err = %v, want ErrNoController", err)
	}
}

func TestSkillsRejectsRetiredProjectControllerBeforeProviderCall(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	conv := &skillfulConversation{
		fakeConversation: newFakeConversation(),
		skills:           []ports.ChatSkill{{Name: "must-not-be-read"}},
	}
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			nextID++
			return fmt.Sprintf("retired-skills-%d", nextID)
		},
	})
	t.Cleanup(func() {
		_ = svc.Stop(context.Background(), testSession)
		_ = conv.Close()
	})

	if _, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
	}); err != nil {
		t.Fatalf("Start project controller: %v", err)
	}
	replacement, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create replacement orchestrator: %v", err)
	}
	if _, err := st.CreateConversation(
		ctx, "unused-skills-replacement", domain.ConversationScopeProject,
		testProject, replacement.ID, time.Now().UTC(),
	); err != nil {
		t.Fatalf("rebind project conversation: %v", err)
	}

	_, err = svc.Skills(ctx, testSession)
	if !errorsIs(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("Skills through retired project controller = %v, want ErrControllerHandoff", err)
	}
	if conv.calls != 0 {
		t.Fatalf("retired provider skill calls = %d, want 0", conv.calls)
	}
}
