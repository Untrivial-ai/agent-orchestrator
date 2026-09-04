package sessionmanager

import (
	"sync"
	"time"
)

// defaultBranchCacheTTL is how long a project's resolved base refs may be
// reused across spawns.
//
// It exists for bulk work. Importing a history spawns many sessions into the
// same few projects, and each spawn was resolving and fetching that project's
// default branch again. Where a repository has no usable remote those calls do
// not fail fast, they wait: measured on a real import, nine of thirty-one
// imports took over ten seconds each and accounted for 87% of the total time,
// every one of them sitting on this refresh.
//
// A repository's default branch does not meaningfully change inside such a
// window, and the refresh is already best-effort: it falls back to local refs
// on failure, so reusing a recent answer is no weaker than the failure the
// caller already tolerates.
const defaultBranchCacheTTL = 60 * time.Second

type defaultBranchCache struct {
	mu      sync.Mutex
	entries map[string]defaultBranchEntry
	now     func() time.Time
}

type defaultBranchEntry struct {
	at       time.Time
	baseRefs map[string]string
}

func newDefaultBranchCache() *defaultBranchCache {
	return &defaultBranchCache{entries: map[string]defaultBranchEntry{}, now: time.Now}
}

// lookup returns a project's recently resolved base refs, if any.
//
// The stored map is copied out. Callers index and mutate what they receive, and
// handing back the cached map itself would let one spawn corrupt the next.
func (c *defaultBranchCache) lookup(id string) (map[string]string, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[id]
	if !ok || c.now().Sub(entry.at) > defaultBranchCacheTTL {
		return nil, false
	}
	return copyBaseRefs(entry.baseRefs), true
}

func (c *defaultBranchCache) store(id string, baseRefs map[string]string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = defaultBranchEntry{at: c.now(), baseRefs: copyBaseRefs(baseRefs)}
}

func copyBaseRefs(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
