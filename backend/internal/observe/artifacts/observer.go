// Package artifacts polls sessions for artifact files written outside the SCM
// observation path (there is no filesystem watcher on a session's artifact
// directory) and reconciles each one's persisted OutputType accordingly.
package artifacts

import (
	"context"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe"
)

// DefaultTickInterval is the artifact reconciliation cadence.
const DefaultTickInterval = 30 * time.Second

// Config controls artifact-output reconciliation polling.
type Config struct {
	Tick   time.Duration
	Logger *slog.Logger
}

type sessionSource interface {
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
}

type outputTypeSink interface {
	ReconcileSessionOutputType(ctx context.Context, id domain.SessionID) error
}

// Observer periodically rescans each live session's artifact directory and
// persists a resulting OutputType change through the sink.
type Observer struct {
	sessions sessionSource
	sink     outputTypeSink
	tick     time.Duration
	logger   *slog.Logger
}

// New builds an artifact-output observer.
func New(sessions sessionSource, sink outputTypeSink, cfg Config) *Observer {
	o := &Observer{
		sessions: sessions,
		sink:     sink,
		tick:     cfg.Tick,
		logger:   cfg.Logger,
	}
	if o.tick <= 0 {
		o.tick = DefaultTickInterval
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return o
}

// Start runs polling until the context is canceled.
func (o *Observer) Start(ctx context.Context) <-chan struct{} {
	return observe.StartPollLoop(ctx, o.tick, o.Poll, o.logger, "artifact output observer")
}

// Poll rescans every non-terminated session not already classified as PR
// output — PR output never reverts, so it is skipped — and reconciles the
// rest so a session whose agent writes an artifact file gets its OutputType
// (and therefore its Kanban column) updated without a PR ever landing.
func (o *Observer) Poll(ctx context.Context) error {
	sessions, err := o.sessions.ListAllSessions(ctx)
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		if sess.IsTerminated || sess.OutputType == domain.SessionOutputPR {
			continue
		}
		if err := o.sink.ReconcileSessionOutputType(ctx, sess.ID); err != nil {
			o.logger.Debug("artifact output observer: reconcile failed", "session", sess.ID, "err", err)
		}
	}
	return nil
}
