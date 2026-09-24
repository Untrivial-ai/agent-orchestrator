package sessionmanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/attachmentstore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRestoreAttachmentsFailsWhenGitExcludeCannotBeWritten(t *testing.T) {
	m, _, _, workspace := newManager()
	m.attachments = attachmentstore.New(t.TempDir())
	want := errors.New("exclude is read-only")
	workspace.addExcludeErr = want
	if err := m.attachments.PutCanonical(context.Background(), "mer-1", "attachment-1.png", []byte("private")); err != nil {
		t.Fatal(err)
	}
	ws := ports.WorkspaceInfo{SessionID: "mer-1", Path: t.TempDir()}
	if err := m.restoreAttachments(context.Background(), "mer-1", ws); !errors.Is(err, want) {
		t.Fatalf("restore attachments error = %v, want %v", err, want)
	}
	file := filepath.Join(ws.Path, filepath.FromSlash(attachmentsDir), "attachment-1.png")
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unignored restored file %s exists or stat failed: %v", file, err)
	}
}

func TestRestoreWithoutAttachmentsDoesNotRequireGitExclude(t *testing.T) {
	m, _, _, workspace := newManager()
	m.attachments = attachmentstore.New(t.TempDir())
	workspace.addExcludeErr = errors.New("exclude is read-only")
	ws := ports.WorkspaceInfo{SessionID: "mer-1", Path: t.TempDir()}
	if err := m.restoreAttachments(context.Background(), "mer-1", ws); err != nil {
		t.Fatalf("attachment-free restore failed: %v", err)
	}
	if len(workspace.excludePatterns) != 0 {
		t.Fatalf("attachment-free restore wrote excludes: %v", workspace.excludePatterns)
	}
}

func TestStageAttachmentsDoesNotExposeFileWhenGitExcludeFails(t *testing.T) {
	m, st, _, workspace := newManager()
	m.attachments = attachmentstore.New(t.TempDir())
	m.attachmentSuffix = func() (string, error) { return "fixed", nil }
	workspacePath := t.TempDir()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		Metadata: domain.SessionMetadata{WorkspacePath: workspacePath},
	}
	want := errors.New("exclude is read-only")
	workspace.addExcludeErr = want
	refs, err := m.StageAttachments(context.Background(), "mer-1", []ports.SpawnAttachment{{Ext: ".png", Data: []byte("private")}})
	if !errors.Is(err, want) || len(refs) != 0 {
		t.Fatalf("stage attachments = (%v, %v), want no refs and exclude error", refs, err)
	}
	file := filepath.Join(workspacePath, filepath.FromSlash(attachmentsDir), "attachment-fixed.png")
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unignored file %s exists or stat failed: %v", file, err)
	}
}

func TestSynchronousSpawnDoesNotExposeAttachmentWhenGitExcludeFails(t *testing.T) {
	m, st, runtime, workspace := newManager()
	m.attachments = attachmentstore.New(t.TempDir())
	workspace.path = t.TempDir()
	want := errors.New("exclude is read-only")
	workspace.addExcludeErr = want
	_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		Attachments: []ports.SpawnAttachment{{Ext: ".png", Data: []byte("private")}},
	})
	if !errors.Is(err, want) {
		t.Fatalf("spawn error = %v, want exclude failure", err)
	}
	file := filepath.Join(workspace.path, filepath.FromSlash(attachmentsDir), "attachment-1.png")
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unignored spawn attachment %s exists or stat failed: %v", file, err)
	}
	if runtime.created != 0 {
		t.Fatal("agent runtime launched after exclusion failed")
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("failed spawn left a visible seed row")
	}
}
