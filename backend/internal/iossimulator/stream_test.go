package iossimulator

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/png"
	"io"
	"sync"
	"testing"
	"time"
)

// fakePNG is a real 1-bit PNG with the requested dimensions, produced via
// png.Encode so png.DecodeConfig in the frame source recognizes it.
func fakePNG(width, height int) []byte {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// waitWriters blocks until the fake spawn created at least n pipes.
func waitWriters(t *testing.T, mu *sync.Mutex, writers *[]*io.PipeWriter, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		count := len(*writers)
		mu.Unlock()
		if count >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("capture helper was never spawned (have %d writers)", count)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func frameWith(png []byte) []byte {
	out := make([]byte, 4+len(png))
	binary.BigEndian.PutUint32(out, uint32(len(png)))
	copy(out[4:], png)
	return out
}

func h264Frame(width, height uint32, payload []byte) []byte {
	out := make([]byte, 12+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(out[4:8], width)
	binary.BigEndian.PutUint32(out[8:12], height)
	copy(out[12:], payload)
	return out
}

func TestH264FrameSourceFansOutAnnexBFrames(t *testing.T) {
	src := NewH264FrameSource("/unused")
	src.codec = "h264"
	var writer *io.PipeWriter
	var mu sync.Mutex
	src.spawn = func(ctx context.Context, helper string) (io.ReadCloser, func(), error) {
		reader, nextWriter := io.Pipe()
		mu.Lock()
		writer = nextWriter
		mu.Unlock()
		return reader, func() { _ = nextWriter.Close() }, nil
	}
	frames, unsubscribe := src.Subscribe()
	defer unsubscribe()
	wait := time.After(2 * time.Second)
	var current *io.PipeWriter
	for {
		mu.Lock()
		current = writer
		mu.Unlock()
		if current != nil {
			break
		}
		select {
		case <-wait:
			t.Fatal("capture helper did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	payload := []byte{0, 0, 0, 1, 9}
	if _, err := current.Write(h264Frame(393, 852, payload)); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		if frame.Codec != "h264" || frame.Width != 393 || frame.Height != 852 || string(frame.Data) != string(payload) {
			t.Fatalf("unexpected H264 frame: %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for H264 frame")
	}
}

func TestFrameSourceFansOutFramesAndRecordsSize(t *testing.T) {
	src := NewFrameSource("/unused")
	var pipes []*io.PipeWriter
	var mu sync.Mutex
	src.spawn = func(ctx context.Context, helper string) (io.ReadCloser, func(), error) {
		pr, pw := io.Pipe()
		mu.Lock()
		pipes = append(pipes, pw)
		mu.Unlock()
		return pr, func() { _ = pw.Close() }, nil
	}

	ch, unsubscribe := src.Subscribe()
	defer unsubscribe()
	// Second subscriber sees the same frames (single capture process).
	ch2, unsubscribe2 := src.Subscribe()
	defer unsubscribe2()

	waitWriters(t, &mu, &pipes, 1)
	pw := pipes[0]
	png := fakePNG(393, 852)
	if _, err := pw.Write(frameWith(png)); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for range 2 {
		select {
		case frame := <-ch:
			if !bytes.Equal(frame.Data, png) {
				t.Fatalf("frame data mismatch: %d vs %d bytes", len(frame.Data), len(png))
			}
			if frame.Width != 393 || frame.Height != 852 {
				t.Fatalf("unexpected frame size: %dx%d", frame.Width, frame.Height)
			}
		case frame := <-ch2:
			if frame.Width != 393 || frame.Height != 852 {
				t.Fatalf("unexpected frame size on second subscriber: %dx%d", frame.Width, frame.Height)
			}
		case <-deadline:
			t.Fatal("timed out waiting for frames")
		}
	}

	width, height := src.Size()
	if width != 393 || height != 852 {
		t.Fatalf("capture size not recorded: %dx%d", width, height)
	}
}

func TestFrameSourceRestartsAfterHelperExit(t *testing.T) {
	src := NewFrameSource("/unused")
	src.baseDelay = time.Millisecond
	src.maxDelay = 5 * time.Millisecond
	var spawns int
	var mu sync.Mutex
	var writers []*io.PipeWriter
	src.spawn = func(ctx context.Context, helper string) (io.ReadCloser, func(), error) {
		mu.Lock()
		defer mu.Unlock()
		spawns++
		pr, pw := io.Pipe()
		writers = append(writers, pw)
		return pr, func() { _ = pw.Close() }, nil
	}

	ch, unsubscribe := src.Subscribe()
	defer unsubscribe()

	waitWriters(t, &mu, &writers, 1)
	// First helper writes a frame then dies (pipe closed → EOF → restart).
	mu.Lock()
	_, _ = writers[0].Write(frameWith(fakePNG(200, 400)))
	mu.Unlock()
	// Give the loop time to consume the frame; then kill the helper.
	deadline := time.After(1 * time.Second)
	for {
		width, height := src.Size()
		if width == 200 && height == 400 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first frame never arrived")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	mu.Lock()
	_ = writers[0].Close()
	mu.Unlock()

	// A respawn must happen, and the new helper's frames must flow.
	waitSpawns := func(want int) bool {
		deadline := time.After(2 * time.Second)
		for {
			mu.Lock()
			count := spawns
			mu.Unlock()
			if count >= want {
				return true
			}
			select {
			case <-deadline:
				return false
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
	if !waitSpawns(2) {
		t.Fatal("helper was not restarted after exit")
	}
	// Drain any stale pre-restart frames still buffered on the subscriber
	// channel so the post-restart assertion sees only fresh frames.
	for {
		select {
		case <-ch:
		default:
			goto drained
		}
	}
drained:
	mu.Lock()
	_, _ = writers[1].Write(frameWith(fakePNG(300, 600)))
	mu.Unlock()
	select {
	case frame := <-ch:
		if frame.Width != 300 || frame.Height != 600 {
			t.Fatalf("post-restart frame size: %dx%d", frame.Width, frame.Height)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frames after restart")
	}
}

func TestFrameSourceTearsDownOnLastUnsubscribe(t *testing.T) {
	src := NewFrameSource("/unused")
	src.baseDelay = time.Millisecond
	src.maxDelay = 5 * time.Millisecond
	var spawns int
	var mu sync.Mutex
	src.spawn = func(ctx context.Context, helper string) (io.ReadCloser, func(), error) {
		mu.Lock()
		defer mu.Unlock()
		spawns++
		pr, pw := io.Pipe()
		return pr, func() { _ = pw.Close() }, nil
	}

	waitSpawns := func(want int) bool {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			mu.Lock()
			count := spawns
			mu.Unlock()
			if count >= want {
				return true
			}
			select {
			case <-deadline:
				return false
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}

	// First subscriber: capture process starts.
	_, unsubscribe := src.Subscribe()
	if !waitSpawns(1) {
		t.Fatal("first subscription never started capture")
	}
	// Last subscriber detaches: the capture process is torn down.
	unsubscribe()
	// Second subscriber must start a fresh capture process (no shared process
	// leak that would keep the old helper alive, and no reuse of the stopped one).
	_, unsubscribe2 := src.Subscribe()
	if !waitSpawns(2) {
		t.Fatal("second subscription did not start a fresh capture")
	}
	unsubscribe2()
}

func TestFrameSourceNoHelperStillSubscribes(t *testing.T) {
	src := NewFrameSource("") // helper unavailable
	src.baseDelay = time.Millisecond
	src.maxDelay = 5 * time.Millisecond
	ch, unsubscribe := src.Subscribe()
	defer unsubscribe()
	width, height := src.Size()
	if width != 0 || height != 0 {
		t.Fatalf("unexpected size without helper: %dx%d", width, height)
	}
	// Channel stays open (supervisor retrying) until the source is torn down:
	// this matches callers that keep a live WS open while the helper is absent.
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while capture should keep retrying")
		}
	case <-time.After(20 * time.Millisecond):
	}
}
