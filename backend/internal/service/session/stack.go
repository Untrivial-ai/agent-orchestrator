package session

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

// stackInfo is the derived position of one PR within its session's set of PRs.
// PRs form a stack when one targets the source branch of another: PR B is a
// child of PR A when B.TargetBranch == A.SourceBranch and A is open.
type stackInfo struct {
	// Blocked is true when an open PR in the set owns the branch this PR targets,
	// i.e. this PR is a child stacked on a parent that has not merged yet.
	Blocked bool
	// BottomOfStack is true when no open PR sits below this one. It is the only
	// PR in a stack that should receive a merge-conflict rebase nudge; an
	// independent PR (targeting the base branch) is its own bottom.
	BottomOfStack bool
}

// buildStacks derives the stack position of every PR from the source/target
// branch columns alone. A parent counts only while open, matching the rule that
// a merged or closed parent no longer blocks its children.
func buildStacks(prs []domain.PRFacts) map[string]stackInfo {
	positions := contract.BuildStacks(toContractPRFacts(prs))
	out := make(map[string]stackInfo, len(prs))
	for url, position := range positions {
		out[url] = stackInfo{
			Blocked:       position.Blocked,
			BottomOfStack: position.BottomOfStack,
		}
	}
	return out
}
