// Package conpty implements the Windows ConPTY runtime adapter for agent sessions.
// This file contains the OS-agnostic binary framing protocol codec used by the
// named-pipe protocol between pty-host.js and this Go client.
//
// Frame layout: [1-byte type][4-byte big-endian length][payload]
package conpty

import (
	"encoding/binary"
	"fmt"
	"math"
)

const (
	conPTYHostProtocolVersion         = 2
	conPTYStyledOutputProtocolVersion = 2
)

// Message type constants. Values must match pty-host.ts MSG_* constants exactly.
const (
	MsgTerminalData       byte = 0x01 // host -> client: raw PTY output
	MsgTerminalInput      byte = 0x02 // client -> host: raw keystrokes
	MsgResize             byte = 0x03 // client -> host: JSON {cols, rows}
	MsgGetOutputReq       byte = 0x04 // client -> host: JSON {lines}
	MsgGetOutputRes       byte = 0x05 // host -> client: UTF-8 text
	MsgStatusReq          byte = 0x06 // client -> host: empty
	MsgStatusRes          byte = 0x07 // host -> client: JSON {alive, pid, exitCode?}
	MsgKillReq            byte = 0x08 // client -> host: empty
	MsgGetStyledOutputReq byte = 0x09 // client -> host: JSON {lines}
	MsgGetStyledOutputRes byte = 0x0a // host -> client: rendered UTF-8 text with ANSI styles
)

// JSON payload structs shared with later tasks (kept minimal).

// ResizePayload is the JSON body for MsgResize.
type ResizePayload struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// StatusPayload is the JSON body for MsgStatusRes.
type StatusPayload struct {
	Alive           bool `json:"alive"`
	PID             int  `json:"pid"`
	ExitCode        *int `json:"exitCode,omitempty"`
	ProtocolVersion int  `json:"protocolVersion,omitempty"`
}

// GetOutputReq is the JSON body for MsgGetOutputReq.
type GetOutputReq struct {
	Lines int `json:"lines"`
}

// EncodeMessage encodes a single frame into the binary protocol format.
// It allocates a fresh slice of exactly 5+len(payload) bytes.
// Returns an error if the payload exceeds the 4-byte length field capacity.
func EncodeMessage(msgType byte, payload []byte) ([]byte, error) {
	n := len(payload)
	if n > math.MaxUint32 {
		return nil, fmt.Errorf("conpty: payload too large (%d bytes, max %d)", n, math.MaxUint32)
	}
	payloadLen := uint32(n) // safe: n <= math.MaxUint32 checked above
	frame := make([]byte, 5+n)
	frame[0] = msgType
	binary.BigEndian.PutUint32(frame[1:5], payloadLen)
	copy(frame[5:], payload)
	return frame, nil
}

// MessageParser is a streaming parser for the binary framing protocol.
// It accumulates arbitrary-sized chunks from a pipe/socket stream and fires
// onMessage exactly once per complete frame, regardless of chunk boundaries.
// Safe to call Feed from a single goroutine; not concurrency-safe itself.
type MessageParser struct {
	buf       []byte
	onMessage func(msgType byte, payload []byte)
}

// NewMessageParser returns a parser that calls onMessage for each complete frame.
// onMessage receives a COPY of the payload so callers may retain it safely.
func NewMessageParser(onMessage func(msgType byte, payload []byte)) *MessageParser {
	return &MessageParser{onMessage: onMessage}
}

// Feed appends chunk to the internal buffer and dispatches all complete frames.
// It matches the semantics of MessageParser.feed in pty-host.ts exactly:
// arbitrary chunk boundaries and multiple frames per chunk are both handled.
func (p *MessageParser) Feed(chunk []byte) {
	p.buf = append(p.buf, chunk...)

	for len(p.buf) >= 5 {
		payloadLen := binary.BigEndian.Uint32(p.buf[1:5])
		frameLen := 5 + int(payloadLen)
		if len(p.buf) < frameLen {
			break
		}

		msgType := p.buf[0]
		// ponytail: explicit copy so callers that retain the slice are not
		// corrupted when p.buf grows/reallocates on a later Feed call.
		payload := make([]byte, payloadLen)
		copy(payload, p.buf[5:frameLen])

		p.buf = p.buf[frameLen:]
		p.onMessage(msgType, payload)
	}
}
