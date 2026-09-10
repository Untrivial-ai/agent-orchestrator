package agent

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	codexMaxCredentialBytes = 8 << 20
	codexFileStagingPrefix  = ".ao-vault-stage-"
	codexFileRemovalPrefix  = ".ao-vault-remove-"
	codexMaxRemovalCleanup  = 32
)

var errCodexFileChanged = errors.New("codex file changed during replacement")

var renameCodexFileForRemoval = os.Rename
var syncCodexDirectory = syncDirectory

type codexFileMutationError struct {
	err       error
	committed bool
}

func (e *codexFileMutationError) Error() string { return e.err.Error() }
func (e *codexFileMutationError) Unwrap() error { return e.err }

func codexFileMutationCommitted(err error) bool {
	var outcome *codexFileMutationError
	return errors.As(err, &outcome) && outcome.committed
}

// codexStorageIOError keeps a filesystem operation's opaque, path-free summary
// for the API and log boundary while preserving the underlying cause. Bootstrap
// unwraps the cause to tell a transient I/O fault (retryable) from an unsafe
// storage rejection (fail closed); Error only ever exposes the summary, so a
// credential path never crosses the boundary through these helpers.
type codexStorageIOError struct {
	summary string
	cause   error
}

func (e *codexStorageIOError) Error() string { return e.summary }
func (e *codexStorageIOError) Unwrap() error { return e.cause }

func codexStorageIOFailure(summary string, cause error) error {
	return &codexStorageIOError{summary: summary, cause: cause}
}

type codexFileState struct {
	exists bool
	info   os.FileInfo
	hash   [sha256.Size]byte
}

type codexFileReplacement struct {
	target    string
	staged    string
	admitted  codexFileState
	stageInfo os.FileInfo
	wantHash  [sha256.Size]byte
	done      bool
}

func readCodexFileState(path string, allowMissing bool) ([]byte, codexFileState, error) {
	parent := filepath.Dir(path)
	if err := validateCodexDirectoryAncestors(parent); err != nil {
		return nil, codexFileState{}, errors.New("codex file has an unsafe ancestor")
	}
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return nil, codexFileState{}, nil
		}
		return nil, codexFileState{}, os.ErrNotExist
	} else if err != nil || validateCodexDirectory(parent, false) != nil {
		return nil, codexFileState{}, errors.New("codex file has an unsafe ancestor")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return nil, codexFileState{}, nil
		}
		return nil, codexFileState{}, os.ErrNotExist
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > codexMaxCredentialBytes || !codexPrivateFileMode(info) {
		return nil, codexFileState{}, errors.New("codex file is not a safe private regular file")
	}
	f, err := openCodexFileNoFollow(path)
	if err != nil {
		return nil, codexFileState{}, errors.New("codex file could not be opened safely")
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, codexFileState{}, errCodexFileChanged
	}
	data, err := io.ReadAll(io.LimitReader(f, codexMaxCredentialBytes+1))
	if err != nil || int64(len(data)) != info.Size() || len(data) > codexMaxCredentialBytes {
		return nil, codexFileState{}, errCodexFileChanged
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() {
		return nil, codexFileState{}, errCodexFileChanged
	}
	if err := validateCodexDirectory(parent, false); err != nil {
		return nil, codexFileState{}, errCodexFileChanged
	}
	return data, codexFileState{exists: true, info: info, hash: sha256.Sum256(data)}, nil
}

func inspectCodexFile(path string, allowMissing bool) (codexFileState, error) {
	_, state, err := readCodexFileState(path, allowMissing)
	return state, err
}

func sameCodexFileState(left, right codexFileState) bool {
	if left.exists != right.exists {
		return false
	}
	return !left.exists || (os.SameFile(left.info, right.info) && left.hash == right.hash)
}

func prepareCodexFileReplacement(path string, data []byte) (*codexFileReplacement, error) {
	return prepareCodexFileReplacementInDirectory(path, data, true)
}

func prepareCodexFileReplacementInDirectory(path string, data []byte, requirePrivateDirectory bool) (*codexFileReplacement, error) {
	if len(data) == 0 || len(data) > codexMaxCredentialBytes {
		return nil, errors.New("codex file content is empty or too large")
	}
	dir := filepath.Dir(path)
	if requirePrivateDirectory {
		if err := ensurePrivateDirectory(dir); err != nil {
			return nil, err
		}
	} else if err := validateCodexDirectory(dir, false); err != nil {
		return nil, err
	}
	if err := cleanupCodexFileStaging(dir); err != nil {
		return nil, err
	}
	admitted, err := inspectCodexFile(path, true)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, codexFileStagingPrefix)
	if err != nil {
		return nil, codexStorageIOFailure("codex replacement staging could not be created", err)
	}
	tmpName := tmp.Name()
	abort := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := protectCodexPrivateFile(tmpName, tmp); err != nil {
		abort()
		return nil, codexStorageIOFailure("codex replacement staging could not be protected", err)
	}
	if _, err := tmp.Write(data); err != nil {
		abort()
		return nil, codexStorageIOFailure("codex replacement staging could not be written", err)
	}
	if err := tmp.Sync(); err != nil {
		abort()
		return nil, codexStorageIOFailure("codex replacement staging could not be synchronized", err)
	}
	stageInfo, err := tmp.Stat()
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		abort()
		return nil, codexStorageIOFailure("codex replacement staging could not be finalized", err)
	}
	return &codexFileReplacement{
		target: path, staged: tmpName, admitted: admitted, stageInfo: stageInfo, wantHash: sha256.Sum256(data),
	}, nil
}

func (r *codexFileReplacement) Abort() {
	if r == nil || r.done {
		return
	}
	r.done = true
	_ = os.Remove(r.staged)
}

func (r *codexFileReplacement) Commit() error {
	if r == nil || r.done {
		return errors.New("codex replacement is no longer pending")
	}
	current, err := inspectCodexFile(r.target, true)
	if err != nil || !sameCodexFileState(r.admitted, current) {
		return errCodexFileChanged
	}
	if err := replaceCodexFile(r.staged, r.target); err != nil {
		return codexStorageIOFailure("codex replacement could not be committed", err)
	}
	r.done = true
	if err := syncCodexDirectory(filepath.Dir(r.target)); err != nil {
		return &codexFileMutationError{err: errors.New("codex replacement directory could not be synchronized"), committed: true}
	}
	after, err := inspectCodexFile(r.target, false)
	if err != nil || !after.exists || !os.SameFile(r.stageInfo, after.info) || after.hash != r.wantHash {
		return errors.New("codex replacement could not be verified")
	}
	return nil
}

func cleanupCodexFileStaging(dir string) error {
	if err := validateCodexDirectory(dir, false); err != nil {
		return errors.New("codex cleanup directory is unsafe")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.New("codex replacement staging could not be inspected")
	}
	removed := false
	removalCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), codexFileRemovalPrefix) {
			removalCount++
		}
	}
	if removalCount > codexMaxRemovalCleanup {
		return errors.New("too many Codex removal tombstones")
	}
	for _, entry := range entries {
		isStaging := strings.HasPrefix(entry.Name(), codexFileStagingPrefix)
		isRemoval := strings.HasPrefix(entry.Name(), codexFileRemovalPrefix)
		if !isStaging && !isRemoval {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if isRemoval {
			admitted, inspectErr := inspectCodexCleanupFile(path)
			if inspectErr != nil {
				return errors.New("codex removal tombstone is unsafe")
			}
			current, inspectErr := inspectCodexCleanupFile(path)
			if inspectErr != nil || !os.SameFile(admitted, current) || admitted.Size() != current.Size() {
				return errors.New("codex removal tombstone changed during cleanup")
			}
			if err := os.Remove(path); err != nil {
				return errors.New("codex removal tombstone could not be cleaned")
			}
			removed = true
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("codex replacement staging changed during cleanup")
		}
		if info.Mode()&os.ModeSymlink == 0 && (!info.Mode().IsRegular() || !codexPrivateFileMode(info)) {
			return errors.New("codex replacement staging is unsafe")
		}
		if info.Mode()&os.ModeSymlink == 0 {
			f, openErr := openCodexFileNoFollow(path)
			if openErr != nil {
				return errors.New("codex replacement staging is unsafe")
			}
			_ = f.Close()
		}
		if err := os.Remove(path); err != nil {
			return errors.New("codex replacement staging could not be cleaned")
		}
		removed = true
	}
	if removed {
		return syncDirectory(dir)
	}
	return nil
}

func inspectCodexCleanupFile(path string) (os.FileInfo, error) {
	parent := filepath.Dir(path)
	if err := validateCodexDirectory(parent, false); err != nil {
		return nil, errors.New("codex cleanup file has an unsafe ancestor")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > codexMaxCredentialBytes || !codexPrivateFileMode(info) {
		return nil, errors.New("codex cleanup file is unsafe")
	}
	f, err := openCodexFileNoFollow(path)
	if err != nil {
		return nil, errors.New("codex cleanup file could not be opened safely")
	}
	opened, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(info, opened) || info.Size() != opened.Size() {
		return nil, errCodexFileChanged
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() {
		return nil, errCodexFileChanged
	}
	if err := validateCodexDirectory(parent, false); err != nil {
		return nil, errCodexFileChanged
	}
	return after, nil
}

func removeCodexFileIdentityBound(path string) error {
	dir := filepath.Dir(path)
	if _, dirErr := os.Lstat(dir); dirErr == nil {
		if err := validateCodexDirectory(dir, false); err != nil {
			return errors.New("codex removal directory is unsafe")
		}
		if err := cleanupCodexFileStaging(dir); err != nil {
			return err
		}
	} else if !errors.Is(dirErr, os.ErrNotExist) {
		return errors.New("codex removal directory could not be inspected")
	}
	_, admitted, err := readCodexFileState(path, true)
	if err != nil || !admitted.exists {
		return err
	}
	marker, err := os.CreateTemp(dir, codexFileRemovalPrefix)
	if err != nil {
		return errors.New("codex removal staging could not be created")
	}
	tombstone := marker.Name()
	if closeErr := marker.Close(); closeErr != nil {
		_ = os.Remove(tombstone)
		return errors.New("codex removal staging could not be closed")
	}
	if err := os.Remove(tombstone); err != nil {
		return errors.New("codex removal staging could not be prepared")
	}
	current, err := inspectCodexFile(path, false)
	if err != nil || !sameCodexFileState(admitted, current) {
		return errCodexFileChanged
	}
	if err := renameCodexFileForRemoval(path, tombstone); err != nil {
		return errors.New("codex file could not be isolated for removal")
	}
	restoreSubstitute := func() {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			_ = os.Rename(tombstone, path)
		}
	}
	moved, err := inspectCodexFile(tombstone, false)
	if err != nil || !sameCodexFileState(admitted, moved) {
		restoreSubstitute()
		return errCodexFileChanged
	}
	if err := os.Remove(tombstone); err != nil {
		restoreSubstitute()
		return errors.New("codex isolated file could not be removed")
	}
	if err := syncCodexDirectory(dir); err != nil {
		return &codexFileMutationError{err: errors.New("codex removal directory could not be synchronized"), committed: true}
	}
	return nil
}

func writePrivateFileAtomic(path string, data []byte) error {
	replacement, err := prepareCodexFileReplacement(path, data)
	if err != nil {
		return err
	}
	defer replacement.Abort()
	return replacement.Commit()
}
