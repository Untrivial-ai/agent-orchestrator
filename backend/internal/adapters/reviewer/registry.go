// Package reviewer is the single source of truth for the code-review adapters
// the daemon ships. It mirrors the worker agent registry but is a separate set:
// adding a reviewer here does not widen the worker AgentHarness vocabulary.
package reviewer

import (
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Adapter is a registered reviewer: a ports.Reviewer that names its harness.
type Adapter interface {
	ports.Reviewer
	Harness() domain.ReviewerHarness
}

// Constructors returns every reviewer adapter the daemon ships. Add a reviewer
// here (and to domain.AllReviewerHarnesses) to register it.
func Constructors() []Adapter {
	return []Adapter{
		opencode.New(),
	}
}

// Resolver maps a reviewer harness onto its adapter.
type Resolver struct {
	reviewers map[domain.ReviewerHarness]ports.Reviewer
}

var _ ports.ReviewerResolver = (*Resolver)(nil)

// NewResolver builds a Resolver from the shipped reviewer adapters. It fails if
// two adapters claim the same harness, or if a registered harness is not in the
// domain reviewer vocabulary (the two must stay in sync).
func NewResolver() (*Resolver, error) {
	m := make(map[domain.ReviewerHarness]ports.Reviewer)
	for _, a := range Constructors() {
		h := a.Harness()
		if !h.IsKnown() {
			return nil, fmt.Errorf("reviewer adapter %q is not in domain.AllReviewerHarnesses", h)
		}
		if _, dup := m[h]; dup {
			return nil, fmt.Errorf("reviewer harness %q is registered twice", h)
		}
		m[h] = a
	}
	for _, harness := range domain.AllReviewerHarnesses {
		if _, ok := m[harness]; !ok {
			return nil, fmt.Errorf("reviewer harness %q has no registered adapter", harness)
		}
	}
	return &Resolver{reviewers: m}, nil
}

// Reviewer returns the adapter for a harness, ok=false when none is registered.
func (r *Resolver) Reviewer(harness domain.ReviewerHarness) (ports.Reviewer, bool) {
	rv, ok := r.reviewers[harness]
	return rv, ok
}
