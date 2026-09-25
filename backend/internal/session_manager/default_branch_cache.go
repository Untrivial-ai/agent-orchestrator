package sessionmanager

import (
	"sync"
	"time"
)

// defaultBranchCacheTTL is how long a project's locally resolved base refs may
// be reused across imports.
//
// Importing a history registers many sessions into the same few projects, and
// each one was resolving that project's default branch again. Only the import
// path shares the answer: it resolves locally and never fetches, so a reused
// ref is exactly what the next import would have computed. An ordinary spawn
// fetches from the remote and bypasses this entirely, because basing a fresh
// worktree on a minute-old origin is a real regression.
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
