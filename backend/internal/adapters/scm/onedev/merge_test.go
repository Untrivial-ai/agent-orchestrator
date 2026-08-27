package onedev

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestMergePullRequestIsUnsupported pins the decision documented in merge.go:
// the adapter registers as a merger so `ao pr merge` can report a capability
// answer, but it never merges — OneDev's merge endpoint has no expected-head
// precondition, so honouring SCMMergeRequest.ExpectedHeadSHA is impossible
// without a check-then-merge race on an irreversible action.
func TestMergePullRequestIsUnsupported(t *testing.T) {
	// A server that must never be called: an unsupported capability is
	// answered locally, not by attempting the merge and hoping.
	srv := newTestServer(t, func(http.ResponseWriter, *http.Request) {
		t.Error("MergePullRequest contacted the OneDev instance; it must not")
	})
	p := newTestProvider(t, []string{srv.URL})

	var merger ports.SCMMerger = p
	res, err := merger.MergePullRequest(context.Background(), ports.SCMMergeRequest{
		PR: ports.SCMPRRef{
			Repo:   ports.SCMRepo{Provider: ProviderKey, Host: strings.TrimPrefix(srv.URL, "http://"), Repo: "Homelab/curatarr"},
			Number: 106,
		},
		ExpectedHeadSHA: "0123456789abcdef0123456789abcdef01234567",
		Method:          ports.SCMMergeSquash,
	})
	if !errors.Is(err, ports.ErrSCMUnsupported) {
		t.Fatalf("err = %v, want ports.ErrSCMUnsupported", err)
	}
	if res.MergeCommitSHA != "" {
		t.Errorf("MergeCommitSHA = %q, want empty", res.MergeCommitSHA)
	}
	// The message has to point somewhere useful: an operator who hits this
	// needs to know where to go instead.
	for _, want := range []string{"Homelab/curatarr", "106", "OneDev UI"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %q", err.Error(), want)
		}
	}
}
