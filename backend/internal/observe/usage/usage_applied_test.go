package usage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The OnUsageApplied callback must fire after a chunk's events are durably
// applied, carrying the source's session id and the parsed usage events. This
// is the exactly-once seam the cost exporter hangs off.
func TestIngestorFiresOnUsageAppliedAfterApply(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, source, path, now := seedCodexIngestionSource(t, dataDir)

	transcript := `{"type":"session_meta","payload":{"model_provider":"openai"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-5.6"}}` + "\n" +
		string(codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)) + "\n"
	mustNoError(t, os.WriteFile(path, []byte(transcript), 0o600))

	var gotSession domain.SessionID
	var gotEvents []domain.ModelUsageEvent
	calls := 0
	ingestor := NewIngestor(store, IngestorConfig{
		Clock: func() time.Time { return now },
		OnUsageApplied: func(_ context.Context, sid domain.SessionID, evs []domain.ModelUsageEvent) {
			calls++
			gotSession = sid
			gotEvents = append(gotEvents, evs...)
		},
	})
	if _, err := ingestor.Ingest(ctx, source.ID); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if len(gotEvents) == 0 {
		t.Fatal("OnUsageApplied was not called with any events")
	}
	if want := sourceSessionID(t, store, source.ID); gotSession != want {
		t.Fatalf("callback session = %q, want %q", gotSession, want)
	}
	if gotEvents[0].ModelID == "" {
		t.Fatalf("event missing model id: %+v", gotEvents[0])
	}
}
