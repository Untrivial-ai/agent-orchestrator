package activity

import (
	"context"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Default activity observation settings.
const (
	DefaultTickInterval = 30 * time.Second
	DefaultStaleAfter   = 2 * time.Minute
	DefaultOutputLines  = 40
)

// Config controls activity reconciliation polling.
type Config struct {
	Tick        time.Duration
	StaleAfter  time.Duration
	OutputLines int
	Clock       func() time.Time
	Logger      *slog.Logger
}

type sessionSource interface {
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
}

type activitySink interface {
	ApplyActivitySignal(ctx context.Context, id domain.SessionID, signal ports.ActivitySignal) error
}

type outputReader interface {
	GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
}

// Observer reconciles stale hook activity from adapter-owned terminal markers.
type Observer struct {
	sessions    sessionSource
	sink        activitySink
	runtime     outputReader
	agents      ports.AgentResolver
	tick        time.Duration
	staleAfter  time.Duration
	outputLines int
	clock       func() time.Time
	logger      *slog.Logger
}

// New builds an activity observer.
func New(sessions sessionSource, sink activitySink, runtime outputReader, agents ports.AgentResolver, cfg Config) *Observer {
	o := &Observer{
		sessions:    sessions,
		sink:        sink,
		runtime:     runtime,
		agents:      agents,
		tick:        cfg.Tick,
		staleAfter:  cfg.StaleAfter,
		outputLines: cfg.OutputLines,
		clock:       cfg.Clock,
		logger:      cfg.Logger,
	}
	if o.tick <= 0 {
		o.tick = DefaultTickInterval
	}
	if o.staleAfter <= 0 {
		o.staleAfter = DefaultStaleAfter
	}
	if o.outputLines <= 0 {
		o.outputLines = DefaultOutputLines
	}
	if o.clock == nil {
		// UTC, not bare time.Now: this timestamp becomes sessions.activity_last_at,
		// and the SQLite driver stores a time.Time by its String() form. A
		// local-zone value keeps its monotonic reading and offset, which then
		// fails to compare as a timestamp against the "… +0000 UTC" rows the
		// other writers produce.
		o.clock = func() time.Time { return time.Now().UTC() }
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return o
}

// Start runs polling until the context is canceled.
func (o *Observer) Start(ctx context.Context) <-chan struct{} {
	return observe.StartPollLoop(ctx, o.tick, o.Poll, o.logger, "activity observer")
}

// Poll performs one activity reconciliation pass.
func (o *Observer) Poll(ctx context.Context) error {
	sessions, err := o.sessions.ListAllSessions(ctx)
	if err != nil {
		return err
	}
	now := o.clock()
	for _, session := range sessions {
		o.reconcile(ctx, session, now)
	}
	return nil
}

func (o *Observer) reconcile(ctx context.Context, session domain.SessionRecord, now time.Time) {
	if session.IsTerminated || session.Metadata.RuntimeHandleID == "" || o.agents == nil {
		return
	}
	agent, ok := o.agents.Agent(session.Harness)
	if !ok {
		return
	}
	detector, ok := agent.(ports.TerminalActivityDetector)
	if !ok {
		return
	}
	continuous, ok := agent.(ports.ContinuousTerminalActivityDetector)
	if !ok || !continuous.ContinuouslyDetectTerminalActivity() {
		if session.Activity.State == domain.ActivityWaitingInput {
			waitingDetector, canRecoverWaiting := agent.(ports.WaitingTerminalActivityDetector)
			if !canRecoverWaiting || !waitingDetector.ContinuouslyDetectTerminalActivityWhileWaiting() {
				return
			}
		} else if session.Activity.State != domain.ActivityActive || session.Activity.LastActivityAt.IsZero() ||
			now.Sub(session.Activity.LastActivityAt) < o.staleAfter {
			return
		}
	} else if session.Activity.State != domain.ActivityActive &&
		session.Activity.State != domain.ActivityIdle &&
		session.Activity.State != domain.ActivityWaitingInput {
		return
	}
	output, err := o.runtime.GetOutput(ctx, ports.RuntimeHandle{ID: session.Metadata.RuntimeHandleID}, o.outputLines)
	if err != nil {
		o.logger.Debug("activity observer: terminal output unavailable", "session", session.ID, "err", err)
		return
	}
	state, ok := detector.DetectTerminalActivity(output)
	if !ok || state == session.Activity.State ||
		(state != domain.ActivityActive && state != domain.ActivityIdle && state != domain.ActivityWaitingInput) {
		return
	}
	event := "terminal-" + string(state)
	if state == domain.ActivityWaitingInput {
		event = "terminal-waiting-input"
	}
	err = o.sink.ApplyActivitySignal(ctx, session.ID, ports.ActivitySignal{
		Valid:             true,
		State:             state,
		Timestamp:         now,
		ExpectedUpdatedAt: session.UpdatedAt,
		Event:             event,
		LaunchID:          session.Metadata.RuntimeLaunchID,
	})
	if err != nil {
		o.logger.Error("activity observer: reconciliation failed", "session", session.ID, "err", err)
	}
}
