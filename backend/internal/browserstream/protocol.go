// Package browserstream carries live browser frames and remote input between
// the loopback daemon and the Electron process that owns AO's WebContentsViews.
package browserstream

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

const (
	// ProtocolVersion identifies the private daemon-to-Electron wire format.
	ProtocolVersion = 1
	// RuntimeAddressEnv carries the private listener address to Electron.
	RuntimeAddressEnv = "AO_BROWSER_STREAM_ADDRESS"
	// KindControl identifies a JSON control packet.
	KindControl byte = 1
	// KindJPEG identifies a binary JPEG frame packet.
	KindJPEG byte = 2

	// MaxControlBytes bounds one JSON control packet.
	MaxControlBytes = 64 << 10
	// MaxFrameBytes bounds one encoded browser frame.
	MaxFrameBytes   = 2 << 20
	maxPacketBytes  = MaxFrameBytes + 64
	frameHeaderSize = 25 // kind + stream id + seq + captured ms + width + height
)

// Control is the versioned JSON envelope exchanged on the private stream link.
type Control struct {
	Type      string          `json:"type"`
	Version   int             `json:"version,omitempty"`
	Token     string          `json:"token,omitempty"`
	StreamID  uint32          `json:"streamId,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Code      string          `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
}

// Frame is one encoded browser image. Data is immutable after publication.
type Frame struct {
	StreamID uint32
	Sequence uint64
	Captured time.Time
	Width    uint16
	Height   uint16
	JPEG     []byte
}

func writeControl(w io.Writer, c Control) error {
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(body) > MaxControlBytes {
		return fmt.Errorf("browser stream control exceeds %d bytes", MaxControlBytes)
	}
	packet := append([]byte{KindControl}, body...)
	return writePacket(w, packet)
}

func writeFrame(w io.Writer, frame Frame) error {
	if len(frame.JPEG) == 0 || len(frame.JPEG) > MaxFrameBytes {
		return fmt.Errorf("browser stream frame size %d is invalid", len(frame.JPEG))
	}
	capturedMillis := frame.Captured.UnixMilli()
	if capturedMillis < 0 {
		return errors.New("browser stream frame timestamp is invalid")
	}
	packet := make([]byte, frameHeaderSize+len(frame.JPEG))
	packet[0] = KindJPEG
	binary.BigEndian.PutUint32(packet[1:5], frame.StreamID)
	binary.BigEndian.PutUint64(packet[5:13], frame.Sequence)
	binary.BigEndian.PutUint64(packet[13:21], uint64(capturedMillis))
	binary.BigEndian.PutUint16(packet[21:23], frame.Width)
	binary.BigEndian.PutUint16(packet[23:25], frame.Height)
	copy(packet[25:], frame.JPEG)
	return writePacket(w, packet)
}

func writePacket(w io.Writer, packet []byte) error {
	if len(packet) == 0 || len(packet) > maxPacketBytes {
		return errors.New("invalid browser stream packet size")
	}
	if uint64(len(packet)) > uint64(math.MaxUint32) {
		return errors.New("browser stream packet exceeds wire size")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(packet))) //nolint:gosec // Guarded above by MaxUint32.
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err := w.Write(packet)
	return err
}

func readPacket(r io.Reader) (byte, []byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > maxPacketBytes {
		return 0, nil, fmt.Errorf("invalid browser stream packet size %d", size)
	}
	packet := make([]byte, int(size))
	if _, err := io.ReadFull(r, packet); err != nil {
		return 0, nil, err
	}
	return packet[0], packet[1:], nil
}

func decodeControl(body []byte) (Control, error) {
	if len(body) == 0 || len(body) > MaxControlBytes {
		return Control{}, errors.New("invalid browser stream control size")
	}
	var control Control
	if err := json.Unmarshal(body, &control); err != nil {
		return Control{}, err
	}
	return control, nil
}

func decodeFrame(body []byte) (Frame, error) {
	// readPacket strips the kind byte, leaving 24 bytes of metadata.
	if len(body) < frameHeaderSize-1 {
		return Frame{}, errors.New("browser stream frame header is truncated")
	}
	jpeg := body[24:]
	if len(jpeg) == 0 || len(jpeg) > MaxFrameBytes {
		return Frame{}, errors.New("browser stream frame payload is invalid")
	}
	capturedMillis := binary.BigEndian.Uint64(body[12:20])
	if capturedMillis > math.MaxInt64 {
		return Frame{}, errors.New("browser stream frame timestamp is invalid")
	}
	return Frame{
		StreamID: binary.BigEndian.Uint32(body[0:4]),
		Sequence: binary.BigEndian.Uint64(body[4:12]),
		Captured: time.UnixMilli(int64(capturedMillis)).UTC(),
		Width:    binary.BigEndian.Uint16(body[20:22]),
		Height:   binary.BigEndian.Uint16(body[22:24]),
		JPEG:     append([]byte(nil), jpeg...),
	}, nil
}
