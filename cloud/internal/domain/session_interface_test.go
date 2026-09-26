package domain

import "testing"

func TestSessionInterfaceOppositeIsTwoWay(t *testing.T) {
	if got := SessionInterfaceTUI.Opposite(); got != SessionInterfaceChat {
		t.Fatalf("TUI opposite = %q, want chat", got)
	}
	if got := SessionInterfaceChat.Opposite(); got != SessionInterfaceTUI {
		t.Fatalf("Chat opposite = %q, want tui", got)
	}
}
