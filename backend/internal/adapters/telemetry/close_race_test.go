package telemetry

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type noopLocalStore struct{}

func (noopLocalStore) CreateTelemetryEvent(context.Context, sqlitestore.TelemetryEventRecord) error {
	return nil
}

func (noopLocalStore) PruneTelemetryEventsBefore(context.Context, time.Time, int64) (int64, error) {
	return 0, nil
}

// Telemetry is emitted from background goroutines that outlive the call that
// started them (project-added events wait on a GitHub owner classification, for
// one), so an Emit racing shutdown is reachable in production. Both buffered
// sinks must drop that event instead of panicking on a closed channel and
// taking the daemon down on its way out.
func TestBufferedSinksDropEmitsAfterClose(t *testing.T) {
	t.Parallel()
	ev := ports.TelemetryEvent{
		Name:       "ao.projects.created",
		Source:     "project_service",
		OccurredAt: time.Unix(1700000000, 0).UTC(),
		Level:      ports.TelemetryLevelInfo,
	}

	t.Run("local-sqlite", func(t *testing.T) {
		t.Parallel()
		sink := NewLocalSQLiteSink(noopLocalStore{}, telemetryLogger(nil))
		if err := sink.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		sink.Emit(context.Background(), ev)
	})

	t.Run("posthog", func(t *testing.T) {
		t.Parallel()
		sink, err := NewPostHogSink(t.TempDir(), "phc_test", "https://us.i.posthog.com", "", "",
			roundTripClient(func(req *http.Request) (*http.Response, error) {
				_ = req.Body.Close()
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}}, nil
			}), nil)
		if err != nil {
			t.Fatalf("NewPostHogSink: %v", err)
		}
		if err := sink.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		sink.Emit(context.Background(), ev)
	})
}
