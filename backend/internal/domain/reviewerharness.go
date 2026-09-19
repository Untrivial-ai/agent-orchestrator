package domain

// ReviewerHarness identifies a code-review agent. It is a separate vocabulary
// from AgentHarness on purpose: a reviewer-only tool must not become a valid
// worker, and a worker harness does not automatically become a valid reviewer.
type ReviewerHarness string

// Supported reviewer harnesses.
const (
	ReviewerOpenCode ReviewerHarness = "opencode"
)

// AllReviewerHarnesses is the canonical set used to validate a configured
// reviewer harness.
var AllReviewerHarnesses = []ReviewerHarness{
	ReviewerOpenCode,
}

// IsKnown reports whether h is one of the supported reviewer harnesses.
func (h ReviewerHarness) IsKnown() bool {
	for _, k := range AllReviewerHarnesses {
		if h == k {
			return true
		}
	}
	return false
}
