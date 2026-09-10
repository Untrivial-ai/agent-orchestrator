package session

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	maxWorkspaceFiles     = 5000
	maxWorkspaceFileBytes = 256 * 1024
	maxWorkspaceDiffBytes = 512 * 1024
	// maxWorkspaceImageBytes caps a single image revision streamed to the diff
	// viewer. Anything larger is refused rather than buffered.
	maxWorkspaceImageBytes = 16 * 1024 * 1024
)

// WorkspaceFileStatus describes a session-worktree file relative to its compare base.
type WorkspaceFileStatus string

// Workspace file status values reported by the session workspace browser.
const (
	WorkspaceFileUnmodified WorkspaceFileStatus = "unmodified"
	WorkspaceFileModified   WorkspaceFileStatus = "modified"
	WorkspaceFileAdded      WorkspaceFileStatus = "added"
	WorkspaceFileDeleted    WorkspaceFileStatus = "deleted"
	WorkspaceFileRenamed    WorkspaceFileStatus = "renamed"
)

// WorkspaceCompareMode describes the Git revision used for workspace diffs.
type WorkspaceCompareMode string

const (
	// WorkspaceCompareBase means diffs are against the session's recorded base.
	WorkspaceCompareBase WorkspaceCompareMode = "base"
	// WorkspaceCompareHeadFallback means AO could not resolve a base and used the
	// previous HEAD-only behavior.
	WorkspaceCompareHeadFallback WorkspaceCompareMode = "head_fallback"
)

// WorkspaceFiles is the read model for the session workspace file browser.
type WorkspaceFiles struct {
	SessionID      domain.SessionID
	CompareBaseSHA string
	CompareBaseRef string
	CompareMode    WorkspaceCompareMode
	Files          []WorkspaceFileSummary
	Truncated      bool
	// Sections splits the same working tree into git-state groups (staged,
	// unstaged, untracked, committed-since-base), independent of the
	// base..worktree Files list above. Only populated for single-repo
	// sessions; workspace-project (multi-repo) and scratch sessions leave it
	// zero-valued.
	Sections WorkspaceFileSections
	// Commits are the commits between the compare base and HEAD, newest last.
	Commits []CommitSummary
	// Summary aggregates Files (excluding unmodified entries) into totals for
	// the panel header.
	Summary WorkspaceSummary
	// Ahead and Behind are commit counts against the branch's upstream (or
	// origin/<branch> when no upstream is configured). Nil when neither can
	// be resolved — that means "no push/pull data," not an error.
	Ahead  *int
	Behind *int
}

// WorkspaceFileSections groups the session worktree's changed files by git
// state: staged (index vs HEAD), unstaged (worktree vs index), untracked, and
// committed (HEAD vs the compare base). A partially staged file can appear in
// both Staged and Unstaged.
type WorkspaceFileSections struct {
	Staged    []WorkspaceFileSummary
	Unstaged  []WorkspaceFileSummary
	Untracked []WorkspaceFileSummary
	Committed []WorkspaceFileSummary
}

// WorkspaceFileSection identifies which of WorkspaceFileSections a
// GetWorkspaceFile request's diff should be scoped to. A partially staged
// file has independent staged and unstaged diffs, so the caller must say
// which one it opened.
type WorkspaceFileSection string

// Git-state sections a workspace file row can belong to.
const (
	WorkspaceFileSectionStaged    WorkspaceFileSection = "staged"
	WorkspaceFileSectionUnstaged  WorkspaceFileSection = "unstaged"
	WorkspaceFileSectionUntracked WorkspaceFileSection = "untracked"
	WorkspaceFileSectionCommitted WorkspaceFileSection = "committed"
)

// CommitSummary is one commit between the compare base and HEAD.
type CommitSummary struct {
	SHA       string
	Subject   string
	Author    string
	Timestamp time.Time
}

// WorkspaceSummary aggregates the base..worktree diff into totals.
type WorkspaceSummary struct {
	Files     int
	Additions int
	Deletions int
}

// WorkspaceFileSummary is one file row in the session workspace browser.
type WorkspaceFileSummary struct {
	Path         string
	PreviousPath string
	Status       WorkspaceFileStatus
	Additions    int
	Deletions    int
	Size         int64
	Binary       bool
}

// WorkspaceFileDetail is the selected file's current content and diff.
type WorkspaceFileDetail struct {
	SessionID          domain.SessionID
	Path               string
	PreviousPath       string
	Status             WorkspaceFileStatus
	Additions          int
	Deletions          int
	Size               int64
	Binary             bool
	Deleted            bool
	ImageMediaType     string
	Content            string
	ContentTruncated   bool
	Diff               string
	DiffTruncated      bool
	WorkspaceTruncated bool
	CompareBaseSHA     string
	CompareBaseRef     string
	CompareMode        WorkspaceCompareMode
}

// WorkspaceWatchPaths returns every worktree that contributes files to a
// session workspace read model. Workspace projects include child repositories
// that are deliberately ignored by the parent Git repository.
func (s *Service) WorkspaceWatchPaths(ctx context.Context, id domain.SessionID) ([]string, error) {
	rec, err := s.sessionWorkspaceRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	paths := []string{rec.Metadata.WorkspacePath}
	seen := map[string]struct{}{filepath.Clean(rec.Metadata.WorkspacePath): {}}
	rows, err := s.store.ListSessionWorktrees(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list workspace project rows: %w", err)
	}
	for _, row := range rows {
		workspacePath := strings.TrimSpace(row.WorktreePath)
		if workspacePath == "" {
			continue
		}
		workspacePath = filepath.Clean(workspacePath)
		if _, ok := seen[workspacePath]; ok {
			continue
		}
		info, statErr := os.Stat(workspacePath)
		if statErr != nil || !info.IsDir() {
			continue
		}
		seen[workspacePath] = struct{}{}
		paths = append(paths, workspacePath)
	}
	return paths, nil
}

// InvalidateWorkspaceCache purges cached compare-base and git-status data for
// a session. Called from the workspace SSE stream the instant its filesystem
// watcher observes a real change, so a list/detail request racing a fresh
// git mutation never returns stale cached state.
func (s *Service) InvalidateWorkspaceCache(id domain.SessionID) {
	s.workspaceCache.invalidateSession(id)
}

// ListWorkspaceFiles returns all tracked and untracked, non-ignored files in a
// session worktree, annotated with their current git status against the
// session's recorded base when available.
func (s *Service) ListWorkspaceFiles(ctx context.Context, id domain.SessionID) (WorkspaceFiles, error) {
	rec, err := s.sessionWorkspaceRecord(ctx, id)
	if err != nil {
		return WorkspaceFiles{}, err
	}
	project, projectOK, err := s.sessionProject(ctx, rec)
	if err != nil {
		return WorkspaceFiles{}, err
	}
	projectKind := domain.ProjectKindSingleRepo
	if projectOK {
		projectKind = project.Kind.WithDefault()
	}
	if projectKind == domain.ProjectKindScratch {
		files, truncated, err := scratchWorkspaceFiles(rec.Metadata.WorkspacePath)
		if err != nil {
			return WorkspaceFiles{}, err
		}
		return WorkspaceFiles{SessionID: id, Files: files, Truncated: truncated}, nil
	}
	if projectKind == domain.ProjectKindWorkspace {
		return s.listWorkspaceProjectFiles(ctx, rec, project)
	}
	prs, err := s.workspaceComparePRs(ctx, rec.ID)
	if err != nil {
		return WorkspaceFiles{}, err
	}
	resolve := func(rctx context.Context) workspaceCompareTarget {
		return resolveWorkspaceCompare(rctx, rec.Metadata.WorkspacePath, rec.Metadata.DiffBaseSHA, rec.Metadata.DiffBaseRef, defaultBranchForProject(project, projectOK), prs)
	}
	files, truncated, compare, err := s.workspaceFileSummariesCached(ctx, id, rec.Metadata.WorkspacePath, "", nil, resolve)
	if err != nil {
		return WorkspaceFiles{}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sections, commits, ahead, behind, err := workspaceGitState(ctx, rec.Metadata.WorkspacePath, compare.gitBase())
	if err != nil {
		return WorkspaceFiles{}, err
	}
	return WorkspaceFiles{
		SessionID:      id,
		CompareBaseSHA: compare.BaseSHA,
		CompareBaseRef: compare.BaseRef,
		CompareMode:    compare.Mode,
		Files:          files,
		Truncated:      truncated,
		Sections:       sections,
		Commits:        commits,
		Summary:        workspaceSummaryFromFiles(files),
		Ahead:          ahead,
		Behind:         behind,
	}, nil
}

// GetWorkspaceFile returns one session-worktree file's current text content and
// the git diff for that path. Binary or deleted files omit content. section
// scopes the diff to the git-state section the caller opened the file from
// (see WorkspaceFileSection) since a partially staged file's staged and
// unstaged changes are different diffs.
func (s *Service) GetWorkspaceFile(ctx context.Context, id domain.SessionID, rawPath string, section WorkspaceFileSection) (WorkspaceFileDetail, error) {
	target, err := s.resolveWorkspaceFileTarget(ctx, id, rawPath)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	if target.scratch {
		return scratchWorkspaceFile(target.root, id, target.rel)
	}
	return workspaceFileDetail(ctx, id, target.root, target.prefix, target.rel, section, target.compare, target.changes)
}

// workspaceFileTarget is one resolved workspace path: the worktree that owns
// it, the display prefix the path carries in a workspace project, and the
// compare base and change set that worktree is diffed against.
type workspaceFileTarget struct {
	root    string
	prefix  string
	rel     string
	compare workspaceCompareTarget
	changes workspaceChangeSet
	scratch bool
}

// resolveWorkspaceFileTarget maps a request path onto the worktree that owns it
// and that worktree's compare state. Shared by the file detail read model and
// the raw blob route so both resolve a path the same way.
func (s *Service) resolveWorkspaceFileTarget(ctx context.Context, id domain.SessionID, rawPath string) (workspaceFileTarget, error) {
	rec, err := s.sessionWorkspaceRecord(ctx, id)
	if err != nil {
		return workspaceFileTarget{}, err
	}
	rel, err := cleanWorkspaceRelativePath(rawPath)
	if err != nil {
		return workspaceFileTarget{}, err
	}
	project, projectOK, err := s.sessionProject(ctx, rec)
	if err != nil {
		return workspaceFileTarget{}, err
	}
	projectKind := domain.ProjectKindSingleRepo
	if projectOK {
		projectKind = project.Kind.WithDefault()
	}
	if projectKind == domain.ProjectKindScratch {
		return workspaceFileTarget{root: rec.Metadata.WorkspacePath, rel: rel, scratch: true}, nil
	}
	if projectKind == domain.ProjectKindWorkspace {
		return s.resolveWorkspaceProjectFileTarget(ctx, rec, project, rel)
	}
	prs, err := s.workspaceComparePRs(ctx, rec.ID)
	if err != nil {
		return workspaceFileTarget{}, err
	}
	resolve := func(rctx context.Context) workspaceCompareTarget {
		return resolveWorkspaceCompare(rctx, rec.Metadata.WorkspacePath, rec.Metadata.DiffBaseSHA, rec.Metadata.DiffBaseRef, defaultBranchForProject(project, projectOK), prs)
	}
	compare, changes, err := s.resolveWorkspaceChanges(ctx, id, rec.Metadata.WorkspacePath, resolve)
	if err != nil {
		return workspaceFileTarget{}, err
	}
	return workspaceFileTarget{root: rec.Metadata.WorkspacePath, rel: rel, compare: compare, changes: changes}, nil
}

// WorkspaceFileBlobSide selects which revision of a workspace file to read.
type WorkspaceFileBlobSide string

const (
	// WorkspaceBlobBefore is the file as it exists in the compare base.
	WorkspaceBlobBefore WorkspaceFileBlobSide = "before"
	// WorkspaceBlobAfter is the file as it exists in the session worktree.
	WorkspaceBlobAfter WorkspaceFileBlobSide = "after"
)

// WorkspaceFileBlob is one revision of a workspace file's raw bytes.
type WorkspaceFileBlob struct {
	Path      string
	Side      WorkspaceFileBlobSide
	MediaType string
	Data      []byte
}

// GetWorkspaceFileBlob returns the raw bytes of one side of a workspace file so
// the diff viewer can render an image change. Only image media types are
// served: text content already travels in the file detail response, and the
// diff viewer is this route's only consumer.
func (s *Service) GetWorkspaceFileBlob(ctx context.Context, id domain.SessionID, rawPath string, side WorkspaceFileBlobSide) (WorkspaceFileBlob, error) {
	if side != WorkspaceBlobBefore && side != WorkspaceBlobAfter {
		return WorkspaceFileBlob{}, apierr.Invalid("INVALID_WORKSPACE_BLOB_SIDE", "side must be before or after", nil)
	}
	target, err := s.resolveWorkspaceFileTarget(ctx, id, rawPath)
	if err != nil {
		return WorkspaceFileBlob{}, err
	}
	mediaType := workspaceImageMediaType(target.rel)
	if mediaType == "" {
		return WorkspaceFileBlob{}, apierr.Invalid("UNSUPPORTED_WORKSPACE_BLOB", "workspace blobs are only served for image files", nil)
	}
	blob := WorkspaceFileBlob{
		Path:      joinWorkspaceRelative(target.prefix, target.rel),
		Side:      side,
		MediaType: mediaType,
	}
	status := target.changes.statuses[target.rel]
	if side == WorkspaceBlobAfter {
		if status == WorkspaceFileDeleted {
			return WorkspaceFileBlob{}, apierr.NotFound("WORKSPACE_BLOB_NOT_FOUND", "File does not exist in the session worktree")
		}
		data, err := readWorkspaceFileBytes(target.root, target.rel, maxWorkspaceImageBytes)
		if err != nil {
			return WorkspaceFileBlob{}, err
		}
		blob.Data = data
		return blob, nil
	}
	if target.scratch || status == WorkspaceFileAdded {
		return WorkspaceFileBlob{}, apierr.NotFound("WORKSPACE_BLOB_NOT_FOUND", "File does not exist in the compare base")
	}
	basePath := target.rel
	if status == WorkspaceFileRenamed {
		if previous := target.changes.previous[target.rel]; previous != "" {
			basePath = previous
		}
	}
	// A rename can change the extension, so type the historical bytes by the path
	// they are read from rather than the worktree path. The controller sends
	// nosniff, so a mismatched media type makes the browser refuse to render.
	baseMediaType := workspaceImageMediaType(basePath)
	if baseMediaType == "" {
		return WorkspaceFileBlob{}, apierr.Invalid("UNSUPPORTED_WORKSPACE_BLOB", "workspace blobs are only served for image files", nil)
	}
	blob.MediaType = baseMediaType
	data, err := gitWorkspaceBlob(ctx, target.root, target.compare.gitBase(), basePath, maxWorkspaceImageBytes)
	if err != nil {
		return WorkspaceFileBlob{}, err
	}
	blob.Data = data
	return blob, nil
}

// workspaceImageMediaTypes are the raster formats the diff viewer previews.
// SVG is deliberately absent: it is a text file, so it keeps its line diff.
var workspaceImageMediaTypes = map[string]string{
	".apng": "image/apng",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".gif":  "image/gif",
	".ico":  "image/x-icon",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

// workspaceImageMediaType reports the image media type for a path, or "" when
// the extension is not a previewable image.
func workspaceImageMediaType(rel string) string {
	return workspaceImageMediaTypes[strings.ToLower(path.Ext(rel))]
}

// workspaceImageDetailMediaType reports the media type the diff viewer should
// preview a file with. A file that still has readable text keeps its line diff,
// so only binary (or deleted, where nothing is left to read) paths qualify.
func workspaceImageDetailMediaType(rel string, binary, deleted bool) string {
	if !binary && !deleted {
		return ""
	}
	return workspaceImageMediaType(rel)
}

// readWorkspaceFileBytes reads a workspace file whole, refusing anything past
// limit instead of buffering it.
func readWorkspaceFileBytes(root, rel string, limit int64) ([]byte, error) {
	file, info, err := confinedWorkspaceFile(root, rel)
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, apierr.Invalid("WORKSPACE_BLOB_TOO_LARGE", "File is too large to preview", map[string]any{"limitBytes": limit})
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, apierr.NotFound("WORKSPACE_FILE_NOT_FOUND", "Workspace file not found")
	}
	return data, nil
}

// gitWorkspaceBlob reads one path out of a revision. The object size is asked
// for first so an oversized blob is refused rather than buffered, matching how
// readWorkspaceFileBytes stats the worktree copy before reading it.
func gitWorkspaceBlob(ctx context.Context, root, rev, rel string, limit int64) ([]byte, error) {
	spec := rev + ":" + rel
	sizeOut, err := gitWorkspaceOutput(ctx, root, "cat-file", "-s", spec)
	if err != nil {
		return nil, apierr.NotFound("WORKSPACE_BLOB_NOT_FOUND", "File does not exist in the compare base")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOut), 10, 64)
	if err != nil {
		return nil, apierr.NotFound("WORKSPACE_BLOB_NOT_FOUND", "File does not exist in the compare base")
	}
	if size > limit {
		return nil, apierr.Invalid("WORKSPACE_BLOB_TOO_LARGE", "File is too large to preview", map[string]any{"limitBytes": limit})
	}
	out, err := gitWorkspaceOutput(ctx, root, "cat-file", "blob", spec)
	if err != nil {
		return nil, apierr.NotFound("WORKSPACE_BLOB_NOT_FOUND", "File does not exist in the compare base")
	}
	return []byte(out), nil
}

func (s *Service) sessionWorkspaceRecord(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error) {
	rec, ok, err := s.store.GetSession(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, fmt.Errorf("get %s: %w", id, err)
	}
	if !ok {
		return domain.SessionRecord{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if strings.TrimSpace(rec.Metadata.WorkspacePath) == "" {
		return domain.SessionRecord{}, apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	info, err := os.Stat(rec.Metadata.WorkspacePath)
	if err != nil || !info.IsDir() {
		return domain.SessionRecord{}, apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	return rec, nil
}

func (s *Service) sessionProject(ctx context.Context, rec domain.SessionRecord) (domain.ProjectRecord, bool, error) {
	if s.store == nil || rec.ProjectID == "" {
		return domain.ProjectRecord{}, false, nil
	}
	project, ok, err := s.store.GetProject(ctx, string(rec.ProjectID))
	if err != nil {
		return domain.ProjectRecord{}, false, fmt.Errorf("get project %s: %w", rec.ProjectID, err)
	}
	return project, ok, nil
}

func defaultBranchForProject(project domain.ProjectRecord, ok bool) string {
	if !ok {
		return ""
	}
	return project.Config.WorktreeBaseBranch()
}

type workspaceCompareTarget struct {
	BaseSHA string
	BaseRef string
	Mode    WorkspaceCompareMode
}

func (c workspaceCompareTarget) gitBase() string {
	if c.Mode == WorkspaceCompareBase && strings.TrimSpace(c.BaseSHA) != "" {
		return c.BaseSHA
	}
	return "HEAD"
}

func (s *Service) workspaceComparePRs(ctx context.Context, id domain.SessionID) ([]domain.PullRequest, error) {
	if s.store == nil {
		return nil, nil
	}
	prs, err := s.store.ListPRsBySession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list PRs for compare base: %w", err)
	}
	return prs, nil
}

func resolveWorkspaceCompare(ctx context.Context, root, recordedSHA, recordedRef, defaultBranch string, prs []domain.PullRequest) workspaceCompareTarget {
	recordedSHA = strings.TrimSpace(recordedSHA)
	recordedRef = strings.TrimSpace(recordedRef)

	// The best available local candidate: the live, ref-derived merge base
	// (e.g. origin/main) when it resolves, else the session's spawn-time
	// recorded base. Both are re-derived through git merge-base — never used
	// as a raw revision — so a rewritten or unrelated commit can't become the
	// diff base outright.
	var localTarget *workspaceCompareTarget
	if recordedRef != "" && recordedRef != "HEAD" {
		for _, ref := range workspaceBaseRefCandidates(recordedRef) {
			if sha, ok := mergeBaseCandidate(ctx, root, ref); ok {
				target := workspaceCompareTarget{BaseSHA: sha, BaseRef: ref, Mode: WorkspaceCompareBase}
				localTarget = &target
				break
			}
		}
	}
	if localTarget == nil {
		if sha, ok := mergeBaseCandidate(ctx, root, recordedSHA); ok {
			target := workspaceCompareTarget{BaseSHA: sha, BaseRef: recordedRef, Mode: WorkspaceCompareBase}
			localTarget = &target
		}
	}

	// The provider PR's own, independently-synced base. A local
	// remote-tracking ref can go stale (AO never fetches a session
	// worktree), so this can be more advanced than the local candidate above
	// — but it's equally re-derived through merge-base rather than trusted as
	// a raw revision, since pr.BaseSHA is the target branch's current tip,
	// not necessarily an ancestor of HEAD.
	var prTarget *workspaceCompareTarget
	var prHeadSHA string
	if pr, ok := selectWorkspaceComparePR(prs, defaultBranch); ok {
		if sha, ok := mergeBaseCandidate(ctx, root, pr.BaseSHA); ok {
			target := workspaceCompareTarget{BaseSHA: sha, BaseRef: strings.TrimSpace(pr.TargetBranch), Mode: WorkspaceCompareBase}
			prTarget = &target
			prHeadSHA = strings.TrimSpace(pr.HeadSHA)
		}
	}

	switch {
	case localTarget != nil && prTarget != nil:
		if localTarget.BaseSHA == prTarget.BaseSHA {
			return *localTarget
		}
		if gitIsAncestor(ctx, root, localTarget.BaseSHA, prTarget.BaseSHA) {
			return *prTarget // PR candidate is the more advanced descendant.
		}
		if gitIsAncestor(ctx, root, prTarget.BaseSHA, localTarget.BaseSHA) {
			return *localTarget // Local candidate is the more advanced descendant.
		}
		// Neither candidate is an ancestor of the other — divergent
		// histories, e.g. the target branch was force-pushed to a
		// replacement line. Only trust the PR snapshot when it describes the
		// exact commit the Files tab is displaying; otherwise a stale
		// snapshot could silently outrank a valid local candidate.
		if head, ok := gitRevision(ctx, root, "HEAD"); ok && prHeadSHA != "" && prHeadSHA == head {
			return *prTarget
		}
		return *localTarget
	case prTarget != nil:
		return *prTarget
	case localTarget != nil:
		return *localTarget
	}

	for _, ref := range workspaceBaseRefCandidates(defaultBranch) {
		if sha, ok := gitMergeBase(ctx, root, ref); ok {
			return workspaceCompareTarget{BaseSHA: sha, BaseRef: ref, Mode: WorkspaceCompareBase}
		}
	}
	return workspaceCompareTarget{Mode: WorkspaceCompareHeadFallback}
}

func resolveWorkspaceProjectCompare(ctx context.Context, root, recordedSHA, recordedRef string) workspaceCompareTarget {
	recordedRef = workspaceDefaultBranch(recordedRef)
	for _, ref := range workspaceBaseRefCandidates(recordedRef) {
		if sha, ok := gitMergeBase(ctx, root, ref); ok {
			return workspaceCompareTarget{BaseSHA: sha, BaseRef: ref, Mode: WorkspaceCompareBase}
		}
	}
	recordedSHA = strings.TrimSpace(recordedSHA)
	if recordedSHA != "" && gitCommitExists(ctx, root, recordedSHA) {
		return workspaceCompareTarget{BaseSHA: recordedSHA, BaseRef: recordedRef, Mode: WorkspaceCompareBase}
	}
	return workspaceCompareTarget{Mode: WorkspaceCompareHeadFallback}
}

func workspaceBaseRefCandidates(defaultBranch string) []string {
	defaultBranch = workspaceDefaultBranch(defaultBranch)
	if defaultBranch == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var refs []string
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		if _, ok := seen[ref]; ok {
			return
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	if !strings.HasPrefix(defaultBranch, "origin/") && !strings.HasPrefix(defaultBranch, "refs/") {
		add("origin/" + defaultBranch)
		add("refs/remotes/origin/" + defaultBranch)
	}
	add(defaultBranch)
	return refs
}

func workspaceDefaultBranch(defaultBranch string) string {
	defaultBranch = strings.TrimSpace(defaultBranch)
	if defaultBranch == domain.DefaultBranchAuto {
		return ""
	}
	return defaultBranch
}

func selectWorkspaceComparePR(prs []domain.PullRequest, defaultBranch string) (domain.PullRequest, bool) {
	if len(prs) == 0 {
		return domain.PullRequest{}, false
	}
	defaultBranch = strings.TrimSpace(defaultBranch)
	candidates := append([]domain.PullRequest(nil), prs...)
	sort.SliceStable(candidates, func(i, j int) bool {
		leftDefault := prTargetsDefaultBranch(candidates[i], defaultBranch)
		rightDefault := prTargetsDefaultBranch(candidates[j], defaultBranch)
		if leftDefault != rightDefault {
			return leftDefault
		}
		leftOpen := !candidates[i].Merged && !candidates[i].Closed
		rightOpen := !candidates[j].Merged && !candidates[j].Closed
		if leftOpen != rightOpen {
			return leftOpen
		}
		leftCreated := candidates[i].CreatedAtProvider
		rightCreated := candidates[j].CreatedAtProvider
		if !leftCreated.IsZero() && !rightCreated.IsZero() && !leftCreated.Equal(rightCreated) {
			return leftCreated.Before(rightCreated)
		}
		if candidates[i].Number != candidates[j].Number {
			if candidates[i].Number == 0 {
				return false
			}
			if candidates[j].Number == 0 {
				return true
			}
			return candidates[i].Number < candidates[j].Number
		}
		return candidates[i].URL < candidates[j].URL
	})
	for _, pr := range candidates {
		if strings.TrimSpace(pr.BaseSHA) != "" {
			return pr, true
		}
	}
	return domain.PullRequest{}, false
}

func prTargetsDefaultBranch(pr domain.PullRequest, defaultBranch string) bool {
	target := strings.TrimSpace(pr.TargetBranch)
	if target == "" || defaultBranch == "" {
		return false
	}
	if target == defaultBranch {
		return true
	}
	if !strings.HasPrefix(defaultBranch, "origin/") && !strings.HasPrefix(defaultBranch, "refs/") {
		return target == "origin/"+defaultBranch || target == "refs/heads/"+defaultBranch || target == "refs/remotes/origin/"+defaultBranch
	}
	return false
}

func gitCommitExists(ctx context.Context, root, rev string) bool {
	_, err := gitWorkspaceOutput(ctx, root, "cat-file", "-e", rev+"^{commit}")
	return err == nil
}

func gitMergeBase(ctx context.Context, root, ref string) (string, bool) {
	out, err := gitWorkspaceOutput(ctx, root, "merge-base", "HEAD", ref)
	if err != nil {
		return "", false
	}
	sha := strings.TrimSpace(out)
	return sha, sha != ""
}

// gitIsAncestor reports whether ancestor is reachable from descendant
// (including ancestor == descendant).
func gitIsAncestor(ctx context.Context, root, ancestor, descendant string) bool {
	_, err := gitWorkspaceOutput(ctx, root, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

// mergeBaseCandidate validates a revision-derived compare-base candidate: the
// revision must exist locally and share a common ancestor with HEAD. It
// always returns the merge-base SHA, never the raw revision, so a rewritten
// or unrelated commit can never be used directly as a two-tree diff base.
func mergeBaseCandidate(ctx context.Context, root, revision string) (string, bool) {
	revision = strings.TrimSpace(revision)
	if revision == "" || !gitCommitExists(ctx, root, revision) {
		return "", false
	}
	return gitMergeBase(ctx, root, revision)
}

// gitRevision resolves rev to its full SHA, or ok=false if it cannot be
// resolved.
func gitRevision(ctx context.Context, root, rev string) (string, bool) {
	out, err := gitWorkspaceOutput(ctx, root, "rev-parse", "--verify", rev)
	if err != nil {
		return "", false
	}
	sha := strings.TrimSpace(out)
	return sha, sha != ""
}

func (s *Service) listWorkspaceProjectFiles(ctx context.Context, rec domain.SessionRecord, project domain.ProjectRecord) (WorkspaceFiles, error) {
	rows, err := s.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		return WorkspaceFiles{}, fmt.Errorf("list workspace project rows: %w", err)
	}
	if len(rows) == 0 {
		prs, err := s.workspaceComparePRs(ctx, rec.ID)
		if err != nil {
			return WorkspaceFiles{}, err
		}
		resolve := func(rctx context.Context) workspaceCompareTarget {
			return resolveWorkspaceCompare(rctx, rec.Metadata.WorkspacePath, rec.Metadata.DiffBaseSHA, rec.Metadata.DiffBaseRef, defaultBranchForProject(project, true), prs)
		}
		files, truncated, compare, err := s.workspaceFileSummariesCached(ctx, rec.ID, rec.Metadata.WorkspacePath, "", nil, resolve)
		if err != nil {
			return WorkspaceFiles{}, err
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		return WorkspaceFiles{SessionID: rec.ID, CompareBaseSHA: compare.BaseSHA, CompareBaseRef: compare.BaseRef, CompareMode: compare.Mode, Files: files, Truncated: truncated}, nil
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
			// Compatibility for sessions created before per-repository base refs
			// were persisted. New rows always use their adapter-resolved ref.
			baseRef = defaultBranch
		}
		resolve := func(rctx context.Context) workspaceCompareTarget {
			return resolveWorkspaceProjectCompare(rctx, row.WorktreePath, row.BaseSHA, baseRef)
		}
		repoFiles, repoTruncated, compare, err := s.workspaceFileSummariesCached(ctx, rec.ID, row.WorktreePath, prefix, exclude, resolve)
		if err != nil {
			return WorkspaceFiles{}, err
		}
		compares = append(compares, compare)
		files, truncated = appendWorkspaceFilesWithCap(files, repoFiles, truncated)
		truncated = truncated || repoTruncated
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	compare := aggregateWorkspaceCompare(compares)
	return WorkspaceFiles{
		SessionID:      rec.ID,
		CompareBaseSHA: compare.BaseSHA,
		CompareBaseRef: compare.BaseRef,
		CompareMode:    compare.Mode,
		Files:          files,
		Truncated:      truncated,
	}, nil
}

func (s *Service) resolveWorkspaceProjectFileTarget(ctx context.Context, rec domain.SessionRecord, project domain.ProjectRecord, rel string) (workspaceFileTarget, error) {
	rows, err := s.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		return workspaceFileTarget{}, fmt.Errorf("list workspace project rows: %w", err)
	}
	if len(rows) == 0 {
		prs, err := s.workspaceComparePRs(ctx, rec.ID)
		if err != nil {
			return workspaceFileTarget{}, err
		}
		resolve := func(rctx context.Context) workspaceCompareTarget {
			return resolveWorkspaceCompare(rctx, rec.Metadata.WorkspacePath, rec.Metadata.DiffBaseSHA, rec.Metadata.DiffBaseRef, defaultBranchForProject(project, true), prs)
		}
		compare, changes, err := s.resolveWorkspaceChanges(ctx, rec.ID, rec.Metadata.WorkspacePath, resolve)
		if err != nil {
			return workspaceFileTarget{}, err
		}
		return workspaceFileTarget{root: rec.Metadata.WorkspacePath, rel: rel, compare: compare, changes: changes}, nil
	}
	row, prefix, repoRel, ok := workspaceProjectFileTarget(rec.Metadata.WorkspacePath, rows, rel)
	if !ok {
		return workspaceFileTarget{}, apierr.NotFound("WORKSPACE_FILE_NOT_FOUND", "Workspace file not found")
	}
	defaultBranch := defaultBranchForProject(project, true)
	baseRef := row.BaseRef
	if strings.TrimSpace(baseRef) == "" {
		baseRef = defaultBranch
	}
	resolve := func(rctx context.Context) workspaceCompareTarget {
		return resolveWorkspaceProjectCompare(rctx, row.WorktreePath, row.BaseSHA, baseRef)
	}
	compare, changes, err := s.resolveWorkspaceChanges(ctx, rec.ID, row.WorktreePath, resolve)
	if err != nil {
		return workspaceFileTarget{}, err
	}
	return workspaceFileTarget{root: row.WorktreePath, prefix: prefix, rel: repoRel, compare: compare, changes: changes}, nil
}

func appendWorkspaceFilesWithCap(files, repoFiles []WorkspaceFileSummary, truncated bool) ([]WorkspaceFileSummary, bool) {
	remaining := maxWorkspaceFiles - len(files)
	if remaining <= 0 {
		if len(repoFiles) > 0 {
			truncated = true
		}
		return files, truncated
	}
	if len(repoFiles) > remaining {
		files = append(files, repoFiles[:remaining]...)
		return files, true
	}
	return append(files, repoFiles...), truncated
}

// workspaceFileSummariesCached resolves the changed-file list for one
// worktree root, reusing a cached compare+status result when a recent
// ListWorkspaceFiles/GetWorkspaceFile call for the same session/root already
// computed it (see workspaceCache). On a cache miss it resolves the compare
// base, computes git status/diff/numstat, and lists working-tree files
// concurrently, since none of those git calls depend on each other.
func (s *Service) workspaceFileSummariesCached(
	ctx context.Context, id domain.SessionID, root, prefix string, excludePrefixes []string,
	resolve func(ctx context.Context) workspaceCompareTarget,
) ([]WorkspaceFileSummary, bool, workspaceCompareTarget, error) {
	var compare workspaceCompareTarget
	var changes workspaceChangeSet
	var lsParts []string
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		compare, changes, err = s.resolveWorkspaceChanges(gctx, id, root, resolve)
		return err
	})
	g.Go(func() (err error) {
		lsParts, err = gitLsFilesParts(gctx, root)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, false, workspaceCompareTarget{}, err
	}
	paths, truncated := mergeWorkspaceFilePaths(lsParts, changes.changedPaths())
	files := buildWorkspaceFileSummaries(root, prefix, excludePrefixes, paths, changes)
	return files, truncated, compare, nil
}

func buildWorkspaceFileSummaries(root, prefix string, excludePrefixes, paths []string, changes workspaceChangeSet) []WorkspaceFileSummary {
	files := make([]WorkspaceFileSummary, 0, len(paths))
	for _, rel := range paths {
		if workspacePathExcluded(rel, excludePrefixes) {
			continue
		}
		status := changes.statuses[rel]
		if status == "" {
			status = WorkspaceFileUnmodified
		}
		additions, deletions := changes.counts[rel][0], changes.counts[rel][1]
		size, binary := workspaceFileSizeAndBinary(root, rel, status)
		files = append(files, WorkspaceFileSummary{
			Path:         joinWorkspaceRelative(prefix, rel),
			PreviousPath: joinWorkspaceRelative(prefix, changes.previous[rel]),
			Status:       status,
			Additions:    additions,
			Deletions:    deletions,
			Size:         size,
			Binary:       binary,
		})
	}
	return files
}

// resolveWorkspaceChanges resolves the compare base and git status/diff maps
// for one worktree root, reusing a cached result from a recent list/detail
// call for the same session/root instead of re-running resolve+
// workspaceChangeMaps from scratch. On a cache miss, concurrent callers for
// the same (session, root) — e.g. "Expand All" firing GetWorkspaceFile for
// every changed file in the same instant — share a single in-flight
// resolve+workspaceChangeMaps call via singleflight instead of each spawning
// its own redundant set of git subprocesses.
func (s *Service) resolveWorkspaceChanges(
	ctx context.Context, id domain.SessionID, root string,
	resolve func(ctx context.Context) workspaceCompareTarget,
) (workspaceCompareTarget, workspaceChangeSet, error) {
	key := workspaceCacheKey{session: id, root: root}
	if cached, ok := s.workspaceCache.get(key); ok {
		return cached.compare, cached.changes, nil
	}
	v, err, _ := s.workspaceGroup.Do(key.String(), func() (any, error) {
		// A concurrent caller may have already populated the cache while this
		// call was queued behind the singleflight lock — re-check before
		// doing the work again.
		if cached, ok := s.workspaceCache.get(key); ok {
			return cached, nil
		}
		compare := resolve(ctx)
		changes, err := workspaceChangeMaps(ctx, root, compare.gitBase())
		if err != nil {
			return nil, err
		}
		entry := workspaceCacheEntry{at: s.now(), compare: compare, changes: changes}
		s.workspaceCache.set(key, entry)
		return entry, nil
	})
	if err != nil {
		return workspaceCompareTarget{}, workspaceChangeSet{}, err
	}
	entry, ok := v.(workspaceCacheEntry)
	if !ok {
		return workspaceCompareTarget{}, workspaceChangeSet{}, fmt.Errorf("resolveWorkspaceChanges: unexpected singleflight result type %T", v)
	}
	return entry.compare, entry.changes, nil
}

func workspaceFileDetail(ctx context.Context, id domain.SessionID, root, prefix, rel string, section WorkspaceFileSection, compare workspaceCompareTarget, changes workspaceChangeSet) (WorkspaceFileDetail, error) {
	status := changes.statuses[rel]
	if status == "" {
		status = WorkspaceFileUnmodified
	}
	additions, deletions := changes.counts[rel][0], changes.counts[rel][1]
	previousPath := changes.previous[rel]
	detail := WorkspaceFileDetail{
		SessionID:      id,
		Path:           joinWorkspaceRelative(prefix, rel),
		PreviousPath:   joinWorkspaceRelative(prefix, previousPath),
		Status:         status,
		Additions:      additions,
		Deletions:      deletions,
		Deleted:        status == WorkspaceFileDeleted,
		CompareBaseSHA: compare.BaseSHA,
		CompareBaseRef: compare.BaseRef,
		CompareMode:    compare.Mode,
	}
	if !detail.Deleted {
		file, info, err := confinedWorkspaceFile(root, rel)
		if err != nil {
			return WorkspaceFileDetail{}, err
		}
		detail.Size = info.Size()
		content, binary, truncated, err := readWorkspaceTextFile(file, maxWorkspaceFileBytes)
		if err != nil {
			return WorkspaceFileDetail{}, err
		}
		detail.Binary = binary
		detail.ContentTruncated = truncated
		if !binary {
			detail.Content = content
		}
	}
	detail.ImageMediaType = workspaceImageDetailMediaType(rel, detail.Binary, detail.Deleted)
	_, untracked := changes.untracked[rel]
	diff, truncated, err := workspaceFileDiff(ctx, root, compare.gitBase(), rel, status, section, previousPath, detail.Content, detail.Binary, untracked)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	detail.Diff = diff
	detail.DiffTruncated = truncated
	return detail, nil
}

func workspaceProjectPrefixes(root string, rows []domain.SessionWorktreeRecord) map[string]string {
	prefixes := map[string]string{}
	for _, row := range rows {
		prefix, ok := workspaceProjectPrefix(root, row.WorktreePath)
		if !ok {
			continue
		}
		prefixes[row.RepoName] = prefix
	}
	return prefixes
}

func workspaceProjectPrefix(root, worktree string) (string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	worktreeAbs, err := filepath.Abs(worktree)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, worktreeAbs)
	if err != nil {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func nonEmptyWorkspacePrefixes(prefixes map[string]string) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix != "" {
			out = append(out, prefix)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func workspaceProjectFileTarget(root string, rows []domain.SessionWorktreeRecord, rel string) (domain.SessionWorktreeRecord, string, string, bool) {
	var best domain.SessionWorktreeRecord
	bestPrefix := ""
	bestRel := rel
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
				bestRel = rel
				found = true
			}
			continue
		}
		if !strings.HasPrefix(rel, prefix+"/") {
			continue
		}
		if !found || len(prefix) > len(bestPrefix) {
			best = row
			bestPrefix = prefix
			bestRel = strings.TrimPrefix(rel, prefix+"/")
			found = true
		}
	}
	return best, bestPrefix, bestRel, found
}

func aggregateWorkspaceCompare(compares []workspaceCompareTarget) workspaceCompareTarget {
	if len(compares) == 0 {
		return workspaceCompareTarget{Mode: WorkspaceCompareHeadFallback}
	}
	baseFound := false
	fallbackFound := false
	baseSHA := ""
	baseRef := ""
	for _, compare := range compares {
		if compare.Mode != WorkspaceCompareBase {
			fallbackFound = true
			continue
		}
		if !baseFound {
			baseFound = true
			baseSHA = compare.BaseSHA
			baseRef = compare.BaseRef
			continue
		}
		if compare.BaseSHA != baseSHA {
			baseSHA = ""
		}
		if compare.BaseRef != baseRef {
			baseRef = ""
		}
	}
	if !baseFound {
		return workspaceCompareTarget{Mode: WorkspaceCompareHeadFallback}
	}
	if fallbackFound {
		baseSHA = ""
		baseRef = ""
	}
	return workspaceCompareTarget{BaseSHA: baseSHA, BaseRef: baseRef, Mode: WorkspaceCompareBase}
}

func workspacePathExcluded(rel string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix == "" {
			continue
		}
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
}

func joinWorkspaceRelative(prefix, rel string) string {
	if rel == "" {
		return ""
	}
	if prefix == "" {
		return rel
	}
	return prefix + "/" + rel
}

func scratchWorkspaceFiles(root string) ([]WorkspaceFileSummary, bool, error) {
	rootResolved, err := resolvedWorkspaceRoot(root)
	if err != nil {
		return nil, false, err
	}
	var files []WorkspaceFileSummary
	truncated := false
	err = filepath.WalkDir(rootResolved, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fullPath == rootResolved {
			return nil
		}
		relPath, err := filepath.Rel(rootResolved, fullPath)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relPath)
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, include, err := scratchWorkspaceFileInfo(rootResolved, fullPath, entry)
		if err != nil {
			return err
		}
		if !include {
			return nil
		}
		if len(files) >= maxWorkspaceFiles {
			truncated = true
			return filepath.SkipAll
		}
		_, binary, _, err := readWorkspaceTextFile(fullPath, 8192)
		if err != nil {
			return err
		}
		files = append(files, WorkspaceFileSummary{
			Path:      rel,
			Status:    WorkspaceFileAdded,
			Additions: 0,
			Deletions: 0,
			Size:      info.Size(),
			Binary:    binary,
		})
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, truncated, nil
}

func scratchWorkspaceFile(root string, id domain.SessionID, rel string) (WorkspaceFileDetail, error) {
	file, info, err := confinedWorkspaceFile(root, rel)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	content, binary, truncated, err := readWorkspaceTextFile(file, maxWorkspaceFileBytes)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	additions := 0
	if !binary {
		additions = textLineCount(content)
	}
	detail := WorkspaceFileDetail{
		SessionID:        id,
		Path:             rel,
		Status:           WorkspaceFileAdded,
		Additions:        additions,
		Deletions:        0,
		Size:             info.Size(),
		Binary:           binary,
		ContentTruncated: truncated,
		ImageMediaType:   workspaceImageDetailMediaType(rel, binary, false),
	}
	if !binary {
		detail.Content = content
	}
	return detail, nil
}

func resolvedWorkspaceRoot(root string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	rootResolved, err := resolvedFilesystemPath(rootAbs)
	if err != nil {
		return "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	info, err := os.Stat(rootResolved)
	if err != nil || !info.IsDir() {
		return "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	return rootResolved, nil
}

func scratchWorkspaceFileInfo(rootResolved, fullPath string, entry fs.DirEntry) (os.FileInfo, bool, error) {
	info, err := entry.Info()
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if !info.Mode().IsRegular() {
			return nil, false, nil
		}
		abs, err := filepath.Abs(fullPath)
		if err != nil {
			return nil, false, err
		}
		if !pathWithin(rootResolved, abs) {
			return nil, false, nil
		}
		return info, true, nil
	}
	targetInfo, ok := statScratchFileTarget(fullPath)
	if !ok {
		return nil, false, nil
	}
	resolved, ok := resolvedScratchPath(fullPath)
	if !ok {
		return nil, false, nil
	}
	if !pathWithin(rootResolved, resolved) {
		return nil, false, nil
	}
	return targetInfo, true, nil
}

func statScratchFileTarget(filePath string) (os.FileInfo, bool) {
	info, err := os.Stat(filePath)
	return info, err == nil && !info.IsDir()
}

func resolvedScratchPath(filePath string) (string, bool) {
	resolved, err := resolvedFilesystemPath(filePath)
	return resolved, err == nil
}

// gitLsFilesParts runs the `git ls-files` subprocess, kept separate from the
// (pure, no I/O) merge step in mergeWorkspaceFilePaths so callers can run it
// concurrently with other independent git calls (see workspaceFileSummariesCached).
func gitLsFilesParts(ctx context.Context, root string) ([]string, error) {
	out, err := gitWorkspaceOutput(ctx, root, "ls-files", "-z", "-co", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

func mergeWorkspaceFilePaths(parts []string, extraPaths map[string]struct{}) ([]string, bool) {
	paths := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	truncated := false
	addPath := func(part string) {
		rel := filepath.ToSlash(part)
		if rel == "" {
			return
		}
		if _, ok := seen[rel]; ok {
			return
		}
		seen[rel] = struct{}{}
		if len(paths) >= maxWorkspaceFiles {
			truncated = true
			return
		}
		paths = append(paths, rel)
	}
	extra := make([]string, 0, len(extraPaths))
	for rel := range extraPaths {
		extra = append(extra, rel)
	}
	sort.Strings(extra)
	for _, rel := range extra {
		addPath(rel)
	}
	for _, part := range parts {
		addPath(part)
	}
	return paths, truncated
}

type workspaceChangeSet struct {
	statuses map[string]WorkspaceFileStatus
	counts   map[string][2]int
	previous map[string]string
	// untracked holds paths git diff's machinery doesn't know about relative
	// to base (status "??" from `git status`, absent from `git diff
	// --name-status`) — computed once here so workspaceFileDiff can tell
	// whether to synthesize a diff without its own extra git spawn.
	untracked map[string]struct{}
}

func (c workspaceChangeSet) changedPaths() map[string]struct{} {
	paths := map[string]struct{}{}
	for rel := range c.statuses {
		paths[rel] = struct{}{}
	}
	for rel := range c.counts {
		paths[rel] = struct{}{}
	}
	return paths
}

func workspaceChangeMaps(ctx context.Context, root, base string) (workspaceChangeSet, error) {
	var (
		diffStatuses, statusStatuses map[string]WorkspaceFileStatus
		diffPrevious, statusPrevious map[string]string
		counts                       map[string][2]int
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		diffStatuses, diffPrevious, err = workspaceDiffStatuses(gctx, root, base)
		return err
	})
	g.Go(func() (err error) {
		statusStatuses, statusPrevious, err = workspaceStatuses(gctx, root)
		return err
	})
	g.Go(func() (err error) {
		counts, err = workspaceNumstat(gctx, root, base)
		return err
	})
	if err := g.Wait(); err != nil {
		return workspaceChangeSet{}, err
	}
	statuses := map[string]WorkspaceFileStatus{}
	previous := map[string]string{}
	for rel, status := range diffStatuses {
		statuses[rel] = status
	}
	for rel, prev := range diffPrevious {
		previous[rel] = prev
	}
	for rel, status := range statusStatuses {
		if _, ok := statuses[rel]; ok {
			continue
		}
		statuses[rel] = status
	}
	for rel, prev := range statusPrevious {
		if _, ok := previous[rel]; ok {
			continue
		}
		previous[rel] = prev
	}
	untracked := map[string]struct{}{}
	for rel, status := range statuses {
		if status != WorkspaceFileAdded {
			continue
		}
		// diffStatuses (git diff --name-status against base) is the correct
		// "does git diff machinery know this path" signal — unlike counts
		// (git diff --numstat), which deliberately omits binary files
		// ("-\t-\tpath") even when they ARE tracked relative to base, so
		// counts membership alone would misclassify a tracked binary Added
		// file as untracked.
		if _, ok := diffStatuses[rel]; ok {
			continue
		}
		untracked[rel] = struct{}{}
		if _, ok := counts[rel]; ok {
			continue
		}
		additions, ok := countUntrackedTextLines(root, rel)
		if ok {
			counts[rel] = [2]int{additions, 0}
		}
	}
	return workspaceChangeSet{statuses: statuses, counts: counts, previous: previous, untracked: untracked}, nil
}

// workspaceGitState computes the git-state sections, the commit list between
// base and HEAD, and ahead/behind counts for one worktree root. The four
// section diffs, the commit log, and the ahead/behind lookup are independent
// git subprocesses and run concurrently.
func workspaceGitState(ctx context.Context, root, base string) (WorkspaceFileSections, []CommitSummary, *int, *int, error) {
	var sections WorkspaceFileSections
	var commits []CommitSummary
	var ahead, behind *int
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
		g.Go(func() (err error) {
			commits, err = gitCommitLog(gctx, root, base)
			return err
		})
	}
	g.Go(func() error {
		ahead, behind = gitAheadBehind(gctx, root)
		return nil
	})
	if err := g.Wait(); err != nil {
		return WorkspaceFileSections{}, nil, nil, nil, err
	}
	return sections, commits, ahead, behind, nil
}

// buildSectionSummaries turns a diff's name-status/numstat maps into sorted
// WorkspaceFileSummary rows.
func buildSectionSummaries(root string, statuses map[string]WorkspaceFileStatus, counts map[string][2]int, previous map[string]string) []WorkspaceFileSummary {
	paths := make([]string, 0, len(statuses))
	for rel := range statuses {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	files := make([]WorkspaceFileSummary, 0, len(paths))
	for _, rel := range paths {
		status := statuses[rel]
		additions, deletions := counts[rel][0], counts[rel][1]
		size, binary := workspaceFileSizeAndBinary(root, rel, status)
		files = append(files, WorkspaceFileSummary{
			Path:         rel,
			PreviousPath: previous[rel],
			Status:       status,
			Additions:    additions,
			Deletions:    deletions,
			Size:         size,
			Binary:       binary,
		})
	}
	return files
}

// buildUntrackedSummaries turns a set of untracked paths into sorted
// WorkspaceFileSummary rows, counting text lines the same way the base..
// worktree Files list treats an untracked added file.
func buildUntrackedSummaries(root string, paths []string) []WorkspaceFileSummary {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	files := make([]WorkspaceFileSummary, 0, len(sorted))
	for _, rel := range sorted {
		rel = filepath.ToSlash(rel)
		additions, _ := countUntrackedTextLines(root, rel)
		size, binary := workspaceFileSizeAndBinary(root, rel, WorkspaceFileAdded)
		files = append(files, WorkspaceFileSummary{Path: rel, Status: WorkspaceFileAdded, Additions: additions, Size: size, Binary: binary})
	}
	return files
}

// workspaceSummaryFromFiles aggregates the base..worktree Files list
// (excluding unmodified entries) into panel-header totals.
func workspaceSummaryFromFiles(files []WorkspaceFileSummary) WorkspaceSummary {
	var summary WorkspaceSummary
	for _, file := range files {
		if file.Status == WorkspaceFileUnmodified {
			continue
		}
		summary.Files++
		summary.Additions += file.Additions
		summary.Deletions += file.Deletions
	}
	return summary
}

// gitUntrackedFiles lists untracked, non-ignored files in the worktree.
func gitUntrackedFiles(ctx context.Context, root string) ([]string, error) {
	out, err := gitWorkspaceOutput(ctx, root, "ls-files", "-z", "-o", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// gitCommitLog lists the commits reachable from HEAD but not base, oldest
// first, matching the order they'd be reviewed in.
func gitCommitLog(ctx context.Context, root, base string) ([]CommitSummary, error) {
	out, err := gitWorkspaceOutput(ctx, root, "log", "--format=%H%x1f%s%x1f%an%x1f%aI", "--reverse", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimRight(out, "\n")
	if trimmed == "" {
		return nil, nil
	}
	lines := strings.Split(trimmed, "\n")
	commits := make([]CommitSummary, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\x1f")
		if len(fields) < 4 {
			continue
		}
		timestamp, _ := time.Parse(time.RFC3339, fields[3])
		commits = append(commits, CommitSummary{SHA: fields[0], Subject: fields[1], Author: fields[2], Timestamp: timestamp})
	}
	return commits, nil
}

// gitAheadBehind reports HEAD's commit counts against its upstream, falling
// back to origin/<branch> when no upstream is configured. Both return values
// are nil when neither can be resolved (detached HEAD, no remote, git
// failure) — that means "no push/pull data," not an error worth propagating.
func gitAheadBehind(ctx context.Context, root string) (*int, *int) {
	ref, err := gitWorkspaceOutput(ctx, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	upstream := strings.TrimSpace(ref)
	if err != nil || upstream == "" {
		branchOut, branchErr := gitWorkspaceOutput(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
		branch := strings.TrimSpace(branchOut)
		if branchErr != nil || branch == "" || branch == "HEAD" {
			return nil, nil
		}
		candidate := "origin/" + branch
		if !gitCommitExists(ctx, root, candidate) {
			return nil, nil
		}
		upstream = candidate
	}
	out, err := gitWorkspaceOutput(ctx, root, "rev-list", "--left-right", "--count", "HEAD..."+upstream)
	if err != nil {
		return nil, nil
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return nil, nil
	}
	aheadCount, aheadErr := strconv.Atoi(fields[0])
	behindCount, behindErr := strconv.Atoi(fields[1])
	if aheadErr != nil || behindErr != nil {
		return nil, nil
	}
	return &aheadCount, &behindCount
}

func workspaceStatuses(ctx context.Context, root string) (map[string]WorkspaceFileStatus, map[string]string, error) {
	out, err := gitWorkspaceOutput(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, nil, err
	}
	parts := splitNUL(out)
	statuses := map[string]WorkspaceFileStatus{}
	previous := map[string]string{}
	for i := 0; i < len(parts); i++ {
		entry := parts[i]
		if len(entry) < 4 {
			continue
		}
		xy := entry[:2]
		rel := filepath.ToSlash(entry[3:])
		statuses[rel] = classifyWorkspaceStatus(xy)
		if strings.ContainsAny(xy, "RC") && i+1 < len(parts) {
			previous[rel] = filepath.ToSlash(parts[i+1])
			i++
		}
	}
	return statuses, previous, nil
}

func classifyWorkspaceStatus(xy string) WorkspaceFileStatus {
	switch {
	case xy == "??", strings.Contains(xy, "A"):
		return WorkspaceFileAdded
	case strings.ContainsAny(xy, "RC"):
		return WorkspaceFileRenamed
	case strings.Contains(xy, "D"):
		return WorkspaceFileDeleted
	case strings.ContainsAny(xy, "M"):
		return WorkspaceFileModified
	default:
		return WorkspaceFileModified
	}
}

func workspaceDiffStatuses(ctx context.Context, root, base string) (map[string]WorkspaceFileStatus, map[string]string, error) {
	return workspaceDiffNameStatus(ctx, root, base)
}

// workspaceDiffNameStatus runs `git diff --name-status` with the given
// revision arguments (e.g. a single base for base..worktree, "--cached" for
// index..HEAD, or two revisions for a committed range) and parses the result.
func workspaceDiffNameStatus(ctx context.Context, root string, revArgs ...string) (map[string]WorkspaceFileStatus, map[string]string, error) {
	args := append([]string{"diff", "--name-status", "--find-renames", "-z"}, revArgs...)
	args = append(args, "--")
	out, err := gitWorkspaceOutput(ctx, root, args...)
	if err != nil {
		return nil, nil, err
	}
	statuses, previous := parseNameStatusOutput(out)
	return statuses, previous, nil
}

func parseNameStatusOutput(out string) (map[string]WorkspaceFileStatus, map[string]string) {
	parts := splitNUL(out)
	statuses := map[string]WorkspaceFileStatus{}
	previous := map[string]string{}
	for i := 0; i < len(parts); {
		statusCode, paths, next := parseGitStatusRecord(parts, i)
		i = next
		if statusCode == "" || len(paths) == 0 {
			continue
		}
		status := classifyNameStatus(statusCode)
		rel := filepath.ToSlash(paths[len(paths)-1])
		statuses[rel] = status
		if status == WorkspaceFileRenamed && len(paths) > 1 {
			previous[rel] = filepath.ToSlash(paths[0])
		}
	}
	return statuses, previous
}

func parseGitStatusRecord(parts []string, i int) (string, []string, int) {
	if i >= len(parts) {
		return "", nil, i + 1
	}
	token := parts[i]
	if fields := strings.Split(token, "\t"); len(fields) >= 2 {
		status := fields[0]
		if status == "" {
			return "", nil, i + 1
		}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if len(fields) >= 3 {
				return status, []string{fields[1], fields[2]}, i + 1
			}
			return status, fields[1:], i + 1
		}
		return status, []string{fields[1]}, i + 1
	}
	status := token
	if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
		if i+2 >= len(parts) {
			return status, nil, len(parts)
		}
		return status, []string{parts[i+1], parts[i+2]}, i + 3
	}
	if i+1 >= len(parts) {
		return status, nil, len(parts)
	}
	return status, []string{parts[i+1]}, i + 2
}

func classifyNameStatus(status string) WorkspaceFileStatus {
	switch status[0] {
	case 'A':
		return WorkspaceFileAdded
	case 'D':
		return WorkspaceFileDeleted
	case 'R', 'C':
		return WorkspaceFileRenamed
	case 'M', 'T':
		return WorkspaceFileModified
	default:
		return WorkspaceFileModified
	}
}

func workspaceNumstat(ctx context.Context, root, base string) (map[string][2]int, error) {
	return workspaceDiffNumstat(ctx, root, base)
}

// workspaceDiffNumstat runs `git diff --numstat` with the given revision
// arguments; see workspaceDiffNameStatus for the argument forms.
func workspaceDiffNumstat(ctx context.Context, root string, revArgs ...string) (map[string][2]int, error) {
	args := append([]string{"diff", "--numstat", "--find-renames", "-z"}, revArgs...)
	args = append(args, "--")
	out, err := gitWorkspaceOutput(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	return parseNumstatOutput(out), nil
}

func parseNumstatOutput(out string) map[string][2]int {
	counts := map[string][2]int{}
	parts := splitNUL(out)
	for i := 0; i < len(parts); {
		fields := strings.Split(parts[i], "\t")
		i++
		if len(fields) < 3 {
			continue
		}
		additions, addOK := parseNumstatField(fields[0])
		deletions, delOK := parseNumstatField(fields[1])
		if !addOK || !delOK {
			if fields[2] == "" && i+2 <= len(parts) {
				i += 2
			}
			continue
		}
		rel := fields[2]
		if rel == "" && i+2 <= len(parts) {
			rel = parts[i+1]
			i += 2
		}
		if rel == "" {
			continue
		}
		counts[filepath.ToSlash(rel)] = [2]int{additions, deletions}
	}
	return counts
}

func parseNumstatField(raw string) (int, bool) {
	if raw == "-" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil
}

func workspaceFileSizeAndBinary(root, rel string, status WorkspaceFileStatus) (int64, bool) {
	if status == WorkspaceFileDeleted {
		return 0, false
	}
	file, info, err := confinedWorkspaceFile(root, rel)
	if err != nil {
		return 0, false
	}
	_, binary, _, err := readWorkspaceTextFile(file, 8192)
	if err != nil {
		return info.Size(), false
	}
	return info.Size(), binary
}

func workspaceFileDiff(ctx context.Context, root, base, rel string, status WorkspaceFileStatus, section WorkspaceFileSection, previousPath, content string, binary, untracked bool) (string, bool, error) {
	if status == WorkspaceFileUnmodified {
		return "", false, nil
	}
	if status == WorkspaceFileAdded && untracked {
		if binary {
			return "", false, nil
		}
		diff := syntheticAddedFileDiff(rel, content)
		truncatedDiff, truncated := truncateUTF8(diff, maxWorkspaceDiffBytes)
		return truncatedDiff, truncated, nil
	}
	// staged mirrors the `--cached` diff sections.Staged is built from (index
	// vs HEAD); unstaged mirrors the bare diff sections.Unstaged is built from
	// (worktree vs index). Anything else falls back to base..worktree, the
	// combined change shown before per-section diffs existed.
	args := []string{"diff", "--no-ext-diff", "--find-renames", "--unified=3"}
	switch section {
	case WorkspaceFileSectionStaged:
		args = append(args, "--cached")
	case WorkspaceFileSectionUnstaged:
		// No revision arg: worktree vs index.
	default:
		args = append(args, base)
	}
	args = append(args, "--")
	if status == WorkspaceFileRenamed && previousPath != "" {
		args = append(args, previousPath)
	}
	args = append(args, rel)
	out, err := gitWorkspaceOutput(ctx, root, args...)
	if err != nil {
		return "", false, err
	}
	truncatedDiff, truncated := truncateUTF8(out, maxWorkspaceDiffBytes)
	return truncatedDiff, truncated, nil
}

func syntheticAddedFileDiff(rel, content string) string {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", rel, rel)
	b.WriteString("new file mode 100644\n")
	b.WriteString("--- /dev/null\n")
	fmt.Fprintf(&b, "+++ b/%s\n", rel)
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		b.WriteByte('+')
		b.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func confinedWorkspaceFile(root, rel string) (string, os.FileInfo, error) {
	clean, err := cleanWorkspaceRelativePath(rel)
	if err != nil {
		return "", nil, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", nil, apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	rootResolved, err := resolvedFilesystemPath(rootAbs)
	if err != nil {
		return "", nil, apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	rootInfo, err := os.Stat(rootResolved)
	if err != nil || !rootInfo.IsDir() {
		return "", nil, apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	target := filepath.Join(rootResolved, filepath.FromSlash(clean))
	targetAbs, err := filepath.Abs(target)
	if err != nil || !pathWithin(rootResolved, targetAbs) {
		return "", nil, apierr.Invalid("INVALID_WORKSPACE_PATH", "path escapes session workspace", nil)
	}
	resolved := rootResolved
	for _, part := range strings.Split(clean, "/") {
		resolved = filepath.Join(resolved, part)
		resolved, err = resolvedFilesystemPath(resolved)
		if err != nil {
			return "", nil, apierr.NotFound("WORKSPACE_FILE_NOT_FOUND", "Workspace file not found")
		}
		if !pathWithin(rootResolved, resolved) {
			return "", nil, apierr.Invalid("INVALID_WORKSPACE_PATH", "path escapes session workspace", nil)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, apierr.NotFound("WORKSPACE_FILE_NOT_FOUND", "Workspace file not found")
	}
	if info.IsDir() {
		return "", nil, apierr.Invalid("WORKSPACE_PATH_IS_DIRECTORY", "Workspace path is a directory", nil)
	}
	return resolved, info, nil
}

func cleanWorkspaceRelativePath(raw string) (string, error) {
	// Backslash is a path separator only on Windows. Everywhere else it is a
	// legal filename character, so folding it here makes a file named `a\b.txt`
	// unopenable — and worse, silently resolves it to `a/b.txt`, a different
	// file that may well exist.
	normalized := raw
	if runtime.GOOS == "windows" {
		normalized = strings.ReplaceAll(raw, "\\", "/")
	}
	trimmed := strings.TrimSpace(normalized)
	if trimmed == "" || path.IsAbs(trimmed) || filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" {
		return "", apierr.Invalid("INVALID_WORKSPACE_PATH", "workspace path must be relative", nil)
	}
	clean := path.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", apierr.Invalid("INVALID_WORKSPACE_PATH", "path escapes session workspace", nil)
	}
	return clean, nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readWorkspaceTextFile(file string, limit int) (string, bool, bool, error) {
	handle, err := os.Open(file)
	if err != nil {
		return "", false, false, apierr.NotFound("WORKSPACE_FILE_NOT_FOUND", "Workspace file not found")
	}
	defer func() { _ = handle.Close() }()
	data, err := io.ReadAll(io.LimitReader(handle, int64(limit+1)))
	if err != nil {
		return "", false, false, err
	}
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
	}
	if isBinary(data) {
		return "", true, truncated, nil
	}
	content := string(data)
	if !utf8.ValidString(content) {
		return "", true, truncated, nil
	}
	return content, false, truncated, nil
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
}

func countUntrackedTextLines(root, rel string) (int, bool) {
	file, _, err := confinedWorkspaceFile(root, rel)
	if err != nil {
		return 0, false
	}
	content, binary, _, err := readWorkspaceTextFile(file, maxWorkspaceFileBytes)
	if err != nil || binary {
		return 0, false
	}
	if content == "" {
		return 0, true
	}
	return textLineCount(content), true
}

func textLineCount(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + btoi(!strings.HasSuffix(content, "\n"))
}

func truncateUTF8(in string, limit int) (string, bool) {
	if len(in) <= limit {
		return in, false
	}
	end := 0
	for i := range in {
		if i > limit {
			break
		}
		end = i
	}
	if end == 0 {
		end = limit
	}
	return in[:end], true
}

func gitWorkspaceOutput(ctx context.Context, root string, args ...string) (string, error) {
	cmd := aoprocess.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if stdout := strings.TrimSpace(string(out)); stdout != "" {
			if detail != "" {
				detail += ": "
			}
			detail += stdout
		}
		// A git command against a worktree whose owning repository has been
		// deleted from disk fails with an opaque exit-128 the caller can't act
		// on. Detect that single condition so the session read surface maps to
		// a clean 404 (PROJECT_FOLDER_MISSING) instead of a raw 500.
		if workspaceRepoUnavailable(root) {
			return "", fmt.Errorf("git -C %s %s: %w", root, strings.Join(args, " "), ports.ErrWorkspaceRepoUnavailable)
		}
		return "", fmt.Errorf("git -C %s %s: %w: %s", root, strings.Join(args, " "), err, detail)
	}
	return string(out), nil
}

// workspaceRepoUnavailable reports whether a worktree root can no longer reach
// its owning repository on disk. A git worktree keeps a `.git` file at its
// root whose `gitdir:` line points at the owning repository's common git
// directory; when that repository has been deleted the pointed-to path no
// longer exists and git fails with "not a git repository". A plain checkout
// instead keeps its metadata in a `.git` directory at the root, whose absence
// is the equivalent signal. Only the genuinely-gone-repository case returns
// true; any other (available) repository reports false so the original git
// error keeps its existing semantics.
func workspaceRepoUnavailable(root string) bool {
	gitPath := filepath.Join(root, ".git")
	if data, err := os.ReadFile(gitPath); err == nil {
		gitdir, ok := parseGitDirFile(string(data))
		if !ok {
			return false
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(root, gitdir)
		}
		info, err := os.Stat(gitdir)
		return err != nil || !info.IsDir()
	}
	info, err := os.Stat(gitPath)
	return err != nil || !info.IsDir()
}

// parseGitDirFile extracts the path from a git worktree `.git` file's
// "gitdir: <path>" line.
func parseGitDirFile(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		const prefix = "gitdir:"
		if strings.HasPrefix(line, prefix) {
			gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if gitdir != "" {
				return gitdir, true
			}
		}
	}
	return "", false
}

func splitNUL(out string) []string {
	raw := strings.TrimRight(out, "\x00")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\x00")
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}
