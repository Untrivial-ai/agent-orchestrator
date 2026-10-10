// Package testingevidence saves attempt evidence outside the target's data dir.
package testingevidence

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	maxArtifactBytes = 32 << 20
	// Movies can span the whole attempt. Stream them under a separate disk
	// bound instead of retaining the complete recording in daemon memory.
	maxRecordingBytes = 512 << 20
)

// AttemptLookup resolves the owning run without depending on SQLite.
type AttemptLookup interface {
	GetTestAttempt(context.Context, domain.TestAttemptID) (domain.TestAttemptRecord, bool, error)
}

// Store writes append-only evidence beneath the supervisor data directory.
type Store struct {
	dataDir string
	lookup  AttemptLookup
	mu      sync.Mutex
}

// New constructs an evidence store; directories are created on first write.
func New(dataDir string, lookup AttemptLookup) *Store {
	return &Store{dataDir: dataDir, lookup: lookup}
}

var _ ports.TestingEvidenceStore = (*Store)(nil)

type savedReceipt struct {
	Receipt domain.TestEvidenceReceipt `json:"receipt"`
	Frame   *domain.TestDesktopFrame   `json:"frame,omitempty"`
}

func safePart(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 128 {
		return false
	}
	for _, c := range s {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !allowed {
			return false
		}
	}
	return true
}
func (s *Store) directory(ctx context.Context, id domain.TestAttemptID, create bool) (*os.Root, error) {
	if !safePart(string(id)) || s.lookup == nil {
		return nil, fmt.Errorf("invalid evidence attempt")
	}
	rec, ok, err := s.lookup.GetTestAttempt(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok || !safePart(string(rec.RunID)) {
		return nil, fmt.Errorf("unknown evidence attempt")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(s.dataDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	path := filepath.Join("testing", string(rec.RunID), string(id))
	if create {
		if err := root.MkdirAll(path, 0o700); err != nil {
			return nil, err
		}
	}
	return root.OpenRoot(path)
}
func writeSynced(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func syncDir(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// writeArtifact hashes while copying with bounded memory. Failed copies remove
// only their newly created file and never publish a receipt or change the source.
func writeArtifact(ctx context.Context, root *os.Root, name string, data io.Reader, limit int64) (size int64, digest string, err error) {
	staging := name + ".pending"
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", err
	}
	defer func() {
		if err != nil {
			_ = root.Remove(staging)
		}
	}()
	hash := sha256.New()
	size, err = io.CopyBuffer(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx: ctx, reader: data}, limit+1), make([]byte, 64<<10))
	if err == nil && size > limit {
		err = fmt.Errorf("evidence exceeds %d MiB", limit>>20)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = root.Rename(staging, name)
	}
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Write saves an artifact and its receipt before returning success.
func (s *Store) Write(ctx context.Context, id domain.TestAttemptID, a ports.TestingEvidenceArtifact, data io.Reader) (domain.TestEvidenceReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero domain.TestEvidenceReceipt
	if !safePart(a.Kind) || a.MIMEType == "" {
		return zero, fmt.Errorf("invalid evidence artifact")
	}
	root, err := s.directory(ctx, id, true)
	if err != nil {
		return zero, err
	}
	defer func() { _ = root.Close() }()
	artifactID := uuid.NewString()
	extension := ".bin"
	switch a.MIMEType {
	case "image/png":
		extension = ".png"
	case "text/plain":
		extension = ".txt"
	case "text/markdown":
		extension = ".md"
	case "application/json":
		extension = ".json"
	case "video/mp4":
		extension = ".mp4"
	case "video/quicktime":
		extension = ".mov"
	}
	name := a.Kind + "-" + artifactID + extension
	limit := int64(maxArtifactBytes)
	if a.Kind == "recording" && a.MIMEType == "video/quicktime" {
		limit = maxRecordingBytes
	}
	size, digest, err := writeArtifact(ctx, root, name, data, limit)
	if err != nil {
		return zero, err
	}
	receipt := domain.TestEvidenceReceipt{ID: artifactID, AttemptID: id, Kind: a.Kind, RelativePath: name, MIMEType: a.MIMEType, SizeBytes: size, SHA256: digest, CreatedAt: time.Now().UTC()}
	if a.Frame != nil {
		frame := *a.Frame
		frame.ScreenshotID = artifactID
		a.Frame = &frame
	}
	complete := false
	defer func() {
		if !complete {
			_ = root.Remove(name)
			_ = root.Remove(artifactID + ".receipt.json")
			_ = root.Remove(artifactID + ".receipt.json.pending")
		}
	}()
	metadata, err := json.Marshal(savedReceipt{Receipt: receipt, Frame: a.Frame})
	if err != nil {
		return zero, err
	}
	if err := writeSynced(root, artifactID+".receipt.json.pending", metadata); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := root.Rename(artifactID+".receipt.json.pending", artifactID+".receipt.json"); err != nil {
		return zero, err
	}
	if err := syncDir(root); err != nil {
		return zero, err
	}
	complete = true
	return receipt, nil
}

// AppendAction refuses a second dispatching entry with the same request ID,
// including after a supervisor restart. Completion entries can only append.
func (s *Store) AppendAction(ctx context.Context, a domain.TestActionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.RequestID == "" || len(a.RequestID) > 128 || a.Tool == "" {
		return fmt.Errorf("invalid journal entry")
	}
	root, err := s.directory(ctx, a.AttemptID, true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if a.State == "dispatching" {
		old, e := root.Open("actions.jsonl")
		if e == nil {
			scanner := bufio.NewScanner(old)
			scanner.Buffer(make([]byte, 4096), 1<<20)
			for scanner.Scan() {
				var prior domain.TestActionRecord
				if e = json.Unmarshal(scanner.Bytes(), &prior); e != nil {
					break
				}
				if prior.RequestID == a.RequestID {
					e = fmt.Errorf("duplicate testing request ID")
					break
				}
			}
			if e == nil {
				e = scanner.Err()
			}
			_ = old.Close()
			if e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("journal entry too large")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := root.OpenFile("actions.jsonl", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncDir(root)
	}
	return err
}

// List reads saved receipts even after an attempt finishes.
func (s *Store) List(ctx context.Context, id domain.TestAttemptID) ([]domain.TestEvidenceReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipts := []domain.TestEvidenceReceipt{}
	root, err := s.directory(ctx, id, false)
	if os.IsNotExist(err) {
		return receipts, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(entry.Name(), ".receipt.json") {
			continue
		}
		f, e := root.Open(entry.Name())
		if e != nil {
			return nil, e
		}
		var saved savedReceipt
		e = json.NewDecoder(io.LimitReader(f, 65536)).Decode(&saved)
		_ = f.Close()
		if e != nil {
			return nil, e
		}
		if saved.Receipt.AttemptID != id || filepath.Base(saved.Receipt.RelativePath) != saved.Receipt.RelativePath {
			return nil, fmt.Errorf("invalid saved receipt")
		}
		receipts = append(receipts, saved.Receipt)
	}
	if f, e := root.Open("actions.jsonl"); e == nil {
		h := sha256.New()
		n, e := io.Copy(h, f)
		info, se := f.Stat()
		_ = f.Close()
		if e != nil {
			return nil, e
		}
		if se != nil {
			return nil, se
		}
		receipts = append(receipts, domain.TestEvidenceReceipt{ID: "journal", AttemptID: id, Kind: "journal", RelativePath: "actions.jsonl", MIMEType: "application/x-ndjson", SizeBytes: n, SHA256: hex.EncodeToString(h.Sum(nil)), CreatedAt: info.ModTime().UTC()})
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	return receipts, nil
}
