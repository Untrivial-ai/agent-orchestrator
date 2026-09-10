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
	mu        sync.Mutex
	shared    int
	exclusive bool
	drained   chan struct{}
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
	if g.shared == 0 {
		g.drained = make(chan struct{})
	}
	g.shared++
	g.mu.Unlock()

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
	}, nil
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
	drained := g.drained
	g.mu.Unlock()

	select {
	case <-drained:
		return &exclusiveLease{gate: g}, nil
	case <-ctx.Done():
		g.mu.Lock()
		g.exclusive = false
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
		l.gate.mu.Unlock()
	})
}

var _ ports.CodexOperationGate = (*Gate)(nil)
