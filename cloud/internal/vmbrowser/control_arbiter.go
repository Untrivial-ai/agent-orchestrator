package vmbrowser

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	defaultUserControlLease = 1500 * time.Millisecond
	defaultAgentControlWait = 3 * time.Second
)

var ErrUserControlActive = errors.New("browser user control is active")

type ControlOwner string

const (
	ControlIdle  ControlOwner = "idle"
	ControlAgent ControlOwner = "agent"
	ControlUser  ControlOwner = "user"
)

type ControlArbiter struct {
	mu        sync.Mutex
	owner     ControlOwner
	userUntil time.Time
	lease     time.Duration
	agentWait time.Duration
	onChange  func(ControlOwner)
}

func NewControlArbiter(onChange func(ControlOwner)) *ControlArbiter {
	return &ControlArbiter{
		owner: ControlIdle, lease: defaultUserControlLease,
		agentWait: defaultAgentControlWait, onChange: onChange,
	}
}

func (a *ControlArbiter) Owner() ControlOwner {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ownerLocked(time.Now())
}

// TryUser accepts one viewer event. Active input renews the user lease;
// passive pointer movement is accepted without claiming ownership.
func (a *ControlArbiter) TryUser(active bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	owner := a.ownerLocked(time.Now())
	if owner == ControlAgent {
		return false
	}
	if active {
		a.userUntil = time.Now().Add(a.lease)
		a.setOwnerLocked(ControlUser)
	}
	return true
}

func (a *ControlArbiter) ReleaseUser() {
	a.mu.Lock()
	a.userUntil = time.Time{}
	if a.owner == ControlUser {
		a.setOwnerLocked(ControlIdle)
	}
	a.mu.Unlock()
}

func (a *ControlArbiter) AcquireAgent(ctx context.Context) (func(), error) {
	deadline := time.Now().Add(a.agentWait)
	for {
		a.mu.Lock()
		owner := a.ownerLocked(time.Now())
		if owner == ControlIdle {
			a.setOwnerLocked(ControlAgent)
			a.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					a.mu.Lock()
					if a.owner == ControlAgent {
						a.setOwnerLocked(ControlIdle)
					}
					a.mu.Unlock()
				})
			}, nil
		}
		a.mu.Unlock()
		if time.Now().After(deadline) {
			return nil, ErrUserControlActive
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (a *ControlArbiter) Reset() {
	a.mu.Lock()
	a.userUntil = time.Time{}
	a.setOwnerLocked(ControlIdle)
	a.mu.Unlock()
}

func (a *ControlArbiter) ownerLocked(now time.Time) ControlOwner {
	if a.owner == ControlUser && !a.userUntil.After(now) {
		a.userUntil = time.Time{}
		a.setOwnerLocked(ControlIdle)
	}
	return a.owner
}

func (a *ControlArbiter) setOwnerLocked(owner ControlOwner) {
	if a.owner == owner {
		return
	}
	a.owner = owner
	if a.onChange != nil {
		a.onChange(owner)
	}
}
