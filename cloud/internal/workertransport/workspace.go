package workertransport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

const (
	maxWorkspaceFile = 1 << 20
	maxDiffOutput    = 2 << 20
)

var errUnsafePath = errors.New("path is outside the workspace")

type workspace struct {
	path string
	root *os.Root
}

func openWorkspace(path string) (*workspace, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &workspace{path: path, root: root}, nil
}

func (w *workspace) Close() error {
	return w.root.Close()
}

func (w *workspace) List(input worker.WorkspaceListRequest) (worker.WorkspaceEntryPage, error) {
	path, err := cleanWorkspacePath(input.Path, true)
	if err != nil {
		return worker.WorkspaceEntryPage{}, err
	}
	if input.Limit < 1 || input.Limit > 100 {
		return worker.WorkspaceEntryPage{}, errors.New("limit must be between 1 and 100")
	}
	directory, err := w.root.Open(path)
	if err != nil {
		return worker.WorkspaceEntryPage{}, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return worker.WorkspaceEntryPage{}, err
	}
	if !info.IsDir() {
		return worker.WorkspaceEntryPage{}, errors.New("workspace path is not a directory")
	}
	items, err := directory.ReadDir(-1)
	if err != nil {
		return worker.WorkspaceEntryPage{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })

	after := ""
	if input.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		if err != nil {
			return worker.WorkspaceEntryPage{}, errors.New("invalid workspace cursor")
		}
		after = string(decoded)
	}
	page := worker.WorkspaceEntryPage{Path: wirePath(path), Items: []worker.WorkspaceEntry{}}
	for _, entry := range items {
		if entry.Name() <= after {
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return worker.WorkspaceEntryPage{}, err
		}
		entryPath := entry.Name()
		if path != "." {
			entryPath = filepath.Join(path, entry.Name())
		}
		page.Items = append(page.Items, worker.WorkspaceEntry{
			Name: entry.Name(), Path: wirePath(entryPath), IsDir: entryInfo.IsDir(),
			Size: entryInfo.Size(), Mode: entryInfo.Mode().String(), ModTime: entryInfo.ModTime().UTC(),
		})
		if len(page.Items) == input.Limit {
			break
		}
	}
	if len(page.Items) == input.Limit {
		last := page.Items[len(page.Items)-1].Name
		for _, entry := range items {
			if entry.Name() > last {
				page.HasMore = true
				page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(last))
				break
			}
		}
	}
	return page, nil
}

func (w *workspace) Read(input worker.WorkspaceReadRequest) (worker.WorkspaceFile, error) {
	path, err := cleanWorkspacePath(input.Path, false)
	if err != nil {
		return worker.WorkspaceFile{}, err
	}
	file, err := w.root.Open(path)
	if err != nil {
		return worker.WorkspaceFile{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return worker.WorkspaceFile{}, err
	}
	if !info.Mode().IsRegular() {
		return worker.WorkspaceFile{}, errors.New("workspace path is not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxWorkspaceFile+1))
	if err != nil {
		return worker.WorkspaceFile{}, err
	}
	if len(content) > maxWorkspaceFile {
		return worker.WorkspaceFile{}, errors.New("workspace file exceeds 1 MiB")
	}
	if !utf8.Valid(content) {
		return worker.WorkspaceFile{}, errors.New("workspace file is not UTF-8 text")
	}
	return worker.WorkspaceFile{
		Path: wirePath(path), Content: string(content), Size: int64(len(content)),
	}, nil
}

// DiffFile returns the selected workspace file and its combined (HEAD to
// worktree) patch. This is the Cloud Docker equivalent of the local daemon's
// file-detail read model: deleted and binary files are still reviewable even
// when no text content can be returned, untracked text gets a synthetic patch,
// and every payload remains bounded.
func (w *workspace) DiffFile(ctx context.Context, input worker.WorkspaceDiffFileRequest) (worker.WorkspaceDiffFile, error) {
	path, err := cleanWorkspacePath(input.Path, false)
	if err != nil {
		return worker.WorkspaceDiffFile{}, err
	}

	if input.Category != "" && input.Category != "uncommitted" {
		return w.diffCommittedFile(ctx, path, input.Category)
	}
	status, err := w.fileStatus(ctx, path)
	if err != nil {
		return worker.WorkspaceDiffFile{}, err
	}
	file := worker.WorkspaceDiffFile{Path: wirePath(path), Status: status}
	file.Deleted = status == "deleted"

	if !file.Deleted {
		content, size, binary, truncated, readErr := w.diffFileContent(path)
		if readErr != nil {
			return worker.WorkspaceDiffFile{}, readErr
		}
		file.Content, file.Size = content, size
		file.Binary, file.ContentTruncated = binary, truncated
	}
	file.BaseContent, _, _ = w.gitFileContent(ctx, "HEAD", path)

	if status == "unmodified" {
		return file, nil
	}
	if status == "untracked" {
		if !file.Binary && !file.ContentTruncated {
			file.Diff, file.DiffTruncated = truncateDiff(syntheticAddedFileDiff(file.Path, file.Content), maxDiffOutput)
		}
		return file, nil
	}

	numstat, _, err := w.git(ctx, "diff", "--numstat", "HEAD", "--", path)
	if err != nil {
		return worker.WorkspaceDiffFile{}, err
	}
	file.Additions, file.Deletions, file.Binary = diffNumstat(numstat, file.Binary)
	diff, truncated, err := w.git(ctx, "diff", "--no-ext-diff", "--no-textconv", "--find-renames", "--unified=3", "HEAD", "--", path)
	if err != nil {
		return worker.WorkspaceDiffFile{}, err
	}
	file.Diff, file.DiffTruncated = diff, truncated
	return file, nil
}

func (w *workspace) diffCommittedFile(ctx context.Context, path, category string) (worker.WorkspaceDiffFile, error) {
	base, err := w.defaultBranchRef(ctx)
	if err != nil { return worker.WorkspaceDiffFile{}, err }
	from, to := base, "HEAD"
	branch, _, _ := w.git(ctx, "branch", "--show-current")
	remote := "origin/" + strings.TrimSpace(branch)
	if _, _, remoteErr := w.git(ctx, "rev-parse", "--verify", remote); remoteErr == nil {
		if category == "pushed" { to = remote } else if category == "unpushed" { from = remote }
	}
	name, _, err := w.git(ctx, "diff", "--name-status", "--find-renames", from+"..."+to, "--", path)
	if err != nil { return worker.WorkspaceDiffFile{}, err }
	line := strings.TrimSpace(name)
	if line == "" { return worker.WorkspaceDiffFile{Path: wirePath(path), Status: "unmodified"}, nil }
	parts := strings.Split(line, "\t")
	status := gitStatus(parts[0])
	file := worker.WorkspaceDiffFile{Path: wirePath(path), Status: status, Deleted: status == "deleted"}
	if !file.Deleted {
		content, truncated, contentErr := w.gitFileContent(ctx, to, path)
		if contentErr != nil { return worker.WorkspaceDiffFile{}, contentErr }
		file.Content, file.Size, file.ContentTruncated = content, int64(len(content)), truncated
	}
	numstat, _, err := w.git(ctx, "diff", "--numstat", from+"..."+to, "--", path)
	if err != nil { return worker.WorkspaceDiffFile{}, err }
	file.Additions, file.Deletions, file.Binary = diffNumstat(numstat, false)
	patch, truncated, err := w.git(ctx, "diff", "--no-ext-diff", "--no-textconv", "--find-renames", "--unified=3", from+"..."+to, "--", path)
	if err != nil { return worker.WorkspaceDiffFile{}, err }
	file.Diff, file.DiffTruncated = patch, truncated
	file.BaseContent, _, _ = w.gitFileContent(ctx, from, path)
	return file, nil
}

func (w *workspace) gitFileContent(ctx context.Context, ref, path string) (string, bool, error) {
	content, truncated, err := w.git(ctx, "show", ref+":"+filepath.ToSlash(path))
	if err != nil { return "", false, nil }
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 { return "", truncated, nil }
	return content, truncated, nil
}

func (w *workspace) diffFileContent(path string) (string, int64, bool, bool, error) {
	file, err := w.root.Open(path)
	if err != nil {
		return "", 0, false, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, false, false, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, false, false, errors.New("workspace path is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxWorkspaceFile+1))
	if err != nil {
		return "", 0, false, false, err
	}
	if len(data) > maxWorkspaceFile {
		return "", info.Size(), false, true, nil
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", info.Size(), true, false, nil
	}
	return string(data), info.Size(), false, false, nil
}

func (w *workspace) fileStatus(ctx context.Context, path string) (string, error) {
	output, _, err := w.git(ctx, "status", "--porcelain=v1", "--untracked-files=all", "--", path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.SplitN(output, "\n", 2)[0])
	if len(line) < 2 {
		return "unmodified", nil
	}
	return gitStatus(line[:2]), nil
}

func diffNumstat(output string, binary bool) (int, int, bool) {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 2 {
		return 0, 0, binary
	}
	if fields[0] == "-" || fields[1] == "-" {
		return 0, 0, true
	}
	var additions, deletions int
	_, _ = fmt.Sscanf(fields[0], "%d", &additions)
	_, _ = fmt.Sscanf(fields[1], "%d", &deletions)
	return additions, deletions, binary
}

func syntheticAddedFileDiff(path, content string) string {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	var result strings.Builder
	fmt.Fprintf(&result, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", path, path, path, len(lines))
	for _, line := range lines {
		result.WriteByte('+')
		result.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			result.WriteByte('\n')
		}
	}
	return result.String()
}

func truncateDiff(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

func (w *workspace) Write(input worker.WorkspaceWriteRequest) (worker.WorkspaceFile, error) {
	path, err := cleanWorkspacePath(input.Path, false)
	if err != nil {
		return worker.WorkspaceFile{}, err
	}
	if len(input.Content) > maxWorkspaceFile || !utf8.ValidString(input.Content) {
		return worker.WorkspaceFile{}, errors.New("workspace content must be UTF-8 and at most 1 MiB")
	}
	if info, err := w.root.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return worker.WorkspaceFile{}, errors.New("workspace write target must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return worker.WorkspaceFile{}, err
	}
	parent := filepath.Dir(path)
	if info, err := w.root.Stat(parent); err != nil || !info.IsDir() {
		return worker.WorkspaceFile{}, errors.New("workspace file parent does not exist")
	}
	random, err := randomName()
	if err != nil {
		return worker.WorkspaceFile{}, err
	}
	temp := filepath.Join(parent, ".ao-write-"+random)
	file, err := w.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return worker.WorkspaceFile{}, err
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = w.root.Remove(temp)
		}
	}()
	if _, err := io.WriteString(file, input.Content); err != nil {
		return worker.WorkspaceFile{}, err
	}
	if err := file.Sync(); err != nil {
		return worker.WorkspaceFile{}, err
	}
	if err := file.Close(); err != nil {
		return worker.WorkspaceFile{}, err
	}
	if err := w.root.Rename(temp, path); err != nil {
		return worker.WorkspaceFile{}, err
	}
	cleanup = false
	return worker.WorkspaceFile{
		Path: wirePath(path), Content: input.Content, Size: int64(len(input.Content)),
	}, nil
}

func (w *workspace) Diff(ctx context.Context) (map[string]any, error) {
	status, statusTruncated, err := w.git(ctx, "status", "--short", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	unstaged, unstagedTruncated, err := w.git(ctx, "diff", "--no-ext-diff", "--")
	if err != nil {
		return nil, err
	}
	staged, stagedTruncated, err := w.git(ctx, "diff", "--cached", "--no-ext-diff", "--")
	if err != nil {
		return nil, err
	}
	base, _, _ := w.git(ctx, "rev-parse", "HEAD")
	numstat, numstatTruncated, err := w.git(ctx, "diff", "--numstat", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	stats := diffNumstats(numstat)
	files := make([]map[string]any, 0)
	untracked := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSuffix(status, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		code := line[:2]
		path := strings.TrimSpace(line[3:])
		fileStatus := gitStatus(code)
		if fileStatus == "untracked" {
			untracked = append(untracked, path)
		}
		additions, deletions, binary := 0, 0, false
		if stat, found := stats[path]; found {
			additions, deletions, binary = stat.additions, stat.deletions, stat.binary
		} else if fileStatus == "untracked" {
			content, _, isBinary, truncated, readErr := w.diffFileContent(filepath.FromSlash(path))
			if readErr != nil {
				return nil, readErr
			}
			binary = isBinary
			if !binary && !truncated {
				additions = lineCount(content)
			}
		}
		files = append(files, map[string]any{
			"path": path, "status": fileStatus, "additions": additions,
			"deletions": deletions, "binary": binary,
		})
	}
	combined := staged + unstaged
	combinedTruncated := stagedTruncated || unstagedTruncated
	if len(combined) > maxDiffOutput {
		combined = combined[:maxDiffOutput]
		combinedTruncated = true
	}
	categories := map[string]any{
		"uncommitted": map[string]any{"files": files},
	}
	if base, baseErr := w.defaultBranchRef(ctx); baseErr == nil {
		branch, _, branchErr := w.git(ctx, "branch", "--show-current")
		if branchErr == nil && strings.TrimSpace(branch) != "" {
			branch = strings.TrimSpace(branch)
			remoteBranch := "origin/" + branch
			if _, _, remoteErr := w.git(ctx, "rev-parse", "--verify", remoteBranch); remoteErr == nil {
				if pushed, err := w.diffSummary(ctx, base, remoteBranch); err == nil {
					categories["pushed"] = pushed
				}
				if unpushed, err := w.diffSummary(ctx, remoteBranch, "HEAD"); err == nil {
					categories["unpushed"] = unpushed
				}
			} else if unpushed, err := w.diffSummary(ctx, base, "HEAD"); err == nil {
				categories["unpushed"] = unpushed
			}
		}
	}
	return map[string]any{
		"status": status, "unstaged": unstaged, "staged": staged,
		"combined": combined, "diffBaseRef": "HEAD",
		"diffBaseSha": strings.TrimSpace(base), "files": files,
		"untrackedFiles": untracked,
		"categories": categories,
		"truncated": map[string]bool{
			"combined": combinedTruncated,
			"stats":    statusTruncated || numstatTruncated,
		},
	}, nil
}

func (w *workspace) defaultBranchRef(ctx context.Context) (string, error) {
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master"} {
		if _, _, err := w.git(ctx, "rev-parse", "--verify", ref); err == nil {
			return ref, nil
		}
	}
	return "", errors.New("default branch reference is unavailable")
}

func (w *workspace) diffSummary(ctx context.Context, from, to string) (map[string]any, error) {
	nameStatus, _, err := w.git(ctx, "diff", "--name-status", "--find-renames", from+"..."+to)
	if err != nil { return nil, err }
	numstat, _, err := w.git(ctx, "diff", "--numstat", "--find-renames", from+"..."+to)
	if err != nil { return nil, err }
	stats := diffNumstats(numstat)
	files := make([]map[string]any, 0)
	for _, line := range strings.Split(strings.TrimSuffix(nameStatus, "\n"), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 { continue }
		path := parts[len(parts)-1]
		status := gitStatus(parts[0])
		stat := stats[path]
		files = append(files, map[string]any{"path": path, "status": status, "additions": stat.additions, "deletions": stat.deletions, "binary": stat.binary})
	}
	return map[string]any{"files": files, "baseRef": from, "headRef": to}, nil
}

type diffStat struct {
	additions int
	deletions int
	binary    bool
}

func diffNumstats(output string) map[string]diffStat {
	stats := make(map[string]diffStat)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		additions, deletions, binary := diffNumstat(strings.Join(parts[:2], "\t"), false)
		stats[parts[2]] = diffStat{additions: additions, deletions: deletions, binary: binary}
	}
	return stats
}

func lineCount(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + boolToInt(!strings.HasSuffix(content, "\n"))
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (w *workspace) git(ctx context.Context, args ...string) (string, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(runCtx, "git", append([]string{"-C", w.path}, args...)...)
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxDiffOutput}
	command.Stderr = &limitedWriter{writer: &output, remaining: maxDiffOutput}
	err := command.Run()
	if runCtx.Err() != nil {
		return "", false, errors.New("git operation timed out")
	}
	truncated := output.Len() >= maxDiffOutput
	if err != nil {
		return "", truncated, fmt.Errorf("git %s: %w: %s", args[0], err, output.String())
	}
	return output.String(), truncated, nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) > w.remaining {
		data = data[:w.remaining]
	}
	if len(data) > 0 {
		if _, err := w.writer.Write(data); err != nil {
			return 0, err
		}
		w.remaining -= len(data)
	}
	return original, nil
}

func cleanWorkspacePath(value string, allowRoot bool) (string, error) {
	if strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
		return "", errUnsafePath
	}
	path := filepath.Clean(filepath.FromSlash(value))
	if path == "." {
		if allowRoot {
			return path, nil
		}
		return "", errUnsafePath
	}
	if path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", errUnsafePath
	}
	return path, nil
}

func wirePath(path string) string {
	if path == "." {
		return ""
	}
	return filepath.ToSlash(path)
}

func randomName() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func gitStatus(code string) string {
	switch {
	case code == "??":
		return "untracked"
	case strings.Contains(code, "A"):
		return "added"
	case strings.Contains(code, "D"):
		return "deleted"
	case strings.Contains(code, "R"):
		return "renamed"
	case strings.Contains(code, "C"):
		return "copied"
	case strings.Contains(code, "M"):
		return "modified"
	default:
		return "changed"
	}
}
