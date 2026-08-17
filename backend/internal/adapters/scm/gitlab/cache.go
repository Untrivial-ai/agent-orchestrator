package gitlab

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// TTL constants for the three provider-level cache domains. Package-level per
// the spec's decision to keep the tuning surface small — no env-var
// configuration. If a deployment needs different TTLs, that is a future code
// change. See .scratch/gitlab-api-request-optimization/spec.md.
const (
	// mrDetailCacheTTL is the freshness window for cached MR metadata. MR
	// detail is populated by ListPRsByRepo and consulted by fetchSingleMR to
	// skip the /merge_requests/:iid GET when a fresh entry exists. 60s
	// matches the ~30s observer poll interval — data is at most one cycle old.
	mrDetailCacheTTL = 60 * time.Second

	// approvalsCacheTTL is the freshness window for cached approvals data.
	// Both FetchPullRequests and FetchReviewThreads consult the approvals
	// cache to dedupe the /merge_requests/:iid/approvals GET within a cycle.
	approvalsCacheTTL = 60 * time.Second

	// forkProjectCacheTTL is the freshness window for a fork's source-project
	// path_with_namespace. Fork source-project paths are stable for days or
	// weeks in practice; a 5-min TTL serves repeat lookups across many cycles
	// without holding stale data for long.
	forkProjectCacheTTL = 5 * time.Minute
)

// cacheEntry stores a cached value alongside the instant it was fetched.
// Lookups compare fetchedAt against the domain's TTL to decide freshness;
// an expired entry is treated as a miss (lazy eviction) and left in the map
// until it is overwritten by a subsequent set.
type cacheEntry[T any] struct {
	value     T
	fetchedAt time.Time
}

// cache is a thread-safe in-memory TTL cache shared across three domains:
// MR detail, approvals, and fork-project path resolution. There is no LRU
// bounded eviction — the number of active MRs per provider is small (the
// observer batches at most 25 per FetchPullRequests call), so unbounded
// growth is not a concern.
//
// All three domains share a single sync.Mutex. This is sufficient because
// cache operations (map reads/writes under the lock) are fast relative to the
// HTTP round-trips they guard; the contention from fetchConcurrency=5 is low.
//
// The cache is internal to the GitLab adapter package and is not part of the
// scm.Provider port interface. Callers (tickets 03, 04, 05) consult it
// through the per-domain get/set helpers below.
type cache struct {
	mu sync.Mutex

	// now returns the current time. Defaults to time.Now; overridable in tests
	// so TTL-expiry assertions are fast and deterministic (no real sleeping).
	now func() time.Time

	// mrDetail caches full MR metadata keyed by "host|repo|iid". Populated by
	// ListPRsByRepo as it paginates; consulted by fetchSingleMR to skip the
	// /merge_requests/:iid GET when a fresh entry exists.
	mrDetail map[string]cacheEntry[*restMR]

	// approvals caches restApprovals keyed by "host|repo|iid". Consulted by
	// both FetchPullRequests and FetchReviewThreads to dedupe the
	// /merge_requests/:iid/approvals GET within a cycle.
	approvals map[string]cacheEntry[*restApprovals]

	// forkProject caches path_with_namespace keyed by "host|source_project_id".
	// Fork source-project paths are stable for days/weeks; the 5-min TTL
	// serves repeat lookups across many cycles.
	forkProject map[string]cacheEntry[string]
}

// newCache returns an initialized empty cache ready for use.
func newCache() *cache {
	return &cache{
		now:         time.Now,
		mrDetail:    map[string]cacheEntry[*restMR]{},
		approvals:   map[string]cacheEntry[*restApprovals]{},
		forkProject: map[string]cacheEntry[string]{},
	}
}

// --- MR detail ---

// getMRDetail returns a cached *restMR if a fresh entry exists for
// (host, repo, iid); otherwise (*restMR)(nil), false). An expired entry is
// treated as a miss (lazy eviction).
func (c *cache) getMRDetail(host, repo string, iid int) (*restMR, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return getCacheEntry(c.mrDetail, mrKey(host, repo, iid), mrDetailCacheTTL, c.now())
}

// setMRDetail stores mr in the MR-detail cache under (host, repo, iid),
// stamping it with the current time.
func (c *cache) setMRDetail(host, repo string, iid int, mr *restMR) {
	c.mu.Lock()
	defer c.mu.Unlock()
	setCacheEntry(c.mrDetail, mrKey(host, repo, iid), mr, c.now())
}

// --- Approvals ---

// getApprovals returns a cached *restApprovals if a fresh entry exists for
// (host, repo, iid); otherwise (nil, false). An expired entry is treated as
// a miss (lazy eviction).
func (c *cache) getApprovals(host, repo string, iid int) (*restApprovals, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return getCacheEntry(c.approvals, mrKey(host, repo, iid), approvalsCacheTTL, c.now())
}

// setApprovals stores a in the approvals cache under (host, repo, iid),
// stamping it with the current time.
func (c *cache) setApprovals(host, repo string, iid int, a *restApprovals) {
	c.mu.Lock()
	defer c.mu.Unlock()
	setCacheEntry(c.approvals, mrKey(host, repo, iid), a, c.now())
}

// --- Fork project ---

// getForkProject returns a cached path_with_namespace if a fresh entry exists
// for (host, sourceProjectID); otherwise ("", false). An expired entry is
// treated as a miss (lazy eviction).
func (c *cache) getForkProject(host string, sourceProjectID int) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return getCacheEntry(c.forkProject, forkProjectKey(host, sourceProjectID), forkProjectCacheTTL, c.now())
}

// setForkProject stores path in the fork-project cache under
// (host, sourceProjectID), stamping it with the current time.
func (c *cache) setForkProject(host string, sourceProjectID int, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	setCacheEntry(c.forkProject, forkProjectKey(host, sourceProjectID), path, c.now())
}

// --- Key builders ---
//
// Keys include host (and repo or source-project ID) so that the same MR on
// different hosts or repos does not collide. Host and repo are lowercased so
// that case variation in remotes (e.g. "GitLab.com" vs "gitlab.com") does not
// fragment the cache.

// mrKey builds the cache key for both the MR-detail and approvals domains.
// Both are keyed by the same (host, repo, iid) tuple per the spec; the domain
// distinction is carried by which map the caller passes to getCacheEntry/
// setCacheEntry, so a single key builder suffices.
func mrKey(host, repo string, iid int) string {
	return strings.ToLower(host) + "|" + strings.ToLower(repo) + "|" + fmt.Sprint(iid)
}

func forkProjectKey(host string, sourceProjectID int) string {
	return strings.ToLower(host) + "|" + fmt.Sprint(sourceProjectID)
}

// --- Generic helpers (shared lazy-eviction logic across all three domains) ---
//
// These are free functions rather than methods because Go methods cannot
// introduce type parameters beyond the receiver's. Each takes the map it
// operates on plus the current time, so the caller (which holds the mutex)
// passes c.now() explicitly. The caller MUST hold c.mu.

// getCacheEntry returns a cached value if a fresh entry exists for key;
// otherwise the zero value and false. An expired entry (fetchedAt older than
// ttl) is treated as a miss.
func getCacheEntry[T any](m map[string]cacheEntry[T], key string, ttl time.Duration, now time.Time) (T, bool) {
	e, ok := m[key]
	if !ok {
		var zero T
		return zero, false
	}
	if now.Sub(e.fetchedAt) > ttl {
		var zero T
		return zero, false
	}
	return e.value, true
}

// setCacheEntry stores value under key with fetchedAt set to now, overwriting
// any existing (possibly expired) entry.
func setCacheEntry[T any](m map[string]cacheEntry[T], key string, value T, now time.Time) {
	m[key] = cacheEntry[T]{value: value, fetchedAt: now}
}
