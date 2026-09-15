package domain

import "testing"

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
