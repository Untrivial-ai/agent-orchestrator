// Package attachmentstore owns the durable copy of files attached to session
// messages. Worktree copies are projections for agents; the canonical bytes
// live under AO's data directory so worktree teardown cannot erase history.
package attachmentstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	// WorkspaceDir is the worktree-relative directory named in chat messages.
	WorkspaceDir = ".ao/attachments"
	durableDir   = "attachments"
	// MaxFileBytes matches the HTTP attachment limit and also bounds legacy
	// imports from agent-writable worktrees.
	MaxFileBytes = 10 << 20

	// leaseFileName holds the uncommitted-lease manifest for one session. It
	// never matches validateName (no "attachment-"/"image-" prefix), so the
	// directory listers in ImportWorkspace/MaterializeWorkspace already skip it.
	leaseFileName = ".leases.json"

	// DefaultLeaseTTL bounds how long a staged-but-uncommitted chat draft
	// attachment survives before GCExpiredLeases reclaims it. Generous enough
	// that an ordinary compose-then-send round trip never races it, bounded
	// enough that an abandoned draft cannot grow session storage forever.
	DefaultLeaseTTL = 24 * time.Hour
)

var (
	// ErrExists reports that the generated name is already owned by history.
	ErrExists   = errors.New("attachment already exists")
	errEmpty    = errors.New("attachment is empty")
	errTooLarge = errors.New("attachment is too large")
)

// attachmentRefPattern recognizes a worktree-relative attachment reference
// embedded in free-form chat message text, the shape the composer writes
// (see ChatComposer's withAttachmentReferences: "- .ao/attachments/<name>").
var attachmentRefPattern = regexp.MustCompile(`\.ao/attachments/[A-Za-z0-9._-]+`)

// Store persists canonical attachment bytes beneath an AO data directory.
//
// mu serializes lease-manifest reads/writes across sessions. Lease churn is rare
// (staging a file, sending a message, an app restart) next to the bytes I/O it
// guards, so one process-wide lock is simpler than a per-session lock registry
// and cannot leak entries the way a lazily created per-session map would.
type Store struct {
	dataDir string
	mu      sync.Mutex
}

// New returns a store rooted at dataDir.
func New(dataDir string) *Store {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = ""
	}
	return &Store{dataDir: dataDir}
}

// Put writes the canonical copy before projecting it into the live worktree.
// Returning success therefore means both the history copy and the agent-visible
// copy exist.
func (s *Store) Put(ctx context.Context, id domain.SessionID, workspacePath, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(id); err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return err
	}
	if len(data) == 0 {
		return errEmpty
	}
	if len(data) > MaxFileBytes {
		return errTooLarge
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("attachment workspace path is empty")
	}
	if s.dataDir == "" {
		return errors.New("attachment data directory is empty")
	}
	sessionRoot, err := s.openCanonicalSession(ctx, id, true)
	if err != nil {
		return fmt.Errorf("open canonical attachment directory: %w", err)
	}
	defer func() { _ = sessionRoot.Close() }()
	if err := writeReaderAtomicRoot(ctx, sessionRoot, ".", name, bytes.NewReader(data), false); err != nil {
		return fmt.Errorf("write canonical attachment: %w", err)
	}
	if err := writeReaderAtomicUnder(ctx, workspacePath, filepath.FromSlash(WorkspaceDir), name, bytes.NewReader(data), false); err != nil {
		if removeErr := sessionRoot.Remove(name); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return errors.Join(fmt.Errorf("write workspace attachment: %w", err), fmt.Errorf("rollback canonical attachment: %w", removeErr))
		}
		return fmt.Errorf("write workspace attachment: %w", err)
	}
	return nil
}

// ImportWorkspace migrates legacy worktree-only attachments into canonical
// storage. Existing canonical files win because they are already the durable
// source of truth. Symlinks and non-regular entries are ignored.
func (s *Store) ImportWorkspace(ctx context.Context, id domain.SessionID, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(id); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("attachment workspace path is empty")
	}
	workspaceRoot, err := os.OpenRoot(workspacePath)
	if errors.Is(err, fs.ErrNotExist) {
		// A stale or already-removed worktree has no legacy bytes to import.
		// Teardown must still be able to finish for that session.
		return nil
	}
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { _ = workspaceRoot.Close() }()
	aoRoot, err := openChildDir(ctx, workspaceRoot, ".ao", false, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open workspace AO directory: %w", err)
	}
	defer func() { _ = aoRoot.Close() }()
	sourceRoot, err := openChildDir(ctx, aoRoot, "attachments", false, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open workspace attachments: %w", err)
	}
	defer func() { _ = sourceRoot.Close() }()
	entries, err := fs.ReadDir(sourceRoot.FS(), ".")
	if err != nil {
		return fmt.Errorf("list workspace attachments: %w", err)
	}
	var sessionRoot *os.Root
	defer func() {
		if sessionRoot != nil {
			_ = sessionRoot.Close()
		}
	}()

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || validateName(entry.Name()) != nil {
			continue
		}
		file, _, openErr := openRegularFile(ctx, sourceRoot, entry.Name())
		if errors.Is(openErr, fs.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return fmt.Errorf("open workspace attachment %q: %w", entry.Name(), openErr)
		}
		if sessionRoot == nil {
			sessionRoot, err = s.openCanonicalSession(ctx, id, true)
			if err != nil {
				_ = file.Close()
				return fmt.Errorf("open canonical attachment root: %w", err)
			}
		}
		copyErr := writeReaderAtomicRoot(ctx, sessionRoot, ".", entry.Name(), file, false)
		closeErr := file.Close()
		if errors.Is(copyErr, ErrExists) || errors.Is(copyErr, errEmpty) || errors.Is(copyErr, errTooLarge) {
			continue
		}
		if copyErr != nil {
			return fmt.Errorf("import workspace attachment %q: %w", entry.Name(), copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close workspace attachment %q: %w", entry.Name(), closeErr)
		}
	}
	return nil
}

// MaterializeWorkspace projects every canonical attachment into a restored
// worktree before its controller is relaunched. The boolean reports whether at
// least one projection was written, so callers only add attachment-specific
// workspace configuration when it is needed.
func (s *Store) MaterializeWorkspace(ctx context.Context, id domain.SessionID, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("attachment workspace path is empty")
	}
	if s.dataDir == "" {
		return false, nil
	}
	if err := validateSessionID(id); err != nil {
		return false, err
	}
	sessionRoot, err := s.openCanonicalSession(ctx, id, false)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open canonical attachment root: %w", err)
	}
	defer func() { _ = sessionRoot.Close() }()
	entries, err := fs.ReadDir(sessionRoot.FS(), ".")
	if err != nil {
		return false, fmt.Errorf("list canonical attachments: %w", err)
	}

	materialized := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if entry.Type()&os.ModeSymlink != 0 || validateName(entry.Name()) != nil {
			continue
		}
		file, _, openErr := openRegularFile(ctx, sessionRoot, entry.Name())
		if errors.Is(openErr, fs.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return false, fmt.Errorf("open canonical attachment %q: %w", entry.Name(), openErr)
		}
		copyErr := writeReaderAtomicUnder(ctx, workspacePath, filepath.FromSlash(WorkspaceDir), entry.Name(), file, true)
		closeErr := file.Close()
		if copyErr != nil {
			return false, fmt.Errorf("materialize attachment %q: %w", entry.Name(), copyErr)
		}
		if closeErr != nil {
			return false, fmt.Errorf("close canonical attachment %q: %w", entry.Name(), closeErr)
		}
		materialized = true
	}
	return materialized, nil
}

// Open opens a regular canonical attachment for HTTP serving.
func (s *Store) Open(ctx context.Context, id domain.SessionID, name string) (*os.File, fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if s.dataDir == "" {
		return nil, nil, fs.ErrNotExist
	}
	if err := validateSessionID(id); err != nil {
		return nil, nil, fs.ErrNotExist
	}
	if err := validateName(name); err != nil {
		return nil, nil, fs.ErrNotExist
	}
	sessionRoot, err := s.openCanonicalSession(ctx, id, false)
	if err != nil {
		return nil, nil, fs.ErrNotExist
	}
	defer func() { _ = sessionRoot.Close() }()
	return openRegularFile(ctx, sessionRoot, name)
}

// RemoveSession removes canonical files only when the owning session row was
// itself permanently deleted. Kill and cleanup intentionally do not call it.
func (s *Store) RemoveSession(ctx context.Context, id domain.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.dataDir == "" {
		return nil
	}
	if err := validateSessionID(id); err != nil {
		return err
	}
	root, err := s.openCanonicalRoot(ctx, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(string(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("canonical attachment session path is not a directory")
	}
	if err := root.RemoveAll(string(id)); err != nil {
		return err
	}
	return ctx.Err()
}

// NameFromWorkspacePath recognizes a direct file in the attachment projection.
// Nested paths and traversal are deliberately rejected.
func NameFromWorkspacePath(raw string) (string, bool) {
	raw = strings.ReplaceAll(raw, `\`, "/")
	raw = strings.TrimPrefix(raw, "/")
	parts := strings.Split(raw, "/")
	if len(parts) != 3 || parts[0] != ".ao" || parts[1] != "attachments" {
		return "", false
	}
	if validateName(parts[2]) != nil {
		return "", false
	}
	return parts[2], true
}

// NamesFromMessage extracts the attachment names referenced by worktree-relative
// paths embedded in a delivered message's text. It is used only to decide which
// staged leases a successfully sent message committed, never to resolve a path
// for reading, so a malformed or unsafe-looking match is silently skipped rather
// than treated as an error.
func NamesFromMessage(message string) []string {
	if !strings.Contains(message, ".ao/attachments/") {
		return nil
	}
	matches := attachmentRefPattern.FindAllString(message, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	names := make([]string, 0, len(matches))
	for _, raw := range matches {
		name, ok := NameFromWorkspacePath(raw)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// leaseRecord tracks one staged-but-uncommitted attachment. workspacePath is
// carried alongside the name so Release and GCExpiredLeases can clean up the
// worktree projection without a second lookup against session state that may
// itself have already been torn down.
type leaseRecord struct {
	Name          string    `json:"name"`
	WorkspacePath string    `json:"workspacePath,omitempty"`
	LeasedAt      time.Time `json:"leasedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type leaseManifest struct {
	Leases []leaseRecord `json:"leases"`
}

// Lease records name as staged-but-uncommitted for id, expiring after ttl
// unless Commit runs first. The caller must already have written the canonical
// and workspace copies (via Put): Lease only tracks ownership so a later
// Release or GCExpiredLeases knows it is safe to delete them.
func (s *Store) Lease(ctx context.Context, id domain.SessionID, name, workspacePath string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(id); err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	sessionRoot, err := s.openCanonicalSession(ctx, id, true)
	if err != nil {
		return fmt.Errorf("open canonical attachment directory: %w", err)
	}
	defer func() { _ = sessionRoot.Close() }()

	manifest, err := readLeaseManifest(sessionRoot)
	if err != nil {
		return fmt.Errorf("read lease manifest: %w", err)
	}
	manifest.Leases = append(manifest.Leases, leaseRecord{
		Name:          name,
		WorkspacePath: workspacePath,
		LeasedAt:      now,
		ExpiresAt:     now.Add(ttl),
	})
	return writeLeaseManifest(ctx, sessionRoot, manifest)
}

// Commit marks names as permanently owned by accepted conversation history, so
// Release and GCExpiredLeases can no longer delete them. Committing a name that
// was never leased (already committed earlier, or never staged) is a no-op: a
// retried send, or a message that names a path AO never staged, cannot fail
// here.
func (s *Store) Commit(ctx context.Context, id domain.SessionID, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sessionRoot, err := s.openCanonicalSession(ctx, id, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open canonical attachment directory: %w", err)
	}
	defer func() { _ = sessionRoot.Close() }()

	manifest, err := readLeaseManifest(sessionRoot)
	if err != nil {
		return fmt.Errorf("read lease manifest: %w", err)
	}
	drop := make(map[string]bool, len(names))
	for _, name := range names {
		drop[name] = true
	}
	kept := manifest.Leases[:0]
	changed := false
	for _, lease := range manifest.Leases {
		if drop[lease.Name] {
			changed = true
			continue
		}
		kept = append(kept, lease)
	}
	if !changed {
		return nil
	}
	manifest.Leases = kept
	return writeLeaseManifest(ctx, sessionRoot, manifest)
}

// Release deletes the canonical and workspace copies of any of names that are
// still uncommitted leases, and removes their lease entry. A name already
// committed (so no longer in the manifest) is left completely untouched: this
// is what keeps a concurrent Release from ever deleting a file after Commit
// accepted it into history, no matter which one the caller issued first.
func (s *Store) Release(ctx context.Context, id domain.SessionID, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sessionRoot, err := s.openCanonicalSession(ctx, id, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open canonical attachment directory: %w", err)
	}
	defer func() { _ = sessionRoot.Close() }()

	manifest, err := readLeaseManifest(sessionRoot)
	if err != nil {
		return fmt.Errorf("read lease manifest: %w", err)
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	kept := manifest.Leases[:0]
	var toDelete []leaseRecord
	for _, lease := range manifest.Leases {
		if want[lease.Name] {
			toDelete = append(toDelete, lease)
			continue
		}
		kept = append(kept, lease)
	}
	if len(toDelete) == 0 {
		return nil
	}
	manifest.Leases = kept
	for _, lease := range toDelete {
		deleteLeasedCopies(sessionRoot, lease)
	}
	return writeLeaseManifest(ctx, sessionRoot, manifest)
}

// GCExpiredLeases deletes the canonical and workspace copies of every
// uncommitted lease that expired before now, across every session, and returns
// how many it reclaimed. It is meant to run once at daemon startup so a draft
// abandoned by a renderer crash or an app quit before its lease was released
// does not survive indefinitely.
func (s *Store) GCExpiredLeases(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	root, err := s.openCanonicalRoot(ctx, false)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open canonical attachment root: %w", err)
	}
	defer func() { _ = root.Close() }()

	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return 0, fmt.Errorf("list canonical attachment sessions: %w", err)
	}

	reclaimed := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return reclaimed, err
		}
		if !entry.IsDir() || validateSessionID(domain.SessionID(entry.Name())) != nil {
			continue
		}
		sessionRoot, err := openChildDir(ctx, root, entry.Name(), false, 0)
		if err != nil {
			continue
		}
		manifest, readErr := readLeaseManifest(sessionRoot)
		if readErr != nil {
			_ = sessionRoot.Close()
			continue
		}
		kept := manifest.Leases[:0]
		changed := false
		for _, lease := range manifest.Leases {
			if lease.ExpiresAt.After(now) {
				kept = append(kept, lease)
				continue
			}
			deleteLeasedCopies(sessionRoot, lease)
			reclaimed++
			changed = true
		}
		if changed {
			manifest.Leases = kept
			_ = writeLeaseManifest(ctx, sessionRoot, manifest)
		}
		_ = sessionRoot.Close()
	}
	return reclaimed, nil
}

// deleteLeasedCopies removes a lease's canonical file and, best-effort, its
// worktree projection. The canonical delete is what actually reclaims the
// session's durable storage; a missing worktree (already torn down, or the
// session was never restored) is expected and not an error.
func deleteLeasedCopies(sessionRoot *os.Root, lease leaseRecord) {
	if validateName(lease.Name) != nil {
		return
	}
	if err := sessionRoot.Remove(lease.Name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if strings.TrimSpace(lease.WorkspacePath) == "" {
		return
	}
	workspaceRoot, err := os.OpenRoot(lease.WorkspacePath)
	if err != nil {
		return
	}
	defer func() { _ = workspaceRoot.Close() }()
	projected, err := openChildDir(context.Background(), workspaceRoot, filepath.FromSlash(WorkspaceDir), false, 0)
	if err != nil {
		return
	}
	defer func() { _ = projected.Close() }()
	_ = projected.Remove(lease.Name)
}

func readLeaseManifest(sessionRoot *os.Root) (leaseManifest, error) {
	var manifest leaseManifest
	file, err := sessionRoot.Open(leaseFileName)
	if errors.Is(err, fs.ErrNotExist) {
		return manifest, nil
	}
	if err != nil {
		return manifest, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		return manifest, err
	}
	if len(data) == 0 {
		return manifest, nil
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return leaseManifest{}, err
	}
	return manifest, nil
}

func writeLeaseManifest(ctx context.Context, sessionRoot *os.Root, manifest leaseManifest) error {
	if len(manifest.Leases) == 0 {
		if err := sessionRoot.Remove(leaseFileName); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return writeReaderAtomicRoot(ctx, sessionRoot, ".", leaseFileName, bytes.NewReader(data), true)
}

func (s *Store) openCanonicalRoot(ctx context.Context, create bool) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.dataDir == "" {
		return nil, fs.ErrNotExist
	}
	if create {
		if err := os.MkdirAll(s.dataDir, 0o750); err != nil {
			return nil, err
		}
	} else {
		info, err := os.Stat(s.dataDir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fs.ErrNotExist
		}
	}
	dataRoot, err := os.OpenRoot(s.dataDir)
	if err != nil {
		return nil, err
	}
	root, err := openChildDir(ctx, dataRoot, durableDir, create, 0o700)
	_ = dataRoot.Close()
	return root, err
}

func (s *Store) openCanonicalSession(ctx context.Context, id domain.SessionID, create bool) (*os.Root, error) {
	root, err := s.openCanonicalRoot(ctx, create)
	if err != nil {
		return nil, err
	}
	sessionRoot, err := openChildDir(ctx, root, string(id), create, 0o700)
	_ = root.Close()
	return sessionRoot, err
}

func openChildDir(ctx context.Context, parent *os.Root, name string, create bool, perm fs.FileMode) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := parent.Mkdir(name, perm); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("attachment path is not a directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	currentInfo, err := parent.Lstat(name)
	if err != nil || !currentInfo.IsDir() || currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, currentInfo) {
		_ = child.Close()
		return nil, errors.New("attachment directory changed while opening")
	}
	return child, nil
}

func validateSessionID(id domain.SessionID) error {
	raw := string(id)
	if raw == "" || raw == "." || raw == ".." || strings.ContainsAny(raw, `/\`) || strings.ContainsRune(raw, 0) {
		return fmt.Errorf("invalid attachment session id %q", raw)
	}
	return nil
}

func validateName(name string) error {
	if name == "" || len(name) > 255 || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid attachment name %q", name)
	}
	var remainder string
	if after, ok := strings.CutPrefix(name, "attachment-"); ok {
		remainder = after
	} else if after, ok := strings.CutPrefix(name, "image-"); ok {
		remainder = after
	} else {
		return fmt.Errorf("invalid attachment name %q", name)
	}
	dot := strings.IndexByte(remainder, '.')
	if dot <= 0 || dot == len(remainder)-1 || !validNamePart(remainder[:dot], false) || !validNamePart(remainder[dot+1:], true) {
		return fmt.Errorf("invalid attachment name %q", name)
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".svg", ".svgz":
		return fmt.Errorf("unsupported attachment type %q", name)
	}
	return nil
}

func validNamePart(part string, allowDot bool) bool {
	if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.Contains(part, "..") {
		return false
	}
	for _, r := range part {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || allowDot && r == '.' {
			continue
		}
		return false
	}
	return true
}

func writeReaderAtomicUnder(ctx context.Context, rootPath, dir, name string, source io.Reader, replace bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return writeReaderAtomicRoot(ctx, root, dir, name, source, replace)
}

func writeReaderAtomicRoot(ctx context.Context, root *os.Root, dir, name string, source io.Reader, replace bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmpName := filepath.Join(dir, ".attachment-"+hex.EncodeToString(suffix[:]))
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = root.Remove(tmpName)
	}()
	written, err := io.Copy(tmp, io.LimitReader(contextReader{ctx: ctx, reader: source}, MaxFileBytes+1))
	if err != nil {
		return err
	}
	if written == 0 {
		return errEmpty
	}
	if written > MaxFileBytes {
		return errTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	target := filepath.Join(dir, name)
	if !replace {
		if err := root.Link(tmpName, target); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return ErrExists
			}
			return err
		}
		// Linking atomically commits the no-replace target. Temporary-link
		// cleanup is not part of that commit and the deferred removal retries it.
		_ = root.Remove(tmpName)
		return nil
	}
	if err := root.Rename(tmpName, target); err != nil {
		// Windows cannot atomically replace an existing destination with Rename.
		// This path is used only for a disposable worktree projection; canonical
		// creation always uses the no-replace hard-link commit above.
		if runtime.GOOS != "windows" {
			return err
		}
		info, statErr := root.Lstat(target)
		if statErr != nil || info.IsDir() {
			return err
		}
		if removeErr := root.Remove(target); removeErr != nil {
			return err
		}
		if retryErr := root.Rename(tmpName, target); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func openRegularFile(ctx context.Context, root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fs.ErrNotExist
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() == 0 || openedInfo.Size() > MaxFileBytes {
		_ = file.Close()
		return nil, nil, fs.ErrNotExist
	}
	return file, openedInfo, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
