package contract_test

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestSummarizeSession(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		facts contract.SummaryFacts
		want  string
	}{
		{
			name:  "terminated suppresses summary",
			facts: contract.SummaryFacts{IsTerminated: true},
			want:  "",
		},
		{
			name:  "ci failure takes priority",
			facts: contract.SummaryFacts{CIFailing: true, OpenPRs: 1},
			want:  "CI failing on open PR",
		},
		{
			name:  "open pr without update or ci",
			facts: contract.SummaryFacts{OpenPRs: 2},
			want:  "PR open",
		},
		{
			name:  "working status gets a human summary",
			facts: contract.SummaryFacts{DisplayStatus: contract.DisplayWorking},
			want:  "Working on the task",
		},
		{
			name:  "empty facts yield empty summary",
			facts: contract.SummaryFacts{},
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := contract.SummarizeSession(tc.facts); got != tc.want {
				t.Fatalf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}
