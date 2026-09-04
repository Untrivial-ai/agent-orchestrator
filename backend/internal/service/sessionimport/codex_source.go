package sessionimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// codexHomeEnv mirrors Codex's own override for its state root.
const codexHomeEnv = "CODEX_HOME"

// CodexSource discovers Codex CLI conversations.
//
// Codex writes one rollout-<ts>-<segment-uuid>.jsonl per session segment,
// date-sharded under <home>/sessions/YYYY/MM/DD/ (mirrored under
// archived_sessions). Resuming a conversation appends a new segment file, so a
// single conversation the user recognizes is spread across many rollout files
// that all share one root session_id. This source groups segments by that root
// id and surfaces one importable session per conversation, bound to the most
// recent segment. Titles live in <home>/session_index.jsonl keyed by the root
// id; when absent the first user prompt is used.
type CodexSource struct {
	// home overrides the resolved state root; empty resolves from CODEX_HOME or
	// ~/.codex.
	home string
	// includeArchived also scans archived_sessions.
	includeArchived bool
}

// NewCodexSource builds a Codex source over active sessions plus archived ones.
func NewCodexSource() *CodexSource { return &CodexSource{includeArchived: true} }

// NewCodexSourceAt builds a Codex source rooted at an explicit home. Used in
// tests.
func NewCodexSourceAt(home string, includeArchived bool) *CodexSource {
	return &CodexSource{home: home, includeArchived: includeArchived}
}

// Provider identifies this source as Codex.
func (s *CodexSource) Provider() domain.AgentHarness { return domain.HarnessCodex }

func (s *CodexSource) resolveHome() (string, error) {
	if strings.TrimSpace(s.home) != "" {
		return s.home, nil
	}
	return homeConfigDir(codexHomeEnv, ".codex")
}

// codexSegment is one rollout file's contribution to a conversation.
type codexSegment struct {
	rootID         string
	cwd            string
	branch         string
	firstUserText  string
	lastActivity   time.Time
	transcriptPath string
	sizeBytes      int64
	messageCount   int
	signals        Signals
}

// Discover walks the Codex sessions tree, groups rollout segments by root
// conversation id, and returns one importable session per conversation. Missing
// directories yield an empty slice, not an error.
func (s *CodexSource) Discover(ctx context.Context, opts DiscoverOptions) ([]ImportableSession, error) {
	home, err := s.resolveHome()
	if err != nil {
		return nil, fmt.Errorf("codex: resolve home: %w", err)
	}

	titles := loadCodexTitleIndex(filepath.Join(home, "session_index.jsonl"))

	roots := []string{filepath.Join(home, "sessions")}
	if s.includeArchived {
		roots = append(roots, filepath.Join(home, "archived_sessions"))
	}

	// Group segments by root conversation id, keeping the most recent segment as
	// the representative for path/cwd/title and the max message count seen.
	grouped := map[string]*ImportableSession{}
	// Import signals accumulate per conversation, not per segment, so a
	// conversation that did real work in any segment is judged on that.
	signalsByRoot := map[string]*Signals{}
	for _, root := range roots {
		if err := s.scanRoot(ctx, root, titles, opts, grouped, signalsByRoot); err != nil {
			return nil, err
		}
	}

	found := make([]ImportableSession, 0, len(grouped))
	for rootID, session := range grouped {
		if !opts.Since.IsZero() && session.LastActivity.Before(opts.Since) {
			continue
		}
		if merged, ok := signalsByRoot[rootID]; ok {
			session.Meaning = Classify(*merged)
			session.FirstPrompt = merged.FirstPrompt
		}
		// Drop junk before the per-provider cap, so a run of trivial
		// conversations cannot crowd out real ones sitting just outside it.
		if !opts.IncludeTrivial && !session.Meaning.Imported() {
			continue
		}
		// The session index can know about activity the rollout segments do not
		// carry, so it is allowed to advance the displayed recency — but only
		// now that every segment has been compared and a representative chosen.
		if t, ok := titles[rootID]; ok && t.updatedAt.After(session.LastActivity) {
			session.LastActivity = t.updatedAt
		}
		found = append(found, *session)
	}

	return capRecent(found, opts.MaxPerProvider), nil
}

func (s *CodexSource) scanRoot(ctx context.Context, root string, titles map[string]codexTitle, opts DiscoverOptions, grouped map[string]*ImportableSession, signalsByRoot map[string]*Signals) error {
	// Walk first, read after. The walk only lists names, which is cheap; every
	// rollout is then parsed across a pool, since segments are independent.
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return fs.SkipAll
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !isCodexRollout(entry.Name()) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("codex: scan %s: %w", root, err)
	}

	segments, err := scanParallel(ctx, paths, func(ctx context.Context, path string) (codexSegment, bool, error) {
		return s.readSegment(ctx, path, opts)
	})
	if err != nil {
		return fmt.Errorf("codex: scan %s: %w", root, err)
	}

	// Merging stays sequential and in walk order. It folds segments into shared
	// maps and picks a representative by recency, so doing it concurrently would
	// both race and make the winner depend on scheduling.
	for _, seg := range segments {
		mergeCodexSegment(grouped, seg, titles)
		if existing, found := signalsByRoot[seg.rootID]; found {
			*existing = mergeSignals(*existing, seg.signals)
		} else {
			merged := seg.signals
			signalsByRoot[seg.rootID] = &merged
		}
	}
	return nil
}

// mergeCodexSegment folds one segment into the conversation it belongs to,
// keyed by root id. The newest segment wins for the representative fields.
func mergeCodexSegment(grouped map[string]*ImportableSession, seg codexSegment, titles map[string]codexTitle) {
	existing, ok := grouped[seg.rootID]
	if !ok {
		title := ""
		if t, found := titles[seg.rootID]; found {
			title = t.name
		}
		session := &ImportableSession{
			Provider:        domain.HarnessCodex,
			NativeSessionID: seg.rootID,
			ConfigDir:       "",
			TranscriptPath:  seg.transcriptPath,
			CWD:             seg.cwd,
			Branch:          seg.branch,
			Title:           titleFrom(title, seg.firstUserText, filepath.Base(seg.transcriptPath)),
			LastActivity:    seg.lastActivity,
			MessageCount:    seg.messageCount,
			SizeBytes:       seg.sizeBytes,
		}
		grouped[seg.rootID] = session
		return
	}

	// Newer segment: adopt its transcript, cwd, branch as the representative.
	// The comparison is between segments only. Seeding LastActivity from the
	// session index would make it later than every segment's own timestamp, so
	// no segment would ever be adopted and the conversation would keep the
	// oldest segment's transcript, cwd and branch.
	if seg.lastActivity.After(existing.LastActivity) {
		existing.LastActivity = seg.lastActivity
		existing.TranscriptPath = seg.transcriptPath
		existing.SizeBytes = seg.sizeBytes
		if seg.cwd != "" {
			existing.CWD = seg.cwd
		}
		if seg.branch != "" {
			existing.Branch = seg.branch
		}
	}
	// Message count reflects the longest single segment observed rather than a
	// sum, because resume segments overlap and summing would over-count.
	if seg.messageCount > existing.MessageCount {
		existing.MessageCount = seg.messageCount
	}
	// If the conversation had no titled index entry, let an earlier segment's
	// first prompt stand in.
	if existing.Title == "" && seg.firstUserText != "" {
		existing.Title = normalizeTitle(seg.firstUserText)
	}
}

// isCodexRollout reports whether a file name is a Codex rollout transcript.
func isCodexRollout(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

func (s *CodexSource) readSegment(ctx context.Context, path string, opts DiscoverOptions) (codexSegment, bool, error) {
	if err := ctx.Err(); err != nil {
		return codexSegment{}, false, err
	}

	// A segment that vanished or is unreadable mid-scan is skipped, not fatal.
	head, size, ok := readHead(path, opts.MaxScanBytes)
	if !ok {
		return codexSegment{}, false, nil
	}

	meta, ok := parseCodexHead(head)
	if !ok {
		return codexSegment{}, false, nil
	}
	// A spawned sub-agent thread is not a conversation the user started; skip it.
	if meta.isSubagent {
		return codexSegment{}, false, nil
	}

	rootID := meta.rootID
	if rootID == "" {
		rootID = meta.id
	}
	if rootID == "" {
		rootID = codexIDFromFileName(filepath.Base(path))
	}
	if rootID == "" {
		return codexSegment{}, false, nil
	}

	last := meta.lastTimestamp
	if size > opts.MaxScanBytes {
		if tail, err := tailBytes(path, size, opts.MaxScanBytes); err == nil {
			for _, line := range completeLines(tail) {
				if t := parseTime(codexLineTimestamp(line)); t.After(last) {
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

	// Out of scope: stop before the expensive full read. A project's listing
	// otherwise scans every conversation on the machine to show a handful.
	if opts.IncludeCWD != nil && !opts.IncludeCWD(meta.cwd) {
		return codexSegment{}, false, nil
	}

	// One full pass yields both the display count and the import signals.
	signals := Signals{FirstPrompt: meta.firstUserText}
	count := 0
	if !opts.MetadataOnly && size <= fullCountMaxBytes {
		signals, count = scanCodexSignals(path)
		if signals.FirstPrompt == "" {
			signals.FirstPrompt = meta.firstUserText
		}
	}

	return codexSegment{
		rootID:         rootID,
		cwd:            meta.cwd,
		branch:         meta.branch,
		firstUserText:  meta.firstUserText,
		lastActivity:   last,
		transcriptPath: path,
		sizeBytes:      size,
		messageCount:   count,
		signals:        signals,
	}, true, nil
}

type codexHeadMeta struct {
	id            string
	rootID        string
	cwd           string
	branch        string
	isSubagent    bool
	firstUserText string
	lastTimestamp time.Time
}

type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// parseCodexHead reads the session_meta header plus any early user message from
// the head of a rollout file. ok is false when no session_meta line is present.
func parseCodexHead(head []byte) (codexHeadMeta, bool) {
	var (
		meta     codexHeadMeta
		haveMeta bool
	)
	for _, raw := range completeLines(head) {
		var env codexEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if t := parseTime(env.Timestamp); t.After(meta.lastTimestamp) {
			meta.lastTimestamp = t
		}
		switch env.Type {
		case "session_meta":
			var p struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
				CWD       string `json:"cwd"`
				Git       struct {
					Branch string `json:"branch"`
				} `json:"git"`
				Source json.RawMessage `json:"source"`
			}
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				continue
			}
			meta.id = p.ID
			meta.rootID = p.SessionID
			meta.cwd = p.CWD
			meta.branch = p.Git.Branch
			meta.isSubagent = codexSourceIsSubagent(p.Source)
			haveMeta = true
		case "response_item":
			if meta.firstUserText == "" {
				meta.firstUserText = firstCodexUserText(env.Payload)
			}
		}
	}
	return meta, haveMeta
}

// codexSourceIsSubagent reports whether a session_meta source field describes a
// spawned sub-agent rather than a user-initiated thread. The field is either a
// plain string (e.g. "vscode") or an object carrying subagent lineage.
func codexSourceIsSubagent(source json.RawMessage) bool {
	if len(source) == 0 {
		return false
	}
	var asString string
	if err := json.Unmarshal(source, &asString); err == nil {
		return false
	}
	var obj struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if err := json.Unmarshal(source, &obj); err != nil {
		return false
	}
	return len(obj.Subagent) > 0
}

// firstCodexUserText returns the typed text from a user response_item message.
func firstCodexUserText(payload json.RawMessage) string {
	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		return ""
	}
	if item.Type != "message" || item.Role != "user" {
		return ""
	}
	for _, c := range item.Content {
		if c.Type == "input_text" && !isSyntheticPrompt(c.Text) {
			return c.Text
		}
	}
	return ""
}

func codexLineTimestamp(raw []byte) string {
	var env struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	return env.Timestamp
}

// codexToolCallTypes are the response_item payload types Codex writes when the
// agent does something to the machine rather than talk: shell commands, patch
// application, and tool invocations.
var codexToolCallTypes = map[string]struct{}{
	"function_call":    {},
	"local_shell_call": {},
	"custom_tool_call": {},
	"exec_command":     {},
	"apply_patch":      {},
}

// scanCodexSignals reads a rollout segment once and returns the import signals
// plus the visible message count, mirroring scanClaudeSignals so the two
// providers are classified on identical terms.
func scanCodexSignals(path string) (Signals, int) {
	signals := Signals{Scanned: true}
	visible := 0
	_ = scanLines(path, func(raw []byte) bool {
		var env codexEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return true
		}
		if env.Type != "response_item" {
			return true
		}
		var item struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if err := json.Unmarshal(env.Payload, &item); err != nil {
			return true
		}
		if _, isTool := codexToolCallTypes[item.Type]; isTool {
			signals.ToolCalls++
			return true
		}
		if item.Type != "message" || (item.Role != "user" && item.Role != "assistant") {
			return true
		}
		visible++
		if !signals.AuthFailure && hasAuthFailureMarker(strings.ToLower(string(raw))) {
			signals.AuthFailure = true
		}
		if item.Role == "assistant" {
			signals.AssistantMessages++
			return true
		}
		text := firstCodexUserText(env.Payload)
		if isSyntheticPrompt(text) {
			return true
		}
		signals.UserMessages++
		if signals.FirstPrompt == "" {
			signals.FirstPrompt = text
		}
		return true
	})
	return signals, visible
}

type codexTitle struct {
	name      string
	updatedAt time.Time
}

// loadCodexTitleIndex reads session_index.jsonl into a root-id -> title map.
// A missing or malformed index yields an empty map; titles are best-effort.
func loadCodexTitleIndex(path string) map[string]codexTitle {
	titles := map[string]codexTitle{}
	_ = scanLines(path, func(raw []byte) bool {
		var row struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
			UpdatedAt  string `json:"updated_at"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return true
		}
		if row.ID == "" {
			return true
		}
		titles[row.ID] = codexTitle{name: row.ThreadName, updatedAt: parseTime(row.UpdatedAt)}
		return true
	})
	return titles
}

// codexIDFromFileName extracts the trailing thread UUID from a rollout file
// name of the form rollout-<timestamp>-<uuid>.jsonl. It returns "" if the name
// does not end in a UUID.
func codexIDFromFileName(name string) string {
	base := strings.TrimSuffix(name, ".jsonl")
	// A UUID is 36 chars (8-4-4-4-12). The name ends with "-<uuid>".
	if len(base) < 37 {
		return ""
	}
	candidate := base[len(base)-36:]
	if base[len(base)-37] != '-' {
		return ""
	}
	if !looksLikeUUID(candidate) {
		return ""
	}
	return candidate
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
