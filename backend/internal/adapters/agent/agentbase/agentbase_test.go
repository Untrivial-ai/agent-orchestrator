package agentbase

import (
	"bufio"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestModelConfigSpecHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ModelConfigSpec(ctx, "model"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ModelConfigSpec error = %v, want context canceled", err)
	}
}

func TestModelConfigSpecAndFlag(t *testing.T) {
	spec, err := ModelConfigSpec(context.Background(), "Model override.")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" || spec.Fields[0].Description != "Model override." {
		t.Fatalf("spec = %#v", spec)
	}

	cmd := []string{"agent"}
	AppendModelFlag(&cmd, ports.AgentConfig{Model: "  provider/model  "}, "--model")
	if want := []string{"agent", "--model", "provider/model"}; !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %q, want %q", cmd, want)
	}
	AppendModelFlag(&cmd, ports.AgentConfig{Model: "  "}, "--model")
	if len(cmd) != 3 {
		t.Fatalf("blank model changed cmd: %q", cmd)
	}
}

func TestReadTranscriptLineReadsLinesUntilEOF(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("first\nsecond\nthird\n"))
	var got []string
	for {
		line, ok, err := ReadTranscriptLine(r)
		if err != nil {
			t.Fatalf("ReadTranscriptLine: %v", err)
		}
		if !ok {
			break
		}
		got = append(got, line)
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func TestReadTranscriptLineTrimsAndSkipsBlankLines(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("  padded  \n\n   \nplain\n"))
	var got []string
	for {
		line, ok, err := ReadTranscriptLine(r)
		if err != nil {
			t.Fatalf("ReadTranscriptLine: %v", err)
		}
		if !ok {
			break
		}
		got = append(got, line)
	}
	if want := []string{"padded", "plain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func TestReadTranscriptLineEmptyInput(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(""))
	line, ok, err := ReadTranscriptLine(r)
	if err != nil {
		t.Fatalf("ReadTranscriptLine: %v", err)
	}
	if ok || line != "" {
		t.Fatalf("second = (%q, %v), want (\"\", false)", line, ok)
	}
}

// TestReadTranscriptLineSkipsOversizedLine verifies the oversized-line guard:
// a single line over MaxTranscriptLineBytes is drained (not buffered) so the
// reader keeps making progress, and subsequent lines are still returned.
func TestReadTranscriptLineSkipsOversizedLine(t *testing.T) {
	oversized := strings.Repeat("x", MaxTranscriptLineBytes+1024)
	input := "small-before\n" + oversized + "\n" + "small-after\n"
	r := bufio.NewReader(strings.NewReader(input))

	var got []string
	for {
		line, ok, err := ReadTranscriptLine(r)
		if err != nil {
			t.Fatalf("ReadTranscriptLine: %v", err)
		}
		if !ok {
			break
		}
		got = append(got, line)
	}
	if want := []string{"small-before", "small-after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %d entries (oversized %q...), want %q", len(got), oversized[:8], want)
	}
}

// TestReadTranscriptLineOversizedLineAtEOF verifies an oversized line with no
// trailing newline is also dropped and the stream terminates cleanly.
func TestReadTranscriptLineOversizedLineAtEOF(t *testing.T) {
	oversized := strings.Repeat("y", MaxTranscriptLineBytes+1)
	r := bufio.NewReader(strings.NewReader(oversized))

	line, ok, err := ReadTranscriptLine(r)
	if err != nil {
		t.Fatalf("ReadTranscriptLine: %v", err)
	}
	if ok || line != "" {
		t.Fatalf("got (%q, %v), want (\"\", false) — oversized tail line dropped", line, ok)
	}
}

// TestReadTranscriptLineMultipleOversizedLines verifies many oversized lines
// are all drained without stalling on the buffer.
func TestReadTranscriptLineMultipleOversizedLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5; i++ {
		sb.WriteString(strings.Repeat("z", MaxTranscriptLineBytes+64))
		sb.WriteByte('\n')
	}
	sb.WriteString("survivor\n")
	r := bufio.NewReader(strings.NewReader(sb.String()))

	var got []string
	for {
		line, ok, err := ReadTranscriptLine(r)
		if err != nil {
			t.Fatalf("ReadTranscriptLine: %v", err)
		}
		if !ok {
			break
		}
		got = append(got, line)
	}
	if want := []string{"survivor"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}
