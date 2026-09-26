package chat

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

// maxCachedTranscripts bounds how many parsed transcripts are held. Opening a
// session and paging back through it hits the same one repeatedly; a handful is
// enough to cover that without holding a large history in memory indefinitely.
const maxCachedTranscripts = 4

// importedTranscripts keeps recently parsed transcripts so that opening a
// session and scrolling back through it does not reparse the whole file on
// every request. A large JSONL otherwise costs a full parse per page, which
// makes the paging parameters decorative.
//
// The key includes size and modification time, so a transcript the provider has
// appended to is parsed again rather than served stale.
var importedTranscripts = struct {
	mu      sync.Mutex
	entries map[string]cachedTranscript
	order   []string
}{entries: map[string]cachedTranscript{}}

type cachedTranscript struct {
	messages []domain.ConversationMessage
}

func transcriptCacheKey(path string, info os.FileInfo) string {
	return strings.Join([]string{
		path,
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}, "\x00")
}

// readImportedMessages returns a transcript's messages, reusing a recent parse
// and relocating a transcript the provider has moved.
func readImportedMessages(ctx context.Context, harness domain.AgentHarness, path string) ([]domain.ConversationMessage, error) {
	// A cache hit must not let a cancelled request quietly succeed.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, info, err := resolveTranscript(harness, path)
	if err != nil {
		return nil, err
	}

	key := transcriptCacheKey(resolved, info)
	importedTranscripts.mu.Lock()
	if entry, ok := importedTranscripts.entries[key]; ok {
		importedTranscripts.mu.Unlock()
		return entry.messages, nil
	}
	importedTranscripts.mu.Unlock()

	messages, err := sessionimport.ReadMessages(ctx, harness, resolved)
	if err != nil {
		return nil, err
	}

	importedTranscripts.mu.Lock()
	defer importedTranscripts.mu.Unlock()
	if _, ok := importedTranscripts.entries[key]; !ok {
		importedTranscripts.entries[key] = cachedTranscript{messages: messages}
		importedTranscripts.order = append(importedTranscripts.order, key)
		for len(importedTranscripts.order) > maxCachedTranscripts {
			delete(importedTranscripts.entries, importedTranscripts.order[0])
			importedTranscripts.order = importedTranscripts.order[1:]
		}
	}
	return messages, nil
}

// resolveTranscript finds a transcript that the provider may have moved since
// AO recorded its path. Codex relocates a conversation into archived_sessions
// when it is archived, which would otherwise render the import as an error.
func resolveTranscript(harness domain.AgentHarness, path string) (string, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err == nil {
		return path, info, nil
	}
	if !os.IsNotExist(err) || harness != domain.HarnessCodex {
		return "", nil, err
	}
	for _, candidate := range archivedCounterparts(path) {
		if info, statErr := os.Stat(candidate); statErr == nil {
			return candidate, info, nil
		}
	}
	return "", nil, err
}

// archivedCounterparts maps a Codex transcript path between its live and
// archived roots, in both directions.
func archivedCounterparts(path string) []string {
	var out []string
	for _, swap := range [][2]string{
		{string(filepath.Separator) + "sessions" + string(filepath.Separator), string(filepath.Separator) + "archived_sessions" + string(filepath.Separator)},
		{string(filepath.Separator) + "archived_sessions" + string(filepath.Separator), string(filepath.Separator) + "sessions" + string(filepath.Separator)},
	} {
		if strings.Contains(path, swap[0]) {
			out = append(out, strings.Replace(path, swap[0], swap[1], 1))
		}
	}
	// Codex also stores archived rollouts flat under the archive root.
	for _, candidate := range append([]string(nil), out...) {
		if dir := filepath.Dir(candidate); strings.HasSuffix(dir, "archived_sessions") {
			continue
		}
		if idx := strings.Index(candidate, string(filepath.Separator)+"archived_sessions"+string(filepath.Separator)); idx >= 0 {
			root := candidate[:idx] + string(filepath.Separator) + "archived_sessions"
			out = append(out, filepath.Join(root, filepath.Base(candidate)))
		}
	}
	return out
}
