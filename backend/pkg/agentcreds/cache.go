package agentcreds

import (
	"sync"
	"time"
)

// The verdict cache.
//
// A full ladder pass can cost six seconds in the worst case. That is
// unacceptable in a UI and unnecessary in a daemon, so validation never runs
// on the render path: it runs asynchronously and writes here, and every reader
// — the Agents page, chat preflight — reads the cache and never waits on it. A
// cold cache reads as Unknown, which renders as nothing at all, rather than as
// a spinner.
//
// Entries are keyed on the credential fingerprint rather than on time alone.
// A TTL answers "is this stale?"; a fingerprint answers "is this even the same
// credential?" — which is the question that matters when a user swaps a
// revoked key for a working one and expects the UI to notice.

// CacheEntry is a stored verdict.
type CacheEntry struct {
	Result      Result
	Fingerprint string
	StoredAt    time.Time
}

// Cache holds the most recent verdict per agent. It is safe for concurrent
// use.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]CacheEntry
	ttl     time.Duration
	now     func() time.Time
}

// DefaultCacheTTL bounds how long a verdict is trusted without rechecking.
// Credentials are revoked out of band, so even a fresh-looking verdict is only
// ever a recent observation.
const DefaultCacheTTL = 5 * time.Minute

// NewCache builds a cache. A non-positive ttl uses DefaultCacheTTL.
func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{entries: make(map[string]CacheEntry), ttl: ttl, now: time.Now}
}

// Get returns a cached verdict when one is present, fresh, and was produced
// from the same credential that is in play now.
//
// A fingerprint mismatch is a miss, not a stale hit: the previous verdict
// describes a credential the agent will no longer send, so it is not evidence
// about anything. Passing an empty fingerprint means "I could not resolve a
// credential", which cannot match a stored one and so always misses.
func (c *Cache) Get(agentID, fingerprint string) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	c.mu.RLock()
	entry, ok := c.entries[agentID]
	c.mu.RUnlock()
	if !ok || entry.Fingerprint == "" || entry.Fingerprint != fingerprint {
		return Result{}, false
	}
	if c.now().Sub(entry.StoredAt) > c.ttl {
		return Result{}, false
	}
	return entry.Result, true
}

// Put stores a verdict.
//
// Unknown verdicts are deliberately not stored. An unknown is the absence of
// information, and caching it would suppress the retry that might actually
// answer the question — turning one bad network moment into five minutes of
// silence.
func (c *Cache) Put(agentID string, result Result) {
	if c == nil || result.State == StateUnknown {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[agentID] = CacheEntry{
		Result: result, Fingerprint: result.Fingerprint, StoredAt: c.now(),
	}
}

// Invalidate drops an agent's verdict. It is called on any runtime 401: the
// provider has just contradicted whatever is stored, and the stored verdict
// must not outlive that.
func (c *Cache) Invalidate(agentID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, agentID)
}

// Peek returns the stored entry regardless of freshness or fingerprint, for
// diagnostics. It must not be used to make a readiness decision.
func (c *Cache) Peek(agentID string) (CacheEntry, bool) {
	if c == nil {
		return CacheEntry{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[agentID]
	return entry, ok
}
