package domain

import "testing"

func TestOpenCodeV2HarnessIsKnownAndDistinct(t *testing.T) {
	if HarnessOpenCodeV2 != AgentHarness("opencode-v2") {
		t.Fatalf("HarnessOpenCodeV2 = %q, want opencode-v2", HarnessOpenCodeV2)
	}
	if HarnessOpenCodeV2 == HarnessOpenCode {
		t.Fatal("OpenCode 2 harness must remain distinct from OpenCode 1")
	}
	for _, harness := range []AgentHarness{HarnessOpenCode, HarnessOpenCodeV2} {
		if !harness.IsKnown() {
			t.Fatalf("%q.IsKnown() = false, want true", harness)
		}
	}
}

func TestFXHarnessIsKnown(t *testing.T) {
	if HarnessFX != AgentHarness("fx") {
		t.Fatalf("HarnessFX = %q, want fx", HarnessFX)
	}
	if !HarnessFX.IsKnown() {
		t.Fatal("HarnessFX.IsKnown() = false, want true")
	}
	for _, harness := range AllHarnesses {
		if harness == HarnessFX {
			return
		}
	}
	t.Fatal("AllHarnesses does not contain HarnessFX")
}

func TestPrimeAgentHarnessIsKnown(t *testing.T) {
	if HarnessPrimeAgent != AgentHarness("prime-agent") {
		t.Fatalf("HarnessPrimeAgent = %q, want prime-agent", HarnessPrimeAgent)
	}
	if !HarnessPrimeAgent.IsKnown() {
		t.Fatal("HarnessPrimeAgent.IsKnown() = false, want true")
	}
	found := false
	for _, harness := range AllHarnesses {
		if harness == HarnessPrimeAgent {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("AllHarnesses does not contain HarnessPrimeAgent")
	}
}
func TestOMPHarnessIsKnown(t *testing.T) {
	if HarnessOMP != AgentHarness("omp") {
		t.Fatalf("HarnessOMP = %q, want omp", HarnessOMP)
	}
	if !HarnessOMP.IsKnown() {
		t.Fatal("HarnessOMP.IsKnown() = false, want true")
	}
	found := false
	for _, harness := range AllHarnesses {
		if harness == HarnessOMP {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("AllHarnesses does not contain HarnessOMP")
	}
}

func TestMiMoCodeHarnessIsKnown(t *testing.T) {
	if HarnessMiMoCode != AgentHarness("mimo-code") {
		t.Fatalf("HarnessMiMoCode = %q, want mimo-code", HarnessMiMoCode)
	}
	if !HarnessMiMoCode.IsKnown() {
		t.Fatal("HarnessMiMoCode.IsKnown() = false, want true")
	}
}

func TestDeepSeekHarnessIsKnown(t *testing.T) {
	if HarnessDeepSeek != AgentHarness("deepseek-harness") {
		t.Fatalf("HarnessDeepSeek = %q, want deepseek-harness", HarnessDeepSeek)
	}
	if !HarnessDeepSeek.IsKnown() {
		t.Fatal("HarnessDeepSeek.IsKnown() = false, want true")
	}
	found := false
	for _, harness := range AllHarnesses {
		if harness == HarnessDeepSeek {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("AllHarnesses does not contain HarnessDeepSeek")
	}
}
