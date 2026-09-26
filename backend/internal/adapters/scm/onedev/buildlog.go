package onedev

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// buildLogTailLines is how many trailing log lines a failed check
	// contributes to an observation. It matches the GitHub and GitLab
	// adapters so the agent-facing failure context is the same size
	// whichever provider produced it.
	buildLogTailLines = 20

	// buildLogMaxFrameBytes rejects an implausible frame length. The stream is
	// length-prefixed, so a corrupt or non-OneDev response could otherwise ask
	// the reader to allocate gigabytes from four bytes of input.
	buildLogMaxFrameBytes = 1 << 20

	// buildLogMaxStreamBytes bounds how much of a log the reader consumes
	// before giving up on reaching the end. A finished build's log is
	// typically well under a megabyte; this is a backstop against a
	// pathological (or still-running, hence endless) stream, not a normal
	// operating limit.
	buildLogMaxStreamBytes = 32 << 20
)

// errBuildLogFraming reports that the response did not follow OneDev's
// length-prefixed build-log framing.
var errBuildLogFraming = errors.New("onedev scm: malformed build log stream")

// tailBuildLog decodes OneDev's streaming build-log format and returns the
// last maxLines rendered lines.
//
// GET /~api/streaming/build-logs/{buildId} does not return JSON or plain text.
// It returns a sequence of frames, each a big-endian int32 length followed by
// that many bytes:
//
//   - a positive length introduces one JSON log entry,
//     {"date":..., "messages":[{"style":..., "text":"..."}]}
//   - a negative length introduces a status marker of abs(n) bytes, the build
//     status as a bare string ("SUCCESSFUL", "FAILED"). One is emitted before
//     the first entry and one after the last.
//
// Verified against OneDev 16.5.6: a successful build's stream opens with
// 0xFFFFFFF6 ("SUCCESSFUL", 10 bytes) and a failed build's with 0xFFFFFFFA
// ("FAILED", 6 bytes).
//
// The decoder keeps only a maxLines ring buffer, so a large log costs bounded
// memory however long the stream runs. Frames are read in order — the tail is
// what survives in the ring — which is why the whole stream is consumed rather
// than a prefix: a prefix would yield the head of the log, which is the least
// useful part of a failure.
//
// A framing error after at least one decoded line returns the lines collected
// so far rather than nothing: a truncated tail is still actionable, and a
// still-writing build can legitimately cut a frame short.
func tailBuildLog(r io.Reader, maxLines int) (string, error) {
	if maxLines <= 0 {
		maxLines = buildLogTailLines
	}
	br := bufio.NewReader(r)
	ring := make([]string, 0, maxLines)
	appendLine := func(line string) {
		if len(ring) == maxLines {
			ring = append(ring[:0], ring[1:]...)
		}
		ring = append(ring, line)
	}

	var consumed int
	var header [4]byte
	for consumed < buildLogMaxStreamBytes {
		if _, err := io.ReadFull(br, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return finishTail(ring), readErr(err, len(ring))
		}
		consumed += len(header)

		n := int32(binary.BigEndian.Uint32(header[:])) //nolint:gosec // the wire format is a signed length
		if n < 0 {
			// Status marker: abs(n) bytes of a bare status string. It carries
			// no log content, so it is skipped rather than rendered.
			size := -int(n)
			if size > buildLogMaxFrameBytes {
				return finishTail(ring), framingErr(len(ring))
			}
			if _, err := io.CopyN(io.Discard, br, int64(size)); err != nil {
				return finishTail(ring), readErr(err, len(ring))
			}
			consumed += size
			continue
		}
		if n == 0 {
			continue
		}
		if int(n) > buildLogMaxFrameBytes {
			return finishTail(ring), framingErr(len(ring))
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return finishTail(ring), readErr(err, len(ring))
		}
		consumed += int(n)

		var entry buildLogEntry
		if err := json.Unmarshal(payload, &entry); err != nil {
			// One unparseable entry in an otherwise well-framed stream is not
			// worth discarding the rest of the log for; the frame length told
			// us exactly where the next entry starts.
			continue
		}
		appendLine(entry.line())
	}
	return finishTail(ring), nil
}

// readErr converts a mid-stream read failure into either a hard error or a
// tolerated truncation, depending on whether anything usable was decoded.
func readErr(err error, lines int) error {
	if lines > 0 {
		return nil
	}
	return fmt.Errorf("onedev scm: read build log: %w", err)
}

// framingErr reports a frame length the format cannot produce, tolerating it
// once a usable tail has already been decoded.
func framingErr(lines int) error {
	if lines > 0 {
		return nil
	}
	return errBuildLogFraming
}

func finishTail(lines []string) string {
	return strings.Join(lines, "\n")
}

// buildLogEntry is one decoded log frame. Only the rendered text is kept:
// OneDev carries per-message ANSI styling that would be noise in an agent's
// failure context.
type buildLogEntry struct {
	Date     string `json:"date"`
	Messages []struct {
		Text string `json:"text"`
	} `json:"messages"`
}

// line renders an entry's messages as a single log line.
func (e buildLogEntry) line() string {
	if len(e.Messages) == 1 {
		return e.Messages[0].Text
	}
	parts := make([]string, 0, len(e.Messages))
	for _, m := range e.Messages {
		parts = append(parts, m.Text)
	}
	return strings.Join(parts, "")
}
