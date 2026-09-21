package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Break caught: shutdown cancellation could lose a race with an available
// writer token and admit new enrichment work after cancellation.
func TestContextMutexRejectsPreCanceledAdmissionWithoutLosingToken(t *testing.T) {
	mutex := newContextMutex()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mutex.LockContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("LockContext error = %v, want context canceled", err)
	}
	if err := mutex.LockContext(context.Background()); err != nil {
		t.Fatalf("LockContext after cancellation: %v", err)
	}
	mutex.Unlock()
}

// Break caught: a backfill waiting behind an ordinary store writer could keep
// daemon shutdown blocked until that unrelated write released the mutex.
func TestContextMutexCancelsWhileWaiting(t *testing.T) {
	mutex := newContextMutex()
	mutex.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	admission := make(chan struct{})
	observed := &lockAdmissionContext{Context: ctx, admission: admission}
	result := make(chan error, 1)
	go func() { result <- mutex.LockContext(observed) }()
	<-admission
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("LockContext error = %v, want context canceled", err)
	}
	mutex.Unlock()
}

type lockAdmissionContext struct {
	context.Context
	admission chan struct{}
	once      sync.Once
}

func (c *lockAdmissionContext) Err() error {
	c.once.Do(func() { close(c.admission) })
	return c.Context.Err()
}
