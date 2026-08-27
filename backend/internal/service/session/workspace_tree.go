package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// WorkspaceTreeEntryType distinguishes files from directories in a
// WorkspaceTree listing.
type WorkspaceTreeEntryType string

// Workspace tree entry type values.
const (
	WorkspaceTreeFile WorkspaceTreeEntryType = "file"
	WorkspaceTreeDir  WorkspaceTreeEntryType = "dir"
)

// WorkspaceTreeEntry is one immediate child of a listed directory.
type WorkspaceTreeEntry struct {
	Name       string
	Path       string
	Type       WorkspaceTreeEntryType
	Status     WorkspaceFileStatus // files only; zero value for directories
	HasChanges bool                // directories only: a descendant file is non-unmodified
	Size       int64               // files only
	Binary     bool                // files only
}

// WorkspaceTree is the read model for one directory level of a session
// workspace's file explorer.
type WorkspaceTree struct {
	SessionID domain.SessionID
	Path      string
	Entries   []WorkspaceTreeEntry
	Truncated bool
}

// ListWorkspaceTree returns the immediate children of one directory in a
// session worktree, decorated with git status. Unlike ListWorkspaceFiles
// (which lists every changed file across the whole worktree), this lists
// every visible file and directory one level at a time — tracked and
// untracked-but-not-ignored — so callers can lazily expand a tree instead of
// paying for a full-worktree walk on every request.
//
// A directory that is empty, entirely gitignored, or does not exist simply
// returns no entries; ListWorkspaceTree does not distinguish those cases
// with a dedicated error, since the caller only needs to know that browsing
// deeper found nothing.
func (s *Service) ListWorkspaceTree(ctx context.Context, id domain.SessionID, rawDir string) (WorkspaceTree, error) {
	rec, err := s.sessionWorkspaceRecord(ctx, id)
	if err != nil {
		return WorkspaceTree{}, err
	}
	dir, err := cleanWorkspaceRelativeDir(rawDir)
	if err != nil {
		return WorkspaceTree{}, err
	}
	project, projectOK, err := s.sessionProject(ctx, rec)
	if err != nil {
		return WorkspaceTree{}, err
	}
	projectKind := domain.ProjectKindSingleRepo
	if projectOK {
		projectKind = project.Kind.WithDefault()
	}
	if projectKind == domain.ProjectKindScratch {
		entries, truncated, err := scratchWorkspaceTreeChildren(rec.Metadata.WorkspacePath, dir)
		if err != nil {
			return WorkspaceTree{}, err
		}
		return WorkspaceTree{SessionID: id, Path: dir, Entries: entries, Truncated: truncated}, nil
	}
	if projectKind == domain.ProjectKindWorkspace {
		return s.listWorkspaceProjectTree(ctx, rec, project, dir)
	}
	prs, err := s.workspaceComparePRs(ctx, rec.ID)
	if err != nil {
		return WorkspaceTree{}, err
	}
	resolve := func(rctx context.Context) workspaceCompareTarget {
		return resolveWorkspaceCompare(rctx, rec.Metadata.WorkspacePath, rec.Metadata.DiffBaseSHA, rec.Metadata.DiffBaseRef, defaultBranchForProject(project, projectOK), prs)
	}
	entries, truncated, err := s.workspaceTreeChildrenCached(ctx, id, rec.Metadata.WorkspacePath, "", nil, dir, resolve)
	if err != nil {
		return WorkspaceTree{}, err
	}
	return WorkspaceTree{SessionID: id, Path: dir, Entries: entries, Truncated: truncated}, nil
}

// workspaceTreeChildrenCached resolves one directory level of a worktree,
// reusing the same compare/status cache (see resolveWorkspaceChanges) that
// ListWorkspaceFiles/GetWorkspaceFile already share, and running the
// independent git-ls-files listing concurrently with it — mirroring
// workspaceFileSummariesCached's shape for the tree read model.
func (s *Service) workspaceTreeChildrenCached(
	ctx context.Context, id domain.SessionID, root, prefix string, excludePrefixes []string, dir string,
	resolve func(ctx context.Context) workspaceCompareTarget,
) ([]WorkspaceTreeEntry, bool, error) {
	var changes workspaceChangeSet
	var lsParts []string
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		_, changes, err = s.resolveWorkspaceChanges(gctx, id, root, resolve)
		return err
	})
	g.Go(func() (err error) {
		lsParts, err = gitLsFilesParts(gctx, root)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, false, err
	}
	paths, truncated := mergeWorkspaceFilePaths(lsParts, changes.changedPaths())
	entries := buildWorkspaceTreeEntries(root, prefix, excludePrefixes, dir, paths, changes)
	return entries, truncated, nil
}

// buildWorkspaceTreeEntries slices the flat, git-aware visible-file list
// (the same list buildWorkspaceFileSummaries uses for the full changed-files
// view) down to one directory level, aggregating an hasChanges flag onto any
// synthesized directory entries along the way.
func buildWorkspaceTreeEntries(root, prefix string, excludePrefixes []string, dir string, paths []string, changes workspaceChangeSet) []WorkspaceTreeEntry {
	dirPrefix := ""
	if dir != "" {
		dirPrefix = dir + "/"
	}
	entries := make([]WorkspaceTreeEntry, 0, len(paths))
	dirIndex := map[string]int{}
	for _, rel := range paths {
		if workspacePathExcluded(rel, excludePrefixes) {
			continue
		}
		if dirPrefix != "" && !strings.HasPrefix(rel, dirPrefix) {
			continue
		}
		remainder := strings.TrimPrefix(rel, dirPrefix)
		if remainder == "" {
			continue
		}
		status := changes.statuses[rel]
		if status == "" {
			status = WorkspaceFileUnmodified
		}
		if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
			name := remainder[:slash]
			idx, ok := dirIndex[name]
			if !ok {
				idx = len(entries)
				dirIndex[name] = idx
				entries = append(entries, WorkspaceTreeEntry{
					Name: name,
					Path: joinWorkspaceRelative(prefix, dirPrefix+name),
					Type: WorkspaceTreeDir,
				})
			}
			if status != WorkspaceFileUnmodified {
				entries[idx].HasChanges = true
			}
			continue
		}
		size, binary := workspaceFileSizeAndBinary(root, rel, status)
		entries = append(entries, WorkspaceTreeEntry{
			Name:   remainder,
			Path:   joinWorkspaceRelative(prefix, rel),
			Type:   WorkspaceTreeFile,
			Status: status,
			Size:   size,
			Binary: binary,
		})
	}
	sortWorkspaceTreeEntries(entries)
	return entries
}

func sortWorkspaceTreeEntries(entries []WorkspaceTreeEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == WorkspaceTreeDir
		}
		return entries[i].Name < entries[j].Name
	})
}

func (s *Service) listWorkspaceProjectTree(ctx context.Context, rec domain.SessionRecord, project domain.ProjectRecord, dir string) (WorkspaceTree, error) {
	rows, err := s.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		return WorkspaceTree{}, fmt.Errorf("list workspace project rows: %w", err)
	}
	if len(rows) == 0 {
		prs, err := s.workspaceComparePRs(ctx, rec.ID)
		if err != nil {
			return WorkspaceTree{}, err
		}
		resolve := func(rctx context.Context) workspaceCompareTarget {
			return resolveWorkspaceCompare(rctx, rec.Metadata.WorkspacePath, rec.Metadata.DiffBaseSHA, rec.Metadata.DiffBaseRef, defaultBranchForProject(project, true), prs)
		}
		entries, truncated, err := s.workspaceTreeChildrenCached(ctx, rec.ID, rec.Metadata.WorkspacePath, "", nil, dir, resolve)
		if err != nil {
			return WorkspaceTree{}, err
		}
		return WorkspaceTree{SessionID: rec.ID, Path: dir, Entries: entries, Truncated: truncated}, nil
	}

	prefixes := workspaceProjectPrefixes(rec.Metadata.WorkspacePath, rows)
	childPrefixes := nonEmptyWorkspacePrefixes(prefixes)
	defaultBranch := defaultBranchForProject(project, true)

	row, prefix, repoDir, ok := workspaceProjectDirTarget(rec.Metadata.WorkspacePath, rows, dir)
	if !ok {
		return WorkspaceTree{}, apierr.NotFound("WORKSPACE_PATH_NOT_FOUND", "Workspace path not found")
	}
	exclude := []string(nil)
	if prefix == "" {
		exclude = childPrefixes
	}
	baseRef := row.BaseRef
	if strings.TrimSpace(baseRef) == "" {
		// Compatibility for sessions created before per-repository base refs
		// were persisted, mirroring listWorkspaceProjectFiles.
		baseRef = defaultBranch
	}
	resolve := func(rctx context.Context) workspaceCompareTarget {
		return resolveWorkspaceProjectCompare(rctx, row.WorktreePath, row.BaseSHA, baseRef)
	}
	entries, truncated, err := s.workspaceTreeChildrenCached(ctx, rec.ID, row.WorktreePath, prefix, exclude, repoDir, resolve)
	if err != nil {
		return WorkspaceTree{}, err
	}

	if dir == "" {
		// Root listing: the primary repo's own top-level entries (computed
		// above, with child-repo prefixes excluded) sit alongside one
		// synthesized directory entry per child repo.
		for _, childPrefix := range childPrefixes {
			name := childPrefix
			if idx := strings.LastIndex(childPrefix, "/"); idx >= 0 {
				name = childPrefix[idx+1:]
			}
			entries = append(entries, WorkspaceTreeEntry{
				Name:       name,
				Path:       childPrefix,
				Type:       WorkspaceTreeDir,
				HasChanges: s.workspaceProjectPrefixHasChanges(ctx, rec.ID, rows, prefixes, childPrefix, defaultBranch),
			})
		}
		sortWorkspaceTreeEntries(entries)
	}

	return WorkspaceTree{SessionID: rec.ID, Path: dir, Entries: entries, Truncated: truncated}, nil
}

// workspaceProjectDirTarget resolves which worktree row owns a requested
// directory path and the path remaining once that row's prefix is stripped.
// It mirrors workspaceProjectFileTarget but matches a directory boundary
// (`dir == prefix` counts as owned) rather than requiring a file beneath it.
func workspaceProjectDirTarget(root string, rows []domain.SessionWorktreeRecord, dir string) (domain.SessionWorktreeRecord, string, string, bool) {
	var best domain.SessionWorktreeRecord
	bestPrefix := ""
	bestRel := dir
	found := false
	for _, row := range rows {
		prefix, ok := workspaceProjectPrefix(root, row.WorktreePath)
		if !ok {
			continue
		}
		if prefix == "" {
			if !found {
				best = row
				bestPrefix = ""
				bestRel = dir
				found = true
			}
			continue
		}
		if dir != prefix && !strings.HasPrefix(dir, prefix+"/") {
			continue
		}
		if !found || len(prefix) > len(bestPrefix) {
			best = row
			bestPrefix = prefix
			bestRel = strings.TrimPrefix(strings.TrimPrefix(dir, prefix), "/")
			found = true
		}
	}
	return best, bestPrefix, bestRel, found
}

func (s *Service) workspaceProjectPrefixHasChanges(ctx context.Context, id domain.SessionID, rows []domain.SessionWorktreeRecord, prefixes map[string]string, targetPrefix, defaultBranch string) bool {
	for _, row := range rows {
		prefix, ok := prefixes[row.RepoName]
		if !ok || prefix != targetPrefix {
			continue
		}
		baseRef := row.BaseRef
		if strings.TrimSpace(baseRef) == "" {
			baseRef = defaultBranch
		}
		resolve := func(rctx context.Context) workspaceCompareTarget {
			return resolveWorkspaceProjectCompare(rctx, row.WorktreePath, row.BaseSHA, baseRef)
		}
		_, changes, err := s.resolveWorkspaceChanges(ctx, id, row.WorktreePath, resolve)
		if err != nil {
			return false
		}
		for _, status := range changes.statuses {
			if status != WorkspaceFileUnmodified {
				return true
			}
		}
		return false
	}
	return false
}

// scratchWorkspaceTreeChildren lists one directory level of a scratch
// (non-git) workspace directly from the filesystem. It mirrors
// scratchWorkspaceFiles' confinement and symlink handling: symlinked
// directories are excluded (scratchWorkspaceFiles never recurses into them
// either, since filepath.WalkDir does not follow directory symlinks), and
// every file is reported as WorkspaceFileAdded since scratch workspaces have
// no git baseline to compare against.
func scratchWorkspaceTreeChildren(root, dir string) ([]WorkspaceTreeEntry, bool, error) {
	rootResolved, err := resolvedWorkspaceRoot(root)
	if err != nil {
		return nil, false, err
	}
	target := rootResolved
	if dir != "" {
		target = filepath.Join(rootResolved, filepath.FromSlash(dir))
	}
	targetResolved, resolveErr := resolvedFilesystemPath(target)
	if resolveErr != nil || (targetResolved != rootResolved && !pathWithin(rootResolved, targetResolved)) {
		// An unresolvable or out-of-bounds path is treated the same as an
		// empty directory (see the doc comment above) rather than surfaced as
		// an error, matching workspaceTreeChildren's no-stat-check behavior
		// for the git-backed path.
		return nil, false, nil //nolint:nilerr // unresolvable path treated as an empty directory, not an error
	}
	dirEntries, err := os.ReadDir(targetResolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	entries := make([]WorkspaceTreeEntry, 0, len(dirEntries))
	truncated := false
	for _, entry := range dirEntries {
		name := entry.Name()
		if name == ".git" {
			continue
		}
		if len(entries) >= maxWorkspaceFiles {
			truncated = true
			break
		}
		rel := name
		if dir != "" {
			rel = dir + "/" + name
		}
		fullPath := filepath.Join(targetResolved, name)
		if entry.Type()&os.ModeSymlink != 0 {
			targetInfo, ok := statScratchFileTarget(fullPath)
			if !ok {
				continue // symlink to a directory or broken symlink: excluded, matching scratchWorkspaceFiles
			}
			resolved, ok := resolvedScratchPath(fullPath)
			if !ok || !pathWithin(rootResolved, resolved) {
				continue
			}
			_, binary, _, err := readWorkspaceTextFile(fullPath, 8192)
			if err != nil {
				continue
			}
			entries = append(entries, WorkspaceTreeEntry{
				Name: name, Path: rel, Type: WorkspaceTreeFile,
				Status: WorkspaceFileAdded, Size: targetInfo.Size(), Binary: binary,
			})
			continue
		}
		if entry.IsDir() {
			entries = append(entries, WorkspaceTreeEntry{Name: name, Path: rel, Type: WorkspaceTreeDir, HasChanges: true})
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		_, binary, _, err := readWorkspaceTextFile(fullPath, 8192)
		if err != nil {
			continue
		}
		entries = append(entries, WorkspaceTreeEntry{
			Name: name, Path: rel, Type: WorkspaceTreeFile,
			Status: WorkspaceFileAdded, Size: info.Size(), Binary: binary,
		})
	}
	sortWorkspaceTreeEntries(entries)
	return entries, truncated, nil
}

// cleanWorkspaceRelativeDir is cleanWorkspaceRelativePath's counterpart for
// directories: "" and "." both mean the workspace root, which
// cleanWorkspaceRelativePath (built for single-file lookups) rejects.
func cleanWorkspaceRelativeDir(raw string) (string, error) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if trimmed == "" || trimmed == "." {
		return "", nil
	}
	return cleanWorkspaceRelativePath(raw)
}
