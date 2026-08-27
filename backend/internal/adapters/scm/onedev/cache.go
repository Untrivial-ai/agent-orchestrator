package onedev

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// userCacheTTL is the freshness window for a resolved user login. OneDev
	// exposes authors as numeric ids, so every listing would otherwise cost
	// one /users/{id} round-trip per author. Logins change rarely (an account
	// rename), so an hour of staleness is cheap insurance against re-fetching
	// the same handful of ids on every poll.
	userCacheTTL = time.Hour

	// projectCacheTTL is the freshness window for a project id's path. Paths
	// change only when a project is renamed or moved in the project tree, so
	// the same reasoning as userCacheTTL applies.
	projectCacheTTL = time.Hour

	// cacheMaxEntries bounds each cache map. OneDev PR numbers are never
	// reused, so requestIDs would otherwise grow without limit across a
	// long-lived daemon. On overflow the map is emptied wholesale rather than
	// evicted entry-by-entry: the cost of a miss is one cheap query, and a
	// simple reset keeps the cache free of the bookkeeping an LRU would need.
	cacheMaxEntries = 4096
)

// cacheEntry stores a value alongside the instant it was fetched. Lookups
// compare fetchedAt against the domain's TTL; an expired entry is a miss and
// is left in place until overwritten.
type cacheEntry[T any] struct {
	value     T
	fetchedAt time.Time
}

// cache memoizes the three id-to-name lookups the observer methods would
// otherwise repeat on every poll: a PR number's internal request id, a user
// id's login, and a project id's path.
//
// It is internal to this adapter and is not part of the scm.Provider port.
type cache struct {
	mu sync.Mutex

	// now supplies the current time. Defaults to time.Now; tests override it
	// so TTL expiry can be asserted without sleeping.
	now func() time.Time

	// requestIDs maps "host|project|number" to the internal pull-request id
	// OneDev's /pulls/{requestId} routes are keyed by. The mapping is
	// immutable — OneDev never reassigns a PR number within a project — so
	// entries carry no TTL.
	requestIDs map[string]int64

	// users maps "host|userId" to the account's login name.
	users map[string]cacheEntry[string]

	// projects maps "host|projectId" to the project's full path.
	projects map[string]cacheEntry[string]
}

// newCache returns an initialized cache ready for use.
func newCache() *cache {
	return &cache{
		now:        time.Now,
		requestIDs: map[string]int64{},
		users:      map[string]cacheEntry[string]{},
		projects:   map[string]cacheEntry[string]{},
	}
}

func cacheKey(parts ...string) string { return strings.Join(parts, "|") }

func (c *cache) getRequestID(host, project string, number int) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.requestIDs[cacheKey(host, project, strconv.Itoa(number))]
	return id, ok
}

func (c *cache) setRequestID(host, project string, number int, id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requestIDs) >= cacheMaxEntries {
		c.requestIDs = map[string]int64{}
	}
	c.requestIDs[cacheKey(host, project, strconv.Itoa(number))] = id
}

func (c *cache) getUser(host string, userID int64) (string, bool) {
	return getTTL(c, c.users, cacheKey(host, strconv.FormatInt(userID, 10)), userCacheTTL)
}

func (c *cache) setUser(host string, userID int64, login string) {
	setTTL(c, c.users, cacheKey(host, strconv.FormatInt(userID, 10)), login)
}

func (c *cache) getProjectPath(host string, projectID int64) (string, bool) {
	return getTTL(c, c.projects, cacheKey(host, strconv.FormatInt(projectID, 10)), projectCacheTTL)
}

func (c *cache) setProjectPath(host string, projectID int64, path string) {
	setTTL(c, c.projects, cacheKey(host, strconv.FormatInt(projectID, 10)), path)
}

func getTTL[T any](c *cache, m map[string]cacheEntry[T], key string, ttl time.Duration) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := m[key]
	if !ok || c.now().Sub(entry.fetchedAt) > ttl {
		var zero T
		return zero, false
	}
	return entry.value, true
}

func setTTL[T any](c *cache, m map[string]cacheEntry[T], key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(m) >= cacheMaxEntries {
		clear(m)
	}
	m[key] = cacheEntry[T]{value: value, fetchedAt: c.now()}
}
