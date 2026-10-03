package domain

import "testing"

func TestOpenCodeV2ReviewerHarnessIsKnownAndDistinct(t *testing.T) {
	if ReviewerOpenCodeV2 != ReviewerHarness("opencode-v2") {
		t.Fatalf("ReviewerOpenCodeV2 = %q, want opencode-v2", ReviewerOpenCodeV2)
	}
	if ReviewerOpenCodeV2 == ReviewerOpenCode {
		t.Fatal("OpenCode 2 reviewer must remain distinct from OpenCode 1")
	}
	for _, harness := range []ReviewerHarness{ReviewerOpenCode, ReviewerOpenCodeV2} {
		if !harness.IsKnown() {
			t.Fatalf("%q.IsKnown() = false, want true", harness)
		}
	}
}
