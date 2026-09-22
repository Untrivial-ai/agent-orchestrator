package notify

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestEnrichReadyToMergePrioritizesPRContext(t *testing.T) {
	t.Parallel()

	rec, err := enrich(Intent{
		Type:               domain.NotificationReadyToMerge,
		SessionID:          "sess-1",
		ProjectID:          "proj-1",
		PRURL:              "https://github.com/acme/app/pull/67",
		SessionDisplayName: "Checkout flow",
		PRNumber:           67,
		PRTitle:            "Fix checkout totals",
		CreatedAt:          time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("enrich ready notification: %v", err)
	}

	if want := "Fix checkout totals · PR #67"; rec.Title != want {
		t.Fatalf("title = %q, want %q", rec.Title, want)
	}
	if want := "PR from session Checkout flow is ready to merge. CI passed with no blocking review feedback."; rec.Body != want {
		t.Fatalf("body = %q, want %q", rec.Body, want)
	}
}

func TestEnrichReadyToMergeFallsBackWithoutPRTitle(t *testing.T) {
	t.Parallel()

	rec, err := enrich(Intent{
		Type:      domain.NotificationReadyToMerge,
		SessionID: "sess-1",
		ProjectID: "proj-1",
		PRURL:     "https://github.com/acme/app/pull/67",
		PRNumber:  67,
		CreatedAt: time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("enrich ready notification: %v", err)
	}

	if want := "PR #67 is ready to merge"; rec.Title != want {
		t.Fatalf("title = %q, want %q", rec.Title, want)
	}
}

func TestEnrichAgentTurnNotificationsAreSessionScoped(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		typ       domain.NotificationType
		wantTitle string
		wantBody  string
	}{
		{
			typ:       domain.NotificationTurnCompleted,
			wantTitle: "Checkout flow finished",
			wantBody:  "Your agent finished its turn and is ready for more work.",
		},
		{
			typ:       domain.NotificationTurnFailed,
			wantTitle: "Checkout flow failed",
			wantBody:  "The agent could not complete its turn. Open the session for details.",
		},
	} {
		t.Run(string(tt.typ), func(t *testing.T) {
			rec, err := enrich(Intent{
				Type:               tt.typ,
				SessionID:          "sess-1",
				ProjectID:          "proj-1",
				EventKey:           "turn-1",
				SessionDisplayName: "Checkout flow",
				CreatedAt:          time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("enrich: %v", err)
			}
			if rec.Title != tt.wantTitle || rec.Body != tt.wantBody || rec.EventKey != "turn-1" {
				t.Fatalf("record = %+v", rec)
			}
		})
	}
}

func TestEnrichCIFailureRequiresPR(t *testing.T) {
	t.Parallel()

	_, err := enrich(Intent{
		Type:      domain.NotificationCIFailed,
		SessionID: "sess-1",
		ProjectID: "proj-1",
		CreatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected missing PR URL to fail")
	}
}
