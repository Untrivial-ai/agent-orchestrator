// Package sessionimport discovers agent conversations that other coding CLIs
// (Claude Code, Codex, and any future provider) have already written to disk,
// so AO can import them as resumable sessions.
//
// Discovery is provider-agnostic: each provider implements Source, and the
// Service fans out across every registered Source, merges the results, flags
// the ones AO has already imported, and returns a single recency-ordered list.
// Adding another IDE is a matter of implementing Source for it; nothing else in
// the pipeline changes.
package sessionimport

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ImportableSession is one on-disk conversation AO could import. Every field is
// derived from the provider's own transcript, never from AO state, except
// AlreadyImported which the Service stamps against existing AO sessions.
type ImportableSession struct {
	// Provider is the harness that wrote the transcript (claude-code, codex, ...).
	Provider domain.AgentHarness
	// NativeSessionID is the provider's own session/thread id. It is what AO
	// binds to so the imported session can be resumed.
	NativeSessionID string
	// ConfigDir is the provider state root the transcript was found under
	// (e.g. ~/.claude or ~/.codex), so import can re-locate it deterministically.
	ConfigDir string
	// TranscriptPath is the absolute path to the JSONL transcript.
	TranscriptPath string
	// CWD is the working directory the conversation ran in, read from the
	// transcript itself (never reverse-engineered from a slugified dir name).
	CWD string
	// Branch is the git branch recorded in the transcript, when present.
	Branch string
	// Title is a human label: the provider's own title if it recorded one,
	// otherwise the first user prompt, otherwise the transcript file name.
	Title string
	// LastActivity is the most recent timestamp observed for the conversation.
	LastActivity time.Time
	// MessageCount is a best-effort count of visible messages. It is 0 (unknown)
	// for transcripts too large to scan cheaply; callers should fall back to
	// SizeBytes for display in that case.
	MessageCount int
	// SizeBytes is the transcript size on disk.
	SizeBytes int64
	// AlreadyImported is true when an AO session is already bound to
	// NativeSessionID. Stamped by the Service, not by a Source.
	AlreadyImported bool
	// Meaning is the import verdict from the transcript's own content. Sources
	// set it; the Service drops trivial conversations before returning.
	Meaning Meaning
	// FirstPrompt is the first typed human turn. It stays inside the daemon as
	// input for classifying an ambiguous conversation and is never sent to the
	// renderer, which has no use for it.
	FirstPrompt string
}

// DiscoverOptions bounds a discovery scan so a large history directory cannot
// turn a single request into an unbounded amount of work.
type DiscoverOptions struct {
	// Since drops conversations whose LastActivity is older than this instant.
	// The zero value applies no age filter.
	Since time.Time
	// MaxPerProvider caps how many conversations each Source returns, keeping the
	// most recent. 0 means no cap.
	MaxPerProvider int
	// MaxScanBytes caps the bytes read from each transcript's head and tail when
	// extracting metadata. 0 selects a sensible default.
	MaxScanBytes int64
	// IncludeCWD, when set, decides from a conversation's working directory
	// whether it is wanted at all. It is consulted as soon as the head is
	// parsed, before the full transcript read, so a scoped listing pays only a
	// cheap head read for everything it is going to discard.
	IncludeCWD func(cwd string) bool
	// MetadataOnly skips the full transcript read that produces message counts
	// and the import verdict, keeping only what the head and tail already give:
	// identity, working directory, branch, title, recency. Resolving one known
	// id needs nothing more, and reading every transcript end to end to answer
	// that is what made importing a long history take minutes.
	MetadataOnly bool
	// ExcludeRoots drops conversations whose working directory lies inside one
	// of these paths. AO passes its own data directory: asking a user's agent to
	// classify conversations records a transcript of that question, and it must
	// never come back as something to import.
	ExcludeRoots []string
	// IncludeTrivial keeps conversations classified MeaningTrivial — greetings,
	// aborted attempts, failed logins, smoke tests. The default (false) drops
	// them, so they never reach the sidebar, the board, or the import list.
	// Diagnostics set it to see what was withheld.
	IncludeTrivial bool
}

func (o DiscoverOptions) normalized() DiscoverOptions {
	if o.MaxScanBytes <= 0 {
		o.MaxScanBytes = defaultMaxScanBytes
	}
	return o
}

const (
	// defaultMaxScanBytes bounds head/tail metadata reads per transcript.
	defaultMaxScanBytes int64 = 256 * 1024
	// fullCountMaxBytes is the largest transcript we fully scan for an exact
	// message count. Above it MessageCount is left unknown (0).
	fullCountMaxBytes int64 = 8 * 1024 * 1024
)

// Source discovers importable conversations for one provider.
type Source interface {
	// Provider returns the harness this Source scans for.
	Provider() domain.AgentHarness
	// Discover returns the importable conversations found on disk. A missing
	// provider directory is not an error; it yields an empty slice.
	Discover(ctx context.Context, opts DiscoverOptions) ([]ImportableSession, error)
}

// ExistingNativeIDs reports the set of native session ids AO already has a
// session for, so discovery can flag duplicates. It returns a set keyed by the
// native id.
type ExistingNativeIDs func(ctx context.Context) (map[string]struct{}, error)

// Service fans discovery out across every registered Source.
type Service struct {
	sources  []Source
	existing ExistingNativeIDs
	// snapshots reuses a metadata scan across a burst of id lookups, which is
	// what importing a whole history is.
	snapshots *snapshotCache
}

// NewService builds a discovery service over the given sources. existing may be
// nil, in which case no session is flagged AlreadyImported.
func NewService(existing ExistingNativeIDs, sources ...Source) *Service {
	return &Service{sources: sources, existing: existing, snapshots: newSnapshotCache()}
}

// Sources returns the registered provider sources, in registration order.
func (s *Service) Sources() []Source { return s.sources }

// Discover scans every registered provider and returns a single list ordered by
// LastActivity, newest first. One provider failing does not fail the whole scan;
// its error is collected and returned alongside whatever the others found, so a
// single unreadable history directory never hides the rest.
func (s *Service) Discover(ctx context.Context, opts DiscoverOptions) ([]ImportableSession, error) {
	opts = opts.normalized()

	var (
		all  []ImportableSession
		errs []error
	)
	for _, src := range s.sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		found, err := src.Discover(ctx, opts)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, session := range found {
			if underAnyRoot(session.CWD, opts.ExcludeRoots) {
				continue
			}
			// Importing junk is worse than importing nothing: a trivial
			// conversation is withheld outright rather than shown in a
			// low-value section, which would still be clutter.
			if !opts.IncludeTrivial && !session.Meaning.Imported() {
				continue
			}
			all = append(all, session)
		}
	}

	if err := s.flagImported(ctx, all); err != nil {
		errs = append(errs, err)
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].LastActivity.Equal(all[j].LastActivity) {
			return all[i].NativeSessionID < all[j].NativeSessionID
		}
		return all[i].LastActivity.After(all[j].LastActivity)
	})

	return all, errors.Join(errs...)
}

// Locate returns the importable conversation for one provider + native id, or
// ok=false when it is no longer on disk. It scans that provider's source with no
// age or count limit so an older conversation the discovery window dropped can
// still be imported by id.
func (s *Service) Locate(ctx context.Context, provider domain.AgentHarness, nativeID string) (ImportableSession, bool, error) {
	nativeID = strings.TrimSpace(nativeID)
	// Normalize so the bounded head/tail reads use their real default size; a
	// zero MaxScanBytes would read nothing and leave cwd/title empty.
	// IncludeTrivial: an id the caller names explicitly is always importable,
	// even if the heuristic would have withheld it from the browse list.
	// MetadataOnly: resolving an id needs the conversation's identity and
	// working directory, never its message count or verdict. Reading every
	// transcript in full to answer that cost about twelve seconds per import
	// on a real history, which a bulk import multiplied into half an hour.
	opts := DiscoverOptions{IncludeTrivial: true, MetadataOnly: true}.normalized()
	for _, src := range s.sources {
		if src.Provider() != provider {
			continue
		}
		found, err := s.scanForLocate(ctx, src, opts)
		if err != nil {
			return ImportableSession{}, false, err
		}
		for _, f := range found {
			if f.NativeSessionID == nativeID {
				return f, true, nil
			}
		}
	}
	return ImportableSession{}, false, nil
}

func (s *Service) flagImported(ctx context.Context, sessions []ImportableSession) error {
	if s.existing == nil || len(sessions) == 0 {
		return nil
	}
	imported, err := s.existing(ctx)
	if err != nil {
		return err
	}
	for i := range sessions {
		if _, ok := imported[sessions[i].NativeSessionID]; ok {
			sessions[i].AlreadyImported = true
		}
	}
	return nil
}

// titleFrom picks the best available label given a provider title, a first user
// prompt, and a fallback (usually the transcript file name). It trims and
// single-lines each candidate and truncates to a display-friendly length.
func titleFrom(providerTitle, firstPrompt, fallback string) string {
	for _, candidate := range []string{providerTitle, firstPrompt, fallback} {
		if t := normalizeTitle(candidate); t != "" {
			return t
		}
	}
	return ""
}

const maxTitleLen = 120

func normalizeTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse to the first non-empty line so a multi-line prompt reads as a
	// single label.
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.Join(strings.Fields(s), " ")
	// Truncating by bytes can split a multi-byte rune and leave broken text in
	// the sidebar, so the cut is made on runes.
	if runes := []rune(s); len(runes) > maxTitleLen {
		s = strings.TrimSpace(string(runes[:maxTitleLen])) + "…"
	}
	return s
}

// underAnyRoot reports whether dir is one of roots or sits inside one.
func underAnyRoot(dir string, roots []string) bool {
	if dir == "" || len(roots) == 0 {
		return false
	}
	dir = filepath.Clean(dir)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if dir == root {
			return true
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
