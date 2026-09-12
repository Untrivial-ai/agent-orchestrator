package session

import (
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// workspaceCacheTTL bridges the short window between a session's workspace
// file list loading and the user immediately expanding a file, so the
// expansion doesn't redundantly re-resolve the compare base and re-run the
// same git status/diff/numstat calls the list request just made. It is not
// the primary correctness mechanism for freshness — the filesystem watcher
// that powers the workspace SSE stream invalidates entries the instant a
// real change is observed (see Service.InvalidateWorkspaceCache). The TTL
// only matters as a backstop for the brief window before that watcher
// connects.
const workspaceCacheTTL = 3 * time.Second

type workspaceCacheKey struct {
	session domain.SessionID
	root    string
}

// String gives workspaceCacheKey a stable identity for use as a
// singleflight.Group key. NUL can't appear in a session ID or filesystem
// path, so it's a safe separator against collisions between the two fields.
func (k workspaceCacheKey) String() string {
	return string(k.session) + "\x00" + k.root
}

type workspaceCacheEntry struct {
	at      time.Time
	compare workspaceCompareTarget
	changes workspaceChangeSet
}

type workspaceCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[workspaceCacheKey]workspaceCacheEntry
	loads   map[workspaceCacheKey]*workspaceCacheEntry
}

func newWorkspaceCache(ttl time.Duration, now func() time.Time) *workspaceCache {
	if now == nil {
		now = time.Now
	}
	return &workspaceCache{
		ttl: ttl, now: now,
		entries: make(map[workspaceCacheKey]workspaceCacheEntry),
		loads:   make(map[workspaceCacheKey]*workspaceCacheEntry),
	}
}

// Cache methods are nil-receiver-safe: a *Service built via
// a bare struct literal (common in this package's tests) leaves
// workspaceCache nil, and a nil cache should behave as permanently empty
// rather than panicking or requiring every construction site to remember
// initialization — the same nil-safe idiom Go gives nil maps and slices.
func (c *workspaceCache) get(key workspaceCacheKey) (workspaceCacheEntry, bool) {
	if c == nil {
		return workspaceCacheEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return workspaceCacheEntry{}, false
	}
	if c.now().Sub(entry.at) > c.ttl {
		delete(c.entries, key)
		return workspaceCacheEntry{}, false
	}
	return entry, true
}

// beginLoad reserves publication before Git work starts. The entry's identity
// is the token, so invalidation needs no retained per-session generation map.
func (c *workspaceCache) beginLoad(key workspaceCacheKey) *workspaceCacheEntry {
	entry := new(workspaceCacheEntry)
	if c == nil {
		return entry
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads[key] = entry
	return entry
}

// finishLoad releases this load's reservation and optionally publishes it.
// An invalidated or superseded load must neither publish nor remove a newer
// load's reservation. The caller defers a non-publishing finish for error paths.
func (c *workspaceCache) finishLoad(key workspaceCacheKey, entry *workspaceCacheEntry, publish bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loads[key] != entry {
		return
	}
	delete(c.loads, key)
	if publish {
		c.entries[key] = *entry
	}
}

// invalidateSession purges every cached entry for a session, across every
// worktree root it spans (workspace projects can span several repos), and
// prevents loads already in progress from repopulating the cache.
func (c *workspaceCache) invalidateSession(id domain.SessionID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if key.session == id {
			delete(c.entries, key)
		}
	}
	for key := range c.loads {
		if key.session == id {
			delete(c.loads, key)
		}
	}
}
