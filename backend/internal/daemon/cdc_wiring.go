package daemon

import (
	"context"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// cdcPipeline owns the running CDC poller and live-event broadcaster. The DB
// triggers write change_log; the poller tails it and fans each new event out to
// live transports such as terminal session-state subscriptions. Durable catch-up
// is a client concern; the poller only pushes live events and re-seeks to head
// on restart.
type cdcPipeline struct {
	Broadcaster   *cdc.Broadcaster
	done          <-chan struct{}
	retentionDone <-chan struct{}
}

// startCDC seeks the poller to the current head and starts its loop. It stops
// when ctx is cancelled; Stop waits for it to drain.
func startCDC(ctx context.Context, store *sqlite.Store, logger *slog.Logger) (*cdcPipeline, error) {
	bcast := cdc.NewBroadcaster()
	poller := cdc.NewPoller(store, bcast, cdc.PollerConfig{Logger: logger})
	if err := poller.SeekToHead(ctx); err != nil {
		return nil, err
	}
	janitor := cdc.NewRetentionJanitor(store, cdc.RetentionConfig{Logger: logger})
	return &cdcPipeline{
		Broadcaster:   bcast,
		done:          poller.Start(ctx),
		retentionDone: janitor.Start(ctx),
	}, nil
}

// Stop waits for the poller goroutine to exit (the caller must have cancelled the
// ctx passed to startCDC).
func (p *cdcPipeline) Stop() error {
	<-p.done
	if p.retentionDone != nil {
		<-p.retentionDone
	}
	return nil
}
