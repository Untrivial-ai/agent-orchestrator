// Package idlepause pauses sandboxes for sessions the user has gone quiet on.
package idlepause

import (
	"context"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

// Store is the durable state the scanner reads and pauses.
type Store interface {
	// RunningSandboxSessions lists every session whose sandbox is fully up,
	// across every organization.
	RunningSandboxSessions(ctx context.Context) ([]domain.SandboxRef, error)
	// PauseIfIdle pauses one session's sandbox if it is still idle by the time
	// the check runs, and reports whether it did.
	PauseIfIdle(ctx context.Context, orgID, sessionID string, idleThreshold time.Duration) (bool, error)
}

// Options configures a Scanner. Zero values fall back to the defaults below.
type Options struct {
	// Interval is how often the scanner looks for idle sandboxes.
	Interval time.Duration
	// IdleThreshold is how long a session must have had no user message, with
	// no turn in flight, before its sandbox is paused.
	IdleThreshold time.Duration
	// Logger receives lifecycle events.
	Logger *slog.Logger
}

const (
	DefaultInterval      = 30 * time.Second
	DefaultIdleThreshold = time.Hour
)

// Scanner periodically pauses sandboxes whose session has gone quiet.
type Scanner struct {
	store   Store
	options Options
	log     *slog.Logger
}

// New creates an idle-pause scanner.
func New(store Store, options Options) *Scanner {
	if options.Interval <= 0 {
		options.Interval = DefaultInterval
	}
	if options.IdleThreshold <= 0 {
		options.IdleThreshold = DefaultIdleThreshold
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Scanner{store: store, options: options, log: options.Logger}
}

// Run scans on Options.Interval until ctx is canceled.
func (s *Scanner) Run(ctx context.Context) error {
	if err := s.ScanOnce(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("idle-pause scan failed", "err", err)
	}
	ticker := time.NewTicker(s.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.ScanOnce(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("idle-pause scan failed", "err", err)
			}
		}
	}
}

// ScanOnce pauses every currently idle running sandbox once.
func (s *Scanner) ScanOnce(ctx context.Context) error {
	refs, err := s.store.RunningSandboxSessions(ctx)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		paused, err := s.store.PauseIfIdle(ctx, ref.OrgID, ref.SessionID, s.options.IdleThreshold)
		if err != nil {
			s.log.Error("idle-pause check failed",
				"session_id", ref.SessionID, "org_id", ref.OrgID, "err", err)
			continue
		}
		if paused {
			s.log.Info("paused idle sandbox",
				"session_id", ref.SessionID, "org_id", ref.OrgID,
				"idle_threshold", s.options.IdleThreshold)
		}
	}
	return nil
}
