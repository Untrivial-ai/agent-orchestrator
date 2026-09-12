package sessionimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var ErrInvalidMetadataSource = errors.New("invalid provider source")

// MetadataSource provides bounded metadata reads without usage scans or an in-memory inventory.
// The visitor owns persisted fingerprints and decides whether to read each file.
type MetadataSource interface {
	Source
	MetadataRoot() (string, error)
	VisitMetadata(context.Context, func(string, os.FileInfo) error) error
	ReadMetadata(context.Context, string) (ImportableSession, bool, error)
	VisitTitles(context.Context, func(string, string) error) error
}

func canonicalRoot(path string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, e := filepath.EvalSymlinks(path)
	if e == nil {
		path = resolved
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	return filepath.Clean(path), nil
}
func (s *ClaudeSource) MetadataRoot() (string, error)                                 { return canonicalRoot(s.resolveConfigDir()) }
func (s *CodexSource) MetadataRoot() (string, error)                                  { return canonicalRoot(s.resolveHome()) }
func (s *ClaudeSource) VisitTitles(context.Context, func(string, string) error) error { return nil }
func (s *CodexSource) VisitTitles(ctx context.Context, visit func(string, string) error) error {
	root, err := s.MetadataRoot()
	if err != nil {
		return err
	}
	var visitErr error
	err = scanLines(filepath.Join(root, "session_index.jsonl"), func(raw []byte) bool {
		if visitErr = ctx.Err(); visitErr != nil {
			return false
		}
		var row struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(raw, &row) == nil && row.ID != "" {
			visitErr = visit(row.ID, metadataTitle(row.Name, "", ""))
		}
		return visitErr == nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return errors.Join(err, visitErr)
}
func (s *ClaudeSource) VisitMetadata(ctx context.Context, visit func(string, os.FileInfo) error) error {
	root, err := s.MetadataRoot()
	if err != nil {
		return err
	}
	return walkMetadata(ctx, filepath.Join(root, "projects"), 2, isClaudeTranscript, visit, true)
}
func (s *CodexSource) VisitMetadata(ctx context.Context, visit func(string, os.FileInfo) error) error {
	root, err := s.MetadataRoot()
	if err != nil {
		return err
	}
	err = walkCodexDates(ctx, filepath.Join(root, "sessions"), 0, visit)
	if s.includeArchived {
		err = errors.Join(err, walkCodexDates(ctx, filepath.Join(root, "archived_sessions"), 0, visit))
	}
	return err
}

// ReadDir batches bound memory even when a provider places thousands of files in one directory.
func walkMetadata(ctx context.Context, path string, depth int, accept func(string) bool, visit func(string, os.FileInfo) error, root bool) error {
	f, err := os.Open(path)
	if root && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var failures error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := f.ReadDir(128)
		// Claude has no date shards. Prioritize activity within bounded batches;
		// project-directory mtimes are a best-effort hint, not a global chronology.
		infos := make(map[string]os.FileInfo, len(entries))
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil {
				infos[entry.Name()] = info
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			a, b := infos[entries[i].Name()], infos[entries[j].Name()]
			if a != nil && b != nil && !a.ModTime().Equal(b.ModTime()) {
				return a.ModTime().After(b.ModTime())
			}
			return entries[i].Name() > entries[j].Name()
		})
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			p := filepath.Join(path, entry.Name())
			if entry.IsDir() {
				if depth > 1 {
					if e := walkMetadata(ctx, p, depth-1, accept, visit, false); e != nil && failures == nil {
						failures = e
					}
				}
				continue
			}
			if !accept(entry.Name()) {
				continue
			}
			info, e := entry.Info()
			if e == nil {
				e = visit(p, info)
			}
			if e != nil {
				if failures == nil {
					failures = e
				}
			}
		}
		if readErr == io.EOF {
			return failures
		}
		if readErr != nil {
			return errors.Join(failures, readErr)
		}
	}
}
func metadataBytes(ctx context.Context, root, path string) ([]byte, []byte, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, nil, nil, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, nil, nil, fmt.Errorf("%w: transcript escapes provider root", ErrInvalidMetadataSource)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, nil, fmt.Errorf("%w: transcript is not a regular file", ErrInvalidMetadataSource)
	}
	head, err := io.ReadAll(io.LimitReader(f, defaultMaxScanBytes))
	if err != nil {
		return nil, nil, nil, err
	}
	var tail []byte
	if info.Size() > defaultMaxScanBytes {
		if _, err = f.Seek(max(defaultMaxScanBytes, info.Size()-defaultMaxScanBytes), io.SeekStart); err != nil {
			return nil, nil, nil, err
		}
		tail, err = io.ReadAll(io.LimitReader(f, defaultMaxScanBytes))
		if err != nil {
			return nil, nil, nil, err
		}
		if i := strings.IndexByte(string(tail), '\n'); i >= 0 {
			tail = tail[i+1:]
		} else {
			tail = nil
		}
	}
	return head, tail, info, ctx.Err()
}
func (s *ClaudeSource) ReadMetadata(ctx context.Context, path string) (ImportableSession, bool, error) {
	root, err := s.MetadataRoot()
	if err != nil {
		return ImportableSession{}, false, err
	}
	head, tail, info, err := metadataBytes(ctx, root, path)
	if err != nil {
		return ImportableSession{}, false, err
	}
	meta := parseClaudeHead(head)
	last := meta.lastTimestamp
	title := meta.aiTitle
	for _, part := range [][]byte{head, tail} {
		for _, raw := range completeLines(part) {
			var row struct {
				Type      string `json:"type"`
				Title     string `json:"customTitle"`
				AI        string `json:"aiTitle"`
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal(raw, &row) != nil {
				continue
			}
			if row.Type == "custom-title" && row.Title != "" {
				title = row.Title
			}
			if row.Type == "ai-title" && row.AI != "" {
				title = row.AI
			}
			if t := parseTime(row.Timestamp); t.After(last) {
				last = t
			}
		}
	}
	if last.IsZero() {
		last = info.ModTime()
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return ImportableSession{Provider: s.Provider(), ConfigDir: root, NativeSessionID: id, TranscriptPath: path, CWD: meta.cwd, Branch: meta.gitBranch, Title: metadataTitle(title, meta.firstUserText, id), LastActivity: last, SizeBytes: info.Size(), TokenCount: -1}, true, nil
}
func (s *CodexSource) ReadMetadata(ctx context.Context, path string) (ImportableSession, bool, error) {
	root, err := s.MetadataRoot()
	if err != nil {
		return ImportableSession{}, false, err
	}
	head, tail, info, err := metadataBytes(ctx, root, path)
	if err != nil {
		return ImportableSession{}, false, err
	}
	meta, ok := parseCodexHead(head)
	if !ok || meta.isSubagent {
		return ImportableSession{}, false, nil
	}
	id := meta.rootID
	if id == "" {
		id = meta.id
	}
	if id == "" {
		id = codexIDFromFileName(filepath.Base(path))
	}
	if id == "" {
		return ImportableSession{}, false, nil
	}
	last := meta.lastTimestamp
	for _, raw := range completeLines(tail) {
		if t := parseTime(codexLineTimestamp(raw)); t.After(last) {
			last = t
		}
	}
	if last.IsZero() {
		last = info.ModTime()
	}
	return ImportableSession{Provider: s.Provider(), ConfigDir: root, NativeSessionID: id, TranscriptPath: path, CWD: meta.cwd, Branch: meta.branch, Title: titleFrom("", meta.firstUserText, id), LastActivity: last, SizeBytes: info.Size(), TokenCount: -1}, true, nil
}

// Preserve explicit titles separately from the short first-prompt fallback. The
// 16Ki-rune ceiling bounds untrusted provider metadata stored in the local cache.
func metadataTitle(explicit, prompt, fallback string) string {
	explicit = strings.Join(strings.Fields(explicit), " ")
	if explicit == "" {
		return titleFrom("", prompt, fallback)
	}
	r := []rune(explicit)
	if len(r) > 16384 {
		explicit = string(r[:16384])
	}
	return explicit
}

// Codex date levels have a fixed numeric domain, so global newest-first date
// traversal needs at most 10000 booleans, independent of transcript count.
func walkCodexDates(ctx context.Context, path string, level int, visit func(string, os.FileInfo) error) error {
	if level == 3 {
		return walkMetadata(ctx, path, 1, isCodexRollout, visit, false)
	}
	f, err := os.Open(path)
	if level == 0 && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	maxValue, width := 9999, 4
	if level == 1 {
		maxValue, width = 12, 2
	}
	if level == 2 {
		maxValue, width = 31, 2
	}
	present := make([]bool, maxValue+1)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := f.ReadDir(128)
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || len(entry.Name()) != width {
				continue
			}
			n, e := strconv.Atoi(entry.Name())
			if e == nil && n > 0 && n <= maxValue {
				present[n] = true
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	var failure error
	for n := maxValue; n > 0; n-- {
		if !present[n] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err := walkCodexDates(ctx, filepath.Join(path, fmt.Sprintf("%0*d", width, n)), level+1, visit)
		if failure == nil && err != nil {
			failure = err
		}
	}
	return failure
}
