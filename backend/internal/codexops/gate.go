// Package codexops owns admission to operations that use or mutate the
// device-global Codex home.
package codexops

import (
	"context"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Gate admits concurrent readers until an exclusive request publishes intent.
// Publishing intent before draining existing readers prevents a late launch
// from registering after a switch has built its controller snapshot.
type Gate struct {
	mu            sync.Mutex
	shared        int
	exclusive     bool
	exclusiveDone chan struct{}
	drained       chan struct{}
}

// NewGate creates an idle device-global operation gate.
func NewGate() *Gate {
	drained := make(chan struct{})
	close(drained)
	return &Gate{drained: drained}
}

// AcquireShared admits an operation only when no exclusive intent or owner
// exists. The returned release is idempotent.
func (g *Gate) AcquireShared(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.exclusive {
		g.mu.Unlock()
		return nil, ports.ErrCodexAccountSwitchInProgress
	}
	release := g.acquireSharedLocked()
	g.mu.Unlock()
	return release, nil
}

// AcquireSharedWait admits an operation after an active exclusive owner
// releases the gate. Controller and reviewer launches use this path so a
// short-lived device reconciliation delays them instead of looking like an
// account switch failure.
func (g *Gate) AcquireSharedWait(ctx context.Context) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.mu.Lock()
		if !g.exclusive {
			release := g.acquireSharedLocked()
			g.mu.Unlock()
			return release, nil
		}
		done := g.exclusiveDone
		g.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// acquireSharedLocked registers one shared owner. The caller holds g.mu.
func (g *Gate) acquireSharedLocked() func() {
	if g.shared == 0 {
		g.drained = make(chan struct{})
	}
	g.shared++

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.shared--
			if g.shared == 0 {
				close(g.drained)
			}
			g.mu.Unlock()
		})
	}
}

// AcquireExclusive closes shared admission before waiting for already-admitted
// operations to finish registration or global-home use.
func (g *Gate) AcquireExclusive(ctx context.Context) (ports.CodexOperationLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.exclusive {
		g.mu.Unlock()
		return nil, ports.ErrCodexAccountSwitchInProgress
	}
	g.exclusive = true
	g.exclusiveDone = make(chan struct{})
	drained := g.drained
	g.mu.Unlock()

	select {
	case <-drained:
		return &exclusiveLease{gate: g}, nil
	case <-ctx.Done():
		g.mu.Lock()
		g.exclusive = false
		close(g.exclusiveDone)
		g.exclusiveDone = nil
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}

// ExclusivePendingOrHeld is a display and fast-rejection projection only.
func (g *Gate) ExclusivePendingOrHeld() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.exclusive
}

type exclusiveLease struct {
	gate *Gate
	once sync.Once
}

func (l *exclusiveLease) Release() {
	if l == nil || l.gate == nil {
		return
	}
	l.once.Do(func() {
		l.gate.mu.Lock()
		l.gate.exclusive = false
		close(l.gate.exclusiveDone)
		l.gate.exclusiveDone = nil
		l.gate.mu.Unlock()
	})
}

var _ ports.CodexOperationGate = (*Gate)(nil)
