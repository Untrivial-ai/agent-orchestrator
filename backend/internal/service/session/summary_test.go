package session

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestToSessionWithFactsDerivesSummaryFromLifecycleState(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	rec := domain.SessionRecord{
		ID:        "sum-1",
		ProjectID: "sum",
		UpdatedAt: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
	}
	rec.Metadata.LatestAssistantUpdate = "Added retry to checkout"

	sess, err := (&Service{store: st, clock: func() time.Time { return rec.UpdatedAt.Add(2 * time.Minute) }}).toSessionWithFacts(rec, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Summary != "Preparing a pull request" {
		t.Fatalf("summary = %q, want lifecycle summary", sess.Summary)
	}
}

func TestToSessionWithFactsSummaryIgnoresConversationText(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	rec := domain.SessionRecord{
		ID:        "sum-3",
		ProjectID: "sum",
		UpdatedAt: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
	}
	rec.Metadata.LatestUserPrompt = "Add a prices.txt file"
	rec.Metadata.NativeCheckpointEvidence = domain.AppendNativeCheckpoint("", "native", domain.NativeCheckpointObservation{
		Generation: "launch", PromptID: "A", Submission: true, SubmissionID: "s", Text: rec.Metadata.LatestUserPrompt,
	})
	rec.Metadata.NativeCheckpointEvidence = domain.AppendNativeCheckpoint(rec.Metadata.NativeCheckpointEvidence, "native", domain.NativeCheckpointObservation{
		Generation: "launch", PromptID: "A", Text: "I wrote prices.txt.",
	})

	sess, err := (&Service{store: st, clock: func() time.Time { return rec.UpdatedAt.Add(2 * time.Minute) }}).toSessionWithFacts(rec, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Summary != "Preparing a pull request" {
		t.Fatalf("summary = %q, want lifecycle summary", sess.Summary)
	}
}

func TestToSessionWithFactsSummaryEmptyWhenTerminated(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	rec := domain.SessionRecord{
		ID:           "sum-2",
		ProjectID:    "sum",
		IsTerminated: true,
		UpdatedAt:    time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
	}
	rec.Metadata.LatestAssistantUpdate = "Added retry to checkout"

	sess, err := (&Service{store: st, clock: func() time.Time { return rec.UpdatedAt.Add(2 * time.Minute) }}).toSessionWithFacts(rec, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Summary != "" {
		t.Fatalf("summary = %q, want empty for terminated session", sess.Summary)
	}
}
