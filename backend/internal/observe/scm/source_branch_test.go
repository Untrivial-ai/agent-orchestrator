package scm

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionMatchBranches(t *testing.T) {
	rec := func(branch, source string) domain.SessionRecord {
		return domain.SessionRecord{Metadata: domain.SessionMetadata{Branch: branch, SourceBranch: source}}
	}

	cases := []struct {
		name string
		in   domain.SessionRecord
		want []string
		why  string
	}{
		{
			name: "ordinary session",
			in:   rec("ao/proj-1/root", ""),
			want: []string{"ao/proj-1/root"},
			why:  "a session that was not imported has only its own branch",
		},
		{
			// The case that made every import sit awaiting a PR: the branch its
			// conversation ran on was already checked out, so the session got a
			// fresh one and the pull request became unreachable.
			name: "import whose branch was taken",
			in:   rec("ao/proj-2/root", "feat/payments"),
			want: []string{"ao/proj-2/root", "feat/payments"},
			why:  "both the session branch and the conversation's branch must match",
		},
		{
			name: "import that kept its branch",
			in:   rec("feat/payments", "feat/payments"),
			want: []string{"feat/payments"},
			why:  "the same branch must not be scanned twice",
		},
		{
			name: "no branch at all",
			in:   rec("", ""),
			want: []string{},
			why:  "nothing to match against",
		},
		{
			name: "source branch only",
			in:   rec("", "feat/payments"),
			want: []string{"feat/payments"},
			why:  "a recorded source branch is still worth matching",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionMatchBranches(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v — %s", got, tc.want, tc.why)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got %v, want %v — %s", got, tc.want, tc.why)
					return
				}
			}
		})
	}
}
