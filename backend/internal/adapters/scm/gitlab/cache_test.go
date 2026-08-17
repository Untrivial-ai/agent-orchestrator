package gitlab

import (
	"sync"
	"testing"
	"time"
)

// TestCache_SetGetWithinTTL verifies that entries written to each of the three
// cache domains (MR detail, approvals, fork project) are returned by a
// subsequent get while still within their TTL. It also checks key isolation —
// a different host, repo, or IID does not collide with an existing entry — and
// case-insensitive keying so that "GitLab.com" and "gitlab.com" do not
// fragment the cache.
func TestCache_SetGetWithinTTL(t *testing.T) {
	c := newCache()

	// --- MR detail ---
	mr := &restMR{IID: 42, Title: "test MR"}
	c.setMRDetail("gitlab.com", "org/repo", 42, mr)
	gotMR, ok := c.getMRDetail("gitlab.com", "org/repo", 42)
	if !ok {
		t.Fatal("expected MR detail cache hit within TTL")
	}
	if gotMR != mr {
		t.Errorf("getMRDetail returned %p, want %p (same pointer)", gotMR, mr)
	}

	// --- Approvals ---
	ap := &restApprovals{Approved: true, ApprovalsRequired: 1}
	c.setApprovals("gitlab.com", "org/repo", 42, ap)
	gotAp, ok := c.getApprovals("gitlab.com", "org/repo", 42)
	if !ok {
		t.Fatal("expected approvals cache hit within TTL")
	}
	if gotAp != ap {
		t.Errorf("getApprovals returned %p, want %p (same pointer)", gotAp, ap)
	}

	// --- Fork project ---
	c.setForkProject("gitlab.com", 100, "fork/source-path")
	gotPath, ok := c.getForkProject("gitlab.com", 100)
	if !ok {
		t.Fatal("expected fork project cache hit within TTL")
	}
	if gotPath != "fork/source-path" {
		t.Errorf("getForkProject = %q, want %q", gotPath, "fork/source-path")
	}

	// --- Key isolation: a different host, repo, or IID must miss. ---
	if _, ok := c.getMRDetail("gitlab.com", "org/repo", 99); ok {
		t.Error("expected miss for different IID")
	}
	if _, ok := c.getMRDetail("gitlab.com", "other/repo", 42); ok {
		t.Error("expected miss for different repo")
	}
	if _, ok := c.getMRDetail("gitlab.example.com", "org/repo", 42); ok {
		t.Error("expected miss for different host")
	}

	// --- Case-insensitive keying: "GitLab.com" / "Org/Repo" resolve to the
	// same cache entry as "gitlab.com" / "org/repo". This prevents remotes
	// with mixed-case hosts from fragmenting the cache. ---
	c.setMRDetail("GitLab.com", "Org/Repo", 7, &restMR{IID: 7})
	if got, ok := c.getMRDetail("gitlab.com", "org/repo", 7); !ok || got.IID != 7 {
		t.Errorf("case-insensitive MR detail lookup: ok=%v, got=%+v", ok, got)
	}
}

// TestCache_GetAfterTTLExpiry verifies that lookups past an entry's TTL return
// a miss (lazy eviction). It uses a controllable clock so the test is fast
// and deterministic. Because the three domains have different TTLs (60s for
// MR detail and approvals, 5min for fork project), the test also asserts that
// advancing past the short TTLs does not evict the longer-lived fork entry.
func TestCache_GetAfterTTLExpiry(t *testing.T) {
	c := newCache()
	clock := &syntheticClock{t: time.Now()}
	c.now = clock.now

	// Populate all three domains.
	c.setMRDetail("gitlab.com", "org/repo", 42, &restMR{IID: 42})
	c.setApprovals("gitlab.com", "org/repo", 42, &restApprovals{Approved: true})
	c.setForkProject("gitlab.com", 100, "org/repo")

	// Sanity: fresh entries are hits.
	if _, ok := c.getMRDetail("gitlab.com", "org/repo", 42); !ok {
		t.Fatal("expected MR detail hit before TTL expiry")
	}
	if _, ok := c.getApprovals("gitlab.com", "org/repo", 42); !ok {
		t.Fatal("expected approvals hit before TTL expiry")
	}
	if _, ok := c.getForkProject("gitlab.com", 100); !ok {
		t.Fatal("expected fork project hit before TTL expiry")
	}

	// Advance past the MR-detail and approvals TTL (60s) but not past the
	// fork-project TTL (5min). The short-TTL entries must miss; the fork
	// entry must still hit.
	clock.advance(mrDetailCacheTTL + time.Second)
	if _, ok := c.getMRDetail("gitlab.com", "org/repo", 42); ok {
		t.Error("expected MR detail miss after TTL expiry")
	}
	if _, ok := c.getApprovals("gitlab.com", "org/repo", 42); ok {
		t.Error("expected approvals miss after TTL expiry")
	}
	if _, ok := c.getForkProject("gitlab.com", 100); !ok {
		t.Error("expected fork project hit (within 5min TTL) after short-TTL expiry")
	}

	// Advance past the fork-project TTL. All three domains must now miss.
	clock.advance(forkProjectCacheTTL + time.Second)
	if _, ok := c.getForkProject("gitlab.com", 100); ok {
		t.Error("expected fork project miss after TTL expiry")
	}
}

// TestCache_ConcurrentAccess exercises set+get across all three domains from
// many goroutines simultaneously. Run with -race to verify the shared mutex
// prevents data races on the cache maps.
func TestCache_ConcurrentAccess(t *testing.T) {
	c := newCache()
	const goroutines = 50
	const iterations = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// A small set of keys (IID 0-4) maximises contention on
				// shared map entries across goroutines.
				iid := n % 5
				c.setMRDetail("gitlab.com", "org/repo", iid, &restMR{IID: iid})
				c.getMRDetail("gitlab.com", "org/repo", iid)
				c.setApprovals("gitlab.com", "org/repo", iid, &restApprovals{Approved: true})
				c.getApprovals("gitlab.com", "org/repo", iid)
				c.setForkProject("gitlab.com", iid, "org/repo")
				c.getForkProject("gitlab.com", iid)
			}
		}(i)
	}
	wg.Wait()

	// After all goroutines finish, a representative entry from each domain
	// must be present and correct. This verifies the cache settled into a
	// consistent state (no torn writes lost to a race).
	if mr, ok := c.getMRDetail("gitlab.com", "org/repo", 0); !ok || mr == nil || mr.IID != 0 {
		t.Errorf("getMRDetail after concurrent writes: ok=%v, mr=%+v", ok, mr)
	}
	if a, ok := c.getApprovals("gitlab.com", "org/repo", 0); !ok || a == nil || !a.Approved {
		t.Errorf("getApprovals after concurrent writes: ok=%v, a=%+v", ok, a)
	}
	if path, ok := c.getForkProject("gitlab.com", 0); !ok || path != "org/repo" {
		t.Errorf("getForkProject after concurrent writes: ok=%v, path=%q", ok, path)
	}
}

// syntheticClock is a controllable time source for the TTL expiry test. It is
// single-goroutine by construction (only used in TestCache_GetAfterTTLExpiry)
// so it needs no mutex.
type syntheticClock struct {
	t time.Time
}

func (c *syntheticClock) now() time.Time { return c.t }

func (c *syntheticClock) advance(d time.Duration) { c.t = c.t.Add(d) }
