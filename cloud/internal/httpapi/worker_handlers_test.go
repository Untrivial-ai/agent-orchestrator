package httpapi

import (
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

var allTicketScopes = []string{
	"worker:connect",
	"worker:event",
	"worker:turn:claim",
	"worker:turn:poll",
	"worker:turn:complete",
	"worker:credential:read",
	"worker:git",
	"worker:orchestrate",
	"worker:report",
	"worker:transport",
}

func TestIssuedWorkerScopes(t *testing.T) {
	cases := []struct {
		name   string
		launch domain.WorkerLaunch
		keeps  []string
		strips []string
	}{
		{
			name:   "orchestrator keeps orchestrate, loses report",
			launch: domain.WorkerLaunch{Kind: "orchestrator"},
			keeps:  []string{"worker:orchestrate", "worker:connect"},
			strips: []string{"worker:report"},
		},
		{
			name:   "child worker keeps report, loses orchestrate",
			launch: domain.WorkerLaunch{Kind: "worker", ParentSessionID: "22222222-2222-4222-8222-222222222222"},
			keeps:  []string{"worker:report", "worker:transport"},
			strips: []string{"worker:orchestrate"},
		},
		{
			name:   "top-level worker loses both",
			launch: domain.WorkerLaunch{Kind: "worker"},
			keeps:  []string{"worker:connect", "worker:git"},
			strips: []string{"worker:orchestrate", "worker:report"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issued := issuedWorkerScopes(allTicketScopes, tc.launch)
			for _, scope := range tc.keeps {
				if !slices.Contains(issued, scope) {
					t.Fatalf("scope %q was stripped: %v", scope, issued)
				}
			}
			for _, scope := range tc.strips {
				if slices.Contains(issued, scope) {
					t.Fatalf("scope %q survived the strip: %v", scope, issued)
				}
			}
		})
	}
}

func TestIssuedWorkerScopesDoesNotMutateTicket(t *testing.T) {
	before := slices.Clone(allTicketScopes)
	_ = issuedWorkerScopes(allTicketScopes, domain.WorkerLaunch{Kind: "worker"})
	if !slices.Equal(before, allTicketScopes) {
		t.Fatal("issuedWorkerScopes mutated the ticket's scope slice")
	}
}
