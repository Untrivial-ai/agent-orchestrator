// Package sessionartifacts scans a session's artifact directory and
// classifies its durable output type. It only imports domain so both the
// session service (the read path) and the lifecycle reducer (the persist
// path) can depend on it without an import cycle.
package sessionartifacts

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// List walks a session's artifact directory and returns its regular files,
// sorted by path.
func List(dir string) ([]domain.SessionArtifactFile, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	files := make([]domain.SessionArtifactFile, 0)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		files = append(files, domain.SessionArtifactFile{
			Path:      rel,
			Name:      filepath.Base(path),
			Kind:      inferKind(path, rel),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().UTC(),
		})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func inferKind(absPath, relPath string) domain.SessionArtifactKind {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".html", ".htm":
		return domain.SessionArtifactHTML
	case ".md", ".markdown":
		return domain.SessionArtifactMarkdown
	}
	file, err := os.Open(absPath)
	if err != nil {
		return domain.SessionArtifactGeneric
	}
	defer func() { _ = file.Close() }()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(file, buf)
	if strings.HasPrefix(http.DetectContentType(buf[:n]), "text/html") {
		return domain.SessionArtifactHTML
	}
	return domain.SessionArtifactGeneric
}

// DeriveOutputType classifies a session's durable output from counts alone: a
// PR outranks an artifact, and neither ever reverts once observed, since a
// caller comparing this against a session's current OutputType before
// persisting is expected to only ever move forward (none -> artifact/pr,
// artifact -> pr).
func DeriveOutputType(prCount, artifactFileCount int) domain.SessionOutputType {
	switch {
	case prCount > 0:
		return domain.SessionOutputPR
	case artifactFileCount > 0:
		return domain.SessionOutputArtifact
	default:
		return domain.SessionOutputNone
	}
}
