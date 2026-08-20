package iossimulator

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// maxFrameBytes caps one capture frame at 32 MiB. Real Retina device frames are
// well under this; the cap turns a corrupt length prefix into a fast restart
// instead of a multi-gigabyte allocation.
const maxFrameBytes = 32 << 20

// Frame is one captured PNG frame and its pixel dimensions. Width/Height are
// the device framebuffer size (what input coordinates are expressed in), not
// the CSS size of the panel.
type Frame struct {
	Data   []byte
	Width  int
	Height int
	Codec  string
}

// liveSource is the per-run subscriber set owned by one supervisor loop. The
// loop closes every channel in this set when it exits so subscribers observe a
// clean end-of-stream; frame delivery itself is a non-blocking drop for slow
// subscribers (the panel always renders the newest frame).
type liveSource struct {
	subscribers map[chan Frame]struct{}
	cancel      context.CancelFunc
}

// FrameSource runs exactly one ScreenCaptureKit helper process (in --stream
// mode) shared by every subscriber, restarting it with exponential backoff when
// it exits. This is the "one authoritative simulator capture" the panel builds
// on: the daemon never spawns a capture process per frame.
type FrameSource struct {
	mu sync.Mutex
	// helper is the ao-ios-capture binary path ("" means unavailable).
	helper string
	// spawn starts the capture helper. Injectable for tests.
	spawn func(ctx context.Context, helper string) (io.ReadCloser, func(), error)
	// baseDelay/maxDelay bound the exponential restart backoff.
	baseDelay time.Duration
	maxDelay  time.Duration
	codec     string
	// live is non-nil while a supervisor loop is running.
	live *liveSource
	// width/height are the most recently captured framebuffer dimensions.
	width, height int
	// lastErr is the most recent capture error, surfaced for diagnostics.
	lastErr error
}

// NewFrameSource builds a frame source for the given helper path. A zero
// helper returns frames never (Size stays 0), matching an unavailable helper.
func NewFrameSource(helper string) *FrameSource {
	return &FrameSource{
		helper:    helper,
		spawn:     spawnCaptureHelper,
		baseDelay: 250 * time.Millisecond,
		maxDelay:  4 * time.Second,
		codec:     "png",
	}
}

// NewH264FrameSource selects the native VideoToolbox/Annex-B helper. The PNG
// constructor remains available as a deterministic fallback for CI and older
// installations.
func NewH264FrameSource(helper string) *FrameSource {
	source := NewFrameSource(helper)
	if os.Getenv("AO_IOS_CAPTURE_CODEC") != "png" {
		source.codec = "h264"
	}
	return source
}

// captureHelperPath resolves the ao-ios-capture binary: $AO_IOS_CAPTURE_HELPER
// wins (development/package override), otherwise the binary next to the daemon.
func captureHelperPath() string {
	if helper := os.Getenv("AO_IOS_CAPTURE_HELPER"); helper != "" {
		return helper
	}
	if executable, err := os.Executable(); err == nil {
		if helper := filepath.Join(filepath.Dir(executable), "ao-sim-helper.app", "Contents", "MacOS", "ao-sim-helper"); fileExists(helper) {
			return helper
		}
		if helper := filepath.Join(filepath.Dir(executable), "ao-ios-capture"); fileExists(helper) {
			return helper
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// spawnCaptureHelper starts the helper in --stream mode and returns its stdout
// plus a cleanup func that waits for exit (CommandContext already killed it on
// context cancel).
func spawnCaptureHelper(ctx context.Context, helper string) (io.ReadCloser, func(), error) {
	args := []string{"--stream"}
	if os.Getenv("AO_IOS_CAPTURE_CODEC") != "png" {
		args = append(args, "--h264")
	}
	cmd := exec.CommandContext(ctx, helper, args...) // #nosec G204 -- helper is an explicit local AO configuration.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return stdout, func() { _ = cmd.Wait() }, nil
}

// Subscribe registers a frame channel with the shared capture process. When
// the last subscriber unsubscribes the helper process is torn down; the next
// subscriber starts a fresh one. The returned channel is closed by the source
// when the subscriber detaches or the capture is stopped.
func (s *FrameSource) Subscribe() (<-chan Frame, func()) {
	s.mu.Lock()
	if s.live == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.live = &liveSource{subscribers: map[chan Frame]struct{}{}, cancel: cancel}
		go s.supervise(ctx, s.live)
	}
	ch := make(chan Frame, 8)
	s.live.subscribers[ch] = struct{}{}
	live := s.live
	s.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			// The supervisor may already have removed and closed this channel
			// when the source stopped; detach rather than double-close. delete
			// is a no-op for a missing key or a nil map.
			delete(live.subscribers, ch)
			if len(live.subscribers) == 0 && s.live == live {
				live.cancel()
				s.live = nil
			}
		})
	}
	return ch, unsubscribe
}

// Size returns the most recently captured framebuffer dimensions, or 0,0 when
// nothing has been captured yet.
func (s *FrameSource) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.width, s.height
}

// LastError returns the most recent capture error, if any.
func (s *FrameSource) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *FrameSource) supervise(ctx context.Context, live *liveSource) {
	attempt := 0
	for {
		reader, cleanup, spawnErr := s.spawn(ctx, s.helper)
		if spawnErr == nil {
			readErr := s.readLoop(ctx, reader, live)
			_ = reader.Close()
			cleanup()
			if ctx.Err() != nil {
				break
			}
			s.mu.Lock()
			s.lastErr = readErr
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.lastErr = spawnErr
			s.mu.Unlock()
		}
		attempt++
		if !sleepWithBackoff(ctx, s.backoffDelay(attempt)) {
			break
		}
	}
	// The source was torn down (or the helper never became spawnable): close
	// every subscriber channel so consumers observe clean EOF.
	s.mu.Lock()
	for ch := range live.subscribers {
		close(ch)
	}
	if s.live == live {
		s.live = nil
	}
	s.mu.Unlock()
}

// readLoop reads length-prefixed PNG frames from the helper and fans them out
// to every subscriber. It returns the first I/O error — the supervisor treats
// any exit as restartable.
func (s *FrameSource) readLoop(ctx context.Context, reader io.Reader, live *liveSource) error {
	if s.codec == "h264" {
		return s.readH264Loop(ctx, reader, live)
	}
	var header [4]byte
	for {
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(header[:])
		if size == 0 || size > maxFrameBytes {
			return fmt.Errorf("implausible capture frame size %d", size)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return err
		}
		frame := Frame{Data: data, Codec: "png"}
		if config, err := png.DecodeConfig(bytes.NewReader(data)); err == nil {
			frame.Width = config.Width
			frame.Height = config.Height
		}

		s.mu.Lock()
		if frame.Width > 0 && frame.Height > 0 {
			s.width, s.height = frame.Width, frame.Height
		}
		for ch := range live.subscribers {
			select {
			case ch <- frame:
			default:
				// Slow subscriber: drop rather than stall capture. The panel
				// renders the newest frame it has, so a drop is invisible.
			}
		}
		s.mu.Unlock()

		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// readH264Loop consumes uint32 payload length, uint32 width, uint32 height,
// followed by one Annex-B H.264 access unit. Dimensions are repeated so
// rotation can be handled without reconnecting the viewer.
func (s *FrameSource) readH264Loop(ctx context.Context, reader io.Reader, live *liveSource) error {
	var header [12]byte
	for {
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(header[:4])
		width := binary.BigEndian.Uint32(header[4:8])
		height := binary.BigEndian.Uint32(header[8:12])
		if size == 0 || size > maxFrameBytes {
			return fmt.Errorf("implausible h264 frame size %d", size)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return err
		}
		frame := Frame{Data: data, Width: int(width), Height: int(height), Codec: "h264"}
		s.mu.Lock()
		if frame.Width > 0 && frame.Height > 0 {
			s.width, s.height = frame.Width, frame.Height
		}
		for ch := range live.subscribers {
			select {
			case ch <- frame:
			default:
			}
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (s *FrameSource) backoffDelay(attempt int) time.Duration {
	level := attempt
	if level > 5 {
		level = 5
	}
	delay := s.baseDelay << level
	if delay <= 0 {
		delay = s.baseDelay
	}
	if s.maxDelay > 0 && delay > s.maxDelay {
		delay = s.maxDelay
	}
	return delay
}

func sleepWithBackoff(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
