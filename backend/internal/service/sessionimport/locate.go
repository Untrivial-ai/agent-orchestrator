package sessionimport

import (
	"context"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func (c *fileCache[T]) values() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	values := make([]T, 0, len(c.entries))
	for _, e := range c.entries {
		values = append(values, e.value)
	}
	return values
}

// Locate refreshes only the selected conversation after a browse. Unknown ids
// (including direct API imports before browsing) fall back to one scoped scan.
func (s *Service) Locate(ctx context.Context, provider domain.AgentHarness, id string, opts DiscoverOptions) (ImportableSession, bool, error) {
	opts = opts.normalized()
	for _, source := range s.sources {
		if source.Provider() != provider {
			continue
		}
		if locator, ok := source.(interface {
			locateCached(context.Context, string, DiscoverOptions) (ImportableSession, bool, error)
		}); ok {
			value, found, err := locator.locateCached(ctx, id, opts)
			if err != nil || found {
				return value, found, err
			}
		}
		found, err := source.Discover(ctx, opts)
		if err != nil {
			return ImportableSession{}, false, err
		}
		for _, value := range found {
			if value.NativeSessionID == id {
				return value, true, nil
			}
		}
	}
	return ImportableSession{}, false, nil
}

func (s *ClaudeSource) locateCached(ctx context.Context, id string, opts DiscoverOptions) (ImportableSession, bool, error) {
	for _, cached := range s.cache.values() {
		if cached.NativeSessionID != id {
			continue
		}
		return s.readSession(ctx, cached.ConfigDir, cached.TranscriptPath, filepath.Base(cached.TranscriptPath), opts)
	}
	return ImportableSession{}, false, nil
}

func (s *CodexSource) locateCached(ctx context.Context, id string, opts DiscoverOptions) (ImportableSession, bool, error) {
	home, err := s.resolveHome()
	if err != nil {
		return ImportableSession{}, false, err
	}
	grouped := map[string]*ImportableSession{}
	segments := map[string][]codexSegment{}
	roots := []string{filepath.Join(home, "sessions")}
	if s.includeArchived {
		roots = append(roots, filepath.Join(home, "archived_sessions"))
	}
	for _, root := range roots {
		if err := s.scanRoot(ctx, root, nil, opts, grouped, segments, id); err != nil {
			return ImportableSession{}, false, err
		}
	}
	value := grouped[id]
	if value == nil || (opts.IncludeCWD != nil && !opts.IncludeCWD(value.CWD)) {
		return ImportableSession{}, false, nil
	}
	for _, seg := range segments[id] {
		if opts.MinTokens > 0 && value.TokenCount >= opts.MinTokens {
			break
		}
		tokens, err := s.segmentUsage(ctx, seg, opts.MinTokens)
		if err != nil {
			return ImportableSession{}, false, err
		}
		grouped[id].TokenCount = max(grouped[id].TokenCount, tokens)
	}
	if opts.MinTokens > 0 && value.TokenCount < opts.MinTokens {
		return ImportableSession{}, false, nil
	}
	// The title index is tiny compared to rollouts and may have been renamed.
	if title, ok := loadCodexTitleIndex(filepath.Join(home, "session_index.jsonl"))[id]; ok {
		value.Title = titleFrom(title.name, value.Title, id)
	}
	value.ConfigDir = home
	return *value, true, nil
}

func (s *CodexSource) segmentUsage(ctx context.Context, seg codexSegment, threshold int64) (int64, error) {
	if seg.tokenCount >= 0 && (usageCount{total: seg.tokenCount, complete: seg.usageComplete}).sufficient(threshold) {
		return seg.tokenCount, nil
	}
	tokens, complete, err := scanUsage(ctx, seg.transcriptPath, true, threshold)
	if err != nil {
		return 0, err
	}
	seg.tokenCount = tokens
	seg.usageComplete = complete
	// Preserve the fingerprint from the metadata read. A provider append
	// between metadata and usage must not validate old metadata as current.
	if seg.fileInfo != nil {
		s.cache.put(seg.transcriptPath, seg.fileInfo, seg)
	}
	return tokens, nil
}
