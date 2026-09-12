package sessionimport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
)

// fileCache reuses parsing only while the underlying file is unchanged. The cap
// bounds daemon memory when a user visits many projects; eviction affects speed,
// never eligibility. Cancelled or failed reads are not cached.
type fileCache[T any] struct {
	mu      sync.Mutex
	entries map[string]fileEntry[T]
}
type fileEntry[T any] struct {
	info  os.FileInfo
	value T
}

func (c *fileCache[T]) get(path string, info os.FileInfo) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if ok && os.SameFile(e.info, info) && e.info.Size() == info.Size() && e.info.ModTime().Equal(info.ModTime()) {
		return e.value, true
	}
	var zero T
	return zero, false
}
func (c *fileCache[T]) put(path string, info os.FileInfo, value T) {
	// A transcript may be appended while scanning; never cache that mixed read.
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= 4096 {
		c.entries = make(map[string]fileEntry[T])
	}
	c.entries[path] = fileEntry[T]{info: info, value: value}
}

type usageCount struct {
	total    int64
	complete bool
}

// A lower bound is reusable only when it already proves the requested threshold.
func (u usageCount) sufficient(threshold int64) bool {
	return u.complete || (threshold > 0 && u.total >= threshold)
}

// scanUsage decodes only provider usage events, never tool output or conversation
// meaning. Claude may repeat a message's usage across streamed content blocks;
// take its maximum rather than charging the same message repeatedly. Codex
// reports cumulative totals (cached input is already included in input_tokens).
// A positive threshold permits an incomplete lower bound once eligible; the
// boolean reports whether the entire file was scanned. Zero requests exact usage.
func scanUsage(ctx context.Context, path string, codex bool, threshold int64) (int64, bool, error) {
	var total int64
	messages := map[string]int64{}
	visit := func(raw []byte) bool {
		if ctx.Err() != nil {
			return false
		}
		if codex {
			if !bytes.Contains(raw, []byte(`"token_count"`)) {
				return true
			}
			var e struct {
				Type    string `json:"type"`
				Payload struct {
					Type string `json:"type"`
					Info struct {
						Total struct {
							Total  int64 `json:"total_tokens"`
							Input  int64 `json:"input_tokens"`
							Output int64 `json:"output_tokens"`
						} `json:"total_token_usage"`
					} `json:"info"`
				} `json:"payload"`
			}
			if json.Unmarshal(raw, &e) != nil || e.Type != "event_msg" || e.Payload.Type != "token_count" {
				return true
			}
			u := e.Payload.Info.Total
			n := max(u.Total, max(0, u.Input)+max(0, u.Output))
			total = max(total, n)
			return threshold <= 0 || total < threshold
		}
		if !bytes.Contains(raw, []byte(`"usage"`)) {
			return true
		}
		var e struct {
			Type      string `json:"type"`
			Sidechain bool   `json:"isSidechain"`
			Message   struct {
				ID    string `json:"id"`
				Usage struct {
					Input    int64 `json:"input_tokens"`
					Output   int64 `json:"output_tokens"`
					Read     int64 `json:"cache_read_input_tokens"`
					Creation int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &e) != nil || e.Type != "assistant" || e.Sidechain || e.Message.ID == "" {
			return true
		}
		u := e.Message.Usage
		n := max(0, u.Input) + max(0, u.Output) + max(0, u.Read) + max(0, u.Creation)
		if n > messages[e.Message.ID] {
			total += n - messages[e.Message.ID]
			messages[e.Message.ID] = n
		}
		return threshold <= 0 || total < threshold
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	// Codex counters can decrease after context-window recovery. A tail counter
	// can prove eligibility, but a low tail never proves ineligibility: fall back
	// to streaming from the start. A zero threshold always requests a full scan.
	if codex && threshold > 0 {
		info, err := os.Stat(path)
		if err != nil {
			return 0, false, err
		}
		tail, err := tailBytes(path, info.Size(), defaultMaxScanBytes)
		if err != nil {
			return 0, false, err
		}
		if info.Size() > defaultMaxScanBytes {
			if i := bytes.IndexByte(tail, '\n'); i >= 0 {
				tail = tail[i+1:]
			} else {
				tail = nil
			}
		}
		for _, raw := range bytes.Split(tail, []byte{'\n'}) {
			if err := ctx.Err(); err != nil {
				return 0, false, err
			}
			if !visit(raw) {
				if err := ctx.Err(); err != nil {
					return 0, false, err
				}
				return total, false, nil
			}
		}
	}
	err := scanUsageLines(ctx, path, visit)
	if ctx.Err() != nil {
		return 0, false, ctx.Err()
	}
	return total, threshold <= 0 || total < threshold, err
}

// Large tool-result lines are irrelevant to usage. Skip them without abandoning
// later token counters or allocating an unbounded buffer. Check cancellation
// between chunks, including inside a huge line.
func scanUsageLines(ctx context.Context, path string, visit func([]byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 64*1024)
	var line []byte
	skipping := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		part, err := r.ReadSlice('\n')
		if !skipping {
			if len(line)+len(part) > maxScanLineBytes {
				line = nil
				skipping = true
			} else {
				line = append(line, part...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !skipping && len(line) > 0 && !visit(bytes.TrimSpace(line)) {
			return nil
		}
		line = line[:0]
		skipping = false
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
