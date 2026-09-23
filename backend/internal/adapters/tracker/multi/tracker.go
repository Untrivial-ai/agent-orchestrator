// Package multi provides a composite tracker that dispatches ports.Tracker
// calls to per-provider sub-trackers based on TrackerID.Provider (for Get)
// or TrackerRepo.Provider (for List). This mirrors the multi.Provider
// dispatch pattern used for SCM observation, allowing the session service to
// resolve issues from both GitHub and GitLab through a single ports.Tracker.
package multi

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ErrUnknownProvider is returned when no sub-tracker is registered for the
// requested provider. It is wrapped via %w so callers can use errors.Is.
var ErrUnknownProvider = errors.New("tracker multi: unknown provider")

// NamedTracker pairs a routing key with a tracker. The Key must match the
// domain.TrackerProvider string that the tracker owns (e.g. "github",
// "gitlab").
type NamedTracker struct {
	Key     string
	Tracker ports.Tracker
}

// Tracker routes Get and List calls to the correct sub-tracker based on
// TrackerID.Provider / TrackerRepo.Provider. Preflight is called against
// every registered sub-tracker; a failure from one does not suppress the
// others — the first error is returned but all trackers are still probed.
type Tracker struct {
	byName map[string]ports.Tracker
}

// New creates a Tracker from one or more named sub-trackers.
func New(trackers ...NamedTracker) *Tracker {
	m := &Tracker{
		byName: make(map[string]ports.Tracker, len(trackers)),
	}
	for _, nt := range trackers {
		m.byName[nt.Key] = nt.Tracker
	}
	return m
}

func (m *Tracker) resolve(key string) (ports.Tracker, error) {
	if t, ok := m.byName[key]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("%w: provider=%q", ErrUnknownProvider, key)
}

// Get delegates to the sub-tracker matching id.Provider.
func (m *Tracker) Get(ctx context.Context, id domain.TrackerID) (domain.Issue, error) {
	t, err := m.resolve(string(id.Provider))
	if err != nil {
		return domain.Issue{}, err
	}
	return t.Get(ctx, id)
}

// List delegates to the sub-tracker matching repo.Provider.
func (m *Tracker) List(ctx context.Context, repo domain.TrackerRepo, filter domain.ListFilter) ([]domain.Issue, error) {
	t, err := m.resolve(string(repo.Provider))
	if err != nil {
		return nil, err
	}
	return t.List(ctx, repo, filter)
}

// Preflight probes every registered sub-tracker. The first error is
// returned, but all trackers are probed so a transient failure from one
// does not mask a successful verification from another. When no
// sub-trackers are registered, Preflight returns nil (the daemon's
// degrade-gracefully pattern handles the nil-tracker case at the wiring
// layer).
func (m *Tracker) Preflight(ctx context.Context) error {
	var firstErr error
	for _, t := range m.byName {
		if err := t.Preflight(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Statically assert Tracker satisfies the port.
var _ ports.Tracker = (*Tracker)(nil)
