// Package automations owns the cadence for durable automation scheduling.
// Recurrence, claiming, and dispatch decisions remain in the service.
package automations

import (
	"context"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/observe"
)

// DefaultTickInterval is the scheduler polling cadence used when Config.Tick is unset.
const DefaultTickInterval = 15 * time.Second

// Poller advances durable automation scheduling at a point in time.
type Poller interface {
	Tick(context.Context, time.Time) error
}

// Config controls the automation observer's cadence, clock, and logging.
type Config struct {
	Tick   time.Duration
	Clock  func() time.Time
	Logger *slog.Logger
}

// Observer periodically asks the automation scheduler to process due work.
type Observer struct {
	poller Poller
	tick   time.Duration
	clock  func() time.Time
	logger *slog.Logger
}

// New constructs an automation observer with defaults for omitted configuration.
func New(poller Poller, cfg Config) *Observer {
	o := &Observer{poller: poller, tick: cfg.Tick, clock: cfg.Clock, logger: cfg.Logger}
	if o.tick <= 0 {
		o.tick = DefaultTickInterval
	}
	if o.clock == nil {
		o.clock = time.Now
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return o
}

// Start runs the polling loop until ctx is canceled and returns its completion channel.
func (o *Observer) Start(ctx context.Context) <-chan struct{} {
	return observe.StartPollLoop(ctx, o.tick, o.Poll, o.logger, "automations")
}

// Poll processes automation work due at the observer's current time.
func (o *Observer) Poll(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.poller == nil {
		return nil
	}
	return o.poller.Tick(ctx, o.clock().UTC())
}
