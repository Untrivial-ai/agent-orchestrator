package reviewer

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestRegistryMatchesDomainVocabulary enforces that the shipped reviewer
// adapters and domain.AllReviewerHarnesses stay in sync: every registered
// adapter is a known reviewer harness, and every known harness has an adapter.
func TestRegistryMatchesDomainVocabulary(t *testing.T) {
	registered := map[domain.ReviewerHarness]bool{}
	oneShotReviewers := map[domain.ReviewerHarness]bool{
		domain.ReviewerAider:  true,
		domain.ReviewerAuggie: true,
		domain.ReviewerDroid:  true,
	}
	for _, a := range Constructors() {
		h := a.Harness()
		if !h.IsKnown() {
			t.Errorf("adapter harness %q is not in domain.AllReviewerHarnesses", h)
		}
		if registered[h] {
			t.Errorf("reviewer harness %q registered twice", h)
		}
		if _, ok := a.(ports.ReviewerRestorer); !ok {
			t.Errorf("reviewer harness %q does not implement restore", h)
		}
		canceller, ok := a.(ports.ReviewerCanceller)
		if !ok {
			t.Errorf("reviewer harness %q does not implement cancellation", h)
		} else if spec, err := canceller.ReviewCancel(context.Background()); err != nil {
			t.Errorf("reviewer harness %q cancel spec: %v", h, err)
		} else {
			switch h {
			case domain.ReviewerCodex, domain.ReviewerKiro, domain.ReviewerPi, domain.ReviewerMuse, domain.ReviewerQodercli:
				if spec.Mode != ports.ReviewCancelInput {
					t.Errorf("reviewer harness %q cancel mode = %q, want %q", h, spec.Mode, ports.ReviewCancelInput)
				}
				if spec.Input != "\x1b" || len(spec.Inputs) != 0 {
					t.Errorf("reviewer harness %q cancel input = %q inputs=%#v, want single escape", h, spec.Input, spec.Inputs)
				}
			case domain.ReviewerClaudeCode, domain.ReviewerOpenCode:
				if spec.Mode != ports.ReviewCancelInput {
					t.Errorf("reviewer harness %q cancel mode = %q, want %q", h, spec.Mode, ports.ReviewCancelInput)
				}
				if len(spec.Inputs) != 2 || spec.Inputs[0] != "\x1b" || spec.Inputs[1] != "\x1b" {
					t.Errorf("reviewer harness %q cancel inputs = %#v, want double escape", h, spec.Inputs)
				}
			case domain.ReviewerAgy, domain.ReviewerDevin, domain.ReviewerDroid:
				if spec.Mode != ports.ReviewCancelInterrupt {
					t.Errorf("reviewer harness %q cancel mode = %q, want %q", h, spec.Mode, ports.ReviewCancelInterrupt)
				}
				if spec.Interrupts != 1 {
					t.Errorf("reviewer harness %q cancel interrupts = %d, want 1", h, spec.Interrupts)
				}
			default:
				if spec.Mode != ports.ReviewCancelInterrupt {
					t.Errorf("reviewer harness %q cancel mode = %q, want %q", h, spec.Mode, ports.ReviewCancelInterrupt)
				}
				if spec.Interrupts != 2 {
					t.Errorf("reviewer harness %q cancel interrupts = %d, want 2", h, spec.Interrupts)
				}
			}
		}
		policy, hasPolicy := a.(ports.ReviewerReusePolicy)
		reusable := !hasPolicy || policy.ReviewProcessReusable()
		if oneShotReviewers[h] == reusable {
			t.Errorf("reviewer harness %q reusable = %v, want %v", h, reusable, !oneShotReviewers[h])
		}
		registered[h] = true
	}
	for _, h := range domain.AllReviewerHarnesses {
		if !registered[h] {
			t.Errorf("reviewer harness %q has no registered adapter", h)
		}
	}
}

func TestNewResolverResolvesShippedReviewers(t *testing.T) {
	resolver, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	for _, h := range domain.AllReviewerHarnesses {
		if _, ok := resolver.Reviewer(h); !ok {
			t.Errorf("resolver missing reviewer %q", h)
		}
	}
	if _, ok := resolver.Reviewer("nope"); ok {
		t.Error("resolver returned an adapter for an unknown harness")
	}
	for _, removed := range []domain.ReviewerHarness{"continue", "goose", "vibe", "qwen"} {
		if _, ok := resolver.Reviewer(removed); ok {
			t.Errorf("resolver returned removed reviewer %q", removed)
		}
	}
}
