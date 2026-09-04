package sessionimport

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// homeConfigDir resolves a provider state directory, honoring an environment
// override and otherwise using <home>/<defaultDir>. It relies on os.UserHomeDir,
// which is $HOME on Unix and %USERPROFILE% on Windows, so the same call resolves
// correctly on macOS, Linux, and Windows.
func homeConfigDir(envKey, defaultDir string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, defaultDir), nil
}

// headBytes returns up to limit bytes from the start of the file plus the file
// size. It never loads more than limit bytes into memory.
func headBytes(path string, limit int64) (data []byte, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size = info.Size()

	n := size
	if n > limit {
		n = limit
	}
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	// A file shorter than the requested window is normal, not a failure.
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, size, err
	}
	return buf[:read], size, nil
}

// readHead is headBytes for callers that treat any read failure as "skip this
// transcript". A file that vanished or turned unreadable mid-scan must not
// abort a whole discovery pass.
func readHead(path string, limit int64) (data []byte, size int64, ok bool) {
	data, size, err := headBytes(path, limit)
	if err != nil {
		return nil, 0, false
	}
	return data, size, true
}

// tailBytes returns up to limit bytes from the end of the file.
func tailBytes(path string, size, limit int64) ([]byte, error) {
	if size <= limit {
		data, _, err := headBytes(path, limit)
		return data, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(size-limit, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, limit)
	read, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:read], nil
}

// completeLines splits a byte slice into whole newline-terminated lines. A
// trailing fragment without a newline is dropped, because a bounded head read
// can cut a JSON object in half and a partial object must never be parsed.
func completeLines(data []byte) [][]byte {
	var lines [][]byte
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := bytes.TrimRight(data[:idx], "\r")
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
		data = data[idx+1:]
	}
	return lines
}

// scanLines invokes fn for each newline-delimited line of the file, streaming so
// even a large transcript is read with bounded memory. Blank lines are skipped.
// fn returning false stops the scan early.
func scanLines(path string, fn func(line []byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxScanLineBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if !fn(line) {
			return nil
		}
	}
	return sc.Err()
}

// maxScanLineBytes bounds a single transcript line. Tool-result payloads can be
// large; anything beyond this is skipped by the scanner rather than parsed.
const maxScanLineBytes = 8 * 1024 * 1024

// parseTime parses a transcript timestamp in the RFC3339 forms both Claude Code
// (millisecond, Z) and Codex (microsecond) emit. It returns the zero time when
// the value is empty or unparseable.
func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}
