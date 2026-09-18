package domain

import "testing"

func TestOpenCodeHarnessIsKnown(t *testing.T) {
	if HarnessOpenCode != AgentHarness("opencode") {
		t.Fatalf("HarnessOpenCode = %q, want opencode", HarnessOpenCode)
	}
	if !HarnessOpenCode.IsKnown() {
		t.Fatal("HarnessOpenCode.IsKnown() = false, want true")
	}
	found := false
	for _, harness := range AllHarnesses {
		if harness == HarnessOpenCode {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("AllHarnesses does not contain HarnessOpenCode")
	}
}