package chat

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAfterProjectForwardsEveryLiveSummarySignal(t *testing.T) {
	var got []string
	c := &Controller{
		sessionID: domain.SessionID("worker-1"),
		now:       time.Now,
		onAssistantMessage: func(_ context.Context, id domain.SessionID, evidence string) {
			if id != "worker-1" {
				t.Errorf("session id = %q", id)
			}
			got = append(got, evidence)
		},
	}
	for _, event := range []ports.ChatEvent{
		{Kind: ports.ChatEventMessageDelta, Delta: "I am locating route handlers"},
		{Kind: ports.ChatEventReasoningDelta, Delta: "Checking callback ownership"},
		{Kind: ports.ChatEventCommandInput, Delta: "rg callback src"},
		{Kind: ports.ChatEventCommandOutputDelta, Delta: "src/app/auth/callback/route.ts"},
		{Kind: ports.ChatEventActivityText, Delta: "Reading the callback implementation"},
		{Kind: ports.ChatEventActivityStarted, Summary: "Read src/app/auth/callback/route.ts"},
		{Kind: ports.ChatEventMessageCompleted, Text: "The callback validates the session before redirecting"},
	} {
		c.afterProject(context.Background(), event, false)
	}
	want := []string{
		"I am locating route handlers",
		"Checking callback ownership",
		"rg callback src",
		"src/app/auth/callback/route.ts",
		"Reading the callback implementation",
		"The agent is currently Read src/app/auth/callback/route.ts",
		"The callback validates the session before redirecting",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forwarded evidence = %#v, want %#v", got, want)
	}
}
