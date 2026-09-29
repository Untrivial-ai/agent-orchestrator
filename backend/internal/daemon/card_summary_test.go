package daemon

import "testing"

func TestLiveProgressSummaryStaysScopedToTheCard(t *testing.T) {
	tests := []struct{ title, evidence, want string }{
		{"Inspect Session Routing", "The agent is currently Read internal/httpapi/server.go", "Inspecting session routing"},
		{"Trace Worker Claim Authorization", "The agent is currently Read worker_turn_store.go", "Inspecting worker claim authorization"},
		{"Inspect Event Streaming", "rg -n events internal/httpapi", "Inspecting event streaming"},
		{"Review Project Onboarding", "The agent is currently Read resource_handlers.go", "Inspecting project onboarding"},
		{"Inspect Token Scopes", "The agent is currently Read token.go\nGmail account handling", "Inspecting token scopes"},
		{"Review Database Migrations", "Running tests for migration changes", "Testing database migrations"},
		{"Create Wishlist Page", "The agent is currently Edit app/wishlist/page.tsx", "Editing wishlist page"},
		{"Fix API Errors", "apply_patch app/api/errors.ts", "Editing API errors"},
		{"Inspect Event Streaming", "", ""},
		{"Untitled Task", "Read server.go", ""},
	}
	for _, tt := range tests {
		if got := liveProgressSummary(tt.title, tt.evidence); got != tt.want {
			t.Errorf("liveProgressSummary(%q, %q) = %q, want %q", tt.title, tt.evidence, got, tt.want)
		}
	}
}
