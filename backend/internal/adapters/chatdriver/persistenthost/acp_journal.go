package persistenthost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxACPJournalBytes int64 = 256 << 20

var errACPJournalFull = errors.New("persistent ACP prompt journal is full")

// acpPromptJournal is a bounded, host-owned WAL for provider notifications
// emitted during one active prompt. It is intentionally separate from AO's
// SQLite projection: the host appends provider bytes before delivery, while the
// daemon owns semantic normalization and transactional deduplication.
type acpPromptJournal struct {
	path string
	file *os.File
	size int64
}

type acpJournalSpan struct {
	offset int64
	length int64
}

func openACPPromptJournal(ctx context.Context, path string) (*acpPromptJournal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open persistent ACP prompt journal: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure persistent ACP prompt journal: %w", err)
	}
	return &acpPromptJournal{path: path, file: file}, nil
}

func (j *acpPromptJournal) append(ctx context.Context, frame []byte) (acpJournalSpan, error) {
	if err := ctx.Err(); err != nil {
		return acpJournalSpan{}, err
	}
	if int64(len(frame)) > maxACPJournalBytes-j.size {
		return acpJournalSpan{}, fmt.Errorf("%w: limit=%d", errACPJournalFull, maxACPJournalBytes)
	}
	span := acpJournalSpan{offset: j.size, length: int64(len(frame))}
	written, err := j.file.Write(frame)
	j.size += int64(written)
	if err != nil {
		return acpJournalSpan{}, fmt.Errorf("append persistent ACP prompt journal: %w", err)
	}
	if written != len(frame) {
		return acpJournalSpan{}, io.ErrShortWrite
	}
	return span, nil
}

func (j *acpPromptJournal) replayTo(ctx context.Context, dst io.Writer, span acpJournalSpan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if span.length == 0 {
		return nil
	}
	if span.offset < 0 || span.length < 0 || span.offset+span.length > j.size {
		return errors.New("invalid persistent ACP prompt journal span")
	}
	_, err := io.Copy(dst, io.NewSectionReader(j.file, span.offset, span.length))
	return err
}

func (j *acpPromptJournal) reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := j.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate persistent ACP prompt journal: %w", err)
	}
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind persistent ACP prompt journal: %w", err)
	}
	j.size = 0
	return nil
}

func (j *acpPromptJournal) close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(j.file.Close(), os.Remove(j.path))
}
