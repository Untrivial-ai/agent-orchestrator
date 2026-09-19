package chat_test

import (
	"context"
	"testing"
	"time"

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

// The catalog arrives by push and by push alone: ACP sends it on session/new and on
// commands_changed, and nothing re-sends it when a controller reattaches to a
// provider that outlived the daemon. Persisting it is what keeps a restart from
// leaving every session unable to say what it can run.
func TestSkillsSurviveARestartThroughTheStoredCatalog(t *testing.T) {
	conv := &skillfulConversation{
		fakeConversation: newFakeConversation(),
		skills: []ports.ChatSkill{
			{Name: "ship", DisplayName: "ship", Description: "Open a PR", InputHint: "<title>", Source: "repo"},
		},
	}
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	conv.emit(ports.ChatEvent{Kind: ports.ChatEventSkills, Skills: conv.skills})
	waitForStoredSkills(t, h, 1)

	// The provider now answers empty, which is what a reattached ACP conversation
	// does for the whole life of the controller.
	conv.skills = nil

	skills, err := h.svc.Skills(ctx, testSession)
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "ship" {
		t.Fatalf("got %+v, want the stored catalog", skills)
	}
	if skills[0].InputHint != "<title>" || skills[0].Source != "repo" {
		t.Errorf("stored catalog lost detail: %+v", skills[0])
	}
}

// An empty push is the provider answering "none", and it has to overwrite what was
// stored -- otherwise uninstalling every skill leaves the old menu in place.
func TestAnEmptyPushClearsTheStoredCatalog(t *testing.T) {
	conv := &skillfulConversation{
		fakeConversation: newFakeConversation(),
		skills:           []ports.ChatSkill{{Name: "ship", DisplayName: "ship"}},
	}
	h := newHarnessWithConversation(t, conv)

	conv.emit(ports.ChatEvent{Kind: ports.ChatEventSkills, Skills: conv.skills})
	waitForStoredSkills(t, h, 1)
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventSkills, Skills: nil})
	waitForStoredSkills(t, h, 0)

	conv.skills = nil
	skills, err := h.svc.Skills(context.Background(), testSession)
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("got %+v, want none", skills)
	}
}

// waitForStoredSkills waits for the projection goroutine to record a push.
func waitForStoredSkills(t *testing.T, h *harness, want int) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, err := h.st.ConversationForSession(ctx, testSession)
		if err != nil {
			t.Fatalf("ConversationForSession: %v", err)
		}
		if len(record.Skills) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored skills = %d, want %d", len(record.Skills), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
