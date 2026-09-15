package claudecode

import (
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentcreds"
)

const defaultAuthCacheTTL = 5 * time.Minute

type authCacheEntry struct {
	result   agentcreds.Result
	storedAt time.Time
}

type authCache struct {
	mu    sync.RWMutex
	entry *authCacheEntry
	ttl   time.Duration
	now   func() time.Time
}

func newAuthCache(ttl time.Duration) *authCache {
	if ttl <= 0 {
		ttl = defaultAuthCacheTTL
	}
	return &authCache{ttl: ttl, now: time.Now}
}

func (c *authCache) get(fingerprint string) (agentcreds.Result, bool) {
	if c == nil || fingerprint == "" {
		return agentcreds.Result{}, false
	}
	c.mu.RLock()
	entry := c.entry
	c.mu.RUnlock()
	if entry == nil || entry.result.Fingerprint != fingerprint || c.now().Sub(entry.storedAt) > c.ttl {
		return agentcreds.Result{}, false
	}
	return entry.result, true
}

func (c *authCache) put(result agentcreds.Result) {
	if c == nil || result.State == agentcreds.StateUnknown || result.Fingerprint == "" {
		return
	}
	c.mu.Lock()
	c.entry = &authCacheEntry{result: result, storedAt: c.now()}
	c.mu.Unlock()
}

func (c *authCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entry = nil
	c.mu.Unlock()
}
