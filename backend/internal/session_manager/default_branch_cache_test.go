package sessionmanager

import (
	"testing"
	"time"
)

// Importing a history spawns many sessions into the same few projects. Each
// spawn was resolving and fetching that project's default branch again, and
// where a repository has no usable remote those calls wait rather than fail
// fast, which is where a bulk import spent most of its time.
func TestDefaultBranchCacheReusesAProjectsRefs(t *testing.T) {
	c := newDefaultBranchCache()
	refs := map[string]string{"/repo": "origin/main"}
	c.store("proj", refs)

	got, ok := c.lookup("proj")
	if !ok {
		t.Fatal("a freshly stored project should be reused")
	}
	if got["/repo"] != "origin/main" {
		t.Errorf("wrong refs: %v", got)
	}
	if _, ok := c.lookup("other"); ok {
		t.Error("a different project must not share the answer")
	}
}

// Callers index and mutate what they receive, so handing back the cached map
// itself would let one spawn corrupt the next.
func TestDefaultBranchCacheHandsOutCopies(t *testing.T) {
	c := newDefaultBranchCache()
	c.store("proj", map[string]string{"/repo": "origin/main"})

	first, _ := c.lookup("proj")
	first["/repo"] = "tampered"
	first["/extra"] = "added"

	second, _ := c.lookup("proj")
	if second["/repo"] != "origin/main" {
		t.Errorf("a caller mutated the cache: %v", second)
	}
	if _, added := second["/extra"]; added {
		t.Error("a caller added a key to the cache")
	}
}

func TestDefaultBranchCacheExpires(t *testing.T) {
	c := newDefaultBranchCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	c.store("proj", map[string]string{"/repo": "origin/main"})

	now = now.Add(defaultBranchCacheTTL + time.Second)
	if _, ok := c.lookup("proj"); ok {
		t.Error("a stale answer must be refreshed rather than reused forever")
	}
}
