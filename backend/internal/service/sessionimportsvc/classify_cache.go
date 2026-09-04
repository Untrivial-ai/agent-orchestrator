package sessionimportsvc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

// maxCachedVerdicts bounds the cache file. Verdicts are cheap to recompute and
// this is a convenience, not a record, so the file is dropped wholesale rather
// than evicted entry by entry once it grows past this.
const maxCachedVerdicts = 5000

// verdictCache remembers what the user's agent decided about a conversation, so
// reopening the import list costs nothing. Asking again would spend their quota
// to re-derive an answer that cannot have changed.
//
// The key includes the transcript's size and last activity, so a conversation
// that has since been continued is judged afresh rather than on a stale answer.
type verdictCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]string
	loaded  bool
	dirty   bool
}

func newVerdictCache(dataDir string) *verdictCache {
	return &verdictCache{
		path:    filepath.Join(dataDir, "session-import-verdicts.json"),
		entries: map[string]string{},
	}
}

func verdictKey(s sessionimport.ImportableSession) string {
	return fmt.Sprintf("%s|%s|%d|%d", s.Provider, s.NativeSessionID, s.SizeBytes, s.LastActivity.UTC().Unix())
}

func (c *verdictCache) get(s sessionimport.ImportableSession) (sessionimport.Meaning, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()

	switch c.entries[verdictKey(s)] {
	case string(sessionimport.MeaningMeaningful):
		return sessionimport.MeaningMeaningful, true
	case string(sessionimport.MeaningTrivial):
		return sessionimport.MeaningTrivial, true
	default:
		return "", false
	}
}

func (c *verdictCache) put(s sessionimport.ImportableSession, verdict sessionimport.Meaning) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()

	if len(c.entries) >= maxCachedVerdicts {
		c.entries = map[string]string{}
	}
	c.entries[verdictKey(s)] = string(verdict)
	c.dirty = true
}

// flush writes the cache back. A failure is logged nowhere and ignored: the
// only cost is asking again next time.
func (c *verdictCache) flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return
	}
	c.dirty = false

	data, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o750); err != nil {
		return
	}
	// Write through a temporary file so a crash mid-write cannot leave a
	// half-written cache that fails to parse on the next launch.
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
	}
}

func (c *verdictCache) loadLocked() {
	if c.loaded {
		return
	}
	c.loaded = true

	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	entries := map[string]string{}
	if json.Unmarshal(data, &entries) != nil {
		// A corrupt cache is not worth recovering; start clean.
		return
	}
	c.entries = entries
}
