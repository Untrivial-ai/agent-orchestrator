package acp

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Claude Code writes a background task's stop notice into its transcript as a
// user record. Replaying history on resume turned each one into a "recovered"
// user turn showing the raw XML. It must not appear as user input; a reply
// Claude gave to it stays with the turn it followed, as it did live.
func TestACPHistoryReplaySkipsClaudeTaskNotifications(t *testing.T) {
	userMessage := func(id, text string) acpsdk.SessionUpdate {
		update := acpsdk.UpdateUserMessageText(text)
		update.UserMessageChunk.MessageId = &id
		return update
	}
	notice := "<task-notification>\n<task-id>b1</task-id>\n<status>killed</status>\n</task-notification>"
	agent := &fakeAgent{
		capabilities: &acpsdk.AgentCapabilities{
			LoadSession:         true,
			SessionCapabilities: acpsdk.SessionCapabilities{Resume: &acpsdk.SessionResumeCapabilities{}},
		},
		loadUpdates: []acpsdk.SessionUpdate{
			userMessage("11111111-1111-4111-8111-111111111111", "Start the dev server"),
			acpsdk.UpdateAgentMessageText("Started it in the background."),
			userMessage("22222222-2222-4222-8222-222222222222", notice),
			acpsdk.UpdateAgentMessageText("The dev server stopped; restarting is not needed."),
			userMessage("33333333-3333-4333-8333-333333333333", notice),
		},
	}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.useTestProcess(fakeSpawn(agent))

	conv, err := driver.Resume(context.Background(), ports.ChatResumeConfig{
		ProviderConversationID: "provider-session-1",
		WorkspacePath:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conv.Close()
	history, err := conv.(ports.ChatHistoryReader).ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}

	turns := map[string]bool{}
	var users []string
	var answer strings.Builder
	for _, event := range history {
		switch event.Kind {
		case ports.ChatEventTurnStarted:
			turns[event.ProviderTurnID] = true
		case ports.ChatEventUserMessageCompleted:
			users = append(users, event.Text)
		case ports.ChatEventMessageDelta:
			answer.WriteString(event.Delta)
		}
	}
	if len(users) != 1 || users[0] != "Start the dev server" {
		t.Fatalf("replayed user messages = %q, want only the user's own prompt", users)
	}
	if len(turns) != 1 {
		t.Fatalf("replayed %d turns, want 1: notices must not start turns", len(turns))
	}
	if got := answer.String(); !strings.Contains(got, "Started it in the background.") ||
		!strings.Contains(got, "restarting is not needed") {
		t.Fatalf("replayed answer = %q, want both replies kept", got)
	}
}

func TestIsTaskNotification(t *testing.T) {
	for text, want := range map[string]bool{
		"<task-notification>\n<task-id>x</task-id>\n</task-notification>":     true,
		"  <task-notification><status>stopped</status></task-notification>\n": true,
		"Please look at <task-notification> handling":                         false,
		"<task-notification> unterminated":                                    false,
		"":                                                                    false,
	} {
		if got := isTaskNotification(text); got != want {
			t.Errorf("isTaskNotification(%q) = %v, want %v", text, got, want)
		}
	}
}
