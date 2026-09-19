package browsercontract_test

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
)

func TestSupportedCoversTheDaemonAllowlist(t *testing.T) {
	must := []string{
		"open", "snapshot", "act", "click", "dblclick", "focus", "fill", "type", "press",
		"hover", "highlight", "unhighlight", "scrollintoview", "drag", "tabs", "tab-new",
		"tab-select", "tab-close", "scroll", "select", "check", "uncheck", "get", "wait",
		"screenshot", "network-start", "network-status", "network-list", "network-stop",
		"network-clear", "console", "errors", "frame", "dialog",
		"devtools-open", "devtools-close",
	}
	for _, action := range must {
		if !browsercontract.Supported(action) {
			t.Errorf("Supported(%q) = false, want true", action)
		}
	}
	for _, action := range []string{"", "navigate", "eval", "shell", "__destroy-session"} {
		if browsercontract.Supported(action) {
			t.Errorf("Supported(%q) = true, want false", action)
		}
	}
}

func TestSupportedNormalizesAction(t *testing.T) {
	if !browsercontract.Supported("  SNAPSHOT ") {
		t.Error("Supported must be case- and whitespace-insensitive")
	}
}

func TestActionsSortedAndComplete(t *testing.T) {
	actions := browsercontract.Actions()
	if len(actions) == 0 {
		t.Fatal("Actions() is empty")
	}
	for i := 1; i < len(actions); i++ {
		if actions[i-1] >= actions[i] {
			t.Fatalf("Actions() not sorted at %d: %v", i, actions)
		}
	}
	for _, action := range actions {
		if !browsercontract.Supported(action) {
			t.Errorf("Actions() lists %q but Supported disagrees", action)
		}
	}
	if len(actions) != 36 {
		t.Errorf("len(Actions()) = %d, want 36", len(actions))
	}
}

func TestRouteAndHeaderConstants(t *testing.T) {
	if browsercontract.CapabilityHeader != "X-AO-Browser-Capability" {
		t.Errorf("CapabilityHeader = %q", browsercontract.CapabilityHeader)
	}
	if browsercontract.RouteCommands != "/api/v1/browser/commands" {
		t.Errorf("RouteCommands = %q", browsercontract.RouteCommands)
	}
	if browsercontract.RouteStatus != "/api/v1/browser/status" {
		t.Errorf("RouteStatus = %q", browsercontract.RouteStatus)
	}
}
