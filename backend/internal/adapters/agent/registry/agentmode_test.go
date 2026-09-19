package registry

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestConfigSpecModeEnumMatchesDomainVocabulary pins every shipped adapter's
// ConfigSpec against the domain mode vocabularies. This is the drift test the
// Amp-shaped AgentConfig.Validate() hardcode needed: zcode advertised
// build|edit|plan|yolo in its spec while domain validation only accepted the
// Amp set, so every zcode mode was rejected at config-write time. The contract
// has two halves:
//
//   - a harness with a domain vocabulary must expose exactly that enum in its
//     spec (adding a vocabulary without wiring the adapter, or editing either
//     side alone, fails here), and
//   - every mode enum in every spec must be accepted by domain validation
//     (a new adapter with a hand-rolled enum and no domain vocabulary fails
//     here, pointing at HarnessModeVocabularies).
func TestConfigSpecModeEnumMatchesDomainVocabulary(t *testing.T) {
	harnessed := Harnessed()
	seenVocab := map[domain.AgentHarness]bool{}
	for _, ha := range harnessed {
		t.Run(string(ha.Harness), func(t *testing.T) {
			spec, err := ha.Agent.GetConfigSpec(context.Background())
			if err != nil {
				t.Fatalf("GetConfigSpec: %v", err)
			}
			var enum []string
			for _, f := range spec.Fields {
				if f.Key == "mode" && f.Type == ports.ConfigFieldEnum {
					enum = f.Enum
					break
				}
			}
			vocab, hasVocab := domain.ModeVocabulary(ha.Harness)
			if hasVocab {
				seenVocab[ha.Harness] = true
				if enum == nil {
					t.Fatalf("harness %q has a domain mode vocabulary %v but its ConfigSpec has no mode enum field; derive the spec from domain.ModeVocabulary", ha.Harness, vocab.Values)
				}
				if !slices.Equal(enum, vocab.Values) {
					t.Fatalf("harness %q mode enum = %v, want the domain vocabulary %v", ha.Harness, enum, vocab.Values)
				}
				return
			}
			if enum != nil {
				t.Fatalf("harness %q exposes mode enum %v but declares no domain mode vocabulary; register it in domain.HarnessModeVocabularies so AgentConfig validation accepts it", ha.Harness, enum)
			}
		})
	}
	for _, v := range domain.HarnessModeVocabularies {
		if !seenVocab[v.Harness] {
			t.Errorf("domain mode vocabulary for %q is not covered by any registered adapter; the harness id likely diverged from the adapter manifest id", v.Harness)
		}
	}
}

// TestDomainValidationAcceptsEverySpecModeValue is the converse cross-check:
// every mode value any shipped adapter advertises must pass AgentConfig
// validation. This is exactly the check the Amp-only hardcode failed for
// zcode's build|edit|plan|yolo.
func TestDomainValidationAcceptsEverySpecModeValue(t *testing.T) {
	for _, ha := range Harnessed() {
		spec, err := ha.Agent.GetConfigSpec(context.Background())
		if err != nil {
			t.Fatalf("%s: GetConfigSpec: %v", ha.Harness, err)
		}
		for _, f := range spec.Fields {
			if f.Key != "mode" || f.Type != ports.ConfigFieldEnum {
				continue
			}
			for _, mode := range f.Enum {
				cfg := domain.AgentConfig{Mode: mode}
				if err := cfg.Validate(); err != nil {
					t.Errorf("%s advertises mode %q but AgentConfig.Validate rejects it: %v", ha.Harness, mode, err)
				}
				if !domain.ModeKnownAnywhere(mode) {
					t.Errorf("%s advertises mode %q not covered by any domain.HarnessModeVocabularies entry", ha.Harness, mode)
				}
			}
		}
	}
}

// TestValidateModeUsageErrors pins the harness-scoped validator's behavior:
// clean usage errors naming the vocabulary, never a raw argv rejection.
func TestValidateModeUsageErrors(t *testing.T) {
	if err := domain.ValidateMode(domain.HarnessZCode, "medium"); err == nil {
		t.Error("zcode mode \"medium\" accepted; want a usage error (Amp's vocabulary, not ZCode's)")
	}
	if err := domain.ValidateMode(domain.HarnessZCode, "yolo"); err != nil {
		t.Errorf("zcode mode \"yolo\" rejected: %v", err)
	}
	if err := domain.ValidateMode(domain.HarnessCodex, ""); err != nil {
		t.Errorf("empty mode on a mode-less harness rejected: %v", err)
	}
	if err := domain.ValidateMode(domain.HarnessCodex, "build"); err == nil {
		t.Error("mode \"build\" accepted for a harness without agent modes; want a usage error")
	}
	if got, want := domain.AllModeValues(), "low, medium, high, ultra, build, edit, plan, yolo"; got != want {
		t.Errorf("AllModeValues() = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(mustVocab(t, domain.HarnessAmp).Values, []string{"low", "medium", "high", "ultra"}) {
		t.Error("amp vocabulary drifted")
	}
}

func mustVocab(t *testing.T, h domain.AgentHarness) domain.AgentModeVocabulary {
	t.Helper()
	v, ok := domain.ModeVocabulary(h)
	if !ok {
		t.Fatalf("no mode vocabulary for %q", h)
	}
	return v
}
