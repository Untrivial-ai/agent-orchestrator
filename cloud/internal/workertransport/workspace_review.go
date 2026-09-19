package workertransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

const maxWorkspaceReviewFiles = 10_000

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
