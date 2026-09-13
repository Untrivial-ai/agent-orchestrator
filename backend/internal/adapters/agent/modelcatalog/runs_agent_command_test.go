package modelcatalog

import "testing"

func TestRunsAgentCommandClassification(t *testing.T) {
	for id, want := range map[string]bool{
		"claude-code": false, "muse": false,
		"codex": true, "kiro": true, "cursor": true, "devin": true,
		"qwen": false, "goose": false, "amp": false,
	} {
		if got := (Discoverer{}).RunsAgentCommand(id); got != want {
			t.Errorf("RunsAgentCommand(%q) = %v, want %v", id, got, want)
		}
	}
}
