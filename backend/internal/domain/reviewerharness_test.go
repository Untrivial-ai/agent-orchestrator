package domain_test

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestReviewerHarnessLabel(t *testing.T) {
	for _, h := range domain.AllReviewerHarnesses {
		label := h.Label()
		if label == "" {
			t.Errorf("expected non-empty label for reviewer harness %q", h)
		}
		if label == string(h) {
			t.Errorf("expected custom display label for reviewer harness %q, got bare id", h)
		}
	}

	// Unknown harness fallback
	unknown := domain.ReviewerHarness("unknown-reviewer")
	if got := unknown.Label(); got != "unknown-reviewer" {
		t.Errorf("expected unknown harness to return bare id, got %q", got)
	}
}
