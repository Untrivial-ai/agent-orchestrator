//go:build !windows

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type codexFileInfoWithSystem struct {
	os.FileInfo
	system any
	mode   *os.FileMode
}

func (i codexFileInfoWithSystem) Sys() any { return i.system }
func (i codexFileInfoWithSystem) Mode() os.FileMode {
	if i.mode != nil {
		return *i.mode
	}
	return i.FileInfo.Mode()
}

func TestPrivateFileWriteRejectsSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "vault")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	target := filepath.Join(link, "account", codexCredentialFilename)
	if err := writePrivateFileAtomic(target, []byte("opaque")); err == nil {
		t.Fatal("write followed a symlinked vault ancestor")
	}
	if _, err := os.Stat(filepath.Join(outside, "account", codexCredentialFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write escaped into symlink target: %v", err)
	}
}

func TestPrivateFileReadRejectsSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, codexCredentialFilename), []byte("opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "vault")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readOpaqueCredential(filepath.Join(link, codexCredentialFilename)); err == nil {
		t.Fatal("read followed a symlinked credential ancestor")
	}
}

func TestPrivateDirectoryValidationChecksBeyondFirstRootOwnedAncestor(t *testing.T) {
	base, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	safe := os.ModeDir | 0o755
	unsafeRoot := os.ModeDir | 0o777
	stickyRoot := os.ModeDir | os.ModeSticky | 0o777
	uid, ok := currentCodexUID()
	if !ok {
		t.Fatal("current user ID is invalid")
	}
	lstat := func(rootMode *os.FileMode) func(string) (os.FileInfo, error) {
		return func(path string) (os.FileInfo, error) {
			owner, mode := uid, &safe
			switch path {
			case "/root-owned":
				owner = 0
			case "/":
				owner, mode = 0, rootMode
			}
			return codexFileInfoWithSystem{FileInfo: base, system: &syscall.Stat_t{Uid: owner}, mode: mode}, nil
		}
	}
	if err := validateCodexDirectoryAncestorsWith("/root-owned/vault", lstat(&unsafeRoot), uid); err == nil {
		t.Fatal("validation stopped at the first root-owned ancestor")
	}
	if err := validateCodexDirectoryAncestorsWith("/root-owned/vault", lstat(&stickyRoot), uid); err != nil {
		t.Fatalf("safe sticky root rejected: %v", err)
	}
}

func TestPrivateCredentialRemovalDoesNotDeleteSubstitutedObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexCredentialFilename)
	parked := filepath.Join(dir, "parked-original")
	substitute := filepath.Join(dir, "external-substitute")
	if err := os.WriteFile(path, []byte("admitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(substitute, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	realRename := os.Rename
	previous := renameCodexFileForRemoval
	renameCodexFileForRemoval = func(source, target string) error {
		if source == path {
			if err := realRename(path, parked); err != nil {
				return err
			}
			if err := realRename(substitute, path); err != nil {
				return err
			}
		}
		return realRename(source, target)
	}
	defer func() { renameCodexFileForRemoval = previous }()
	if err := removePrivateCredential(path); !errors.Is(err, errCodexFileChanged) {
		t.Fatalf("remove error = %v, want changed-file error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "external" {
		t.Fatalf("substituted object was deleted: %q, %v", got, err)
	}
}

func TestPrivateCredentialRemovalRejectsSymlinkedParentBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, codexFileStagingPrefix+"outside-victim")
	credential := filepath.Join(outside, codexCredentialFilename)
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential, []byte("outside-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked-vault")
	if err := os.Symlink(outside, linkedParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := removePrivateCredential(filepath.Join(linkedParent, codexCredentialFilename)); err == nil {
		t.Fatal("removal accepted a symlinked parent")
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "keep" {
		t.Fatalf("outside staging-prefixed victim changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(credential); err != nil || string(got) != "outside-credential" {
		t.Fatalf("outside credential changed: %q, %v", got, err)
	}
}

func TestPreparedPrivateFileReplacementRejectsIdentityOrHashChange(t *testing.T) {
	for _, mutate := range []struct {
		name string
		run  func(*testing.T, string)
	}{
		{name: "hash", run: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("external-same-inode"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "identity", run: func(t *testing.T, path string) {
			t.Helper()
			other := filepath.Join(filepath.Dir(path), "external")
			if err := os.WriteFile(other, []byte("external-new-inode"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(other, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, codexCredentialFilename)
			if err := os.WriteFile(path, []byte("admitted"), 0o600); err != nil {
				t.Fatal(err)
			}
			replacement, err := prepareCodexFileReplacement(path, []byte("ao-replacement"))
			if err != nil {
				t.Fatal(err)
			}
			defer replacement.Abort()
			mutate.run(t, path)
			if err := replacement.Commit(); !errors.Is(err, errCodexFileChanged) {
				t.Fatalf("commit error = %v, want detectable external change", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) == "ao-replacement" {
				t.Fatal("AO overwrote an externally changed credential")
			}
		})
	}
}

func TestPrivateFileWriteCleansOnlyVaultStagingLeftovers(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, codexFileStagingPrefix+"crash-leftover")
	unrelated := filepath.Join(dir, ".provider-state")
	if err := os.WriteFile(orphan, []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(dir, codexCredentialFilename), []byte("opaque")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash staging file remains: %v", err)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "keep" {
		t.Fatalf("unrelated file changed: %q, %v", got, err)
	}
}

func TestPrivateFileWriteCleansRemovalTombstoneAndPreservesUnrelatedFile(t *testing.T) {
	dir := t.TempDir()
	tombstone := filepath.Join(dir, codexFileRemovalPrefix+"crash-leftover")
	unrelated := filepath.Join(dir, ".provider-state")
	if err := os.WriteFile(tombstone, []byte("removed-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(dir, codexCredentialFilename), []byte("opaque")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tombstone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash removal tombstone remains: %v", err)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "keep" {
		t.Fatalf("unrelated file changed: %q, %v", got, err)
	}
}

func TestPrivateFileWriteCleansEmptyRemovalMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, codexFileRemovalPrefix+"empty-crash-marker")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(dir, codexCredentialFilename), []byte("opaque")); err != nil {
		t.Fatalf("write did not recover the empty removal marker: %v", err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty removal marker remains: %v", err)
	}
}

func TestPrivateFileReplacementReportsCommittedAfterDirectorySyncFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexCredentialFilename)
	replacement, err := prepareCodexFileReplacement(path, []byte("replacement"))
	if err != nil {
		t.Fatal(err)
	}
	previous := syncCodexDirectory
	syncCodexDirectory = func(string) error { return errors.New("injected directory sync failure") }
	defer func() { syncCodexDirectory = previous }()

	err = replacement.Commit()
	if err == nil || !codexFileMutationCommitted(err) {
		t.Fatalf("commit error = %v, want committed outcome", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "replacement" {
		t.Fatalf("committed replacement = %q, %v", got, readErr)
	}
}

func TestPrivateFileRemovalReportsCommittedAfterDirectorySyncFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexCredentialFilename)
	if err := os.WriteFile(path, []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := syncCodexDirectory
	syncCodexDirectory = func(string) error { return errors.New("injected directory sync failure") }
	defer func() { syncCodexDirectory = previous }()

	err := removeCodexFileIdentityBound(path)
	if err == nil || !codexFileMutationCommitted(err) {
		t.Fatalf("remove error = %v, want committed outcome", err)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential still exists after committed removal: %v", statErr)
	}
}

func TestPrivateFileWriteRejectsUnsafeRemovalTombstone(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(dir, "external")
	tombstone := filepath.Join(dir, codexFileRemovalPrefix+"unsafe")
	if err := os.WriteFile(external, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, tombstone); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writePrivateFileAtomic(filepath.Join(dir, codexCredentialFilename), []byte("opaque")); err == nil {
		t.Fatal("write ignored an unsafe removal tombstone")
	}
	if info, err := os.Lstat(tombstone); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unsafe tombstone was removed: info=%v, err=%v", info, err)
	}
	if got, err := os.ReadFile(external); err != nil || string(got) != "external" {
		t.Fatalf("external file changed: %q, %v", got, err)
	}
}

func TestPrivateFileReadRejectsUnsafeModeHardLinkAndOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexCredentialFilename)
	if err := os.WriteFile(path, []byte("opaque"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readOpaqueCredential(path); err == nil {
		t.Fatal("read accepted a group/world-readable credential")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "credential-link")
	if err := os.Link(path, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := readOpaqueCredential(path); err == nil {
		t.Fatal("read accepted a multiply-linked credential")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := codexFileInfoWithSystem{FileInfo: info, system: &syscall.Stat_t{Uid: uint32(os.Geteuid() + 1)}}
	if ownedByCurrentUser(foreign) {
		t.Fatal("ownership policy accepted a foreign uid")
	}
}
