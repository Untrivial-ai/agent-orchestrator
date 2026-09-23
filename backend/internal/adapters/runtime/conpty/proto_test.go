package conpty

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestEncodeMessage verifies the 5-byte header and payload are written correctly.
func TestEncodeMessage(t *testing.T) {
	payload := []byte("hello")
	frame, err := EncodeMessage(MsgTerminalData, payload)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}

	if len(frame) != 5+len(payload) {
		t.Fatalf("frame len = %d, want %d", len(frame), 5+len(payload))
	}
	if frame[0] != MsgTerminalData {
		t.Errorf("type byte = 0x%02x, want 0x%02x", frame[0], MsgTerminalData)
	}
	gotLen := binary.BigEndian.Uint32(frame[1:5])
	if int(gotLen) != len(payload) {
		t.Errorf("length field = %d, want %d", gotLen, len(payload))
	}
	if !bytes.Equal(frame[5:], payload) {
		t.Errorf("payload = %q, want %q", frame[5:], payload)
	}
}

// TestEncodeMessageZeroPayload verifies a zero-length payload encodes correctly.
func TestEncodeMessageZeroPayload(t *testing.T) {
	frame, err := EncodeMessage(MsgStatusReq, nil)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	if len(frame) != 5 {
		t.Fatalf("frame len = %d, want 5", len(frame))
	}
	if frame[0] != MsgStatusReq {
		t.Errorf("type byte = 0x%02x, want 0x%02x", frame[0], MsgStatusReq)
	}
	if got := binary.BigEndian.Uint32(frame[1:5]); got != 0 {
		t.Errorf("length field = %d, want 0", got)
	}
}

// collected accumulates (type, payload) pairs received by a MessageParser.
type collected struct {
	typ     byte
	payload []byte
}

func collect(frames *[]collected) func(byte, []byte) {
	return func(typ byte, payload []byte) {
		*frames = append(*frames, collected{typ, payload})
	}
}

// TestParserSingleFrame feeds one complete frame and expects one callback.
func TestParserSingleFrame(t *testing.T) {
	var got []collected
	p := NewMessageParser(collect(&got))

	f, _ := EncodeMessage(MsgTerminalData, []byte("hi"))
	p.Feed(f)

	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].typ != MsgTerminalData {
		t.Errorf("type = 0x%02x, want 0x%02x", got[0].typ, MsgTerminalData)
	}
	if !bytes.Equal(got[0].payload, []byte("hi")) {
		t.Errorf("payload = %q, want %q", got[0].payload, "hi")
	}
}

// TestParserTwoFramesOneChunk feeds two frames concatenated and expects two callbacks.
func TestParserTwoFramesOneChunk(t *testing.T) {
	var got []collected
	p := NewMessageParser(collect(&got))

	f1, _ := EncodeMessage(MsgTerminalData, []byte("frame1"))
	f2, _ := EncodeMessage(MsgTerminalInput, []byte("frame2"))
	chunk := append(f1, f2...)
	p.Feed(chunk)

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].typ != MsgTerminalData || string(got[0].payload) != "frame1" {
		t.Errorf("message 0 = {%02x, %q}", got[0].typ, got[0].payload)
	}
	if got[1].typ != MsgTerminalInput || string(got[1].payload) != "frame2" {
		t.Errorf("message 1 = {%02x, %q}", got[1].typ, got[1].payload)
	}
}

// TestParserByteAtATime feeds one frame one byte at a time and expects exactly
// one callback with the correct type and payload.
func TestParserByteAtATime(t *testing.T) {
	var got []collected
	p := NewMessageParser(collect(&got))

	frame, _ := EncodeMessage(MsgResize, []byte(`{"cols":80,"rows":24}`))
	for _, b := range frame {
		p.Feed([]byte{b})
	}

	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].typ != MsgResize {
		t.Errorf("type = 0x%02x, want 0x%02x", got[0].typ, MsgResize)
	}
	want := []byte(`{"cols":80,"rows":24}`)
	if !bytes.Equal(got[0].payload, want) {
		t.Errorf("payload = %q, want %q", got[0].payload, want)
	}
}

// TestParserInterleavedTypes feeds frames of different types and verifies order.
func TestParserInterleavedTypes(t *testing.T) {
	types := []byte{MsgStatusReq, MsgKillReq, MsgGetOutputReq, MsgStatusRes, MsgGetStyledOutputReq, MsgGetStyledOutputRes}
	payloads := [][]byte{
		nil,
		nil,
		[]byte(`{"lines":10}`),
		[]byte(`{"alive":true,"pid":42}`),
		[]byte(`{"lines":12}`),
		[]byte("\x1b[2mdim\x1b[0m"),
	}

	var chunk []byte
	for i, typ := range types {
		f, _ := EncodeMessage(typ, payloads[i])
		chunk = append(chunk, f...)
	}

	var got []collected
	p := NewMessageParser(collect(&got))
	p.Feed(chunk)

	if len(got) != len(types) {
		t.Fatalf("got %d messages, want %d", len(got), len(types))
	}
	for i, g := range got {
		if g.typ != types[i] {
			t.Errorf("[%d] type = 0x%02x, want 0x%02x", i, g.typ, types[i])
		}
		if !bytes.Equal(g.payload, payloads[i]) {
			t.Errorf("[%d] payload = %q, want %q", i, g.payload, payloads[i])
		}
	}
}

// TestParserPayloadIsCopy verifies that the payload delivered to onMessage is a
// true copy, not a subslice of the parser's internal buffer. It exercises the
// aliasing path that matters in practice: feed frame1, capture its payload, then
// feed frame2 of the SAME length so the parser reuses the same buffer region;
// frame1's captured bytes must be unchanged. This catches a regression where
// payload was a raw subslice of p.buf instead of a make+copy.
func TestParserPayloadIsCopy(t *testing.T) {
	var got []collected
	p := NewMessageParser(collect(&got))

	// Feed frame1 and capture the delivered payload pointer.
	frame1, _ := EncodeMessage(MsgTerminalData, []byte("original"))
	p.Feed(frame1)
	if len(got) != 1 {
		t.Fatalf("after frame1: got %d messages, want 1", len(got))
	}
	captured := got[0].payload

	// Feed frame2 with the same payload length so the parser's internal buffer
	// overwrites the exact byte range that frame1 occupied.
	frame2, _ := EncodeMessage(MsgTerminalInput, []byte("XXXXXXXX")) // same len as "original"
	p.Feed(frame2)
	if len(got) != 2 {
		t.Fatalf("after frame2: got %d messages, want 2", len(got))
	}

	// frame1's captured payload must be unaffected by the subsequent Feed.
	if !bytes.Equal(captured, []byte("original")) {
		t.Errorf("frame1 payload aliased internal buffer: got %q after frame2", captured)
	}
}

// TestParserZeroLengthFrame verifies a zero-payload frame (e.g. MsgStatusReq) parses.
func TestParserZeroLengthFrame(t *testing.T) {
	var got []collected
	p := NewMessageParser(collect(&got))
	f, _ := EncodeMessage(MsgStatusReq, nil)
	p.Feed(f)

	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].typ != MsgStatusReq {
		t.Errorf("type = 0x%02x, want 0x%02x", got[0].typ, MsgStatusReq)
	}
	if len(got[0].payload) != 0 {
		t.Errorf("payload len = %d, want 0", len(got[0].payload))
	}
}
