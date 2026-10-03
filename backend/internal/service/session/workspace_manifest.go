package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// computeWorkspaceManifest computes only the changed-file data needed for the
// initial Changes paint. Callers should normally use GetWorkspaceManifest so
// this work is shared and retained across UI mounts.
func (s *Service) computeWorkspaceManifest(ctx context.Context, id domain.SessionID) (WorkspaceManifest, error) {
	rec, err := s.sessionWorkspaceRecord(ctx, id)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	project, projectOK, err := s.sessionProject(ctx, rec)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	projectKind := domain.ProjectKindSingleRepo
	if isStandaloneScratchWorkspace(rec) {
		projectKind = domain.ProjectKindScratch
	}
	if projectOK {
		projectKind = project.Kind.WithDefault()
	}
	switch projectKind {
	case domain.ProjectKindScratch:
		files, truncated, err := scratchWorkspaceFiles(rec.Metadata.WorkspacePath)
		if err != nil {
			return WorkspaceManifest{}, err
		}
		return finalizeWorkspaceManifest(WorkspaceManifest{
			SessionID: id,
			Files:     files,
			Summary:   workspaceSummaryFromFiles(files),
			Truncated: truncated,
		}), nil
	case domain.ProjectKindWorkspace:
		return s.workspaceProjectManifest(ctx, rec, project)
	default:
		prs, err := s.workspaceComparePRs(ctx, rec.ID)
		if err != nil {
			return WorkspaceManifest{}, err
		}
		resolve := func(rctx context.Context) workspaceCompareTarget {
			return resolveWorkspaceCompare(rctx, rec.Metadata.WorkspacePath, rec.Metadata.DiffBaseSHA, rec.Metadata.DiffBaseRef, defaultBranchForProject(project, projectOK), prs)
		}
		return s.gitWorkspaceManifest(ctx, rec.ID, rec.Metadata.WorkspacePath, "", nil, resolve, true)
	}
}

func (s *Service) gitWorkspaceManifest(
	ctx context.Context,
	id domain.SessionID,
	root, prefix string,
	excludePrefixes []string,
	resolve func(context.Context) workspaceCompareTarget,
	includeSections bool,
) (WorkspaceManifest, error) {
	compare, changes, err := s.resolveWorkspaceChanges(ctx, id, root, resolve)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	paths, truncated := mergeWorkspaceFilePaths(nil, changes.changedPaths())
	files := buildWorkspaceFileSummaries(root, prefix, excludePrefixes, paths, changes)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	manifest := WorkspaceManifest{
		SessionID:      id,
		CompareBaseSHA: compare.BaseSHA,
		CompareBaseRef: compare.BaseRef,
		CompareMode:    compare.Mode,
		Files:          files,
		Summary:        workspaceSummaryFromFiles(files),
		Truncated:      truncated,
	}
	if !includeSections {
		return finalizeWorkspaceManifest(manifest), nil
	}
	sections, err := workspaceGitSections(ctx, root, compare.gitBase())
	if err != nil {
		if errors.Is(err, ports.ErrWorkspaceRepoUnavailable) {
			return WorkspaceManifest{}, err
		}
		manifest.Degraded = true
		manifest.DegradedCode = workspaceDegradedCode(err)
		return finalizeWorkspaceManifest(manifest), nil
	}
	manifest.Sections = sections
	return finalizeWorkspaceManifest(manifest), nil
}

func (s *Service) workspaceProjectManifest(ctx context.Context, rec domain.SessionRecord, project domain.ProjectRecord) (WorkspaceManifest, error) {
	rows, err := s.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("list workspace project rows: %w", err)
	}
	if len(rows) == 0 {
		prs, err := s.workspaceComparePRs(ctx, rec.ID)
		if err != nil {
			return WorkspaceManifest{}, err
		}
		resolve := func(rctx context.Context) workspaceCompareTarget {
			return resolveWorkspaceCompare(rctx, rec.Metadata.WorkspacePath, rec.Metadata.DiffBaseSHA, rec.Metadata.DiffBaseRef, defaultBranchForProject(project, true), prs)
		}
		return s.gitWorkspaceManifest(ctx, rec.ID, rec.Metadata.WorkspacePath, "", nil, resolve, false)
	}

	prefixes := workspaceProjectPrefixes(rec.Metadata.WorkspacePath, rows)
	childPrefixes := nonEmptyWorkspacePrefixes(prefixes)
	defaultBranch := defaultBranchForProject(project, true)
	var files []WorkspaceFileSummary
	truncated := false
	compares := make([]workspaceCompareTarget, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.WorktreePath) == "" {
			continue
		}
		prefix, ok := prefixes[row.RepoName]
		if !ok {
			continue
		}
		exclude := []string(nil)
		if prefix == "" {
			exclude = childPrefixes
		}
		baseRef := row.BaseRef
		if strings.TrimSpace(baseRef) == "" {
			baseRef = defaultBranch
		}
		resolve := func(rctx context.Context) workspaceCompareTarget {
			return resolveWorkspaceProjectCompare(rctx, row.WorktreePath, row.BaseSHA, baseRef)
		}
		compare, changes, err := s.resolveWorkspaceChanges(ctx, rec.ID, row.WorktreePath, resolve)
		if err != nil {
			return WorkspaceManifest{}, err
		}
		paths, repoTruncated := mergeWorkspaceFilePaths(nil, changes.changedPaths())
		repoFiles := buildWorkspaceFileSummaries(row.WorktreePath, prefix, exclude, paths, changes)
		compares = append(compares, compare)
		files, truncated = appendWorkspaceFilesWithCap(files, repoFiles, truncated)
		truncated = truncated || repoTruncated
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	compare := aggregateWorkspaceCompare(compares)
	return finalizeWorkspaceManifest(WorkspaceManifest{
		SessionID:      rec.ID,
		CompareBaseSHA: compare.BaseSHA,
		CompareBaseRef: compare.BaseRef,
		CompareMode:    compare.Mode,
		Files:          files,
		Summary:        workspaceSummaryFromFiles(files),
		Truncated:      truncated,
	}), nil
}

// workspaceGitSections computes only the section summaries required by the
// first review paint. Commit history and ahead/behind remain lazy enrichment.
func workspaceGitSections(ctx context.Context, root, base string) (WorkspaceFileSections, error) {
	var sections WorkspaceFileSections
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		statuses, previous, err := workspaceDiffNameStatus(gctx, root, "--cached")
		if err != nil {
			return err
		}
		counts, err := workspaceDiffNumstat(gctx, root, "--cached")
		if err != nil {
			return err
		}
		sections.Staged = buildSectionSummaries(root, statuses, counts, previous)
		return nil
	})
	g.Go(func() error {
		statuses, previous, err := workspaceDiffNameStatus(gctx, root)
		if err != nil {
			return err
		}
		counts, err := workspaceDiffNumstat(gctx, root)
		if err != nil {
			return err
		}
		sections.Unstaged = buildSectionSummaries(root, statuses, counts, previous)
		return nil
	})
	g.Go(func() error {
		paths, err := gitUntrackedFiles(gctx, root)
		if err != nil {
			return err
		}
		sections.Untracked = buildUntrackedSummaries(root, paths)
		return nil
	})
	if base = strings.TrimSpace(base); base != "" && base != "HEAD" {
		g.Go(func() error {
			statuses, previous, err := workspaceDiffNameStatus(gctx, root, base, "HEAD")
			if err != nil {
				return err
			}
			counts, err := workspaceDiffNumstat(gctx, root, base, "HEAD")
			if err != nil {
				return err
			}
			sections.Committed = buildSectionSummaries(root, statuses, counts, previous)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return WorkspaceFileSections{}, err
	}
	return sections, nil
}
