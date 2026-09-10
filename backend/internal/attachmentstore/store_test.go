package attachmentstore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestStoreImportsBeforeWorkspaceRemovalAndMaterializesAfterRestore(t *testing.T) {
	dataDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "worktree")
	attachmentPath := filepath.Join(workspace, filepath.FromSlash(WorkspaceDir), "attachment-legacy.png")
	if err := os.MkdirAll(filepath.Dir(attachmentPath), 0o750); err != nil {
		t.Fatal(err)
	}
	want := []byte("legacy-image-bytes")
	if err := os.WriteFile(attachmentPath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	store := New(dataDir)
	if err := store.ImportWorkspace(context.Background(), "ao-1", workspace); err != nil {
		t.Fatalf("ImportWorkspace: %v", err)
	}
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MaterializeWorkspace(context.Background(), "ao-1", workspace); err != nil {
		t.Fatalf("MaterializeWorkspace: %v", err)
	}

	got, err := os.ReadFile(attachmentPath)
	if err != nil {
		t.Fatalf("read restored attachment: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("restored attachment = %q, want %q", got, want)
	}
}

func TestStorePutWritesCanonicalAndWorkspaceCopies(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	store := New(dataDir)
	want := []byte("new-image-bytes")

	if err := store.Put(context.Background(), "ao-1", workspace, "attachment-new.png", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(WorkspaceDir), "attachment-new.png"))
	if err != nil {
		t.Fatalf("read workspace attachment: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("workspace attachment = %q, want %q", got, want)
	}

	file, info, err := store.Open(context.Background(), "ao-1", "attachment-new.png")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = file.Close() }()
	if !info.Mode().IsRegular() || info.Size() != int64(len(want)) {
		t.Fatalf("canonical info = %#v", info)
	}
}

func TestStorePutRequiresCanonicalDataDirectory(t *testing.T) {
	workspace := t.TempDir()
	name := "attachment-no-store.bin"
	if err := New("").Put(context.Background(), "ao-1", workspace, name, []byte("bytes")); err == nil {
		t.Fatal("Put succeeded without a canonical data directory")
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(WorkspaceDir), name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Put without durable storage created a workspace-only attachment: %v", err)
	}
}

func TestStorePutRejectsSymlinkedWorkspaceAttachmentDir(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".ao"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ".ao", "attachments")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	name := "attachment-review.png"
	err := New(dataDir).Put(context.Background(), "ao-1", workspace, name, []byte("must stay confined"))
	if err == nil {
		t.Fatal("Put succeeded through a symlinked workspace attachment directory")
	}
	if _, statErr := os.Stat(filepath.Join(outside, name)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("Put wrote outside the workspace: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, durableDir, "ao-1", name)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("failed Put left an unreferenced canonical attachment: %v", statErr)
	}
}

func TestStoreOpenRejectsSymlinkedCanonicalSessionDir(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	store := New(dataDir)
	name := "attachment-review.png"
	if err := store.Put(context.Background(), "ao-1", workspace, name, []byte("session one secret")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dataDir, durableDir, "ao-1"), filepath.Join(dataDir, durableDir, "ao-2")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	file, _, err := store.Open(context.Background(), "ao-2", name)
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open followed a cross-session canonical symlink: %v", err)
	}
}

func TestStoreRemoveSessionRejectsSymlinkedCanonicalRoot(t *testing.T) {
	dataDir := t.TempDir()
	outside := t.TempDir()
	externalSession := filepath.Join(outside, "ao-1")
	if err := os.MkdirAll(externalSession, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(externalSession, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, durableDir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := New(dataDir).RemoveSession(context.Background(), "ao-1")
	if err == nil {
		t.Fatal("RemoveSession accepted a symlinked canonical root")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("RemoveSession deleted data outside the canonical root: %v", statErr)
	}
}

func TestStoreRemoveSessionDeletesOnlyThePermanentOwner(t *testing.T) {
	store := New(t.TempDir())
	for _, id := range []domain.SessionID{"ao-1", "ao-2"} {
		if err := store.Put(context.Background(), id, t.TempDir(), "attachment-owned.bin", []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RemoveSession(context.Background(), "ao-1"); err != nil {
		t.Fatal(err)
	}
	if file, _, err := store.Open(context.Background(), "ao-1", "attachment-owned.bin"); !errors.Is(err, fs.ErrNotExist) {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("deleted owner's attachment error = %v, want not exist", err)
	}
	file, _, err := store.Open(context.Background(), "ao-2", "attachment-owned.bin")
	if err != nil {
		t.Fatalf("other owner's attachment was removed: %v", err)
	}
	defer func() { _ = file.Close() }()
	got, err := io.ReadAll(file)
	if err != nil || string(got) != "ao-2" {
		t.Fatalf("other owner's attachment = %q, %v; want ao-2", got, err)
	}
}

func TestStorePutRejectsSymlinkedCanonicalDirectories(t *testing.T) {
	tests := []struct {
		name string
		link func(t *testing.T, dataDir, outside string)
	}{
		{
			name: "canonical root",
			link: func(t *testing.T, dataDir, outside string) {
				t.Helper()
				if err := os.Symlink(outside, filepath.Join(dataDir, durableDir)); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "session directory",
			link: func(t *testing.T, dataDir, outside string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(dataDir, durableDir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dataDir, durableDir, "ao-1")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			outside := t.TempDir()
			tt.link(t, dataDir, outside)
			name := "attachment-review.png"
			err := New(dataDir).Put(context.Background(), "ao-1", t.TempDir(), name, []byte("must stay confined"))
			if err == nil {
				t.Fatal("Put accepted a symlinked canonical directory")
			}
			if _, statErr := os.Stat(filepath.Join(outside, name)); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("Put wrote outside the canonical store: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(outside, "ao-1", name)); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("Put wrote outside the canonical store: %v", statErr)
			}
		})
	}
}

func TestStoreMaterializeRejectsUnsafeWorkspaceDestination(t *testing.T) {
	dataDir := t.TempDir()
	store := New(dataDir)
	name := "attachment-review.png"
	if err := store.Put(context.Background(), "ao-1", t.TempDir(), name, []byte("canonical bytes")); err != nil {
		t.Fatal(err)
	}

	t.Run("symlinked attachment directory", func(t *testing.T) {
		workspace := t.TempDir()
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(workspace, ".ao"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(workspace, ".ao", "attachments")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		if _, err := store.MaterializeWorkspace(context.Background(), "ao-1", workspace); err == nil {
			t.Fatal("MaterializeWorkspace accepted a symlinked attachment directory")
		}
		if _, err := os.Stat(filepath.Join(outside, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("MaterializeWorkspace wrote outside the workspace: %v", err)
		}
	})

	t.Run("empty workspace path", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if _, err := store.MaterializeWorkspace(context.Background(), "ao-1", ""); err == nil {
			t.Fatal("MaterializeWorkspace accepted an empty workspace path")
		}
	})
}

func TestStoreMaterializeRestoresCanonicalBytesOverExistingProjection(t *testing.T) {
	store := New(t.TempDir())
	workspace := t.TempDir()
	name := "attachment-restored.bin"
	if err := store.Put(context.Background(), "ao-1", workspace, name, []byte("canonical")); err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(workspace, filepath.FromSlash(WorkspaceDir), name)
	if err := os.WriteFile(projection, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MaterializeWorkspace(context.Background(), "ao-1", workspace); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(projection)
	if err != nil || string(got) != "canonical" {
		t.Fatalf("materialized projection = %q, %v; want canonical", got, err)
	}
}

func TestStorePutPreservesExistingAttachmentOnNameCollision(t *testing.T) {
	store := New(t.TempDir())
	workspace := t.TempDir()
	name := "attachment-collision.png"
	if err := store.Put(context.Background(), "ao-1", workspace, name, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "ao-1", workspace, name, []byte("second")); err == nil {
		t.Fatal("second Put overwrote an attachment with the same generated name")
	}
	file, _, err := store.Open(context.Background(), "ao-1", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Fatalf("canonical collision changed bytes to %q, want first", got)
	}
}

func TestStorePutRetainsAttachmentSizeLimit(t *testing.T) {
	store := New(t.TempDir())
	workspace := t.TempDir()
	if err := store.Put(context.Background(), "ao-1", workspace, "attachment-empty.bin", nil); err == nil {
		t.Fatal("Put accepted an empty attachment")
	}
	const maxAttachmentBytes = 10 << 20
	if err := store.Put(context.Background(), "ao-1", workspace, "attachment-large.bin", make([]byte, maxAttachmentBytes+1)); err == nil {
		t.Fatal("Put accepted an attachment larger than 10 MiB")
	}
}

func TestStoreOpenDoesNotServeOversizedCanonicalFile(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, durableDir, "ao-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "attachment-oversized.bin"
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, MaxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	file, _, err := New(dataDir).Open(context.Background(), "ao-1", name)
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open oversized canonical attachment error = %v, want not exist", err)
	}
}

func TestStoreImportWorkspaceRetainsNameTypeAndSizeLimits(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, filepath.FromSlash(WorkspaceDir))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"attachment-legacy.png": []byte("legacy bytes"),
		"notes.txt":             []byte("not an AO-generated attachment"),
		"attachment-vector.svg": []byte("<svg></svg>"),
		"attachment-large.bin":  make([]byte, MaxFileBytes+1),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	store := New(t.TempDir())
	if err := store.ImportWorkspace(context.Background(), "ao-1", workspace); err != nil {
		t.Fatalf("ImportWorkspace: %v", err)
	}
	file, _, err := store.Open(context.Background(), "ao-1", "attachment-legacy.png")
	if err != nil {
		t.Fatalf("Open imported legacy attachment: %v", err)
	}
	_ = file.Close()
	for _, name := range []string{"notes.txt", "attachment-vector.svg", "attachment-large.bin"} {
		if file, _, err := store.Open(context.Background(), "ao-1", name); !errors.Is(err, fs.ErrNotExist) {
			if file != nil {
				_ = file.Close()
			}
			t.Errorf("Open(%q) after import error = %v, want not exist", name, err)
		}
	}
}

func TestStoreImportWorkspaceRejectsSymlinkedParents(t *testing.T) {
	t.Run("workspace AO directory", func(t *testing.T) {
		workspace := t.TempDir()
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(outside, "attachments"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "attachments", "attachment-import.png"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(workspace, ".ao")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		store := New(t.TempDir())
		if err := store.ImportWorkspace(context.Background(), "ao-1", workspace); err == nil {
			t.Fatal("ImportWorkspace accepted a symlinked workspace parent")
		}
		if file, _, err := store.Open(context.Background(), "ao-1", "attachment-import.png"); !errors.Is(err, fs.ErrNotExist) {
			if file != nil {
				_ = file.Close()
			}
			t.Fatalf("outside attachment was imported: %v", err)
		}
	})

	t.Run("canonical root", func(t *testing.T) {
		workspace := t.TempDir()
		dir := filepath.Join(workspace, filepath.FromSlash(WorkspaceDir))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "attachment-import.png"), []byte("inside"), 0o600); err != nil {
			t.Fatal(err)
		}
		dataDir := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dataDir, durableDir)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := New(dataDir).ImportWorkspace(context.Background(), "ao-1", workspace); err == nil {
			t.Fatal("ImportWorkspace accepted a symlinked canonical root")
		}
		if _, err := os.Stat(filepath.Join(outside, "ao-1", "attachment-import.png")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("ImportWorkspace wrote outside the canonical root: %v", err)
		}
	})
}

func TestStoreImportWorkspaceTreatsMissingWorktreeAsNothingToImport(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "already-removed")
	if err := New(t.TempDir()).ImportWorkspace(context.Background(), "ao-1", missing); err != nil {
		t.Fatalf("ImportWorkspace missing worktree: %v", err)
	}
}

func TestStoreImportWorkspaceRequiresCanonicalStorageForLegacyBytes(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, filepath.FromSlash(WorkspaceDir), "attachment-legacy.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("only copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New("").ImportWorkspace(context.Background(), "ao-1", workspace); err == nil {
		t.Fatal("ImportWorkspace reported legacy bytes preserved without canonical storage")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "only copy" {
		t.Fatalf("failed import changed the legacy attachment = %q, %v", got, err)
	}
}

func TestStorePutHonorsCancelledContextBeforeWriting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dataDir := t.TempDir()
	workspace := t.TempDir()
	name := "attachment-cancelled.bin"
	if err := New(dataDir).Put(ctx, "ao-1", workspace, name, []byte("bytes")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put error = %v, want context canceled", err)
	}
	for _, path := range []string{
		filepath.Join(dataDir, durableDir, "ao-1", name),
		filepath.Join(workspace, filepath.FromSlash(WorkspaceDir), name),
	} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("cancelled Put wrote %s: %v", path, err)
		}
	}
}

func TestStorePutRejectsEmptyWorkspaceBeforeCanonicalWrite(t *testing.T) {
	dataDir := t.TempDir()
	name := "attachment-no-workspace.bin"
	if err := New(dataDir).Put(context.Background(), "ao-1", "", name, []byte("bytes")); err == nil {
		t.Fatal("Put accepted an empty workspace path")
	}
	if _, err := os.Stat(filepath.Join(dataDir, durableDir, "ao-1", name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Put persisted canonical bytes without a workspace projection: %v", err)
	}
}

func TestNameFromWorkspacePathRejectsTraversal(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{path: ".ao/attachments/attachment-1.png", want: "attachment-1.png", ok: true},
		{path: "/.ao/attachments/attachment-1.png", want: "attachment-1.png", ok: true},
		{path: ".ao/attachments/image-a1b2c3d4.webp", want: "image-a1b2c3d4.webp", ok: true},
		{path: ".ao/attachments/../secret", ok: false},
		{path: ".ao/attachments/nested/file.png", ok: false},
		{path: ".ao/attachments/secret.txt", ok: false},
		{path: ".ao/attachments/.hidden.png", ok: false},
		{path: ".ao/attachments/attachment-no-extension", ok: false},
		{path: ".ao/attachments/attachment-1.svg", ok: false},
		{path: "dist/attachment-1.png", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, ok := NameFromWorkspacePath(tt.path)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("NameFromWorkspacePath(%q) = %q, %v; want %q, %v", tt.path, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestStoreRejectsUnsafeNamesAndDoesNotImportSymlinks(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	dir := filepath.Join(workspace, filepath.FromSlash(WorkspaceDir))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "attachment-link.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	store := New(dataDir)
	if err := store.Put(context.Background(), domain.SessionID("../escape"), workspace, "attachment.png", []byte("x")); err == nil {
		t.Fatal("Put accepted unsafe session id")
	}
	if err := store.Put(context.Background(), "ao-1", workspace, "../attachment.png", []byte("x")); err == nil {
		t.Fatal("Put accepted unsafe attachment name")
	}
	if err := store.ImportWorkspace(context.Background(), "ao-1", workspace); err != nil {
		t.Fatalf("ImportWorkspace: %v", err)
	}
	if _, _, err := store.Open(context.Background(), "ao-1", "attachment-link.png"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open symlink import error = %v, want not exist", err)
	}
}
