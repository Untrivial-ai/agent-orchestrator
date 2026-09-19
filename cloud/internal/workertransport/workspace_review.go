package workertransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

const maxWorkspaceReviewFiles = 10_000

var (
	ErrWorkspaceSnapshotStale  = errors.New("workspace snapshot is stale")
	ErrWorkspaceCommitNotFound = errors.New("workspace review commit was not found")
)

// ReviewDiffs returns a bounded unified patch for one workspace snapshot.
func (w *workspace) ReviewDiffs(ctx context.Context, input worker.WorkspaceReviewDiffsRequest) (worker.WorkspaceReviewDiffsResponse, error) {
	review, err := w.ReviewSummary(ctx)
	if err != nil {
		return worker.WorkspaceReviewDiffsResponse{}, err
	}
	if input.WorkspaceVersion != "" && input.WorkspaceVersion != review.WorkspaceVersion {
		return worker.WorkspaceReviewDiffsResponse{}, ErrWorkspaceSnapshotStale
	}
	scope, err := normalizeReviewScope(input.Scope)
	if err != nil {
		return worker.WorkspaceReviewDiffsResponse{}, err
	}
	if len(input.Paths) == 0 || len(input.Paths) > 100 {
		return worker.WorkspaceReviewDiffsResponse{}, errors.New("workspace review requires between 1 and 100 paths")
	}
	if input.ContextLines < 0 || input.ContextLines > 20 {
		return worker.WorkspaceReviewDiffsResponse{}, errors.New("workspace review context lines must be between 0 and 20")
	}
	if input.CommitSHA != "" && !reviewContainsCommit(review, input.CommitSHA) {
		return worker.WorkspaceReviewDiffsResponse{}, ErrWorkspaceCommitNotFound
	}

	paths := make([]string, 0, len(input.Paths))
	deferred := make([]worker.WorkspaceReviewDiffDeferred, 0)
	for _, requested := range input.Paths {
		path, pathErr := cleanWorkspacePath(requested, false)
		if pathErr != nil {
			return worker.WorkspaceReviewDiffsResponse{}, pathErr
		}
		wire := wirePath(path)
		if summary, ok := reviewFileForScope(review, scope, input.CommitSHA, wire); ok {
			switch {
			case summary.Binary:
				deferred = append(deferred, worker.WorkspaceReviewDiffDeferred{Path: wire, Reason: "binary"})
				continue
			case summary.Size > maxDiffOutput:
				deferred = append(deferred, worker.WorkspaceReviewDiffDeferred{Path: wire, Reason: "oversized"})
				continue
			}
		}
		paths = append(paths, wire)
	}

	var patch string
	var truncated bool
	if scope == worker.WorkspaceReviewUntracked {
		var builder strings.Builder
		for _, path := range paths {
			content, readErr := w.readReviewFile(path, maxDiffOutput)
			if readErr != nil {
				return worker.WorkspaceReviewDiffsResponse{}, readErr
			}
			builder.WriteString(syntheticAddedFileDiff(path, string(content)))
		}
		patch, truncated = truncateDiff(builder.String(), maxDiffOutput)
	} else if len(paths) > 0 {
		args := reviewDiffArgs(review.CompareBaseSHA, scope, input.CommitSHA, input.ContextLines, input.IgnoreWhitespace)
		args = append(args, "--")
		args = append(args, paths...)
		patch, truncated, err = w.git(ctx, args...)
		if err != nil {
			return worker.WorkspaceReviewDiffsResponse{}, err
		}
	}
	group := worker.WorkspaceReviewDiffGroup{
		Patch: patch, Truncated: truncated, IncludedPaths: paths,
		Deferred: deferred, Errors: []worker.WorkspaceReviewDiffError{},
	}
	return worker.WorkspaceReviewDiffsResponse{
		WorkspaceVersion: review.WorkspaceVersion,
		Groups:           []worker.WorkspaceReviewDiffGroup{group},
	}, nil
}

// ReviewFile returns one scoped file detail. Commit-specific requests read the
// immutable commit snapshot rather than the current worktree.
func (w *workspace) ReviewFile(ctx context.Context, input worker.WorkspaceReviewFileRequest) (worker.WorkspaceReviewFileResponse, error) {
	path, err := cleanWorkspacePath(input.Path, false)
	if err != nil {
		return worker.WorkspaceReviewFileResponse{}, err
	}
	wire := wirePath(path)
	review, err := w.ReviewSummary(ctx)
	if err != nil {
		return worker.WorkspaceReviewFileResponse{}, err
	}
	scope, err := normalizeReviewScope(input.Scope)
	if err != nil {
		return worker.WorkspaceReviewFileResponse{}, err
	}
	if input.CommitSHA != "" && !reviewContainsCommit(review, input.CommitSHA) {
		return worker.WorkspaceReviewFileResponse{}, ErrWorkspaceCommitNotFound
	}
	summary, ok := reviewFileForScope(review, scope, input.CommitSHA, wire)
	if !ok {
		for _, candidate := range review.Files {
			if candidate.Path == wire {
				summary, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return worker.WorkspaceReviewFileResponse{}, os.ErrNotExist
	}
	diffs, err := w.ReviewDiffs(ctx, worker.WorkspaceReviewDiffsRequest{
		Scope: scope, Paths: []string{wire}, ContextLines: 3,
		WorkspaceVersion: review.WorkspaceVersion, CommitSHA: input.CommitSHA,
	})
	if err != nil {
		return worker.WorkspaceReviewFileResponse{}, err
	}
	response := worker.WorkspaceReviewFileResponse{
		WorkspaceReviewFileSummary: summary,
		Deleted:                    summary.Status == worker.WorkspaceReviewDeleted,
		CompareBaseSHA:             review.CompareBaseSHA,
		CompareBaseRef:             review.CompareBaseRef,
		CompareMode:                review.CompareMode,
		WorkspaceVersion:           review.WorkspaceVersion,
		Historical:                 input.CommitSHA != "",
	}
	if len(diffs.Groups) > 0 {
		response.Diff = diffs.Groups[0].Patch
		response.DiffTruncated = diffs.Groups[0].Truncated
	}
	if !response.Deleted {
		var data []byte
		if input.CommitSHA != "" {
			data, _, err = w.gitBytes(ctx, input.CommitSHA+":"+wire, maxWorkspaceFile)
		} else {
			data, err = w.readReviewFile(wire, maxWorkspaceFile)
		}
		if err != nil {
			return worker.WorkspaceReviewFileResponse{}, err
		}
		response.ContentTruncated = len(data) > maxWorkspaceFile
		if !response.ContentTruncated && !summary.Binary && utf8.Valid(data) {
			response.Content = string(data)
		}
	}
	return response, nil
}

// ReviewRevision returns one side of a scoped comparison without mutating the
// checkout or index.
func (w *workspace) ReviewRevision(ctx context.Context, input worker.WorkspaceReviewRevisionRequest) (worker.WorkspaceReviewRevisionResponse, error) {
	path, err := cleanWorkspacePath(input.Path, false)
	if err != nil {
		return worker.WorkspaceReviewRevisionResponse{}, err
	}
	wire := wirePath(path)
	review, err := w.ReviewSummary(ctx)
	if err != nil {
		return worker.WorkspaceReviewRevisionResponse{}, err
	}
	if input.WorkspaceVersion != "" && input.WorkspaceVersion != review.WorkspaceVersion {
		return worker.WorkspaceReviewRevisionResponse{}, ErrWorkspaceSnapshotStale
	}
	scope, err := normalizeReviewScope(input.Scope)
	if err != nil {
		return worker.WorkspaceReviewRevisionResponse{}, err
	}
	side := input.Side
	if side == "" {
		side = worker.WorkspaceReviewAfter
	}
	if side != worker.WorkspaceReviewBefore && side != worker.WorkspaceReviewAfter {
		return worker.WorkspaceReviewRevisionResponse{}, errors.New("workspace review side must be before or after")
	}
	if input.CommitSHA != "" && !reviewContainsCommit(review, input.CommitSHA) {
		return worker.WorkspaceReviewRevisionResponse{}, ErrWorkspaceCommitNotFound
	}
	summary, _ := reviewFileForScope(review, scope, input.CommitSHA, wire)
	readPath := wire
	if side == worker.WorkspaceReviewBefore && summary.PreviousPath != "" {
		readPath = summary.PreviousPath
	}
	data, exists, revision, err := w.readReviewRevision(ctx, review, scope, side, input.CommitSHA, readPath)
	if err != nil {
		return worker.WorkspaceReviewRevisionResponse{}, err
	}
	response := worker.WorkspaceReviewRevisionResponse{
		Path: wire, Side: side, Revision: revision,
		WorkspaceVersion: review.WorkspaceVersion, Size: int64(len(data)), Exists: exists,
	}
	if !exists {
		return response, nil
	}
	response.Truncated = len(data) > maxDiffOutput
	if response.Truncated {
		data = data[:maxDiffOutput]
	}
	response.Binary = !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0
	if !response.Binary {
		response.Encoding = "utf-8"
		response.Content = string(data)
	}
	return response, nil
}

// ReviewTree lists one repository directory level from Git's tracked and
// untracked-but-not-ignored inventory.
func (w *workspace) ReviewTree(ctx context.Context, input worker.WorkspaceReviewTreeRequest) (worker.WorkspaceReviewTreeResponse, error) {
	directory, err := cleanWorkspacePath(input.Path, true)
	if err != nil {
		return worker.WorkspaceReviewTreeResponse{}, err
	}
	review, err := w.ReviewSummary(ctx)
	if err != nil {
		return worker.WorkspaceReviewTreeResponse{}, err
	}
	prefix := ""
	if directory != "." {
		prefix = wirePath(directory) + "/"
	}
	type collected struct {
		entry worker.WorkspaceReviewTreeEntry
	}
	entries := make(map[string]collected)
	for _, file := range review.Files {
		if !strings.HasPrefix(file.Path, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(file.Path, prefix)
		if remainder == "" {
			continue
		}
		name, tail, isDirectory := strings.Cut(remainder, "/")
		path := name
		if prefix != "" {
			path = strings.TrimSuffix(prefix, "/") + "/" + name
		}
		if isDirectory {
			current := entries[path].entry
			current.Name, current.Path, current.Type = name, path, "dir"
			current.HasChanges = current.HasChanges || file.Status != worker.WorkspaceReviewUnmodified
			entries[path] = collected{entry: current}
			_ = tail
			continue
		}
		entries[path] = collected{entry: worker.WorkspaceReviewTreeEntry{
			Name: name, Path: path, Type: "file", Status: file.Status,
			Size: file.Size, Binary: file.Binary,
		}}
	}
	result := make([]worker.WorkspaceReviewTreeEntry, 0, len(entries))
	for _, item := range entries {
		result = append(result, item.entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type == "dir"
		}
		return result[i].Name < result[j].Name
	})
	return worker.WorkspaceReviewTreeResponse{Path: wirePath(directory), Entries: result, Truncated: review.Truncated}, nil
}

// ReviewSearch performs a bounded case-insensitive path and text search over
// the Git-visible file inventory. Binary and oversized files are not scanned.
func (w *workspace) ReviewSearch(ctx context.Context, input worker.WorkspaceReviewSearchRequest) (worker.WorkspaceReviewSearchResponse, error) {
	query := strings.ToLower(strings.TrimSpace(input.Query))
	if query == "" {
		return worker.WorkspaceReviewSearchResponse{}, errors.New("workspace search query is required")
	}
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return worker.WorkspaceReviewSearchResponse{}, errors.New("workspace search limit must be between 1 and 100")
	}
	offset := 0
	if input.Cursor != "" {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(input.Cursor)
		if decodeErr != nil {
			return worker.WorkspaceReviewSearchResponse{}, errors.New("invalid workspace search cursor")
		}
		offset, decodeErr = strconv.Atoi(string(decoded))
		if decodeErr != nil || offset < 0 {
			return worker.WorkspaceReviewSearchResponse{}, errors.New("invalid workspace search cursor")
		}
	}
	review, err := w.ReviewSummary(ctx)
	if err != nil {
		return worker.WorkspaceReviewSearchResponse{}, err
	}
	matches := make([]worker.WorkspaceReviewSearchResult, 0)
	for _, file := range review.Files {
		matched := strings.Contains(strings.ToLower(file.Path), query)
		if !matched && !file.Binary && file.Size <= maxWorkspaceFile {
			content, readErr := w.readReviewFile(file.Path, maxWorkspaceFile)
			matched = readErr == nil && utf8.Valid(content) && strings.Contains(strings.ToLower(string(content)), query)
		}
		if !matched || file.Binary {
			continue
		}
		matches = append(matches, worker.WorkspaceReviewSearchResult{
			Path: file.Path, Status: file.Status, Size: file.Size,
			Binary: file.Binary, FileFingerprint: file.FileFingerprint,
		})
	}
	if offset > len(matches) {
		return worker.WorkspaceReviewSearchResponse{}, errors.New("invalid workspace search cursor")
	}
	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}
	response := worker.WorkspaceReviewSearchResponse{Query: input.Query, Results: matches[offset:end]}
	if end < len(matches) {
		response.Truncated = true
		response.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return response, nil
}

// ReviewSummary returns the Cloud-owned equivalent of the local workspace
// review model. Git state is calculated inside the sandbox for every provider.
func (w *workspace) ReviewSummary(ctx context.Context) (worker.WorkspaceReviewResponse, error) {
	baseRef, baseSHA := w.reviewBase(ctx)
	if baseSHA == "" {
		return worker.WorkspaceReviewResponse{}, errors.New("workspace review base is unavailable")
	}

	staged, stagedTruncated, err := w.reviewChanges(ctx, []string{"diff", "--cached"})
	if err != nil {
		return worker.WorkspaceReviewResponse{}, err
	}
	unstaged, unstagedTruncated, err := w.reviewChanges(ctx, []string{"diff"})
	if err != nil {
		return worker.WorkspaceReviewResponse{}, err
	}
	committed, committedTruncated, err := w.reviewChanges(ctx, []string{"diff", baseSHA, "HEAD"})
	if err != nil {
		return worker.WorkspaceReviewResponse{}, err
	}
	untracked, untrackedTruncated, err := w.reviewUntracked(ctx)
	if err != nil {
		return worker.WorkspaceReviewResponse{}, err
	}
	combined, combinedTruncated, err := w.reviewChanges(ctx, []string{"diff", baseSHA})
	if err != nil {
		return worker.WorkspaceReviewResponse{}, err
	}

	combinedByPath := make(map[string]worker.WorkspaceReviewFileSummary, len(combined)+len(untracked))
	for _, file := range combined {
		combinedByPath[file.Path] = file
	}
	for _, file := range untracked {
		combinedByPath[file.Path] = file
	}
	files, filesTruncated, err := w.reviewAllFiles(ctx, combinedByPath)
	if err != nil {
		return worker.WorkspaceReviewResponse{}, err
	}
	commits, commitsTruncated, err := w.reviewCommits(ctx, baseSHA)
	if err != nil {
		return worker.WorkspaceReviewResponse{}, err
	}

	summary := worker.WorkspaceReviewSummary{}
	for _, file := range combinedByPath {
		summary.Files++
		summary.Additions += file.Additions
		summary.Deletions += file.Deletions
	}
	version := reviewVersion(baseSHA, files, staged, unstaged, untracked, committed)
	ahead, behind := w.reviewAheadBehind(ctx)
	return worker.WorkspaceReviewResponse{
		WorkspaceVersion: version,
		CompareBaseSHA:   baseSHA,
		CompareBaseRef:   baseRef,
		CompareMode:      "base",
		Files:            files,
		Truncated: filesTruncated || stagedTruncated || unstagedTruncated ||
			untrackedTruncated || committedTruncated || combinedTruncated || commitsTruncated,
		Sections: worker.WorkspaceReviewSections{
			Staged: staged, Unstaged: unstaged, Untracked: untracked, Committed: committed,
		},
		Commits: commits,
		Summary: summary,
		Ahead:   ahead,
		Behind:  behind,
	}, nil
}

func (w *workspace) reviewBase(ctx context.Context) (string, string) {
	if output, _, err := w.git(ctx, "rev-parse", "--verify", worker.WorkspaceReviewBaseRef); err == nil {
		return worker.WorkspaceReviewBaseRef, strings.TrimSpace(output)
	}
	ref, sha := w.comparisonBase(ctx)
	return ref, sha
}

func (w *workspace) reviewChanges(ctx context.Context, prefix []string) ([]worker.WorkspaceReviewFileSummary, bool, error) {
	nameArgs := append(append([]string{}, prefix...), "--name-status", "--find-renames", "--find-copies", "--")
	nameOutput, nameTruncated, err := w.git(ctx, nameArgs...)
	if err != nil {
		return nil, false, err
	}
	statArgs := append(append([]string{}, prefix...), "--numstat", "--find-renames", "--find-copies", "--")
	statOutput, statTruncated, err := w.git(ctx, statArgs...)
	if err != nil {
		return nil, false, err
	}
	stats := reviewNumstats(statOutput)
	changes := parseReviewNameStatus(nameOutput)
	files := make([]worker.WorkspaceReviewFileSummary, 0, len(changes))
	for _, change := range changes {
		file := worker.WorkspaceReviewFileSummary{
			Path: change.path, PreviousPath: change.previousPath, Status: change.status,
		}
		if stat, ok := stats[change.path]; ok {
			file.Additions, file.Deletions, file.Binary = stat.additions, stat.deletions, stat.binary
		}
		w.populateReviewMetadata(&file)
		files = append(files, file)
		if len(files) == maxWorkspaceReviewFiles {
			return files, true, nil
		}
	}
	return files, nameTruncated || statTruncated, nil
}

func (w *workspace) reviewUntracked(ctx context.Context) ([]worker.WorkspaceReviewFileSummary, bool, error) {
	output, truncated, err := w.git(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, false, err
	}
	paths := nonEmptyLines(output)
	files := make([]worker.WorkspaceReviewFileSummary, 0, len(paths))
	for _, path := range paths {
		file := worker.WorkspaceReviewFileSummary{Path: path, Status: worker.WorkspaceReviewUntrackedFile}
		w.populateReviewMetadata(&file)
		if !file.Binary {
			if content, readErr := w.readReviewFile(path, maxWorkspaceFile); readErr == nil {
				file.Additions = lineCount(string(content))
			}
		}
		files = append(files, file)
		if len(files) == maxWorkspaceReviewFiles {
			return files, true, nil
		}
	}
	return files, truncated, nil
}

func (w *workspace) reviewAllFiles(ctx context.Context, changed map[string]worker.WorkspaceReviewFileSummary) ([]worker.WorkspaceReviewFileSummary, bool, error) {
	output, truncated, err := w.git(ctx, "ls-files", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, false, err
	}
	paths := nonEmptyLines(output)
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	files := make([]worker.WorkspaceReviewFileSummary, 0, len(paths))
	last := ""
	for _, path := range paths {
		if path == last {
			continue
		}
		last = path
		file, ok := changed[path]
		if !ok {
			file = worker.WorkspaceReviewFileSummary{Path: path, Status: worker.WorkspaceReviewUnmodified}
			w.populateReviewMetadata(&file)
		}
		files = append(files, file)
		if len(files) == maxWorkspaceReviewFiles {
			return files, true, nil
		}
	}
	return files, truncated, nil
}

func (w *workspace) populateReviewMetadata(file *worker.WorkspaceReviewFileSummary) {
	if file.Status == worker.WorkspaceReviewDeleted {
		file.Editable = false
		file.FileFingerprint = reviewFingerprint(*file, nil)
		return
	}
	path, err := cleanWorkspacePath(file.Path, false)
	if err != nil {
		file.FileFingerprint = reviewFingerprint(*file, nil)
		return
	}
	handle, err := w.root.Open(path)
	if err != nil {
		file.FileFingerprint = reviewFingerprint(*file, nil)
		return
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.FileFingerprint = reviewFingerprint(*file, nil)
		return
	}
	file.Size = info.Size()
	data, _ := io.ReadAll(io.LimitReader(handle, maxWorkspaceFile+1))
	if len(data) > maxWorkspaceFile {
		file.Editable = false
	} else {
		file.Binary = file.Binary || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0
		file.Editable = !file.Binary
	}
	file.FileFingerprint = reviewFingerprint(*file, data)
}

func (w *workspace) readReviewFile(path string, limit int) ([]byte, error) {
	clean, err := cleanWorkspacePath(path, false)
	if err != nil {
		return nil, err
	}
	handle, err := w.root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	return io.ReadAll(io.LimitReader(handle, int64(limit)+1))
}

type reviewChange struct {
	path         string
	previousPath string
	status       worker.WorkspaceReviewFileStatus
}

func parseReviewNameStatus(output string) []reviewChange {
	changes := make([]reviewChange, 0)
	for _, line := range nonEmptyLines(output) {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		code := strings.TrimRight(parts[0], "0123456789")
		change := reviewChange{path: parts[len(parts)-1], status: reviewStatus(code)}
		if (code == "R" || code == "C") && len(parts) >= 3 {
			change.previousPath = parts[1]
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].path < changes[j].path })
	return changes
}

func reviewStatus(code string) worker.WorkspaceReviewFileStatus {
	switch {
	case strings.Contains(code, "R"):
		return worker.WorkspaceReviewRenamed
	case strings.Contains(code, "C"):
		return worker.WorkspaceReviewCopied
	case strings.Contains(code, "D"):
		return worker.WorkspaceReviewDeleted
	case strings.Contains(code, "A"):
		return worker.WorkspaceReviewAdded
	default:
		return worker.WorkspaceReviewModified
	}
}

func reviewNumstats(output string) map[string]diffStat {
	stats := make(map[string]diffStat)
	for _, line := range nonEmptyLines(output) {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if arrow := strings.LastIndex(path, " => "); arrow >= 0 {
			path = strings.TrimSuffix(strings.TrimPrefix(path[arrow+4:], "{"), "}")
		}
		additions, deletions, binary := diffNumstat(parts[0]+"\t"+parts[1], false)
		stats[path] = diffStat{additions: additions, deletions: deletions, binary: binary}
	}
	return stats
}

func (w *workspace) reviewCommits(ctx context.Context, baseSHA string) ([]worker.WorkspaceReviewCommit, bool, error) {
	output, truncated, err := w.git(ctx, "log", "--format=%H%x1f%s%x1f%an%x1f%cI", baseSHA+"..HEAD", "--max-count=100")
	if err != nil {
		return nil, false, err
	}
	commits := make([]worker.WorkspaceReviewCommit, 0)
	for _, line := range nonEmptyLines(output) {
		parts := strings.Split(line, "\x1f")
		if len(parts) != 4 {
			continue
		}
		timestamp, parseErr := timeParse(parts[3])
		if parseErr != nil {
			continue
		}
		files, filesTruncated, filesErr := w.reviewChanges(ctx, []string{"show", "--format=", parts[0]})
		if filesErr != nil {
			return nil, false, filesErr
		}
		truncated = truncated || filesTruncated
		commits = append(commits, worker.WorkspaceReviewCommit{
			SHA: parts[0], Subject: parts[1], Author: parts[2], Timestamp: timestamp, Files: files,
		})
	}
	return commits, truncated, nil
}

func (w *workspace) reviewAheadBehind(ctx context.Context) (*int, *int) {
	upstream, _, err := w.git(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return nil, nil
	}
	counts, _, err := w.git(ctx, "rev-list", "--left-right", "--count", strings.TrimSpace(upstream)+"...HEAD")
	if err != nil {
		return nil, nil
	}
	var behind, ahead int
	if _, err := fmt.Sscanf(strings.TrimSpace(counts), "%d\t%d", &behind, &ahead); err != nil {
		return nil, nil
	}
	return &ahead, &behind
}

func reviewVersion(base string, groups ...[]worker.WorkspaceReviewFileSummary) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, base)
	for index, files := range groups {
		_, _ = fmt.Fprintf(hash, "\x00group:%d", index)
		for _, file := range files {
			_, _ = io.WriteString(hash, "\x00"+file.Path+"\x00"+file.PreviousPath+"\x00"+string(file.Status)+"\x00"+file.FileFingerprint)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func reviewFingerprint(file worker.WorkspaceReviewFileSummary, data []byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, file.Path+"\x00"+file.PreviousPath+"\x00"+string(file.Status)+"\x00")
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func normalizeReviewScope(scope worker.WorkspaceReviewScope) (worker.WorkspaceReviewScope, error) {
	if scope == "" {
		return worker.WorkspaceReviewCombined, nil
	}
	switch scope {
	case worker.WorkspaceReviewCombined, worker.WorkspaceReviewCommitted, worker.WorkspaceReviewStaged,
		worker.WorkspaceReviewUnstaged, worker.WorkspaceReviewUntracked:
		return scope, nil
	default:
		return "", errors.New("workspace review scope is invalid")
	}
}

func reviewContainsCommit(review worker.WorkspaceReviewResponse, sha string) bool {
	for _, commit := range review.Commits {
		if commit.SHA == sha {
			return true
		}
	}
	return false
}

func reviewFileForScope(review worker.WorkspaceReviewResponse, scope worker.WorkspaceReviewScope, commitSHA, path string) (worker.WorkspaceReviewFileSummary, bool) {
	var files []worker.WorkspaceReviewFileSummary
	if commitSHA != "" {
		for _, commit := range review.Commits {
			if commit.SHA == commitSHA {
				files = commit.Files
				break
			}
		}
	} else {
		switch scope {
		case worker.WorkspaceReviewStaged:
			files = review.Sections.Staged
		case worker.WorkspaceReviewUnstaged:
			files = review.Sections.Unstaged
		case worker.WorkspaceReviewUntracked:
			files = review.Sections.Untracked
		case worker.WorkspaceReviewCommitted:
			files = review.Sections.Committed
		default:
			files = review.Files
		}
	}
	for _, file := range files {
		if file.Path == path {
			return file, true
		}
	}
	return worker.WorkspaceReviewFileSummary{}, false
}

func reviewDiffArgs(baseSHA string, scope worker.WorkspaceReviewScope, commitSHA string, contextLines int, ignoreWhitespace bool) []string {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--find-renames", fmt.Sprintf("--unified=%d", contextLines)}
	if ignoreWhitespace {
		args = append(args, "--ignore-all-space")
	}
	switch scope {
	case worker.WorkspaceReviewCommitted:
		if commitSHA != "" {
			args = append(args, commitSHA+"^", commitSHA)
		} else {
			args = append(args, baseSHA, "HEAD")
		}
	case worker.WorkspaceReviewStaged:
		args = append(args, "--cached", "HEAD")
	case worker.WorkspaceReviewUnstaged:
		// The default Git diff compares the index with the worktree.
	default:
		args = append(args, baseSHA)
	}
	return args
}

func (w *workspace) gitBytes(ctx context.Context, spec string, limit int) ([]byte, bool, error) {
	output, truncated, err := w.git(ctx, "show", "--format=", "--no-textconv", spec)
	if err != nil {
		return nil, false, err
	}
	data := []byte(output)
	if len(data) > limit {
		data = data[:limit+1]
		truncated = true
	}
	return data, truncated, nil
}

func (w *workspace) readReviewRevision(ctx context.Context, review worker.WorkspaceReviewResponse, scope worker.WorkspaceReviewScope, side worker.WorkspaceReviewSide, commitSHA, path string) ([]byte, bool, string, error) {
	readWorktree := func() ([]byte, bool, error) {
		data, err := w.readReviewFile(path, maxDiffOutput)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return data, err == nil, err
	}
	readGit := func(spec string) ([]byte, bool, error) {
		data, _, err := w.gitBytes(ctx, spec, maxDiffOutput)
		if err != nil {
			return nil, false, nil
		}
		return data, true, nil
	}

	var data []byte
	var exists bool
	var err error
	switch scope {
	case worker.WorkspaceReviewStaged:
		if side == worker.WorkspaceReviewBefore {
			data, exists, err = readGit("HEAD:" + path)
		} else {
			data, exists, err = readGit(":" + path)
		}
	case worker.WorkspaceReviewUnstaged:
		if side == worker.WorkspaceReviewBefore {
			data, exists, err = readGit(":" + path)
		} else {
			data, exists, err = readWorktree()
		}
	case worker.WorkspaceReviewUntracked:
		if side == worker.WorkspaceReviewAfter {
			data, exists, err = readWorktree()
		}
	case worker.WorkspaceReviewCommitted:
		commit := commitSHA
		if commit == "" {
			commit = "HEAD"
		}
		if side == worker.WorkspaceReviewBefore {
			if commitSHA == "" {
				data, exists, err = readGit(review.CompareBaseSHA + ":" + path)
			} else {
				data, exists, err = readGit(commit + "^:" + path)
			}
		} else {
			data, exists, err = readGit(commit + ":" + path)
		}
	default:
		if side == worker.WorkspaceReviewBefore {
			data, exists, err = readGit(review.CompareBaseSHA + ":" + path)
		} else {
			data, exists, err = readWorktree()
		}
	}
	if err != nil {
		return nil, false, "", err
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, string(side)+"\x00"+path+"\x00"+strconv.FormatBool(exists)+"\x00")
	_, _ = hash.Write(data)
	return data, exists, hex.EncodeToString(hash.Sum(nil)), nil
}

func nonEmptyLines(value string) []string {
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	result := lines[:0]
	for _, line := range lines {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func timeParse(value string) (time.Time, error) {
	return time.Parse(time.RFC3339, value)
}
