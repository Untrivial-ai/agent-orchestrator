package sessionmanager

import (
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// keyedMutex serialises operations per session id. It mirrors the house idiom
// (review.go:lockWorker / service.go:lockOrchestratorProject: an outer mutex
// guarding a map of per-key mutexes) but adds refcounted eviction: the finalizer
// runs for every terminated session, so a map that never deletes entries would
// grow unbounded by total sessions ever seen. Each entry is dropped when its
// last holder unlocks.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[domain.SessionID]*refLock
}

type refLock struct {
	mu   sync.Mutex
	refs int
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: map[domain.SessionID]*refLock{}}
}

// lock acquires the per-id mutex and returns an unlock func. The mutex is
// non-reentrant, so a holder must never call lock for the same id again before
// unlocking (the finalizer must therefore never run synchronously inside the CDC
// callback, which is why the reconciler enqueues-and-returns instead).
func (k *keyedMutex) lock(id domain.SessionID) func() {
	k.mu.Lock()
	rl, ok := k.locks[id]
	if !ok {
		rl = &refLock{}
		k.locks[id] = rl
	}
	rl.refs++
	k.mu.Unlock()

	rl.mu.Lock()
	return func() {
		rl.mu.Unlock()
		k.mu.Lock()
		rl.refs--
		if rl.refs == 0 {
			delete(k.locks, id)
		}
		k.mu.Unlock()
	}
}
