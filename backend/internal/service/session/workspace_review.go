package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

const (
	maxWorkspaceDiffPaths      = 100
	maxWorkspaceDiffGroupBytes = 2 * 1024 * 1024
	maxWorkspaceRevisionBytes  = 2 * 1024 * 1024
	maxWorkspaceSearchResults  = 100
	workspaceReviewTimeout     = 15 * time.Second
)

// WorkspaceDiffScope selects the two revisions used by the review APIs.
type WorkspaceDiffScope string

// Supported workspace comparison scopes.
const (
	WorkspaceDiffCombined  WorkspaceDiffScope = "combined"
	WorkspaceDiffCommitted WorkspaceDiffScope = "committed"
	WorkspaceDiffStaged    WorkspaceDiffScope = "staged"
	WorkspaceDiffUnstaged  WorkspaceDiffScope = "unstaged"
	WorkspaceDiffUntracked WorkspaceDiffScope = "untracked"
)

// WorkspaceDiffInput requests a bounded set of patches from one comparison scope.
type WorkspaceDiffInput struct {
	Scope            WorkspaceDiffScope
	Paths            []string
	ContextLines     int
	IgnoreWhitespace bool
	WorkspaceVersion string
}

// WorkspaceDiffDeferred identifies a file intentionally omitted from a patch group.
type WorkspaceDiffDeferred struct {
	Path   string
	Reason string
}

// WorkspaceDiffGroup is one repository-local unified patch.
type WorkspaceDiffGroup struct {
	Repository    string
	Patch         string
	Truncated     bool
	IncludedPaths []string
	Deferred      []WorkspaceDiffDeferred
	Errors        []WorkspaceDiffError
}

// WorkspaceDiffError is a redacted repository-local failure.
type WorkspaceDiffError struct {
	Code    string
	Message string
}

// WorkspaceDiffs is the grouped response consumed by virtualized review surfaces.
type WorkspaceDiffs struct {
	SessionID        domain.SessionID
	WorkspaceVersion string
	Groups           []WorkspaceDiffGroup
}

// WorkspaceFileRevision is one text-capable side of a comparison.
type WorkspaceFileRevision struct {
	SessionID        domain.SessionID
	Path             string
	Side             WorkspaceFileBlobSide
	Revision         string
	WorkspaceVersion string
	MediaType        string
	Encoding         string
	Size             int64
	Exists           bool
	Binary           bool
	Truncated        bool
	Content          string
}

// WorkspaceFileSearchResult is one path match in the complete workspace tree.
type WorkspaceFileSearchResult struct {
	Path            string
	Status          WorkspaceFileStatus
	Size            int64
	Binary          bool
	FileFingerprint string
}

// WorkspaceFileSearch is a bounded, cursor-paginated path search result.
type WorkspaceFileSearch struct {
	SessionID  domain.SessionID
	Query      string
	Results    []WorkspaceFileSearchResult
	NextCursor string
	Truncated  bool
}

func normalizeWorkspaceDiffScope(scope WorkspaceDiffScope) (WorkspaceDiffScope, error) {
	if scope == "" {
		return WorkspaceDiffCombined, nil
	}
	switch scope {
	case WorkspaceDiffCombined, WorkspaceDiffCommitted, WorkspaceDiffStaged, WorkspaceDiffUnstaged, WorkspaceDiffUntracked:
		return scope, nil
	default:
		return "", apierr.Invalid("INVALID_WORKSPACE_DIFF_SCOPE", "scope must be combined, committed, staged, unstaged, or untracked", nil)
	}
}

func summaryFingerprint(file WorkspaceFileSummary) string {
	return hashWorkspaceReviewValue(file.Path, file.PreviousPath, string(file.Status), strconv.Itoa(file.Additions), strconv.Itoa(file.Deletions), strconv.FormatInt(file.Size, 10), strconv.FormatBool(file.Binary))
}

func finalizeWorkspaceFiles(files WorkspaceFiles) WorkspaceFiles {
	for i := range files.Files {
		files.Files[i].FileFingerprint = summaryFingerprint(files.Files[i])
	}
	sections := []*[]WorkspaceFileSummary{&files.Sections.Staged, &files.Sections.Unstaged, &files.Sections.Untracked, &files.Sections.Committed}
	for _, section := range sections {
		for i := range *section {
			(*section)[i].FileFingerprint = summaryFingerprint((*section)[i])
		}
	}
	parts := []string{string(files.SessionID), files.CompareBaseSHA, files.CompareBaseRef, string(files.CompareMode), strconv.FormatBool(files.Truncated)}
	for _, file := range files.Files {
		parts = append(parts, file.FileFingerprint)
	}
	for _, section := range sections {
		for _, file := range *section {
			parts = append(parts, file.FileFingerprint)
		}
	}
	files.WorkspaceVersion = hashWorkspaceReviewValue(parts...)
	return files
}

func finalizeWorkspaceFileDetail(detail WorkspaceFileDetail) WorkspaceFileDetail {
	detail.FileFingerprint = hashWorkspaceReviewValue(detail.Path, detail.PreviousPath, string(detail.Status), detail.Content, detail.Diff, strconv.FormatInt(detail.Size, 10), strconv.FormatBool(detail.Binary), strconv.FormatBool(detail.Deleted))
	detail.WorkspaceVersion = hashWorkspaceReviewValue(string(detail.SessionID), detail.CompareBaseSHA, detail.CompareBaseRef, detail.FileFingerprint)
	return detail
}

func hashWorkspaceReviewValue(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(h, strconv.Itoa(len(part)))
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type workspaceDiffTargetGroup struct {
	root    string
	prefix  string
	base    string
	targets []workspaceFileTarget
}

// GetWorkspaceDiffs returns one bounded unified patch per repository. The
// response deliberately remains renderer-agnostic.
func (s *Service) GetWorkspaceDiffs(ctx context.Context, id domain.SessionID, input WorkspaceDiffInput) (WorkspaceDiffs, error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceReviewTimeout)
	defer cancel()
	scope, err := normalizeWorkspaceDiffScope(input.Scope)
	if err != nil {
		return WorkspaceDiffs{}, err
	}
	if len(input.Paths) == 0 {
		return WorkspaceDiffs{}, apierr.Invalid("WORKSPACE_DIFF_PATHS_REQUIRED", "at least one path is required", nil)
	}
	if len(input.Paths) > maxWorkspaceDiffPaths {
		return WorkspaceDiffs{}, apierr.Invalid("WORKSPACE_DIFF_TOO_MANY_PATHS", "too many paths requested", map[string]any{"limit": maxWorkspaceDiffPaths})
	}
	if input.ContextLines < 0 || input.ContextLines > 20 {
		return WorkspaceDiffs{}, apierr.Invalid("INVALID_WORKSPACE_DIFF_CONTEXT", "contextLines must be between 0 and 20", nil)
	}
	current, err := s.ListWorkspaceFiles(ctx, id)
	if err != nil {
		return WorkspaceDiffs{}, err
	}
	if input.WorkspaceVersion != "" && input.WorkspaceVersion != current.WorkspaceVersion {
		return WorkspaceDiffs{}, apierr.Conflict("WORKSPACE_SNAPSHOT_STALE", "Workspace changed while the diff was loading", map[string]any{"workspaceVersion": current.WorkspaceVersion})
	}

	groupsByKey := map[string]*workspaceDiffTargetGroup{}
	seen := map[string]struct{}{}
	for _, rawPath := range input.Paths {
		clean, err := cleanWorkspaceRelativePath(rawPath)
		if err != nil {
			return WorkspaceDiffs{}, err
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		target, err := s.resolveWorkspaceFileTarget(ctx, id, clean)
		if err != nil {
			return WorkspaceDiffs{}, err
		}
		key := target.root + "\x00" + target.prefix + "\x00" + target.compare.gitBase()
		group := groupsByKey[key]
		if group == nil {
			group = &workspaceDiffTargetGroup{root: target.root, prefix: target.prefix, base: target.compare.gitBase()}
			groupsByKey[key] = group
		}
		group.targets = append(group.targets, target)
	}

	keys := make([]string, 0, len(groupsByKey))
	for key := range groupsByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := WorkspaceDiffs{SessionID: id, WorkspaceVersion: current.WorkspaceVersion, Groups: make([]WorkspaceDiffGroup, 0, len(keys))}
	for _, key := range keys {
		group, err := workspaceDiffGroup(ctx, groupsByKey[key], scope, input.ContextLines, input.IgnoreWhitespace)
		if err != nil {
			group.Repository = groupsByKey[key].prefix
			group.Errors = []WorkspaceDiffError{{Code: "WORKSPACE_DIFF_FAILED", Message: "Unable to load this repository's diff"}}
		}
		result.Groups = append(result.Groups, group)
	}
	return result, nil
}

func workspaceDiffGroup(ctx context.Context, targetGroup *workspaceDiffTargetGroup, scope WorkspaceDiffScope, contextLines int, ignoreWhitespace bool) (WorkspaceDiffGroup, error) {
	group := WorkspaceDiffGroup{Repository: targetGroup.prefix}
	for _, target := range targetGroup.targets {
		group.IncludedPaths = append(group.IncludedPaths, joinWorkspaceRelative(target.prefix, target.rel))
	}
	sort.Strings(group.IncludedPaths)

	if targetGroup.targets[0].scratch || scope == WorkspaceDiffUntracked {
		var patch strings.Builder
		for _, target := range targetGroup.targets {
			file, info, err := confinedWorkspaceFile(target.root, target.rel)
			if err != nil {
				return WorkspaceDiffGroup{}, err
			}
			if info.Size() > maxWorkspaceRevisionBytes {
				group.Deferred = append(group.Deferred, WorkspaceDiffDeferred{Path: joinWorkspaceRelative(target.prefix, target.rel), Reason: "oversized"})
				continue
			}
			content, binary, _, err := readWorkspaceTextFile(file, maxWorkspaceRevisionBytes)
			if err != nil {
				return WorkspaceDiffGroup{}, err
			}
			if binary {
				group.Deferred = append(group.Deferred, WorkspaceDiffDeferred{Path: joinWorkspaceRelative(target.prefix, target.rel), Reason: "binary"})
				continue
			}
			patch.WriteString(syntheticAddedFileDiff(target.rel, content))
		}
		group.Patch, group.Truncated = truncateUTF8(patch.String(), maxWorkspaceDiffGroupBytes)
		return group, nil
	}

	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--find-renames", fmt.Sprintf("--unified=%d", contextLines)}
	if ignoreWhitespace {
		args = append(args, "--ignore-all-space")
	}
	switch scope {
	case WorkspaceDiffStaged:
		args = append(args, "--cached")
	case WorkspaceDiffUnstaged:
	case WorkspaceDiffCommitted:
		args = append(args, targetGroup.base, "HEAD")
	default:
		args = append(args, targetGroup.base)
	}
	args = append(args, "--")
	paths := map[string]struct{}{}
	for _, target := range targetGroup.targets {
		paths[target.rel] = struct{}{}
		if previous := target.changes.previous[target.rel]; previous != "" {
			paths[previous] = struct{}{}
		}
	}
	orderedPaths := make([]string, 0, len(paths))
	for rel := range paths {
		orderedPaths = append(orderedPaths, rel)
	}
	sort.Strings(orderedPaths)
	args = append(args, orderedPaths...)
	out, truncated, err := gitWorkspaceOutputCapped(ctx, targetGroup.root, maxWorkspaceDiffGroupBytes, args...)
	if err != nil {
		return WorkspaceDiffGroup{}, err
	}
	group.Patch, group.Truncated = truncateUTF8(out, maxWorkspaceDiffGroupBytes)
	group.Truncated = group.Truncated || truncated
	return group, nil
}

// GetWorkspaceFileRevision returns a comparison side for diff expansion and
// complete-file viewing. Missing sides are represented explicitly.
func (s *Service) GetWorkspaceFileRevision(ctx context.Context, id domain.SessionID, rawPath string, scope WorkspaceDiffScope, side WorkspaceFileBlobSide, workspaceVersion, expectedRevision string) (WorkspaceFileRevision, error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceReviewTimeout)
	defer cancel()
	resolvedScope, err := normalizeWorkspaceDiffScope(scope)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	if side != WorkspaceBlobBefore && side != WorkspaceBlobAfter {
		return WorkspaceFileRevision{}, apierr.Invalid("INVALID_WORKSPACE_REVISION_SIDE", "side must be before or after", nil)
	}
	target, err := s.resolveWorkspaceFileTarget(ctx, id, rawPath)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	current, err := s.ListWorkspaceFiles(ctx, id)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	if workspaceVersion != "" && workspaceVersion != current.WorkspaceVersion {
		return WorkspaceFileRevision{}, apierr.Conflict("WORKSPACE_SNAPSHOT_STALE", "Workspace changed while the file revision was loading", map[string]any{"workspaceVersion": current.WorkspaceVersion})
	}
	result := WorkspaceFileRevision{SessionID: id, Path: joinWorkspaceRelative(target.prefix, target.rel), Side: side, WorkspaceVersion: current.WorkspaceVersion, Encoding: "utf-8"}
	data, size, exists, truncated, err := workspaceRevisionBytes(ctx, target, resolvedScope, side)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	result.Size = size
	result.Exists = exists
	result.Truncated = truncated
	if !exists || truncated {
		return result, nil
	}
	result.Revision = hashWorkspaceReviewValue(string(data))
	if expectedRevision != "" && expectedRevision != result.Revision {
		return WorkspaceFileRevision{}, apierr.Conflict("WORKSPACE_REVISION_STALE", "File changed while it was being loaded", map[string]any{"revision": result.Revision})
	}
	result.MediaType = "text/plain"
	result.Binary = isBinary(data) || !utf8.Valid(data)
	if !result.Binary {
		result.Content = string(data)
	}
	return result, nil
}

func workspaceRevisionBytes(ctx context.Context, target workspaceFileTarget, scope WorkspaceDiffScope, side WorkspaceFileBlobSide) ([]byte, int64, bool, bool, error) {
	if target.scratch {
		if side == WorkspaceBlobBefore {
			return nil, 0, false, false, nil
		}
		return readWorktreeRevision(target.root, target.rel)
	}
	status, previous, err := scopedWorkspaceFileStatus(ctx, target, scope)
	if err != nil {
		return nil, 0, false, false, err
	}
	beforePath := target.rel
	if previous != "" {
		beforePath = previous
	}
	if side == WorkspaceBlobBefore && status == WorkspaceFileAdded {
		return nil, 0, false, false, nil
	}
	if side == WorkspaceBlobAfter && status == WorkspaceFileDeleted {
		return nil, 0, false, false, nil
	}

	var spec string
	switch scope {
	case WorkspaceDiffStaged:
		if side == WorkspaceBlobBefore {
			spec = "HEAD:" + beforePath
		} else {
			spec = ":" + target.rel
		}
	case WorkspaceDiffUnstaged:
		if side == WorkspaceBlobBefore {
			spec = ":" + beforePath
		} else {
			return readWorktreeRevision(target.root, target.rel)
		}
	case WorkspaceDiffCommitted:
		if side == WorkspaceBlobBefore {
			spec = target.compare.gitBase() + ":" + beforePath
		} else {
			spec = "HEAD:" + target.rel
		}
	case WorkspaceDiffUntracked:
		if side == WorkspaceBlobBefore {
			return nil, 0, false, false, nil
		}
		return readWorktreeRevision(target.root, target.rel)
	default:
		if side == WorkspaceBlobBefore {
			spec = target.compare.gitBase() + ":" + beforePath
		} else {
			return readWorktreeRevision(target.root, target.rel)
		}
	}
	return readGitRevision(ctx, target.root, spec)
}

func scopedWorkspaceFileStatus(ctx context.Context, target workspaceFileTarget, scope WorkspaceDiffScope) (WorkspaceFileStatus, string, error) {
	if scope == WorkspaceDiffCombined {
		status := target.changes.statuses[target.rel]
		if status == "" {
			status = WorkspaceFileUnmodified
		}
		return status, target.changes.previous[target.rel], nil
	}
	if scope == WorkspaceDiffUntracked {
		if _, ok := target.changes.untracked[target.rel]; ok {
			return WorkspaceFileAdded, "", nil
		}
		return WorkspaceFileUnmodified, "", nil
	}
	var args []string
	switch scope {
	case WorkspaceDiffStaged:
		args = []string{"--cached"}
	case WorkspaceDiffUnstaged:
		args = nil
	case WorkspaceDiffCommitted:
		args = []string{target.compare.gitBase(), "HEAD"}
	}
	statuses, previous, err := workspaceDiffNameStatus(ctx, target.root, args...)
	if err != nil {
		return "", "", err
	}
	status := statuses[target.rel]
	if status == "" {
		status = WorkspaceFileUnmodified
	}
	return status, previous[target.rel], nil
}

func readWorktreeRevision(root, rel string) ([]byte, int64, bool, bool, error) {
	file, info, err := confinedWorkspaceFile(root, rel)
	if err != nil {
		var apiError *apierr.Error
		if errors.As(err, &apiError) && apiError.Code == "WORKSPACE_FILE_NOT_FOUND" {
			return nil, 0, false, false, nil
		}
		return nil, 0, false, false, err
	}
	if info.Size() > maxWorkspaceRevisionBytes {
		return nil, info.Size(), true, true, nil
	}
	handle, err := os.Open(file)
	if err != nil {
		return nil, 0, false, false, apierr.NotFound("WORKSPACE_FILE_NOT_FOUND", "Workspace file not found")
	}
	defer func() { _ = handle.Close() }()
	data, err := io.ReadAll(io.LimitReader(handle, maxWorkspaceRevisionBytes+1))
	if err != nil {
		return nil, 0, false, false, err
	}
	return data, int64(len(data)), true, len(data) > maxWorkspaceRevisionBytes, nil
}

func readGitRevision(ctx context.Context, root, spec string) ([]byte, int64, bool, bool, error) {
	sizeOut, err := gitWorkspaceOutput(ctx, root, "cat-file", "-s", spec)
	if err != nil {
		//nolint:nilerr // A path missing from this comparison side is an absent revision.
		return nil, 0, false, false, nil
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOut), 10, 64)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("parse workspace revision size: %w", err)
	}
	if size > maxWorkspaceRevisionBytes {
		return nil, size, true, true, nil
	}
	out, err := gitWorkspaceOutput(ctx, root, "cat-file", "blob", spec)
	if err != nil {
		//nolint:nilerr // A disappearing object is surfaced as an absent stale side.
		return nil, 0, false, false, nil
	}
	return []byte(out), size, true, false, nil
}

// SearchWorkspaceFiles performs a bounded case-insensitive path search over
// AO's existing confined all-files read model.
func (s *Service) SearchWorkspaceFiles(ctx context.Context, id domain.SessionID, query, cursor string, limit int) (WorkspaceFileSearch, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return WorkspaceFileSearch{}, apierr.Invalid("WORKSPACE_SEARCH_QUERY_REQUIRED", "query is required", nil)
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > maxWorkspaceSearchResults {
		limit = maxWorkspaceSearchResults
	}
	offset := 0
	if cursor != "" {
		parsed, err := strconv.Atoi(cursor)
		if err != nil || parsed < 0 {
			return WorkspaceFileSearch{}, apierr.Invalid("INVALID_WORKSPACE_SEARCH_CURSOR", "cursor is invalid", nil)
		}
		offset = parsed
	}
	files, err := s.ListWorkspaceFiles(ctx, id)
	if err != nil {
		return WorkspaceFileSearch{}, err
	}
	needle := strings.ToLower(query)
	matches := make([]WorkspaceFileSearchResult, 0)
	for _, file := range files.Files {
		if !strings.Contains(strings.ToLower(file.Path), needle) {
			continue
		}
		matches = append(matches, WorkspaceFileSearchResult{Path: file.Path, Status: file.Status, Size: file.Size, Binary: file.Binary, FileFingerprint: file.FileFingerprint})
	}
	if offset > len(matches) {
		offset = len(matches)
	}
	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}
	result := WorkspaceFileSearch{SessionID: id, Query: query, Results: matches[offset:end], Truncated: files.Truncated}
	if end < len(matches) {
		result.NextCursor = strconv.Itoa(end)
	}
	return result, nil
}
