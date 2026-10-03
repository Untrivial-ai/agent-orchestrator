package session

import (
	"context"
	"fmt"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type workspaceManifestIndexEntry struct {
	manifest    WorkspaceManifest
	hasManifest bool
	stale       bool
	refreshing  bool
	lastAccess  uint64
	generation  uint64
}

// workspaceManifestIndex retains the last complete review manifest. It is a
// performance cache only: Git and the worktree remain the source of truth.
type workspaceManifestIndex struct {
	mu      sync.Mutex
	entries map[domain.SessionID]workspaceManifestIndexEntry
	clock   uint64
}

const maxWorkspaceManifestEntries = 128

func newWorkspaceManifestIndex() *workspaceManifestIndex {
	return &workspaceManifestIndex{entries: make(map[domain.SessionID]workspaceManifestIndexEntry)}
}

func (i *workspaceManifestIndex) get(id domain.SessionID) (WorkspaceManifest, bool) {
	if i == nil {
		return WorkspaceManifest{}, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	entry, ok := i.entries[id]
	if !ok || !entry.hasManifest {
		return WorkspaceManifest{}, false
	}
	i.clock++
	entry.lastAccess = i.clock
	i.entries[id] = entry
	manifest := entry.manifest
	manifest.Stale = entry.stale
	manifest.Refreshing = entry.refreshing
	return manifest, true
}

func (i *workspaceManifestIndex) markStale(id domain.SessionID) bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	entry, ok := i.entries[id]
	if !ok {
		return false
	}
	entry.generation++
	entry.stale = true
	if entry.refreshing {
		i.entries[id] = entry
		return false
	}
	entry.refreshing = true
	i.entries[id] = entry
	return true
}

func (i *workspaceManifestIndex) beginRefresh(id domain.SessionID) uint64 {
	if i == nil {
		return 0
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	entry := i.entries[id]
	entry.refreshing = true
	i.entries[id] = entry
	return entry.generation
}

// publish returns true only when no invalidation arrived while the snapshot
// was being computed. A newer generation keeps the result as a usable stale
// snapshot and makes the refresh caller loop once more.
func (i *workspaceManifestIndex) publish(id domain.SessionID, manifest WorkspaceManifest, generation uint64) bool {
	if i == nil {
		return true
	}
	i.mu.Lock()
	entry := i.entries[id]
	fresh := entry.generation == generation
	manifest.Stale = !fresh
	manifest.Refreshing = false
	i.clock++
	entry.manifest = manifest
	entry.hasManifest = true
	entry.stale = !fresh
	entry.refreshing = false
	entry.lastAccess = i.clock
	i.entries[id] = entry
	if len(i.entries) > maxWorkspaceManifestEntries {
		var oldestID domain.SessionID
		oldestAccess := ^uint64(0)
		for candidateID, candidate := range i.entries {
			if candidateID != id && candidate.lastAccess < oldestAccess {
				oldestID = candidateID
				oldestAccess = candidate.lastAccess
			}
		}
		if oldestID != "" {
			delete(i.entries, oldestID)
		}
	}
	i.mu.Unlock()
	return fresh
}

func (i *workspaceManifestIndex) fail(id domain.SessionID) {
	if i == nil {
		return
	}
	i.mu.Lock()
	entry, ok := i.entries[id]
	if ok {
		if entry.hasManifest {
			entry.stale = true
			entry.refreshing = false
			i.entries[id] = entry
		} else {
			delete(i.entries, id)
		}
	}
	i.mu.Unlock()
}

func (i *workspaceManifestIndex) invalidateSession(id domain.SessionID) {
	if i == nil {
		return
	}
	i.mu.Lock()
	delete(i.entries, id)
	i.mu.Unlock()
}

// GetWorkspaceManifest returns the last complete snapshot immediately. A
// stale snapshot starts one daemon-owned refresh without blocking the reader.
func (s *Service) GetWorkspaceManifest(ctx context.Context, id domain.SessionID) (WorkspaceManifest, error) {
	if manifest, ok := s.workspaceManifests.get(id); ok {
		if manifest.Stale && !manifest.Refreshing && s.workspaceManifests.markStale(id) {
			s.refreshWorkspaceManifestInBackground(id)
			manifest.Refreshing = true
		}
		return manifest, nil
	}
	return s.RefreshWorkspaceManifest(ctx, id)
}

// RefreshWorkspaceManifest waits for one coalesced manifest computation and
// publishes only its complete result.
func (s *Service) RefreshWorkspaceManifest(ctx context.Context, id domain.SessionID) (WorkspaceManifest, error) {
	type refreshResult struct {
		manifest WorkspaceManifest
		fresh    bool
	}
	for {
		v, err, _ := s.manifestGroup.Do(string(id), func() (any, error) {
			generation := s.workspaceManifests.beginRefresh(id)
			manifest, err := s.computeWorkspaceManifest(ctx, id)
			if err != nil {
				s.workspaceManifests.fail(id)
				return refreshResult{}, err
			}
			return refreshResult{manifest: manifest, fresh: s.workspaceManifests.publish(id, manifest, generation)}, nil
		})
		if err != nil {
			return WorkspaceManifest{}, err
		}
		result, ok := v.(refreshResult)
		if !ok {
			return WorkspaceManifest{}, fmt.Errorf("refresh workspace manifest: unexpected singleflight result type %T", v)
		}
		if result.fresh {
			return result.manifest, nil
		}
	}
}

func (s *Service) refreshWorkspaceManifestInBackground(id domain.SessionID) {
	work := func() {
		ctx := s.backgroundContext
		if ctx == nil {
			ctx = context.Background()
		}
		_, _ = s.RefreshWorkspaceManifest(ctx, id)
	}
	if s.runBackground != nil {
		s.runBackground(work)
		return
	}
	go work()
}
