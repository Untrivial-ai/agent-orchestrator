package notificationoutbox

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
)

const (
	flushIdleInterval = time.Second
	maxRetryBackoff   = 30 * time.Second
)

type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                                { return time.Now() }
func (realClock) After(duration time.Duration) <-chan time.Time { return time.After(duration) }

type Flusher struct {
	Outbox      *Outbox
	WorkerEpoch int64
	Deliver     func(context.Context, Event) error
	Clock       Clock
	Jitter      func(time.Duration) time.Duration
	Logger      *slog.Logger
}

func (f *Flusher) defaults() {
	if f.Clock == nil {
		f.Clock = realClock{}
	}
	if f.Jitter == nil {
		f.Jitter = func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(max)))
		}
	}
	if f.Logger == nil {
		f.Logger = slog.Default()
	}
}

func (f *Flusher) Run(ctx context.Context) error {
	f.defaults()
	for {
		processed, err := f.FlushOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			f.Logger.Warn("deliver cloud notification", "error", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.Clock.After(flushIdleInterval):
		}
	}
}

func (f *Flusher) FlushOne(ctx context.Context) (bool, error) {
	f.defaults()
	if f.Outbox == nil || f.Deliver == nil || f.WorkerEpoch <= 0 {
		return false, errors.New("notification flusher is not configured")
	}
	events, err := f.Outbox.Ready(ctx, f.Clock.Now(), 1)
	if err != nil || len(events) == 0 {
		return false, err
	}
	event := events[0]
	if event.WorkerEpoch != f.WorkerEpoch {
		return true, f.Outbox.Delete(ctx, event.EventID, event.WorkerEpoch)
	}
	if err := f.Deliver(ctx, event); err != nil {
		attempt := event.AttemptCount + 1
		delay := time.Second << min(attempt-1, 5)
		if delay > maxRetryBackoff {
			delay = maxRetryBackoff
		}
		next := f.Clock.Now().Add(delay + f.Jitter(delay/4))
		if markErr := f.Outbox.MarkRetry(ctx, event.EventID, event.WorkerEpoch, attempt, next); markErr != nil {
			return true, errors.Join(err, markErr)
		}
		return true, err
	}
	return true, f.Outbox.Delete(ctx, event.EventID, event.WorkerEpoch)
}
