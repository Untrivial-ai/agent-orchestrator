package domain

import "testing"

func TestConversationContextResetProviderItemID(t *testing.T) {
	const session = SessionID("orchestrator-2")
	if got := ConversationContextResetProviderItemID(session); got != "ao-context-reset:orchestrator-2" {
		t.Fatalf("ConversationContextResetProviderItemID() = %q", got)
	}
}
