package postgres

import (
	"testing"
	"time"
)

// reviewRunRowStub makes the nullable terminal column explicit. PostgreSQL
// returns NULL until OpenReviewTerminal records the terminal ID.
type reviewRunRowStub struct {
	terminalID *string
}

func (s reviewRunRowStub) Scan(dest ...any) error {
	values := []string{
		"run-id", "org-id", "pr-id", "session-id", "target-sha",
		"running", "", "", "", "", "", // status through last_error
	}
	for i, value := range values {
		if i == 9 { // review_terminal_id is nullable.
			continue
		}
		*dest[i].(*string) = value
	}
	*dest[9].(**string) = s.terminalID
	*dest[11].(*time.Time) = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	*dest[12].(**time.Time) = nil
	*dest[13].(**time.Time) = nil
	return nil
}

func TestScanReviewRunAllowsMissingTerminal(t *testing.T) {
	run, err := scanReviewRun(reviewRunRowStub{})
	if err != nil {
		t.Fatalf("scanReviewRun: %v", err)
	}
	if run.ReviewTerminalID != "" {
		t.Fatalf("ReviewTerminalID = %q, want empty for a newly created review", run.ReviewTerminalID)
	}
}

func TestScanReviewRunReadsTerminal(t *testing.T) {
	const terminalID = "terminal-id"
	run, err := scanReviewRun(reviewRunRowStub{terminalID: ptr(terminalID)})
	if err != nil {
		t.Fatalf("scanReviewRun: %v", err)
	}
	if run.ReviewTerminalID != terminalID {
		t.Fatalf("ReviewTerminalID = %q, want %q", run.ReviewTerminalID, terminalID)
	}
}

func TestTerminalTicketPurposeBindsReviewerTerminal(t *testing.T) {
	if got, want := terminalTicketPurpose("agent", "reviewer-terminal-id"), "terminal:agent:reviewer-terminal-id"; got != want {
		t.Fatalf("terminalTicketPurpose = %q, want %q", got, want)
	}
	if got, want := terminalTicketPurpose("agent", ""), "terminal:agent"; got != want {
		t.Fatalf("generic terminalTicketPurpose = %q, want %q", got, want)
	}
}

func ptr(value string) *string { return &value }
