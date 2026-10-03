package chat_test

import (
	"context"
	"errors"
	"testing"

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

// A provider that supports skills but fails this particular call (e.g. Codex
// app-server's `skills/list` returning a JSON-RPC error) is a different failure
// than "this agent has no concept of skills": it must report ErrSkillsUnavailable,
// not ErrSkillsUnsupported, and the raw upstream error must still be reachable
// through it so nothing gets lost behind a generic wrapper.
func TestSkillsReportsUnavailableWhenTheProviderCallFails(t *testing.T) {
	upstream := errors.New(`skills/list: rpc error: {"code":-32000,"message":"boom"}`)
	conv := &skillfulConversation{
		fakeConversation: newFakeConversation(),
		err:              upstream,
	}
	h := newHarnessWithConversation(t, conv)

	_, err := h.svc.Skills(context.Background(), testSession)
	if !errorsIs(err, chatsvc.ErrSkillsUnavailable) {
		t.Fatalf("err = %v, want ErrSkillsUnavailable", err)
	}
	if !errorsIs(err, upstream) {
		t.Fatalf("err = %v, want it to still wrap the raw upstream error", err)
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
