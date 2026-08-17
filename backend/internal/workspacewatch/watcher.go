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
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

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
	if err := addInitialDirectories(ctx, watcher, root, git); err != nil {
		_ = watcher.Close()
		return nil, err
	}

	changes := make(chan struct{}, 1)
	go run(ctx, watcher, root, git, changes)
	return changes, nil
}

type gitWorkspace struct {
	available     bool
	files         []string
	metadataFiles map[string]struct{}
}

func discoverGitWorkspace(ctx context.Context, root string) gitWorkspace {
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

func addInitialDirectories(ctx context.Context, watcher *fsnotify.Watcher, root string, git gitWorkspace) error {
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

	for dir := range dirs {
		if err := watcher.Add(dir); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("watch workspace directory %q: %w", dir, err)
		}
	}
	return nil
}

func run(ctx context.Context, watcher *fsnotify.Watcher, root string, git gitWorkspace, changes chan struct{}) {
	defer close(changes)
	defer func() { _ = watcher.Close() }()
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
			if handleEvent(ctx, watcher, root, git, event) {
				notify()
			}
		}
	}
}

func handleEvent(ctx context.Context, watcher *fsnotify.Watcher, root string, git gitWorkspace, event fsnotify.Event) bool {
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
	if event.Has(fsnotify.Create) {
		if info, statErr := os.Stat(name); statErr == nil && info.IsDir() {
			_ = addCreatedTree(ctx, watcher, root, git, name)
		}
	}
	return true
}

func addCreatedTree(ctx context.Context, watcher *fsnotify.Watcher, root string, git gitWorkspace, created string) error {
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
		return watcher.Add(path)
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
