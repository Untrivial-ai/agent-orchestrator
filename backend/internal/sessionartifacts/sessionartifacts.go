// Package sessionartifacts scans a session's artifact directory and
// classifies its durable output type. It only imports domain so both the
// session service (the read path) and the lifecycle reducer (the persist
// path) can depend on it without an import cycle.
package sessionartifacts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Dir returns the canonical artifact directory for a session under dataDir.
// It is a pure, deterministic path computation (dataDir + "artifacts" + id),
// so callers can derive the correct directory even for a session row whose
// stored ArtifactDir is empty — notably rows created before that column
// existed, which migration 0155 backfilled with ” rather than a real path.
func Dir(dataDir string, id domain.SessionID) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "artifacts", string(id))
}

// MaxFiles bounds how many files List returns so a pathological artifact tree
// cannot stall a session read or the lifecycle reconcile that runs under the
// lifecycle mutex.
const MaxFiles = 1000

// List walks a session's artifact directory and returns up to MaxFiles of its
// regular files, sorted by path. Unreadable entries are skipped rather than
// failing the walk.
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
			if path == root {
				return walkErr
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= MaxFiles {
			return filepath.SkipAll
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
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		files = append(files, domain.SessionArtifactFile{
			Path:      rel,
			Name:      filepath.Base(path),
			Kind:      inferKind(rel),
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

func inferKind(relPath string) domain.SessionArtifactKind {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".html", ".htm":
		return domain.SessionArtifactHTML
	case ".md", ".markdown":
		return domain.SessionArtifactMarkdown
	}
	return domain.SessionArtifactGeneric
}

// DeriveOutputType classifies a session's durable output from counts alone.
// PR and artifact are independent facts that combine into PRAndArtifact when
// both are present. Once a PR row exists for a session it is never deleted
// (merge/close does not remove it), so prCount only grows; combined with
// artifact files only ever being added, never removed from this
// classification's perspective, the result never reverts (none -> artifact
// and/or pr -> pr_artifact).
func DeriveOutputType(prCount, artifactFileCount int) domain.SessionOutputType {
	hasPR := prCount > 0
	hasArtifact := artifactFileCount > 0
	switch {
	case hasPR && hasArtifact:
		return domain.SessionOutputPRAndArtifact
	case hasPR:
		return domain.SessionOutputPR
	case hasArtifact:
		return domain.SessionOutputArtifact
	default:
		return domain.SessionOutputNone
	}
}
