package daemon

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestLiveProgressSummaryUsesBriefForSubjectNotAction(t *testing.T) {
	tests := []struct {
		brief    string
		evidence string
		want     string
	}{
		{"Inspect Gmail token refresh and error handling. Do not edit files.", "Read src/lib/gmail.ts", "Inspecting Gmail API handling"},
		{"Review typography and spacing. Do not edit files.", "Find component styles", "Reviewing interface styling"},
		{"Inspect database migrations. Do not edit files.", "Read migration files", "Reviewing database migrations"},
		{"Inspect database migrations. Do not edit files.", "Reading CREATE POLICY definitions", "Reviewing row-level access policies"},
		{"Audit Gmail token storage.", "npm run lint failed", "Diagnosing Gmail token failures"},
		{"Audit authentication callbacks.", "build failed", "Diagnosing authentication failures"},
		{"Inspect streaming behavior.", "Read packages/ai/src/stream.ts", "Inspecting streaming chunk parsing"},
		{"Inspect Ollama behavior.", "Read packages/ai/src/providers/ollama.ts", "Reviewing Ollama provider settings"},
		{"Inspect CLI behavior.", "Read packages/cli/src/args.ts", "Reviewing command-line argument parsing"},
		{"Inspect agent behavior.", "Read packages/agent/src/loop.ts", "Inspecting agent iteration flow"},
		{"Inspect the cloud proxy.", "The requested source path is unavailable", "Inspecting cloud proxy behavior"},
	}
	for _, test := range tests {
		if got := liveProgressSummary(test.brief, test.evidence); got != test.want {
			t.Errorf("liveProgressSummary(%q, %q) = %q, want %q", test.brief, test.evidence, got, test.want)
		}
	}
}

func TestLiveProgressSummaryDistinguishesConcurrentWork(t *testing.T) {
	tests := []struct {
		evidence string
		want     string
	}{
		{"Read supabase/migrations/0001_gmail_tokens.sql", "Reviewing Gmail token storage"},
		{"Read src/app/auth/callback/route.ts", "Inspecting authentication callbacks"},
		{"Read src/app/api/conversations/route.ts", "Reviewing conversation API routes"},
		{"Read src/components/dashboard/context.tsx", "Reviewing dashboard components"},
	}
	seen := map[string]bool{}
	for _, test := range tests {
		got := liveProgressSummary("Audit the application", test.evidence)
		if got != test.want {
			t.Errorf("liveProgressSummary(%q) = %q, want %q", test.evidence, got, test.want)
		}
		if seen[got] {
			t.Errorf("duplicate summary for distinct evidence: %q", got)
		}
		seen[got] = true
	}
}

func TestUniqueLiveCardSummaryUsesTheCurrentWorkersOwnTask(t *testing.T) {
	other := domain.SessionRecord{
		ID:       "worker-1",
		Metadata: domain.SessionMetadata{LatestAssistantUpdate: cardSummaryMetadataPrefix + "Reviewing Gmail token storage"},
	}
	got := uniqueLiveCardSummary("worker-2", "Audit authentication callbacks", "Reviewing Gmail token storage", []domain.SessionRecord{other})
	if got != "Inspecting authentication callbacks" {
		t.Fatalf("uniqueLiveCardSummary = %q", got)
	}
}

func TestCardSummaryIsGroundedInThatWorkersEvidence(t *testing.T) {
	tests := []struct {
		summary, brief, evidence string
		want                     bool
	}{
		{"Inspecting authentication callbacks", "Inspect the callback route", "Read src/app/auth/callback/route.ts", true},
		{"Reviewing Gmail token storage", "Inspect the callback route", "Read src/app/auth/callback/route.ts", false},
		{"Reviewing dashboard components", "Inspect the dashboard", "Read src/components/dashboard/context.tsx", true},
		{"Reviewing conversation API routes", "Inspect the dashboard", "Read src/components/dashboard/context.tsx", false},
		{"Working on the task", "Inspect the dashboard", "Read src/components/dashboard/context.tsx", false},
		{"Reviewing row-level access policies", "Inspect user avatars", "Read packages/product-ui/src/UserAvatar.tsx; policy text from an earlier command", false},
	}
	for _, test := range tests {
		if got := cardSummaryIsGrounded(test.summary, test.brief, test.evidence); got != test.want {
			t.Errorf("cardSummaryIsGrounded(%q) = %v, want %v", test.summary, got, test.want)
		}
	}
}

func TestSourceFileActivitySummaryDoesNotExposeToolSyntax(t *testing.T) {
	got := sourceFileActivitySummary("Read packages/ai/src/providers/sse.ts")
	if got != "Reviewing SSE behavior" {
		t.Fatalf("sourceFileActivitySummary = %q", got)
	}
	if strings.Contains(got, "/") || strings.Contains(strings.ToLower(got), "read ") {
		t.Fatalf("summary leaked raw tool activity: %q", got)
	}
}
