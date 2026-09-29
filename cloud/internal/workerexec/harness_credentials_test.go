package workerexec

import "testing"

// SupportedHarness must cover every registered harness so the worker's launch
// guard (verifyHarnessAvailable) can never skip one. opencode regressed here:
// the guard's hardcoded switch omitted it, so opencode sessions launched no
// agent terminal (no TUI).
func TestSupportedHarnessCoversRegistry(t *testing.T) {
	wantBinary := map[string]string{
		"claude-code": "claude",
		"codex":       "codex",
		"cursor":      "cursor-agent",
		"opencode":    "opencode",
	}
	for harness, want := range wantBinary {
		binary, ok := SupportedHarness(harness)
		if !ok {
			t.Errorf("SupportedHarness(%q) not supported; the worker would refuse to launch it", harness)
			continue
		}
		if binary != want {
			t.Errorf("SupportedHarness(%q) binary = %q, want %q", harness, binary, want)
		}
	}
	// Every registry entry must resolve, so a future harness can't be added to the
	// registry yet silently rejected by the launch guard.
	for harness := range harnessCredentials {
		if _, ok := SupportedHarness(harness); !ok {
			t.Errorf("registered harness %q is not reported supported", harness)
		}
	}
	if _, ok := SupportedHarness("not-a-harness"); ok {
		t.Errorf("SupportedHarness accepted an unknown harness")
	}
}
