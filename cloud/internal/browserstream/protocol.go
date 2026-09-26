package browserstream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	Version                 = 1
	MaxControlBytes         = 32 << 10
	MaxFrameBytes           = 1 << 20
	MaxTargetID             = 128
	ViewerReplacedCloseCode = 4001

	frameHeaderBytes = 35
	frameKindJPEG    = 1
)

var frameMagic = [4]byte{'A', 'O', 'B', 'R'}

// Control is the allowlisted JSON protocol shared by the viewer, relay,
// worker, and browserd. Fields are interpreted only by the message types that
// own them; keeping one bounded envelope makes unknown-field rejection and
// version negotiation consistent at every hop.
type Control struct {
	Type         string  `json:"type"`
	Version      int     `json:"version"`
	StreamEpoch  uint64  `json:"streamEpoch,omitempty"`
	InputSeq     uint64  `json:"inputSeq,omitempty"`
	MinFrameSeq  uint64  `json:"minFrameSeq,omitempty"`
	Kind         string  `json:"kind,omitempty"`
	TargetID     string  `json:"targetId,omitempty"`
	URL          string  `json:"url,omitempty"`
	Title        string  `json:"title,omitempty"`
	Owner        string  `json:"owner,omitempty"`
	Code         string  `json:"code,omitempty"`
	Message      string  `json:"message,omitempty"`
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	X            float64 `json:"x,omitempty"`
	Y            float64 `json:"y,omitempty"`
	DeltaX       float64 `json:"deltaX,omitempty"`
	DeltaY       float64 `json:"deltaY,omitempty"`
	Button       string  `json:"button,omitempty"`
	Buttons      int     `json:"buttons,omitempty"`
	ClickCount   int     `json:"clickCount,omitempty"`
	Key          string  `json:"key,omitempty"`
	CodeValue    string  `json:"codeValue,omitempty"`
	Text         string  `json:"text,omitempty"`
	Modifiers    int     `json:"modifiers,omitempty"`
	TabID        string  `json:"tabId,omitempty"`
	Operation    string  `json:"operation,omitempty"`
	Accepted     bool    `json:"accepted,omitempty"`
	Running      bool    `json:"running,omitempty"`
	CanGoBack    bool    `json:"canGoBack,omitempty"`
	CanGoForward bool    `json:"canGoForward,omitempty"`
	IsLoading    bool    `json:"isLoading,omitempty"`
	DialogOpen   bool    `json:"dialogOpen,omitempty"`
	DialogType   string  `json:"dialogType,omitempty"`
	DialogText   string  `json:"dialogText,omitempty"`
	DialogPrompt string  `json:"dialogPrompt,omitempty"`
	Quality      int     `json:"quality,omitempty"`
	FPS          int     `json:"fps,omitempty"`
	ActiveTabID  string  `json:"activeTabId,omitempty"`
	Tabs         []Tab   `json:"tabs,omitempty"`

	DevToolsOpen      bool `json:"devtoolsOpen,omitempty"`
	DevToolsSupported bool `json:"devtoolsSupported,omitempty"`
}

type Tab struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Active  bool   `json:"active"`
	Favicon string `json:"favicon,omitempty"`
}

type Frame struct {
	StreamEpoch uint64
	Sequence    uint64
	Width       uint16
	Height      uint16
	CapturedMS  uint64
	TargetID    string
	JPEG        []byte
}

func EncodeFrame(frame Frame) ([]byte, error) {
	if frame.StreamEpoch == 0 || frame.Sequence == 0 {
		return nil, errors.New("browser frame epoch and sequence must be positive")
	}
	if frame.Width == 0 || frame.Height == 0 {
		return nil, errors.New("browser frame dimensions must be positive")
	}
	if len(frame.TargetID) == 0 || len(frame.TargetID) > MaxTargetID || !utf8.ValidString(frame.TargetID) {
		return nil, fmt.Errorf("browser frame target id must be 1 through %d bytes", MaxTargetID)
	}
	if len(frame.JPEG) == 0 || len(frame.JPEG) > MaxFrameBytes {
		return nil, fmt.Errorf("browser frame JPEG must be 1 through %d bytes", MaxFrameBytes)
	}
	out := make([]byte, frameHeaderBytes+len(frame.TargetID)+len(frame.JPEG))
	copy(out[:4], frameMagic[:])
	out[4] = Version
	out[5] = frameKindJPEG
	binary.BigEndian.PutUint64(out[6:14], frame.StreamEpoch)
	binary.BigEndian.PutUint64(out[14:22], frame.Sequence)
	binary.BigEndian.PutUint16(out[22:24], frame.Width)
	binary.BigEndian.PutUint16(out[24:26], frame.Height)
	binary.BigEndian.PutUint64(out[26:34], frame.CapturedMS)
	out[34] = byte(len(frame.TargetID))
	copy(out[35:], frame.TargetID)
	copy(out[35+len(frame.TargetID):], frame.JPEG)
	return out, nil
}

func DecodeFrame(payload []byte) (Frame, error) {
	if len(payload) < frameHeaderBytes+2 {
		return Frame{}, errors.New("browser frame is truncated")
	}
	if string(payload[:4]) != string(frameMagic[:]) {
		return Frame{}, errors.New("browser frame magic is invalid")
	}
	if payload[4] != Version {
		return Frame{}, fmt.Errorf("unsupported browser frame version %d", payload[4])
	}
	if payload[5] != frameKindJPEG {
		return Frame{}, fmt.Errorf("unsupported browser frame kind %d", payload[5])
	}
	targetBytes := int(payload[34])
	if targetBytes == 0 || targetBytes > MaxTargetID || len(payload) <= frameHeaderBytes+targetBytes {
		return Frame{}, errors.New("browser frame target or JPEG is invalid")
	}
	if !utf8.Valid(payload[35 : 35+targetBytes]) {
		return Frame{}, errors.New("browser frame target is not UTF-8")
	}
	jpegBytes := len(payload) - frameHeaderBytes - targetBytes
	if jpegBytes > MaxFrameBytes {
		return Frame{}, fmt.Errorf("browser frame exceeds %d bytes", MaxFrameBytes)
	}
	frame := Frame{
		StreamEpoch: binary.BigEndian.Uint64(payload[6:14]),
		Sequence:    binary.BigEndian.Uint64(payload[14:22]),
		Width:       binary.BigEndian.Uint16(payload[22:24]),
		Height:      binary.BigEndian.Uint16(payload[24:26]),
		CapturedMS:  binary.BigEndian.Uint64(payload[26:34]),
		TargetID:    string(payload[35 : 35+targetBytes]),
		JPEG:        append([]byte(nil), payload[35+targetBytes:]...),
	}
	if frame.StreamEpoch == 0 || frame.Sequence == 0 || frame.Width == 0 || frame.Height == 0 {
		return Frame{}, errors.New("browser frame contains zero-valued required fields")
	}
	return frame, nil
}
