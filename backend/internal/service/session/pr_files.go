package session

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// PRFiles is the exact base...head read model for one associated pull request.
type PRFiles struct {
	SessionID domain.SessionID
	Files     []WorkspaceFileSummary
	// Commits are the PR's own commits (base..head), newest first.
	Commits   []CommitSummary
	Truncated bool
	Summary   WorkspaceSummary
}

// ListPRFiles returns the committed changed-file set for an associated PR.
// Git reads are revision-only and never inspect or mutate the worktree/index.
func (s *Service) ListPRFiles(ctx context.Context, id domain.SessionID, number int, sourceURL string) (PRFiles, error) {
	rec, pr, err := s.prFileSource(ctx, id, number, sourceURL)
	if err != nil {
		return PRFiles{}, err
	}
	root := rec.Metadata.WorkspacePath
	statuses, previous, err := workspaceDiffNameStatus(ctx, root, pr.BaseSHA+"..."+pr.HeadSHA)
	if err != nil {
		return PRFiles{}, unavailablePRSource()
	}
	counts, err := workspaceDiffNumstat(ctx, root, pr.BaseSHA+"..."+pr.HeadSHA)
	if err != nil {
		return PRFiles{}, unavailablePRSource()
	}
	paths := make([]string, 0, len(statuses))
	for rel := range statuses {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	truncated := len(paths) > maxWorkspaceFiles
	if truncated {
		paths = paths[:maxWorkspaceFiles]
	}
	files := make([]WorkspaceFileSummary, 0, len(paths))
	for _, rel := range paths {
		status := statuses[rel]
		additions, deletions := counts[rel][0], counts[rel][1]
		size := gitRevisionFileSize(ctx, root, pr.HeadSHA, rel, status)
		_, hasTextCounts := counts[rel]
		binary := status != WorkspaceFileDeleted && !hasTextCounts
		files = append(files, WorkspaceFileSummary{Path: rel, PreviousPath: previous[rel], Status: status, Additions: additions, Deletions: deletions, Size: size, Binary: binary})
	}
	// The commit list is best-effort: a PR whose files load still opens, just
	// without its per-commit menu, if its log cannot be read.
	commits, _ := prCommitLog(ctx, root, pr)
	return PRFiles{SessionID: id, Files: files, Commits: commits, Truncated: truncated, Summary: workspaceSummaryFromFiles(files)}, nil
}

// GetPRFileAtCommit returns one file's immutable snapshot and patch for a
// single commit of an associated PR.
func (s *Service) GetPRFileAtCommit(ctx context.Context, id domain.SessionID, number int, sourceURL, rawPath, commitSHA string) (WorkspaceFileDetail, error) {
	rec, pr, err := s.prFileSource(ctx, id, number, sourceURL)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	rel, err := cleanWorkspaceRelativePath(rawPath)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	root := rec.Metadata.WorkspacePath
	commit, summary, err := prCommitFile(ctx, root, pr, commitSHA, rel)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	return workspaceCommitFileDetail(ctx, id, root, "", rel, commit.SHA, summary)
}

// GetPRFile returns one file and its exact base...head diff for an associated PR.
func (s *Service) GetPRFile(ctx context.Context, id domain.SessionID, number int, sourceURL, rawPath string, rawPreviousPath *string) (WorkspaceFileDetail, error) {
	rec, pr, err := s.prFileSource(ctx, id, number, sourceURL)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	rel, err := cleanWorkspaceRelativePath(rawPath)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	root := rec.Metadata.WorkspacePath
	revArgs := []string{pr.BaseSHA + "..." + pr.HeadSHA}
	var paths []string
	if rawPreviousPath != nil {
		paths = []string{rel}
		if strings.TrimSpace(*rawPreviousPath) != "" {
			previousPath, cleanErr := cleanWorkspaceRelativePath(*rawPreviousPath)
			if cleanErr != nil {
				return WorkspaceFileDetail{}, cleanErr
			}
			paths = append(paths, previousPath)
		}
	}
	statuses, previous, err := workspaceDiffNameStatusPaths(ctx, root, revArgs, paths)
	if err != nil {
		return WorkspaceFileDetail{}, unavailablePRSource()
	}
	status, ok := statuses[rel]
	if !ok {
		return WorkspaceFileDetail{}, apierr.NotFound("PR_FILE_NOT_FOUND", "File is not part of the selected pull request")
	}
	counts, err := workspaceDiffNumstatPaths(ctx, root, revArgs, paths)
	if err != nil {
		return WorkspaceFileDetail{}, unavailablePRSource()
	}
	additions, deletions := counts[rel][0], counts[rel][1]
	detail := WorkspaceFileDetail{SessionID: id, Path: rel, PreviousPath: previous[rel], Status: status, Additions: additions, Deletions: deletions, Deleted: status == WorkspaceFileDeleted, CompareBaseSHA: pr.BaseSHA, CompareBaseRef: pr.TargetBranch, CompareMode: WorkspaceCompareBase}
	if !detail.Deleted {
		detail.Size = gitRevisionFileSize(ctx, root, pr.HeadSHA, rel, status)
		_, hasTextCounts := counts[rel]
		detail.Binary = !hasTextCounts
		if detail.Size > maxWorkspaceFileBytes {
			detail.ContentTruncated = true
		} else {
			content, contentErr := gitWorkspaceOutput(ctx, root, "show", pr.HeadSHA+":"+rel)
			if contentErr != nil {
				return WorkspaceFileDetail{}, apierr.NotFound("PR_FILE_NOT_FOUND", "File is unavailable at the selected pull request head")
			}
			if !detail.Binary && utf8.ValidString(content) && strings.IndexByte(content, 0) < 0 {
				detail.Content = content
			} else {
				detail.Binary = true
			}
		}
	}
	diffArgs := []string{"diff", "--no-ext-diff", "--find-renames", "--unified=3", pr.BaseSHA + "..." + pr.HeadSHA, "--"}
	if detail.PreviousPath != "" {
		diffArgs = append(diffArgs, detail.PreviousPath)
	}
	diffArgs = append(diffArgs, rel)
	diff, err := gitWorkspaceOutput(ctx, root, diffArgs...)
	if err != nil {
		return WorkspaceFileDetail{}, unavailablePRSource()
	}
	detail.Diff, detail.DiffTruncated = truncateUTF8(diff, maxWorkspaceDiffBytes)
	return detail, nil
}

// GetPRFileRevision reads one immutable side of the selected PR comparison.
// It intentionally does not delegate to workspace revision readers: a PR view
// must never fall back to the mutable session worktree while expanding a hunk.
func (s *Service) GetPRFileRevision(ctx context.Context, id domain.SessionID, number int, sourceURL, rawPath string, side WorkspaceFileBlobSide) (WorkspaceFileRevision, error) {
	rec, pr, err := s.prFileSource(ctx, id, number, sourceURL)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	if side != WorkspaceBlobBefore && side != WorkspaceBlobAfter {
		return WorkspaceFileRevision{}, apierr.Invalid("INVALID_WORKSPACE_REVISION_SIDE", "side must be before or after", nil)
	}
	rel, err := cleanWorkspaceRelativePath(rawPath)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	statuses, previous, err := workspaceDiffNameStatus(ctx, rec.Metadata.WorkspacePath, pr.BaseSHA+"..."+pr.HeadSHA)
	if err != nil {
		return WorkspaceFileRevision{}, unavailablePRSource()
	}
	if _, ok := statuses[rel]; !ok {
		return WorkspaceFileRevision{}, apierr.NotFound("PR_FILE_NOT_FOUND", "File is not part of the selected pull request")
	}
	path, revision := rel, pr.HeadSHA
	if side == WorkspaceBlobBefore {
		if previous[rel] != "" {
			path = previous[rel]
		}
		revision = pr.BaseSHA
	}
	return readPRFileRevision(ctx, rec.Metadata.WorkspacePath, id, rel, side, revision+":"+path)
}

// GetPRFileRevisionAtCommit reads one immutable side of a single PR commit: the
// commit's first parent before, the commit itself after.
func (s *Service) GetPRFileRevisionAtCommit(ctx context.Context, id domain.SessionID, number int, sourceURL, rawPath string, side WorkspaceFileBlobSide, commitSHA string) (WorkspaceFileRevision, error) {
	rec, pr, err := s.prFileSource(ctx, id, number, sourceURL)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	if side != WorkspaceBlobBefore && side != WorkspaceBlobAfter {
		return WorkspaceFileRevision{}, apierr.Invalid("INVALID_WORKSPACE_REVISION_SIDE", "side must be before or after", nil)
	}
	rel, err := cleanWorkspaceRelativePath(rawPath)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	root := rec.Metadata.WorkspacePath
	commit, summary, err := prCommitFile(ctx, root, pr, commitSHA, rel)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	path, revision := rel, commit.SHA
	if side == WorkspaceBlobBefore {
		if summary.PreviousPath != "" {
			path = summary.PreviousPath
		}
		revision = commit.SHA + "^"
	}
	return readPRFileRevision(ctx, root, id, rel, side, revision+":"+path)
}

// readPRFileRevision reads one immutable <revision>:<path> spec of a PR view.
func readPRFileRevision(ctx context.Context, root string, id domain.SessionID, rel string, side WorkspaceFileBlobSide, spec string) (WorkspaceFileRevision, error) {
	data, size, exists, truncated, err := readGitRevision(ctx, root, spec)
	if err != nil {
		return WorkspaceFileRevision{}, unavailablePRSource()
	}
	result := WorkspaceFileRevision{SessionID: id, Path: rel, Side: side, Encoding: "utf-8", Exists: exists, Size: size, Truncated: truncated}
	if !exists || truncated {
		return result, nil
	}
	content := string(data)
	result.Revision = hashWorkspaceReviewValue(content)
	result.Binary = isBinary(data) || !utf8.Valid(data)
	if !result.Binary {
		result.MediaType = "text/plain"
		result.Content = content
	}
	return result, nil
}

// prCommitLog lists the PR's own commits (base..head), newest first. Like the
// rest of the PR read model it reads revisions only, never the session
// worktree, so file sizes are left unset: the list only picks and filters
// files, and reading a file reports its real size.
func prCommitLog(ctx context.Context, root string, pr domain.PullRequest) ([]CommitSummary, error) {
	commits, changes, counts, err := gitCommitLogChanges(ctx, root, pr.BaseSHA+".."+pr.HeadSHA)
	if err != nil {
		return nil, unavailablePRSource()
	}
	for i := range commits {
		change := changes[commits[i].SHA]
		paths := make([]string, 0, len(change.statuses))
		for rel := range change.statuses {
			paths = append(paths, rel)
		}
		sort.Strings(paths)
		files := make([]WorkspaceFileSummary, 0, len(paths))
		for _, rel := range paths {
			status := change.statuses[rel]
			lines, hasTextCounts := counts[commits[i].SHA][rel]
			files = append(files, WorkspaceFileSummary{Path: rel, PreviousPath: change.previous[rel], Status: status, Additions: lines[0], Deletions: lines[1], Binary: status != WorkspaceFileDeleted && !hasTextCounts})
		}
		commits[i].Files = files
	}
	return commits, nil
}

// prCommitFile resolves one of the PR's own commits and the file rel within
// it. A SHA outside base..head is rejected, so a commit view never reads
// revisions the selected PR does not contain.
func prCommitFile(ctx context.Context, root string, pr domain.PullRequest, rawSHA, rel string) (CommitSummary, WorkspaceFileSummary, error) {
	commits, err := prCommitLog(ctx, root, pr)
	if err != nil {
		return CommitSummary{}, WorkspaceFileSummary{}, err
	}
	sha := strings.TrimSpace(rawSHA)
	for _, commit := range commits {
		if commit.SHA != sha {
			continue
		}
		for _, file := range commit.Files {
			if file.Path == rel {
				return commit, file, nil
			}
		}
		return CommitSummary{}, WorkspaceFileSummary{}, apierr.NotFound("PR_COMMIT_FILE_NOT_FOUND", "File was not changed by this commit")
	}
	return CommitSummary{}, WorkspaceFileSummary{}, apierr.NotFound("PR_COMMIT_NOT_FOUND", "Commit is not part of the selected pull request")
}

func (s *Service) prFileSource(ctx context.Context, id domain.SessionID, number int, sourceURL string) (domain.SessionRecord, domain.PullRequest, error) {
	rec, err := s.sessionWorkspaceRecord(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, domain.PullRequest{}, err
	}
	prs, err := s.store.ListPRsBySession(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, domain.PullRequest{}, fmt.Errorf("list PRs for files: %w", err)
	}
	for _, pr := range prs {
		if pr.Number != number || (sourceURL != "" && !strings.EqualFold(strings.TrimSpace(pr.URL), strings.TrimSpace(sourceURL))) {
			continue
		}
		if strings.TrimSpace(pr.BaseSHA) == "" || strings.TrimSpace(pr.HeadSHA) == "" {
			return domain.SessionRecord{}, domain.PullRequest{}, unavailablePRSource()
		}
		root, remote, err := s.prWorkspaceRoot(ctx, rec, pr)
		if err != nil {
			return domain.SessionRecord{}, domain.PullRequest{}, err
		}
		rec.Metadata.WorkspacePath = root
		if err := ensurePRRevisionObjects(ctx, root, remote, pr); err != nil {
			return domain.SessionRecord{}, domain.PullRequest{}, err
		}
		return rec, pr, nil
	}
	return domain.SessionRecord{}, domain.PullRequest{}, apierr.NotFound("PR_NOT_FOUND", "Pull request is not associated with this session")
}

// prWorkspaceRoot chooses the registered repository and remote matching the
// PR's persisted provider repository. Workspace projects can contain child
// repositories, and a fork checkout can keep the PR base repository on a
// non-origin remote.
func (s *Service) prWorkspaceRoot(ctx context.Context, rec domain.SessionRecord, pr domain.PullRequest) (string, string, error) {
	rows, err := s.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		return "", "", err
	}
	if len(rows) == 0 {
		rows = []domain.SessionWorktreeRecord{{WorktreePath: rec.Metadata.WorkspacePath}}
	}
	want := strings.Trim(strings.ToLower(pr.Repo), "/")
	for _, row := range rows {
		if strings.TrimSpace(row.WorktreePath) == "" {
			continue
		}
		remotes, remoteErr := gitWorkspaceOutput(ctx, row.WorktreePath, "remote")
		if remoteErr != nil {
			continue
		}
		for _, remote := range strings.Fields(remotes) {
			rawURL, urlErr := gitWorkspaceOutput(ctx, row.WorktreePath, "config", "--get", "remote."+remote+".url")
			if urlErr != nil {
				continue
			}
			identity, parseErr := domain.ParseRepositoryIdentity(strings.TrimSpace(rawURL))
			if parseErr != nil {
				continue
			}
			got := strings.ToLower(identity.Namespace + "/" + identity.Name)
			if strings.EqualFold(identity.Provider, pr.Provider) && strings.EqualFold(identity.Host, pr.Host) && got == want {
				return row.WorktreePath, remote, nil
			}
		}
	}
	return "", "", apierr.NotFound("PR_SOURCE_REPOSITORY_NOT_FOUND", "No registered workspace repository matches the selected pull request")
}

// ensurePRRevisionObjects fetches provider-owned review refs only when the
// persisted immutable SHAs are absent. Fetch updates git object/ref storage but
// never HEAD, the index, or worktree files.
func ensurePRRevisionObjects(ctx context.Context, root, remote string, pr domain.PullRequest) error {
	baseExists := gitCommitExists(ctx, root, pr.BaseSHA)
	headExists := gitCommitExists(ctx, root, pr.HeadSHA)
	if baseExists && headExists {
		return nil
	}
	refPrefix := "refs/ao/pr/" + strconv.Itoa(pr.Number)
	refspecs := make([]string, 0, 2)
	if !baseExists && strings.TrimSpace(pr.TargetBranch) != "" {
		refspecs = append(refspecs, "+refs/heads/"+pr.TargetBranch+":"+refPrefix+"/base")
	}
	if !headExists {
		providerRef := "refs/pull/" + strconv.Itoa(pr.Number) + "/head"
		if strings.EqualFold(pr.Provider, "gitlab") {
			providerRef = "refs/merge-requests/" + strconv.Itoa(pr.Number) + "/head"
		}
		refspecs = append(refspecs, "+"+providerRef+":"+refPrefix+"/head")
	}
	if strings.TrimSpace(remote) != "" && len(refspecs) > 0 {
		fetchCtx, cancel := context.WithTimeout(ctx, workspaceReviewTimeout)
		_, _ = gitWorkspaceOutput(fetchCtx, root, append([]string{"fetch", "--no-tags", remote}, refspecs...)...)
		cancel()
	}
	if !gitCommitExists(ctx, root, pr.BaseSHA) || !gitCommitExists(ctx, root, pr.HeadSHA) {
		return unavailablePRSource()
	}
	return nil
}

func unavailablePRSource() error {
	return apierr.NotFound("PR_SOURCE_UNAVAILABLE", "The selected pull request revisions are unavailable locally")
}

func gitRevisionFileSize(ctx context.Context, root, rev, rel string, status WorkspaceFileStatus) int64 {
	if status == WorkspaceFileDeleted {
		return 0
	}
	out, err := gitWorkspaceOutput(ctx, root, "cat-file", "-s", rev+":"+rel)
	if err != nil {
		return 0
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	return size
}
