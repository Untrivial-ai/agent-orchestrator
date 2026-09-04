package sessionimport

import (
	"context"
	"sync"
	"time"
)

// snapshotTTL is how long a metadata scan may be reused to resolve ids.
//
// It exists for one shape of work: importing many conversations at once, where
// the caller resolves a hundred ids back to back and the transcripts on disk
// cannot meaningfully change between the first and the last. Outside that burst
// it expires quickly enough that a conversation created moments ago is still
// found, because a miss falls through to a real scan.
const snapshotTTL = 30 * time.Second

// snapshotCache holds the most recent metadata scan per provider.
//
// Only metadata scans are cached. The browse list computes message counts and
// import verdicts from a full read and is always scanned fresh, so nothing a
// user looks at is ever served stale.
type snapshotCache struct {
	mu      sync.Mutex
	entries map[string]snapshotEntry
	now     func() time.Time
}

type snapshotEntry struct {
	at       time.Time
	sessions []ImportableSession
}

func newSnapshotCache() *snapshotCache {
	return &snapshotCache{entries: map[string]snapshotEntry{}, now: time.Now}
}

// lookup returns a provider's cached scan when it is still fresh.
func (c *snapshotCache) lookup(provider string) ([]ImportableSession, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[provider]
	if !ok || c.now().Sub(entry.at) > snapshotTTL {
		return nil, false
	}
	return entry.sessions, true
}

func (c *snapshotCache) store(provider string, sessions []ImportableSession) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[provider] = snapshotEntry{at: c.now(), sessions: sessions}
}

// scanForLocate returns the provider's metadata scan, reusing a fresh one when
// a burst of imports has already paid for it.
func (s *Service) scanForLocate(ctx context.Context, src Source, opts DiscoverOptions) ([]ImportableSession, error) {
	key := string(src.Provider())
	if cached, ok := s.snapshots.lookup(key); ok {
		return cached, nil
	}
	found, err := src.Discover(ctx, opts)
	if err != nil {
		return nil, err
	}
	s.snapshots.store(key, found)
	return found, nil
}
