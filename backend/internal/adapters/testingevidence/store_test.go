package testingevidence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type lookup struct{}

func (lookup) GetTestAttempt(_ context.Context, id domain.TestAttemptID) (domain.TestAttemptRecord, bool, error) {
	return domain.TestAttemptRecord{ID: id, RunID: "run", Phase: domain.TestAttemptFinished}, true, nil
}
func TestEvidenceReceiptJournalAndRestart(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, lookup{})
	ctx := context.Background()
	frame := domain.TestDesktopFrame{Width: 2, Height: 2, CapturedAt: time.Now().UTC()}
	receipt, err := s.Write(ctx, "attempt", ports.TestingEvidenceArtifact{Kind: "screenshot", MIMEType: "image/png", Frame: &frame}, strings.NewReader("original pixels"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "testing", "run", "attempt")
	data, err := os.ReadFile(filepath.Join(path, receipt.RelativePath))
	if err != nil || string(data) != "original pixels" || receipt.SizeBytes != 15 || len(receipt.SHA256) != 64 {
		t.Fatal("artifact not saved", err)
	}
	meta, err := os.ReadFile(filepath.Join(path, receipt.ID+".receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved savedReceipt
	if err = json.Unmarshal(meta, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Frame.ScreenshotID != receipt.ID || saved.Frame.Width != 2 {
		t.Fatal("frame metadata missing")
	}
	action := domain.TestActionRecord{AttemptID: "attempt", RequestID: "request", Tool: "click", Input: json.RawMessage(`{"x":1,"y":1}`), State: "dispatching", At: time.Now().UTC()}
	if err = s.AppendAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	action.State = "completed"
	if err = s.AppendAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	s = New(dir, lookup{})
	receipts, err := s.List(ctx, "attempt")
	if err != nil || len(receipts) != 2 {
		t.Fatal("saved evidence not readable after restart", err)
	}
	action.State = "dispatching"
	if err = s.AppendAction(ctx, action); err == nil {
		t.Fatal("duplicate request accepted after restart")
	}
	journal, err := os.ReadFile(filepath.Join(path, "actions.jsonl"))
	if err != nil || strings.Count(string(journal), "\n") != 2 {
		t.Fatal("journal was not append-only", err)
	}
	info, err := os.Stat(filepath.Join(path, receipt.RelativePath))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("artifact permissions", err)
	}
}
func TestEvidenceTraversalSymlinkBoundsAndCancelledWrites(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir(), lookup{})
	artifact := ports.TestingEvidenceArtifact{Kind: "logs", MIMEType: "text/plain"}
	for _, id := range []domain.TestAttemptID{"../escape", "/absolute", ".."} {
		if _, err := s.Write(ctx, id, artifact, strings.NewReader("x")); err == nil {
			t.Fatal("traversal accepted")
		}
	}
	artifact.Kind = "../escape"
	if _, err := s.Write(ctx, "attempt", artifact, strings.NewReader("x")); err == nil {
		t.Fatal("artifact traversal accepted")
	}
	artifact.Kind = "logs"
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Write(cancelled, "attempt", artifact, strings.NewReader("x")); err == nil {
		t.Fatal("cancelled write accepted")
	}
	if _, err := s.Write(ctx, "attempt", artifact, strings.NewReader(strings.Repeat("x", maxArtifactBytes+1))); err == nil {
		t.Fatal("oversized artifact accepted")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "testing", "run"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "testing", "run", "attempt")); err != nil {
		t.Fatal(err)
	}
	s = New(root, lookup{})
	if _, err := s.Write(ctx, "attempt", artifact, strings.NewReader("x")); err == nil {
		t.Fatal("symlink escaped data dir")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("outside data modified", err)
	}
}
func TestFailedEvidenceWriteAndCorruptJournal(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(blocked, lookup{})
	ctx := context.Background()
	if _, err := s.Write(ctx, "attempt", ports.TestingEvidenceArtifact{Kind: "report", MIMEType: "text/markdown"}, strings.NewReader("report")); err == nil {
		t.Fatal("failed write returned receipt")
	}
	s = New(dir, lookup{})
	action := domain.TestActionRecord{AttemptID: "attempt", RequestID: "request", Tool: "screenshot", State: "dispatching", At: time.Now().UTC()}
	if err := s.AppendAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "testing", "run", "attempt", "actions.jsonl")
	if err := os.WriteFile(path, []byte("partial crash record"), 0600); err != nil {
		t.Fatal(err)
	}
	action.RequestID = "new"
	if err := s.AppendAction(ctx, action); err == nil {
		t.Fatal("corrupt journal admitted a dispatch")
	}
}
