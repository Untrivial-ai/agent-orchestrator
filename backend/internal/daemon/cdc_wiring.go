package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// retentionInterval is how often the daemon prunes change_log back to the
// retention cap. An initial prune runs at startup so a log bloated by a
// previous release shrinks without waiting a full interval.
const retentionInterval = 6 * time.Hour

// pruneTimeout bounds one retention pass; deleting up to the growth accumulated
// over an interval is far smaller than this on any realistic machine.
const pruneTimeout = 5 * time.Minute

// cdcPipeline owns the running CDC poller and live-event broadcaster, plus the
// change_log retention loop. The DB triggers write change_log; the poller tails
// it and fans each new event out to live transports such as terminal
// session-state subscriptions; retention keeps the append-only log bounded.
// Durable catch-up is a client concern; the poller only pushes live events and
// re-seeks to head on restart.
type cdcPipeline struct {
	Broadcaster   *cdc.Broadcaster
	done          <-chan struct{}
	retentionDone <-chan struct{}
}

// startCDC seeks the poller to the current head, starts its loop, and starts
// change_log retention. Both stop when ctx is cancelled; Stop waits for them.
func startCDC(ctx context.Context, store *sqlite.Store, logger *slog.Logger) (*cdcPipeline, error) {
	bcast := cdc.NewBroadcaster()
	poller := cdc.NewPoller(store, bcast, cdc.PollerConfig{Logger: logger})
	if err := poller.SeekToHead(ctx); err != nil {
		return nil, err
	}
	return &cdcPipeline{
		Broadcaster:   bcast,
		done:          poller.Start(ctx),
		retentionDone: startChangeLogRetention(ctx, store, logger),
	}, nil
}

// Stop waits for the poller and retention goroutines to exit (the caller must
// have cancelled the ctx passed to startCDC).
func (p *cdcPipeline) Stop() error {
	<-p.done
	<-p.retentionDone
	return nil
}

// startChangeLogRetention prunes change_log once at startup and then every
// retentionInterval until ctx is cancelled.
func startChangeLogRetention(ctx context.Context, store *sqlite.Store, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		pruneChangeLogOnce(ctx, store, logger)
		t := time.NewTicker(retentionInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pruneChangeLogOnce(ctx, store, logger)
			}
		}
	}()
	return done
}

func pruneChangeLogOnce(ctx context.Context, store *sqlite.Store, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	n, err := store.PruneChangeLog(ctx, sqlitestore.ChangeLogRetentionRows)
	if err != nil {
		logger.Warn("change_log retention prune failed", "err", err)
		return
	}
	if n > 0 {
		logger.Info("change_log retention pruned", "rows", n)
	}
}
