package testingevidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestRecordingAtObservedSizeStreamsAndPublishesMatchingReceipt(t *testing.T) {
	const observedSize = 39787533
	dir := t.TempDir()
	store := New(dir, lookup{})
	artifact := ports.TestingEvidenceArtifact{Kind: "recording", MIMEType: "video/quicktime"}
	receipt, err := store.Write(context.Background(), "attempt", artifact, io.LimitReader(zeroReader{}, observedSize))
	if err != nil || receipt.SizeBytes != observedSize {
		t.Fatal(receipt, err)
	}
	file, err := os.Open(filepath.Join(dir, "testing", "run", "attempt", receipt.RelativePath))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil || n != observedSize || hex.EncodeToString(hash.Sum(nil)) != receipt.SHA256 {
		t.Fatal("incomplete recording or wrong digest", n, err)
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("recording permissions", err)
	}
	listed, err := New(dir, lookup{}).List(context.Background(), "attempt")
	if err != nil || len(listed) != 1 || listed[0] != receipt {
		t.Fatal("receipt not durable", listed, err)
	}
	for _, ordinary := range []ports.TestingEvidenceArtifact{{Kind: "logs", MIMEType: "text/plain"}, {Kind: "recording", MIMEType: "application/json"}, {Kind: "other", MIMEType: "video/quicktime"}} {
		_, err := store.Write(context.Background(), "attempt", ordinary, io.LimitReader(zeroReader{}, observedSize))
		if err == nil || !strings.Contains(err.Error(), "evidence exceeds 32 MiB") {
			t.Fatal("ordinary artifact cap changed", ordinary, err)
		}
	}
}

type failingRecordingReader struct {
	cancel context.CancelFunc
	fail   error
	reads  int
}

func (r *failingRecordingReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		if r.cancel != nil {
			r.cancel()
		}
		clear(p)
		return len(p), nil
	}
	return 0, r.fail
}

func TestFailedRecordingCopyPublishesNoReceiptOrPartialOutput(t *testing.T) {
	for _, kind := range []string{"cancel", "read-error"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			store := New(dir, lookup{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := &failingRecordingReader{fail: errors.New("owned source read failed")}
			want := reader.fail
			if kind == "cancel" {
				reader.cancel, want = cancel, context.Canceled
			}
			_, err := store.Write(ctx, "attempt", ports.TestingEvidenceArtifact{Kind: "recording", MIMEType: "video/quicktime"}, reader)
			if !errors.Is(err, want) {
				t.Fatal("copy cause lost", err)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "testing", "run", "attempt"))
			if err != nil || len(entries) != 0 {
				t.Fatal("partial file or receipt published", entries, err)
			}
		})
	}
}

func TestStreamingCopyEnforcesLimitBeforePublication(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// Exercise the same bounded copy with a small limit rather than writing
	// hundreds of MiB to prove the recording cap boundary.
	_, _, err = writeArtifact(context.Background(), root, "owned.mov", zeroReader{}, 1024)
	if err == nil || !strings.Contains(err.Error(), "evidence exceeds") {
		t.Fatal("unbounded copy", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("oversized copy published", entries, err)
	}
}

func TestStreamingCopyRefusesExistingOutputWithoutChangingIt(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	path := filepath.Join(dir, "owned.mov.pending")
	if err := os.WriteFile(path, []byte("protected existing output"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = writeArtifact(context.Background(), root, "owned.mov", strings.NewReader("new movie"), 1024)
	if err == nil {
		t.Fatal("existing destination overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "protected existing output" {
		t.Fatal("write failure changed existing output", err)
	}
}
