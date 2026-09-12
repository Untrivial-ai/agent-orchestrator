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
	files := make([]map[string]any, 0)
	untracked := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if len(line) < 4 {
			continue
		}
		code := line[:2]
		path := strings.TrimSpace(line[3:])
		fileStatus := gitStatus(code)
		if fileStatus == "untracked" {
			untracked = append(untracked, path)
		}
		files = append(files, map[string]any{
			"path": path, "status": fileStatus, "additions": 0,
			"deletions": 0, "binary": false,
		})
	}
	combined := staged + unstaged
	combinedTruncated := stagedTruncated || unstagedTruncated
	if len(combined) > maxDiffOutput {
		combined = combined[:maxDiffOutput]
		combinedTruncated = true
	}
	return map[string]any{
		"status": status, "unstaged": unstaged, "staged": staged,
		"combined": combined, "diffBaseRef": "HEAD",
		"diffBaseSha": strings.TrimSpace(base), "files": files,
		"untrackedFiles": untracked,
		"truncated": map[string]bool{
			"combined": combinedTruncated,
			"stats":    statusTruncated,
		},
	}, nil
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
