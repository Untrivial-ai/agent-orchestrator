package domain

import "testing"

func TestExplicitPermissionPoliciesAreChatOnly(t *testing.T) {
	for _, mode := range []PermissionMode{PermissionModeManual, PermissionModeDontAsk} {
		if !mode.ValidTurn() {
			t.Fatalf("chat policy %q rejected", mode)
		}
		if mode.Valid() || (AgentConfig{Permissions: mode}).Validate() == nil {
			t.Fatalf("chat-only policy %q accepted by legacy launch adapters", mode)
		}
	}
	if PermissionMode("unknown").ValidTurn() {
		t.Fatal("unknown policy accepted")
	}
}
