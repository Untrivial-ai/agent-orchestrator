package cua

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Only names and stat metadata are inspected. A file belongs to this recorder
// only if it is new since Start and its inode matches the finalized movie, or
// the recorder's exact PID/destination failure log names it. Other files stay
// untouched even if they appeared during the same recording interval.
func stagingSnapshot(dir string) (map[string]os.FileInfo, error) {
	result := make(map[string]os.FileInfo)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".mov" {
			continue
		}
		if _, err := uuid.Parse(strings.TrimSuffix(entry.Name(), ".mov")); err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) { // another recorder may have moved it
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			result[path] = info
		}
	}
	return result, nil
}

func (a *Adapter) finishStaging(ctx context.Context, r *windowRecording) error {
	current, err := stagingSnapshot(a.stagingDir)
	if err != nil {
		r.result.StagingCleanup = "unresolved: cannot inspect staging metadata: " + err.Error()
		return fmt.Errorf("inspect native staging after stop: %w", err)
	}
	for path, info := range current {
		if _, existed := r.stagingBefore[path]; !existed {
			r.stagingSeen[path] = info
		}
	}
	final, finalErr := os.Lstat(r.result.Path)
	var candidates []string
	if finalErr == nil && final.Mode().IsRegular() {
		for path, info := range r.stagingSeen {
			if os.SameFile(info, final) {
				candidates = append(candidates, path)
			}
		}
	}
	if len(candidates) == 1 {
		path := candidates[0]
		r.result.StagingPath = path
		if info, exists := current[path]; exists {
			if !os.SameFile(info, final) {
				r.result.StagingCleanup = "unresolved: staging pathname changed ownership"
				return nil
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			r.result.StagingCleanup = "deleted owned staging link; verified absent"
		} else {
			r.result.StagingCleanup = "verified absent after final move"
		}
		return verifyStagingAbsent(path)
	}
	if r.process.err != nil {
		// The public failure message contains both the source UUID and our
		// unique destination. Private successful-path logs redact the UUID.
		predicate := fmt.Sprintf("process == \"screencapture\" AND processID == %d AND eventMessage CONTAINS %q", r.process.pid, r.result.Path)
		out, err := a.run(ctx, "/usr/bin/log", "show", "--last", "1m", "--style", "compact", "--predicate", predicate)
		if err == nil {
			path := failedStagingPath(string(out.Stdout), a.stagingDir, r.result.Path)
			info, present := current[path]
			_, existed := r.stagingBefore[path]
			if path != "" && present && !existed {
				// Recheck the inode immediately before moving the proved source.
				live, err := os.Lstat(path)
				if err != nil || !os.SameFile(live, info) {
					r.result.StagingCleanup = "unresolved: proved staging file changed before recovery"
					return errors.Join(err, refuse("recording_staging_changed", "staging ownership changed before recovery"))
				}
				r.result.StagingPath = path
				recovered := strings.TrimSuffix(r.result.Path, ".mov") + "-recovered.mov"
				if _, err := os.Lstat(recovered); !errors.Is(err, os.ErrNotExist) {
					return refuse("recording_recovery_busy", "refusing to overwrite a recovery destination")
				}
				if err := os.Rename(path, recovered); err != nil {
					r.result.StagingCleanup = "unresolved: exact owned staging file could not be moved: " + err.Error()
					return err
				}
				r.result.Path = recovered
				r.result.StagingCleanup = "recovered into evidence; verified absent"
				return verifyStagingAbsent(path)
			}
		}
	}
	r.result.StagingCleanup = "unresolved: exact source UUID was not proved; unmatched files left untouched"
	return nil
}

func verifyStagingAbsent(path string) error {
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return refuse("recording_staging_leftover", "exact owned staging file is not verified absent: "+path)
	}
	return nil
}

func failedStagingPath(text, dir, destination string) string {
	prefix := "Failed to move screen recording from " + dir + string(os.PathSeparator)
	suffix := " to " + destination + ". "
	paths := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		_, rest, found := strings.Cut(line, prefix)
		if !found {
			continue
		}
		name, _, found := strings.Cut(rest, suffix)
		if !found || filepath.Base(name) != name || filepath.Ext(name) != ".mov" {
			continue
		}
		if _, err := uuid.Parse(strings.TrimSuffix(name, ".mov")); err == nil {
			paths[filepath.Join(dir, name)] = true
		}
	}
	if len(paths) == 1 {
		for path := range paths {
			return path
		}
	}
	return ""
}
