package systeminstall

import "testing"

func TestUnrealAgentReportsBundledHarness(t *testing.T) {
	plan := newTestService("linux", "go").planAgent(TargetUnreal)
	if !plan.Unsupported || plan.Method != "manual" || len(plan.Command) != 0 {
		t.Fatalf("plan = %#v, want bundled-harness explanation", plan)
	}
	if plan.DocsURL != "https://github.com/unreallabsai/unreal-agent" {
		t.Fatalf("docs URL = %q", plan.DocsURL)
	}
}
