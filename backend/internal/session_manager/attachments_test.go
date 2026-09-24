package sessionmanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/attachmentstore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestWriteSpawnAttachments(t *testing.T) {
	dir := t.TempDir()
	m := New(Deps{DataDir: t.TempDir()})
	refs, err := m.writeSpawnAttachments(context.Background(), "ao-1", dir, []ports.SpawnAttachment{
		{Ext: ".html", Data: []byte("first")},
		{Ext: ".png", Data: []byte("second")},
		{Ext: "", Data: []byte("third")},
	})
	if err != nil {
		t.Fatalf("writeSpawnAttachments: %v", err)
	}

	want := []string{".ao/attachments/attachment-1.html", ".ao/attachments/attachment-2.png", ".ao/attachments/attachment-3.bin"}
	if len(refs) != len(want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	for i, ref := range refs {
		if ref != want[i] {
			t.Errorf("ref[%d] = %q, want %q", i, ref, want[i])
		}
		got, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref)))
		if readErr != nil {
			t.Fatalf("read %s: %v", ref, readErr)
		}
		if len(got) == 0 {
			t.Errorf("attachment %s is empty on disk", ref)
		}
	}
}

func TestStageAttachmentsUsesNeutralFileNames(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID:       "ao-1",
		Metadata: domain.SessionMetadata{WorkspacePath: dir},
	}
	m := New(Deps{Store: st, Workspace: &fakeWorkspace{}, DataDir: dataDir})

	refs, err := m.StageAttachments(context.Background(), "ao-1", []ports.SpawnAttachment{
		{Ext: ".html", Data: []byte("<main>hi</main>")},
	})
	if err != nil {
		t.Fatalf("StageAttachments: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %v, want one", refs)
	}
	if !strings.HasPrefix(refs[0], ".ao/attachments/attachment-") || !strings.HasSuffix(refs[0], ".html") {
		t.Fatalf("ref = %q, want neutral attachment name with .html extension", refs[0])
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(refs[0]))); err != nil {
		t.Fatalf("staged attachment missing on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "attachments", "ao-1", filepath.Base(refs[0]))); err != nil {
		t.Fatalf("canonical attachment missing on disk: %v", err)
	}
}

func TestStageAttachmentsRetriesGeneratedNameCollisionsWithoutOverwritingHistory(t *testing.T) {
	workspace := t.TempDir()
	dataDir := t.TempDir()
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID:       "ao-1",
		Metadata: domain.SessionMetadata{WorkspacePath: workspace},
	}
	m := New(Deps{Store: st, Workspace: &fakeWorkspace{}, DataDir: dataDir})
	suffixes := []string{"deadbeef00", "cafebabe00"}
	m.attachmentSuffix = func() (string, error) {
		next := suffixes[0]
		suffixes = suffixes[1:]
		return next, nil
	}

	oldName := "attachment-deadbeef00.png"
	for _, dir := range []string{
		filepath.Join(dataDir, "attachments", "ao-1"),
		filepath.Join(workspace, filepath.FromSlash(attachmentsDir)),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, oldName), []byte("historical bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	refs, err := m.StageAttachments(context.Background(), "ao-1", []ports.SpawnAttachment{{Ext: ".png", Data: []byte("new bytes")}})
	if err != nil {
		t.Fatalf("StageAttachments: %v", err)
	}
	if len(refs) != 1 || refs[0] != ".ao/attachments/attachment-cafebabe00.png" {
		t.Fatalf("refs = %v, want retried collision-free name", refs)
	}
	oldBytes, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(attachmentsDir), oldName))
	if err != nil || string(oldBytes) != "historical bytes" {
		t.Fatalf("historical attachment = %q, %v; want unchanged", oldBytes, err)
	}
	newBytes, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(refs[0])))
	if err != nil || string(newBytes) != "new bytes" {
		t.Fatalf("new attachment = %q, %v; want new bytes", newBytes, err)
	}
}

func TestAppendAttachmentReferences(t *testing.T) {
	t.Run("appends after a brief", func(t *testing.T) {
		got := appendAttachmentReferences("Fix the button", []string{".ao/attachments/attachment-1.html"})
		if !strings.HasPrefix(got, "Fix the button\n\n") {
			t.Errorf("brief not preserved: %q", got)
		}
		if !strings.Contains(got, "- .ao/attachments/attachment-1.html") {
			t.Errorf("missing reference: %q", got)
		}
	})

	t.Run("handles empty brief", func(t *testing.T) {
		got := appendAttachmentReferences("", []string{".ao/attachments/attachment-1.html"})
		if strings.HasPrefix(got, "\n") {
			t.Errorf("leading blank line for empty brief: %q", got)
		}
		if !strings.Contains(got, "Attached files") {
			t.Errorf("missing header: %q", got)
		}
		if strings.Contains(got, "Attached images") {
			t.Errorf("header still describes attachments as images: %q", got)
		}
	})

	t.Run("no refs returns prompt unchanged", func(t *testing.T) {
		if got := appendAttachmentReferences("brief", nil); got != "brief" {
			t.Errorf("got %q, want %q", got, "brief")
		}
	})
}

// TestStageAttachmentsLeasesUntilCommittedBySend exercises the lease lifecycle
// end-to-end: StageAttachments leases a draft attachment, and a chat message
// that names it (the same format the composer writes; see
// appendAttachmentReferences) commits the lease when Send delivers it. Once
// committed, ReleaseAttachments — the explicit-discard path a composer chip
// removal would take — must no longer be able to delete it.
func TestStageAttachmentsLeasesUntilCommittedBySend(t *testing.T) {
	workspace := t.TempDir()
	dataDir := t.TempDir()
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	// newChatManager points DataDir at a fixed non-existent path (chat tests
	// normally never touch the filesystem); swap in a real store rooted at a
	// temp dir so this test can exercise actual staged bytes.
	m.attachments = attachmentstore.New(dataDir)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Metadata: domain.SessionMetadata{WorkspacePath: workspace},
	}

	refs, err := m.StageAttachments(context.Background(), "mer-1", []ports.SpawnAttachment{
		{Ext: ".png", Data: []byte("draft bytes")},
	})
	if err != nil {
		t.Fatalf("StageAttachments: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %v, want 1", refs)
	}

	message := appendAttachmentReferences("look at this", refs)
	if err := m.Send(context.Background(), "mer-1", message, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(launcher.relayed) != 1 || launcher.relayed[0] != message {
		t.Fatalf("relayed = %v, want [%q]", launcher.relayed, message)
	}

	// The send committed the lease: an explicit release naming the same ref must
	// not be able to delete the now-historical attachment.
	if err := m.ReleaseAttachments(context.Background(), "mer-1", refs); err != nil {
		t.Fatalf("ReleaseAttachments: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(refs[0]))); err != nil {
		t.Fatalf("committed attachment was deleted by release: %v", err)
	}
}

// TestReleaseAttachmentsDeletesAnUnsentDraft covers the chip-removal path: a
// staged attachment that a message never referenced is fully removed, both from
// the worktree and from durable storage, by an explicit release.
func TestReleaseAttachmentsDeletesAnUnsentDraft(t *testing.T) {
	workspace := t.TempDir()
	dataDir := t.TempDir()
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID:       "ao-1",
		Metadata: domain.SessionMetadata{WorkspacePath: workspace},
	}
	m := New(Deps{Store: st, Workspace: &fakeWorkspace{}, DataDir: dataDir})

	refs, err := m.StageAttachments(context.Background(), "ao-1", []ports.SpawnAttachment{
		{Ext: ".png", Data: []byte("unsent draft")},
	})
	if err != nil {
		t.Fatalf("StageAttachments: %v", err)
	}

	if err := m.ReleaseAttachments(context.Background(), "ao-1", refs); err != nil {
		t.Fatalf("ReleaseAttachments: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(refs[0]))); !os.IsNotExist(err) {
		t.Fatalf("released draft attachment still on disk: %v", err)
	}
}
