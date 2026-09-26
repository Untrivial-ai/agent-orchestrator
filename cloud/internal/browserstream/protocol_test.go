package browserstream

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	want := Frame{
		StreamEpoch: 3, Sequence: 9, Width: 1280, Height: 720,
		CapturedMS: 44, TargetID: "page-1", JPEG: []byte{0xff, 0xd8, 0xff, 0xd9},
	}
	encoded, err := EncodeFrame(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.StreamEpoch != want.StreamEpoch || got.Sequence != want.Sequence ||
		got.Width != want.Width || got.Height != want.Height || got.CapturedMS != want.CapturedMS ||
		got.TargetID != want.TargetID || !bytes.Equal(got.JPEG, want.JPEG) {
		t.Fatalf("decoded frame = %#v, want %#v", got, want)
	}
}

func TestFrameRejectsMalformedPayloads(t *testing.T) {
	valid, err := EncodeFrame(Frame{
		StreamEpoch: 1, Sequence: 1, Width: 1, Height: 1,
		TargetID: "page", JPEG: []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"truncated": valid[:10],
		"magic":     append([]byte("NOPE"), valid[4:]...),
		"version":   append([]byte(nil), valid...),
		"kind":      append([]byte(nil), valid...),
		"target":    append([]byte(nil), valid...),
		"utf8":      append([]byte(nil), valid...),
	}
	cases["version"][4] = 99
	cases["kind"][5] = 99
	cases["target"][34] = 0
	cases["utf8"][35] = 0xff
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeFrame(payload); err == nil {
				t.Fatal("expected malformed frame to fail")
			}
		})
	}
}

func TestFrameEncodeEnforcesRequiredFieldsAndLimits(t *testing.T) {
	base := Frame{
		StreamEpoch: 1, Sequence: 1, Width: 1, Height: 1,
		TargetID: "page", JPEG: []byte{1},
	}
	tests := map[string]Frame{
		"zero epoch":    func() Frame { next := base; next.StreamEpoch = 0; return next }(),
		"zero sequence": func() Frame { next := base; next.Sequence = 0; return next }(),
		"zero width":    func() Frame { next := base; next.Width = 0; return next }(),
		"empty target":  func() Frame { next := base; next.TargetID = ""; return next }(),
		"invalid utf8":  func() Frame { next := base; next.TargetID = string([]byte{0xff}); return next }(),
		"large frame":   func() Frame { next := base; next.JPEG = make([]byte, MaxFrameBytes+1); return next }(),
	}
	for name, frame := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := EncodeFrame(frame); err == nil {
				t.Fatal("expected invalid frame to fail")
			}
		})
	}
}

func TestLatestFrameReplacesUnsentFrame(t *testing.T) {
	queue := NewLatest()
	defer queue.Close()
	if queue.Put([]byte("one")) {
		t.Fatal("first frame must not report a replacement")
	}
	if !queue.Put([]byte("two")) {
		t.Fatal("second frame must replace the unsent first frame")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := queue.Next(ctx)
	if !ok || string(got) != "two" {
		t.Fatalf("got %q, %v, want latest frame", got, ok)
	}
}
