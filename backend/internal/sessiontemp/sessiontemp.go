// Package sessiontemp gives every managed session a project-owned temporary
// directory with a traceable cleanup boundary.
//
// Worker processes previously inherited the daemon's shared OS temp location
// (TEMP/TMP/TMPDIR on Windows), so after a crash or kill it was impossible to
// tell one session's leftovers from another application's files. Each project
// now owns a scratch root under the AO data dir, each managed run owns a
// subfolder carrying an ownership record, and covered launch paths point
// TMPDIR/TEMP/TMP at that folder, refusing clearly when the root is unusable.
//
// Cleanup discovery is report-only: Discover lists run folders whose session
// is no longer active, but nothing here deletes user or session data.
package sessiontemp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrTempRootUnusable marks a scratch root that cannot host session temp dirs.
// Callers use errors.Is to distinguish "refuse the launch" from other errors.
var ErrTempRootUnusable = errors.New("session temp: scratch root unusable")

// RootDirName is the AO-data-dir child that owns all project scratch roots.
const RootDirName = "session-temp"

const (
	tmpDirName   = "tmp"
	ownerFile    = "owner.json"
	ownerVersion = 1
)

// Owner is the traceability record stored at <runDir>/owner.json. It lets
// post-crash discovery attribute a leftover folder to exactly one
// project/session pair instead of guessing from shared OS temp contents.
type Owner struct {
	Version   int    `json:"version"`
	ProjectID string `json:"projectId"`
	SessionID string `json:"sessionId"`
	TempDir   string `json:"tempDir"`
	CreatedAt string `json:"createdAt"`
	PID       int    `json:"pid"`
}

// StaleRun is one report-only discovery hit: a run folder whose session id is
// absent from the active set. Discover never deletes anything.
type StaleRun struct {
	ProjectID string
	SessionID string
	RunDir    string
	TempDir   string
	Owner     Owner
}

// Root returns the global scratch root for a data dir without touching disk.
func Root(dataDir string) string {
	return filepath.Join(dataDir, RootDirName)
}

// projectBucket maps a project id to its scratch-root directory name.
// Projectless (standalone) sessions share the "standalone" bucket, mirroring
// the scratch workspace layout (<managedRoot>/standalone/sessions/<id>), so
// every managed run has a project-owned folder even without a project.
func projectBucket(projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "standalone", nil
	}
	if err := validateID("project id", projectID); err != nil {
		return "", err
	}
	return projectID, nil
}

// ProjectRoot returns the explicitly configured scratch root for one project
// without touching disk. A blank project id maps to the "standalone" bucket;
// use Prepare/EnsureProjectRoot when the id needs validation.
func ProjectRoot(dataDir, projectID string) string {
	bucket, err := projectBucket(projectID)
	if err != nil {
		bucket = projectID
	}
	return filepath.Join(Root(dataDir), bucket)
}

// RunDir returns the per-run folder for one managed session without touching
// disk. The live temp dir is RunDir/tmp; RunDir/owner.json attributes it.
func RunDir(dataDir, projectID, sessionID string) string {
	return filepath.Join(ProjectRoot(dataDir, projectID), sessionID)
}

// TempDirFor returns the live temp folder path for one managed session
// without touching disk.
func TempDirFor(dataDir, projectID, sessionID string) string {
	return filepath.Join(RunDir(dataDir, projectID, sessionID), tmpDirName)
}

// EnvFor returns the TMPDIR/TEMP/TMP overrides pointing at tmpDir.
func EnvFor(tmpDir string) map[string]string {
	return map[string]string{
		"TMPDIR": tmpDir,
		"TEMP":   tmpDir,
		"TMP":    tmpDir,
	}
}

// ApplyToEnv pins TMPDIR/TEMP/TMP at tmpDir inside env. Project configuration
// must never redirect a session's temp files back into shared OS temp, so the
// AO-owned values always win. On Windows the match is case-insensitive.
func ApplyToEnv(env map[string]string, tmpDir string, caseInsensitive bool) {
	if env == nil || strings.TrimSpace(tmpDir) == "" {
		return
	}
	for _, key := range []string{"TMPDIR", "TEMP", "TMP"} {
		if caseInsensitive {
			for existing := range env {
				if strings.EqualFold(existing, key) {
					delete(env, existing)
				}
			}
		}
		env[key] = tmpDir
	}
}

func validateID(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrTempRootUnusable, name)
	}
	if strings.ContainsAny(value, `/\`) || value == "." || value == ".." {
		return fmt.Errorf("%w: %s %q must not contain path separators or traversal components", ErrTempRootUnusable, name, value)
	}
	return nil
}

func requireDataDir(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", fmt.Errorf("%w: AO data directory is required", ErrTempRootUnusable)
	}
	if !filepath.IsAbs(dataDir) {
		return "", fmt.Errorf("%w: AO data directory %q must be absolute", ErrTempRootUnusable, dataDir)
	}
	return filepath.Clean(dataDir), nil
}

// EnsureProjectRoot validates the explicitly configured scratch root for one
// project and creates it when missing. A probe file proves the root is
// writable; any failure refuses clearly so launches fail closed instead of
// silently falling back to shared OS temp.
func EnsureProjectRoot(dataDir, projectID string) (string, error) {
	cleanDataDir, err := requireDataDir(dataDir)
	if err != nil {
		return "", err
	}
	bucket, err := projectBucket(projectID)
	if err != nil {
		return "", err
	}
	root := filepath.Join(cleanDataDir, RootDirName, bucket)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("%w: create project scratch root %q: %w", ErrTempRootUnusable, root, err)
	}
	probe, err := os.CreateTemp(root, ".writable-*")
	if err != nil {
		return "", fmt.Errorf("%w: project scratch root %q is not writable: %w", ErrTempRootUnusable, root, err)
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)
	return root, nil
}

// Prepare creates (or reuses) the per-run temp folder for one managed session
// and records ownership at <runDir>/owner.json. It returns the live temp dir.
// An unusable root, an unreadable/inconsistent owner record, or any mkdir
// failure refuses clearly with ErrTempRootUnusable.
func Prepare(dataDir, projectID, sessionID string) (string, error) {
	if err := validateID("session id", sessionID); err != nil {
		return "", err
	}
	bucket, err := projectBucket(projectID)
	if err != nil {
		return "", err
	}
	root, err := EnsureProjectRoot(dataDir, bucket)
	if err != nil {
		return "", err
	}
	runDir := filepath.Join(root, sessionID)
	tmpDir := filepath.Join(runDir, tmpDirName)
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", fmt.Errorf("%w: create session temp dir %q: %w", ErrTempRootUnusable, tmpDir, err)
	}
	if err := ensureOwnerRecord(runDir, tmpDir, bucket, sessionID); err != nil {
		return "", err
	}
	return tmpDir, nil
}

func ensureOwnerRecord(runDir, tmpDir, projectID, sessionID string) error {
	ownerPath := filepath.Join(runDir, ownerFile)
	if prior, err := os.ReadFile(ownerPath); err == nil {
		var existing Owner
		if err := json.Unmarshal(prior, &existing); err != nil {
			return fmt.Errorf("%w: session temp owner %q is corrupt: %w", ErrTempRootUnusable, ownerPath, err)
		}
		if existing.ProjectID != projectID || existing.SessionID != sessionID {
			return fmt.Errorf("%w: session temp owner %q belongs to %s/%s, not %s/%s",
				ErrTempRootUnusable, ownerPath, existing.ProjectID, existing.SessionID, projectID, sessionID)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect session temp owner %q: %w", ErrTempRootUnusable, ownerPath, err)
	}
	record := Owner{
		Version:   ownerVersion,
		ProjectID: projectID,
		SessionID: sessionID,
		TempDir:   tmpDir,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		PID:       os.Getpid(),
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("%w: encode session temp owner: %w", ErrTempRootUnusable, err)
	}
	// O_EXCL closes the create race between concurrent first launches of the
	// same session: the loser re-reads the winner's record below instead of
	// overwriting or duplicating ownership.
	f, err := os.OpenFile(ownerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ensureOwnerRecord(runDir, tmpDir, projectID, sessionID)
		}
		return fmt.Errorf("%w: create session temp owner %q: %w", ErrTempRootUnusable, ownerPath, err)
	}
	writeErr := func() error {
		if _, err := f.Write(raw); err != nil {
			return err
		}
		return f.Sync()
	}()
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(ownerPath)
		return fmt.Errorf("%w: write session temp owner %q: %w", ErrTempRootUnusable, ownerPath, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: close session temp owner %q: %w", ErrTempRootUnusable, ownerPath, closeErr)
	}
	return nil
}

// Discover reports run folders whose session id is absent from active.
// active holds session ids considered live; nil means every recorded run is
// reported. It never deletes anything: callers decide what to surface or
// prune. Folders without a readable owner record are skipped so unrelated
// files can never be mistaken for AO session temp.
func Discover(dataDir string, active map[string]bool) ([]StaleRun, error) {
	cleanDataDir, err := requireDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(cleanDataDir, RootDirName)
	projects, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session temp: discover %q: %w", root, err)
	}
	var stale []StaleRun
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		projectID := project.Name()
		runs, err := os.ReadDir(filepath.Join(root, projectID))
		if err != nil {
			continue
		}
		for _, run := range runs {
			if !run.IsDir() {
				continue
			}
			sessionID := run.Name()
			if active != nil && active[sessionID] {
				continue
			}
			runDir := filepath.Join(root, projectID, sessionID)
			raw, err := os.ReadFile(filepath.Join(runDir, ownerFile))
			if err != nil {
				continue
			}
			var owner Owner
			if err := json.Unmarshal(raw, &owner); err != nil {
				continue
			}
			if owner.ProjectID != projectID || owner.SessionID != sessionID {
				continue
			}
			tmpDir := owner.TempDir
			if strings.TrimSpace(tmpDir) == "" {
				tmpDir = filepath.Join(runDir, tmpDirName)
			}
			stale = append(stale, StaleRun{
				ProjectID: projectID,
				SessionID: sessionID,
				RunDir:    runDir,
				TempDir:   tmpDir,
				Owner:     owner,
			})
		}
	}
	return stale, nil
}
