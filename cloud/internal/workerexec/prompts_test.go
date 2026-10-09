package workerexec

import (
	"strings"
	"testing"
)

func TestWorkerSystemPromptPermitsBoundedNativeDelegation(t *testing.T) {
	for _, hasOrchestrator := range []bool{true, false} {
		got := workerSystemPrompt("/skills", hasOrchestrator)
		flat := strings.Join(strings.Fields(got), " ")
		for _, want := range []string{
			"you may delegate bounded portions of your assigned task",
			"do not fan out serial work",
			"You remain responsible for integrating subagent results, testing, reporting, and this session's PR",
			"Subagents get no broader authority than this session",
			"parallel subagents must be read-only or own disjoint files",
		} {
			if !strings.Contains(flat, want) {
				t.Fatalf("cloud worker prompt (orchestrator=%v) missing %q:\n%s", hasOrchestrator, want, got)
			}
		}
		for _, forbidden := range []string{
			"Do not use your runtime's built-in subagent",
			"complete the task in this session.",
			"AO workers only",
		} {
			if strings.Contains(flat, forbidden) {
				t.Fatalf("cloud worker prompt (orchestrator=%v) retains delegation prohibition %q:\n%s", hasOrchestrator, forbidden, got)
			}
		}
	}
}
