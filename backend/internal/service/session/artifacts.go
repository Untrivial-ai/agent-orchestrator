package session

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

func listSessionArtifactFiles(dir string) ([]domain.SessionArtifactFile, error) {
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
			Kind:      inferSessionArtifactKind(path, rel),
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

func inferSessionArtifactKind(absPath, relPath string) domain.SessionArtifactKind {
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

func deriveSessionOutputType(prs []domain.PRFacts, artifacts []domain.SessionArtifactFile) domain.SessionOutputType {
	switch {
	case len(prs) > 0:
		return domain.SessionOutputPR
	case len(artifacts) > 0:
		return domain.SessionOutputArtifact
	default:
		return domain.SessionOutputNone
	}
}
