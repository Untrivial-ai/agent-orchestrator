package session

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestWorkspaceCacheInvalidationDuringLoad(t *testing.T) {
	for _, invalidations := range []int{0, 1, 3} {
		t.Run(map[int]string{0: "no invalidation", 1: "one invalidation", 3: "repeated invalidation"}[invalidations], func(t *testing.T) {
			roots := []string{newWorkspaceRepo(t), newWorkspaceRepo(t)}
			now := func() time.Time { return time.Unix(100, 0) }
			s := &Service{clock: now, workspaceCache: newWorkspaceCache(workspaceCacheTTL, now)}
			keys := []workspaceCacheKey{
				{session: "changed", root: roots[0]},
				{session: "changed", root: roots[1]},
				{session: "unrelated", root: roots[0]},
			}
			type result struct {
				compare workspaceCompareTarget
				err     error
			}
			started := make(chan struct{}, len(keys))
			release := make(chan struct{})
			done := make([]chan result, len(keys))
			for i, key := range keys {
				done[i] = make(chan result, 1)
				go func() {
					compare, _, err := s.resolveWorkspaceChanges(t.Context(), key.session, key.root, func(context.Context) workspaceCompareTarget {
						started <- struct{}{}
						<-release
						return workspaceCompareTarget{BaseRef: "before"}
					})
					done[i] <- result{compare, err}
				}()
			}
			for range keys {
				<-started
			}
			for range invalidations {
				s.InvalidateWorkspaceCache("changed")
			}
			close(release)
			for i, key := range keys {
				loaded := <-done[i]
				if loaded.err != nil {
					t.Fatalf("load %v: %v", key, loaded.err)
				}
				if loaded.compare.BaseRef != "before" {
					t.Errorf("racing load %v = %q, want before", key, loaded.compare.BaseRef)
				}
				compare, _, err := s.resolveWorkspaceChanges(t.Context(), key.session, key.root, func(context.Context) workspaceCompareTarget {
					return workspaceCompareTarget{BaseRef: "after"}
				})
				if err != nil {
					t.Fatalf("next load %v: %v", key, err)
				}
				want := "before"
				if invalidations > 0 && key.session == "changed" {
					want = "after"
				}
				if compare.BaseRef != want {
					t.Errorf("next load %v = %q, want %q", key, compare.BaseRef, want)
				}
			}
		})
	}
}

func TestWorkspaceCacheInvalidatedFlightStillCoalesces(t *testing.T) {
	root := newWorkspaceRepo(t)
	synctest.Test(t, func(t *testing.T) {
		now := func() time.Time { return time.Unix(100, 0) }
		s := &Service{clock: now, workspaceCache: newWorkspaceCache(workspaceCacheTTL, now)}
		id := domain.SessionID("session")
		started := make(chan struct{})
		release := make(chan struct{})
		done := make(chan workspaceCompareTarget, 2)
		load := func(resolve func(context.Context) workspaceCompareTarget) {
			compare, _, err := s.resolveWorkspaceChanges(t.Context(), id, root, resolve)
			if err != nil {
				t.Errorf("resolve changes: %v", err)
			}
			done <- compare
		}
		go load(func(context.Context) workspaceCompareTarget {
			close(started)
			<-release
			return workspaceCompareTarget{BaseRef: "before"}
		})
		<-started
		s.InvalidateWorkspaceCache(id)
		go load(func(context.Context) workspaceCompareTarget {
			t.Error("caller joining an invalidated flight started a separate load")
			return workspaceCompareTarget{BaseRef: "separate"}
		})
		// Both callers must be blocked in the same flight before it completes.
		synctest.Wait()
		close(release)
		for range 2 {
			if compare := <-done; compare.BaseRef != "before" {
				t.Errorf("shared snapshot = %q, want before", compare.BaseRef)
			}
		}
		compare, _, err := s.resolveWorkspaceChanges(t.Context(), id, root, func(context.Context) workspaceCompareTarget {
			return workspaceCompareTarget{BaseRef: "after"}
		})
		if err != nil {
			t.Fatalf("next load: %v", err)
		}
		if compare.BaseRef != "after" {
			t.Errorf("next load = %q, want after", compare.BaseRef)
		}
	})
}

func TestWorkspaceCacheOldLoadCannotAffectReplacement(t *testing.T) {
	for _, publishOld := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "publication"}[publishOld], func(t *testing.T) {
			c := newWorkspaceCache(workspaceCacheTTL, nil)
			key := workspaceCacheKey{session: "session", root: "root"}
			old := c.beginLoad(key)
			old.compare.BaseRef = "old"
			c.invalidateSession(key.session)
			c.invalidateSession(key.session)
			if len(c.loads) != 0 {
				t.Fatal("invalidation retained a load reservation")
			}
			replacement := c.beginLoad(key)
			*replacement = workspaceCacheEntry{at: c.now(), compare: workspaceCompareTarget{BaseRef: "replacement"}}
			c.finishLoad(key, old, publishOld)
			c.finishLoad(key, replacement, true)
			// A late finish must also leave the replacement's cached result intact.
			c.finishLoad(key, old, publishOld)
			got, ok := c.get(key)
			if !ok || got.compare.BaseRef != "replacement" {
				t.Fatalf("replacement cache = %+v, %v; want replacement", got, ok)
			}
			if len(c.loads) != 0 {
				t.Fatal("completed loads retained reservations")
			}
			c.invalidateSession(key.session)
			c.invalidateSession("never loaded")
			if len(c.entries) != 0 || len(c.loads) != 0 {
				t.Fatal("invalidation retained session bookkeeping")
			}
		})
	}
}

func TestWorkspaceCacheFailedLoadReleasesReservation(t *testing.T) {
	root := t.TempDir() // Git must fail for a directory that is not a repository.
	c := newWorkspaceCache(workspaceCacheTTL, nil)
	s := &Service{workspaceCache: c}
	_, _, err := s.resolveWorkspaceChanges(t.Context(), "session", root, func(context.Context) workspaceCompareTarget {
		return workspaceCompareTarget{}
	})
	if err == nil {
		t.Fatal("expected Git error")
	}
	if len(c.loads) != 0 || len(c.entries) != 0 {
		t.Fatal("failed load retained cache bookkeeping")
	}
}

func TestWorkspaceCacheNilRemainsUncached(t *testing.T) {
	root := newWorkspaceRepo(t)
	s := &Service{}
	s.InvalidateWorkspaceCache("session")
	for _, ref := range []string{"first", "second"} {
		compare, _, err := s.resolveWorkspaceChanges(t.Context(), "session", root, func(context.Context) workspaceCompareTarget {
			return workspaceCompareTarget{BaseRef: ref}
		})
		if err != nil {
			t.Fatalf("load %s: %v", ref, err)
		}
		if compare.BaseRef != ref {
			t.Errorf("load = %q, want %q", compare.BaseRef, ref)
		}
	}
}

func TestWorkspaceCacheExpiresAtConfiguredTTL(t *testing.T) {
	root := newWorkspaceRepo(t)
	const ttl = 250 * time.Millisecond
	start := time.Unix(100, 0)
	now := start
	clock := func() time.Time { return now }
	s := &Service{clock: clock, workspaceCache: newWorkspaceCache(ttl, clock)}
	for _, tt := range []struct {
		elapsed  time.Duration
		resolved string
		want     string
	}{
		{elapsed: 0, resolved: "initial", want: "initial"},
		{elapsed: ttl, resolved: "refreshed", want: "initial"},
		{elapsed: ttl + time.Nanosecond, resolved: "refreshed", want: "refreshed"},
	} {
		now = start.Add(tt.elapsed)
		compare, _, err := s.resolveWorkspaceChanges(t.Context(), "session", root, func(context.Context) workspaceCompareTarget {
			return workspaceCompareTarget{BaseRef: tt.resolved}
		})
		if err != nil {
			t.Fatalf("load at %s: %v", tt.elapsed, err)
		}
		if compare.BaseRef != tt.want {
			t.Errorf("load at %s = %q, want %q", tt.elapsed, compare.BaseRef, tt.want)
		}
	}
}
