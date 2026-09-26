package onedev

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// MergePullRequest reports that AO does not merge OneDev pull requests. It
// satisfies ports.SCMMerger so the daemon can register this adapter in the
// multi-merger: without it, `ao pr merge` on a OneDev pull request fails with
// "unknown provider" wrapped as a generic SCM failure, which reads as a
// misconfiguration rather than as a capability this provider does not have.
//
// # Why this is unsupported rather than unimplemented
//
// ports.SCMMergeRequest carries ExpectedHeadSHA as a compare-and-swap guard,
// and its contract is explicit: "Provider implementations must reject the
// mutation if the live head has advanced." OneDev cannot honour that.
// POST /~api/pulls/{requestId}/merge takes an optional note and nothing else —
// there is no head-SHA precondition, no if-match header, and the response
// carries no merge commit to reconcile against afterwards.
//
// The only way to emulate the guard is to read the head, compare it, and then
// merge — which leaves a real time-of-check-to-time-of-use window. A push
// landing inside that window would have AO merge a revision that was never
// reviewed and never passed CI, and a merge is not reversible by AO. Failing
// closed on an irreversible action is the better trade: the operator merges in
// the OneDev UI, where they can see what they are merging.
//
// If OneDev later grows a head-SHA precondition on its merge endpoint, this is
// the method to implement — not the check-then-merge sequence, which is what
// this comment exists to stop being reintroduced as an obvious fix.
func (p *Provider) MergePullRequest(_ context.Context, request ports.SCMMergeRequest) (ports.SCMMergeResult, error) {
	return ports.SCMMergeResult{}, fmt.Errorf(
		"%w: onedev cannot merge %s#%d safely from AO (its merge API has no expected-head precondition); merge it in the OneDev UI",
		ports.ErrSCMUnsupported, request.PR.Repo.Repo, request.PR.Number)
}
