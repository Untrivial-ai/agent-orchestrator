package modelcatalog

import "testing"

// Only an agent observed to hijack the browser during discovery belongs here.
// Running a CLI is not the hazard, so command-backed adapters that merely shell
// out must stay false — listing them would suppress a catalog AO is expected to
// prefetch for any installed agent.
func TestDiscoveryCanPromptLoginIsNarrow(t *testing.T) {
	for id, want := range map[string]bool{
		"kiro":        true,
		"codex":       false,
		"cursor":      false,
		"devin":       false,
		"claude-code": false,
		"muse":        false,
		"qwen":        false,
	} {
		if got := (Discoverer{}).DiscoveryCanPromptLogin(id); got != want {
			t.Errorf("DiscoveryCanPromptLogin(%q) = %v, want %v", id, got, want)
		}
	}
}
