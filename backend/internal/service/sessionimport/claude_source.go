package sessionimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// claudeConfigDirEnv mirrors Claude Code's own override for its state root.
const claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"

// ClaudeSource discovers Claude Code CLI conversations. Claude writes one
// <session-uuid>.jsonl per conversation under <config>/projects/<slug>/, where
// the slug encodes the cwd. The slug is lossy, so cwd is read from the
// transcript body, never decoded from the directory name.
type ClaudeSource struct {
	// configDir overrides the resolved state root; empty resolves from
	// CLAUDE_CONFIG_DIR or ~/.claude.
	configDir  string
	cache      fileCache[ImportableSession]
	usageCache fileCache[usageCount]
}

// NewClaudeSource builds a Claude Code source using the standard state root.
func NewClaudeSource() *ClaudeSource { return &ClaudeSource{} }

// NewClaudeSourceAt builds a Claude Code source rooted at an explicit config
// directory. Used in tests.
func NewClaudeSourceAt(configDir string) *ClaudeSource {
	return &ClaudeSource{configDir: configDir}
}

// Provider identifies this source as Claude Code.
func (s *ClaudeSource) Provider() domain.AgentHarness { return domain.HarnessClaudeCode }

func (s *ClaudeSource) resolveConfigDir() (string, error) {
	if strings.TrimSpace(s.configDir) != "" {
		return s.configDir, nil
	}
	return homeConfigDir(claudeConfigDirEnv, ".claude")
}

// Discover walks the Claude projects tree and returns the conversations found.
// A missing projects directory yields an empty slice, not an error.
func (s *ClaudeSource) Discover(ctx context.Context, opts DiscoverOptions) ([]ImportableSession, error) {
	configDir, err := s.resolveConfigDir()
	if err != nil {
		return nil, fmt.Errorf("claude-code: resolve config dir: %w", err)
	}
	projectsDir := filepath.Join(configDir, "projects")

	projects, err := os.ReadDir(projectsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claude-code: read projects: %w", err)
	}

	// Collect the transcripts first, then read them across a pool. Listing
	// directories is cheap; reading and parsing every transcript is what takes
	// the time, and those files are independent of one another.
	type transcript struct{ path, name string }
	var pending []transcript
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !project.IsDir() {
			continue
		}
		dir := filepath.Join(projectsDir, project.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A single unreadable project directory should not abort the scan.
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !isClaudeTranscript(entry.Name()) {
				continue
			}
			pending = append(pending, transcript{path: filepath.Join(dir, entry.Name()), name: entry.Name()})
		}
	}

	found, err := scanParallel(ctx, pending, func(ctx context.Context, t transcript) (ImportableSession, bool, error) {
		session, ok, err := s.readSession(ctx, configDir, t.path, t.name, opts)
		if err != nil || !ok {
			return ImportableSession{}, false, err
		}
		if session.TokenCount < opts.MinTokens {
			return ImportableSession{}, false, nil
		}
		return session, true, nil
	})
	if err != nil {
		return nil, err
	}

	return capRecent(found, opts.MaxPerProvider), nil
}

// isClaudeTranscript reports whether a file name is a <uuid>.jsonl transcript.
func isClaudeTranscript(name string) bool {
	if !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	_, err := uuid.Parse(strings.TrimSuffix(name, ".jsonl"))
	return err == nil
}

func (s *ClaudeSource) readSession(ctx context.Context, configDir, path, fileName string, opts DiscoverOptions) (ImportableSession, bool, error) {
	if err := ctx.Err(); err != nil {
		return ImportableSession{}, false, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ImportableSession{}, false, nil
	}
	if err != nil {
		return ImportableSession{}, false, err
	}
	value, cached := s.cache.get(path, info)
	if !cached {
		var ok bool
		value, ok, err = s.readSessionUncached(ctx, configDir, path, fileName, opts)
		if err != nil || !ok {
			return value, ok, err
		}
		s.cache.put(path, info, value)
	}
	if (!opts.Since.IsZero() && value.LastActivity.Before(opts.Since)) || (opts.IncludeCWD != nil && !opts.IncludeCWD(value.CWD)) {
		return ImportableSession{}, false, nil
	}
	usage, cachedUsage := s.usageCache.get(path, info)
	if !cachedUsage || !usage.sufficient(opts.MinTokens) {
		usage.total, usage.complete, err = scanUsage(ctx, path, false, opts.MinTokens)
		if err != nil {
			return ImportableSession{}, false, err
		}
		s.usageCache.put(path, info, usage)
	}
	value.TokenCount = usage.total
	return value, true, nil
}

func (s *ClaudeSource) readSessionUncached(ctx context.Context, configDir, path, fileName string, opts DiscoverOptions) (ImportableSession, bool, error) {
	if err := ctx.Err(); err != nil {
		return ImportableSession{}, false, err
	}

	nativeID := strings.TrimSuffix(fileName, ".jsonl")

	// Skip a transcript that vanished or is unreadable mid-scan rather than
	// failing the whole pass for one bad file.
	head, size, ok := readHead(path, opts.MaxScanBytes)
	if !ok {
		return ImportableSession{}, false, nil
	}

	meta := parseClaudeHead(head)

	last := meta.lastTimestamp
	if size > opts.MaxScanBytes {
		if tail, err := tailBytes(path, size, opts.MaxScanBytes); err == nil {
			for _, line := range completeLines(tail) {
				if t := parseTime(claudeLineTimestamp(line)); t.After(last) {
					last = t
				}
			}
		}
	}
	if last.IsZero() {
		if info, err := os.Stat(path); err == nil {
			last = info.ModTime()
		}
	}

	title := titleFrom(meta.aiTitle, meta.firstUserText, nativeID)

	return ImportableSession{
		Provider:        domain.HarnessClaudeCode,
		NativeSessionID: nativeID,
		ConfigDir:       configDir,
		TranscriptPath:  path,
		CWD:             meta.cwd,
		Branch:          meta.gitBranch,
		Title:           title,
		LastActivity:    last,

		SizeBytes:  size,
		TokenCount: -1,
	}, true, nil
}

type claudeHeadMeta struct {
	cwd           string
	gitBranch     string
	aiTitle       string
	firstUserText string
	lastTimestamp time.Time
}

type claudeLine struct {
	Type      string     `json:"type"`
	CWD       string     `json:"cwd"`
	GitBranch string     `json:"gitBranch"`
	Timestamp string     `json:"timestamp"`
	AiTitle   string     `json:"aiTitle"`
	Message   *claudeMsg `json:"message"`
}

type claudeMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// parseClaudeHead extracts cwd, branch, title and the first typed user prompt
// from the head of a Claude transcript. Only whole lines are parsed; a trailing
// fragment from the bounded read is ignored.
func parseClaudeHead(head []byte) claudeHeadMeta {
	var meta claudeHeadMeta
	for _, raw := range completeLines(head) {
		var line claudeLine
		if err := json.Unmarshal(raw, &line); err != nil {
			continue
		}
		if meta.cwd == "" && line.CWD != "" {
			meta.cwd = line.CWD
		}
		if meta.gitBranch == "" && line.GitBranch != "" {
			meta.gitBranch = line.GitBranch
		}
		if t := parseTime(line.Timestamp); t.After(meta.lastTimestamp) {
			meta.lastTimestamp = t
		}
		switch line.Type {
		case "ai-title":
			if meta.aiTitle == "" {
				meta.aiTitle = line.AiTitle
			}
		case "user":
			if meta.firstUserText == "" && line.Message != nil && line.Message.Role == "user" {
				if text := firstClaudeUserText(line.Message.Content); !isSyntheticPrompt(text) {
					meta.firstUserText = text
				}
			}
		}
	}
	return meta
}

// firstClaudeUserText returns the typed prompt from a user message. Claude
// stores a plain string for typed input and an array of blocks when the turn is
// a tool result; only real typed text is a useful title, so tool-result arrays
// yield "".
func firstClaudeUserText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return asString
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return b.Text
		}
	}
	return ""
}

// claudeLineTimestamp cheaply extracts just the timestamp from a transcript
// line, for tail scanning where the full struct is unnecessary.
func claudeLineTimestamp(raw []byte) string {
	var line struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		return ""
	}
	return line.Timestamp
}

// capRecent sorts newest-first and keeps at most max entries. max <= 0 keeps all.
func capRecent(sessions []ImportableSession, limit int) []ImportableSession {
	if limit <= 0 || len(sessions) <= limit {
		return sessions
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastActivity.After(sessions[j].LastActivity)
	})
	return sessions[:limit]
}
