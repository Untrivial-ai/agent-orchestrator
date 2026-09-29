// Package workspacewatch turns operating-system filesystem notifications for a
// session worktree into a small invalidation stream. The actual Git status and
// diff remain owned by the session service; consumers only use these events to
// know when to read that model again.
package workspacewatch

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// maxWatchedDirectories bounds how many directories one workspace stream has
// concurrently registered. fsnotify consumes one descriptor per watched
// directory (a kqueue descriptor on macOS, an inotify watch slot on Linux), so
// watching every directory of an unbounded tree eventually exhausts
// RLIMIT_NOFILE or max_user_watches and every subsequent Add fails.
// Directories beyond the cap stay unwatched; the model refresh they guard
// remains reachable through the other invalidation paths instead of the whole
// SSE subscription failing.
var maxWatchedDirectories = 4096

// dirWatchLimiter keeps the number of concurrently registered directory
// watches at or below maxWatchedDirectories for the lifetime of one workspace
// stream: both the initial walk and directories created later go through add,
// while directories that disappear reclaim their budget through release, so a
// session with directory churn (build outputs, node_modules, branch switches)
// keeps reusing freed budget instead of exhausting it. Failed Adds are
// counted as skipped and surfaced by warn as a single aggregated log line
// instead of one WARN per directory.
type dirWatchLimiter struct {
	watcher   *fsnotify.Watcher
	limit     int
	mu        sync.Mutex
	watched   map[string]struct{}
	used      int
	skipped   int
	lastError error
}

func newDirWatchLimiter(watcher *fsnotify.Watcher, limit int) *dirWatchLimiter {
	return &dirWatchLimiter{watcher: watcher, limit: limit, watched: make(map[string]struct{})}
}

// add registers dir if budget remains. It returns ctx.Err() when the context
// was cancelled; every other failure is counted as skipped and does not abort
// the caller.
func (l *dirWatchLimiter) add(ctx context.Context, dir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.watched[dir]; ok {
		// Already watched: fsnotify.Add is a no-op for an existing watch, so
		// do not consume budget or re-count the slot.
		return nil
	}
	if l.used >= l.limit {
		l.skipped++
		return nil
	}
	if err := l.watcher.Add(dir); err != nil {
		l.skipped++
		l.lastError = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	l.watched[dir] = struct{}{}
	l.used++
	return nil
}

// release drops the watch on dir and returns its budget so it can be reused.
// It is a no-op for paths this limiter never watched (regular files, ignored
// directories, already-released paths) and safe to call on a nil limiter.
// Best-effort: fsnotify may have already dropped the watch implicitly (for
// example on Rename); reclaiming the accounting slot is what matters.
func (l *dirWatchLimiter) release(dir string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.watched[dir]; !ok {
		return
	}
	delete(l.watched, dir)
	_ = l.watcher.Remove(dir)
	l.used--
}

// warn emits at most one aggregated WARN per call describing how many
// directories could not be watched and why.
func (l *dirWatchLimiter) warn(ctx context.Context, stage string) {
	l.mu.Lock()
	skipped, lastError, limit := l.skipped, l.lastError, l.limit
	l.mu.Unlock()
	if skipped == 0 {
		return
	}
	slog.WarnContext(ctx, "workspace watch skipped directories", "stage", stage, "skipped", skipped, "watch_limit", limit, "last_error", lastError)
}

// Watch subscribes to relevant changes below the workspace roots until ctx is cancelled. The
// returned channel is intentionally edge-triggered and buffered by one: a burst
// of editor writes only needs to tell the caller that its Git read model is
// stale, not reproduce every low-level filesystem operation.
func Watch(ctx context.Context, roots ...string) (<-chan struct{}, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("at least one workspace path is required")
	}

	unique := make(map[string]struct{}, len(roots))
	uniqueRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		cleanRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace path: %w", err)
		}
		if _, seen := unique[cleanRoot]; seen {
			continue
		}
		unique[cleanRoot] = struct{}{}
		uniqueRoots = append(uniqueRoots, cleanRoot)
	}
	if len(uniqueRoots) == 1 {
		return watchRoot(ctx, uniqueRoots[0])
	}

	watchCtx, cancel := context.WithCancel(ctx)
	sources := make([]<-chan struct{}, 0, len(uniqueRoots))
	for _, cleanRoot := range uniqueRoots {
		source, err := watchRoot(watchCtx, cleanRoot)
		if err != nil {
			cancel()
			return nil, err
		}
		sources = append(sources, source)
	}
	changes := make(chan struct{}, 1)
	var group sync.WaitGroup
	group.Add(len(sources))
	for _, source := range sources {
		go func() {
			defer group.Done()
			for range source {
				select {
				case changes <- struct{}{}:
				default:
				}
			}
		}()
	}
	go func() {
		group.Wait()
		cancel()
		close(changes)
	}()
	return changes, nil
}

func watchRoot(ctx context.Context, root string) (<-chan struct{}, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace path: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat workspace path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace path is not a directory")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create workspace watcher: %w", err)
	}

	git := discoverGitWorkspace(ctx, root)
	limiter := newDirWatchLimiter(watcher, maxWatchedDirectories)
	if err := addInitialDirectories(ctx, limiter, root, git); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	limiter.warn(ctx, "initial")

	changes := make(chan struct{}, 1)
	go run(ctx, watcher, root, git, limiter, changes)
	return changes, nil
}

type gitWorkspace struct {
	available     bool
	files         []string
	metadataFiles map[string]struct{}
}

func discoverGitWorkspace(ctx context.Context, root string) gitWorkspace {
	// Git searches upward for a repository, so rev-parse can resolve a parent
	// repo (or a stray .git above the workspace) instead of one that belongs
	// to this workspace. Trusting that answer watches almost nothing: the git
	// file list is empty for the workspace, so only the root directory is
	// registered and the non-recursive fsnotify backends never see changes in
	// subdirectories. Only use the git-derived watch set when the repository
	// actually has this workspace as its toplevel.
	topRaw, err := aoprocess.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return gitWorkspace{}
	}
	// Git prints forward slashes on Windows and canonicalizes the toplevel
	// through symlinks, so the reported path may differ textually from root
	// while naming the same directory. Compare the strings first (folding
	// case on Windows, where the filesystem is case-insensitive), then fall
	// back to filesystem identity for symlinked roots.
	toplevel := filepath.Clean(filepath.FromSlash(strings.TrimSpace(string(topRaw))))
	samePath := toplevel == root || (runtime.GOOS == "windows" && strings.EqualFold(toplevel, root))
	if !samePath {
		topInfo, topErr := os.Stat(toplevel)
		rootInfo, rootErr := os.Stat(root)
		if topErr != nil || rootErr != nil || !os.SameFile(topInfo, rootInfo) {
			return gitWorkspace{}
		}
	}
	gitDirRaw, err := aoprocess.CommandContext(ctx, "git", "-C", root, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return gitWorkspace{}
	}
	filesRaw, err := aoprocess.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return gitWorkspace{}
	}
	files := make([]string, 0)
	for _, raw := range bytes.Split(filesRaw, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		files = append(files, filepath.FromSlash(string(raw)))
	}
	gitDir := strings.TrimSpace(string(gitDirRaw))
	metadataFiles := map[string]struct{}{
		filepath.Join(gitDir, "HEAD"): {},
	}
	commonDir := gitDir
	if raw, commonErr := aoprocess.CommandContext(ctx, "git", "-C", root, "rev-parse", "--git-common-dir").Output(); commonErr == nil {
		commonDir = strings.TrimSpace(string(raw))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(root, commonDir)
		}
	}
	metadataFiles[filepath.Join(commonDir, "packed-refs")] = struct{}{}
	if raw, refErr := aoprocess.CommandContext(ctx, "git", "-C", root, "symbolic-ref", "-q", "HEAD").Output(); refErr == nil {
		ref := strings.TrimSpace(string(raw))
		if ref != "" {
			metadataFiles[filepath.Join(commonDir, filepath.FromSlash(ref))] = struct{}{}
		}
	}
	return gitWorkspace{available: true, files: files, metadataFiles: metadataFiles}
}

func addInitialDirectories(ctx context.Context, limiter *dirWatchLimiter, root string, git gitWorkspace) error {
	dirs := map[string]struct{}{root: {}}
	if git.available {
		for _, rel := range git.files {
			dir := filepath.Dir(filepath.Join(root, rel))
			for isWithin(root, dir) {
				dirs[dir] = struct{}{}
				if dir == root {
					break
				}
				dir = filepath.Dir(dir)
			}
		}
		for metadataFile := range git.metadataFiles {
			if info, err := os.Stat(filepath.Dir(metadataFile)); err == nil && info.IsDir() {
				dirs[filepath.Dir(metadataFile)] = struct{}{}
			}
		}
	} else if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		dirs[path] = struct{}{}
		return nil
	}); err != nil {
		return fmt.Errorf("walk workspace directories: %w", err)
	}

	// Register watches shallowest-first so that when the platform refuses
	// further watches (fd or inotify limits) the remaining coverage degrades
	// toward the leaves, which matter least for workspace-level invalidation.
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Slice(ordered, func(i, j int) bool {
		iDepth := directoryDepth(root, ordered[i])
		jDepth := directoryDepth(root, ordered[j])
		if iDepth != jDepth {
			return iDepth < jDepth
		}
		return ordered[i] < ordered[j]
	})
	if len(ordered) > maxWatchedDirectories {
		ordered = ordered[:maxWatchedDirectories]
	}

	for _, dir := range ordered {
		if err := limiter.add(ctx, dir); err != nil {
			return err
		}
	}
	return nil
}

func directoryDepth(root, dir string) int {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func run(ctx context.Context, watcher *fsnotify.Watcher, root string, git gitWorkspace, limiter *dirWatchLimiter, changes chan struct{}) {
	defer close(changes)
	defer func() { _ = watcher.Close() }()
	defer limiter.warn(ctx, "runtime")
	notify := func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
			// The precise event may have been dropped. Force one model refresh;
			// subsequent filesystem notifications can still keep the stream live.
			notify()
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if handleEvent(ctx, limiter, root, git, event) {
				notify()
			}
		}
	}
}

func handleEvent(ctx context.Context, limiter *dirWatchLimiter, root string, git gitWorkspace, event fsnotify.Event) bool {
	// On macOS, kqueue can report CHMOD while Git is only reading files to
	// build the diff model. Treating those metadata-only notifications as
	// content changes creates a read -> event -> read feedback loop.
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	name, err := filepath.Abs(event.Name)
	if err != nil {
		return false
	}
	if _, ok := git.metadataFiles[name]; ok {
		return true
	}
	if !isWithin(root, name) || hasGitMetadataComponent(root, name) {
		return false
	}
	if git.available && gitIgnored(ctx, root, name) {
		return false
	}
	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		// A watched directory that goes away frees its descriptor and its
		// budget slot, so later directory creation can still be watched even
		// in workspaces with heavy directory churn. Paths the limiter never
		// watched (regular files, ignored directories) are no-ops.
		limiter.release(name)
	}
	if event.Has(fsnotify.Create) {
		if info, statErr := os.Stat(name); statErr == nil && info.IsDir() {
			_ = addCreatedTree(ctx, limiter, root, git, name)
		}
	}
	return true
}

func addCreatedTree(ctx context.Context, limiter *dirWatchLimiter, root string, git gitWorkspace, created string) error {
	return filepath.WalkDir(created, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if hasGitMetadataComponent(root, path) || (git.available && gitIgnored(ctx, root, path)) {
			return filepath.SkipDir
		}
		// Created directories draw from the same budget as the initial
		// walk, so a workspace that grows during the session cannot push
		// concurrent watches past the cap and back into fd exhaustion.
		return limiter.add(ctx, path)
	})
}

func gitIgnored(ctx context.Context, root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return aoprocess.CommandContext(ctx, "git", "-C", root, "check-ignore", "-q", "--", filepath.ToSlash(rel)).Run() == nil
}

func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hasGitMetadataComponent(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return true
		}
	}
	return false
}
