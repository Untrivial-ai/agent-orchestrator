package iossimulator

import (
	"strings"
	"testing"
)

// bootedRunner answers simctl list devices with the device booted.
func bootedRunner(name string, args ...string) ([]byte, error) {
	if join(args) == "simctl list devices -j" {
		return []byte(`{"devices":{"runtime":[{"udid":"device-1","state":"Booted"}]}}`), nil
	}
	return nil, nil
}

func newBootedManager() *Manager {
	m := NewWithRunner(bootedRunner)
	m.device = Status{Available: true, DeviceID: "device-1", Name: "iPhone 15", State: "Booted"}
	return m
}

type recordedPoint struct {
	action string
	x, y   float64
	x2, y2 float64
	text   string
	code   int
}

func recordingPost(t *testing.T) (*Manager, *[]recordedPoint) {
	t.Helper()
	m := newBootedManager()
	var posts []recordedPoint
	m.post = func(input Input) error {
		posts = append(posts, recordedPoint{action: input.Action, x: input.X, y: input.Y, x2: input.X2, y2: input.Y2, text: input.Text, code: input.KeyCode})
		return nil
	}
	m.postPtr = func(action string, x, y, x2, y2 float64) error {
		posts = append(posts, recordedPoint{action: action, x: x, y: y, x2: x2, y2: y2})
		return nil
	}
	return m, &posts
}

func TestInputTapMapsFramebufferToWindow(t *testing.T) {
	m, posts := recordingPost(t)
	m.windowBounds = func() (windowBounds, error) { return windowBounds{X: 100, Y: 200, Width: 400, Height: 800}, nil }
	m.recordCaptureSize(200, 400)

	if err := m.Input(Input{Action: "tap", X: 100, Y: 200}); err != nil {
		t.Fatal(err)
	}
	got := (*posts)[0]
	// 100/200*400 + 100 => 300; 200/400*800 + 200 => 600
	if got.x != 300 || got.y != 600 {
		t.Fatalf("unexpected mapped tap: %v", got)
	}
}

func TestInputSwipeMapsBothEndpoints(t *testing.T) {
	m, posts := recordingPost(t)
	m.windowBounds = func() (windowBounds, error) { return windowBounds{X: 0, Y: 0, Width: 800, Height: 1600}, nil }
	m.recordCaptureSize(400, 800)

	if err := m.Input(Input{Action: "swipe", X: 0, Y: 0, X2: 400, Y2: 800}); err != nil {
		t.Fatal(err)
	}
	got := (*posts)[0]
	if got.x != 0 || got.y != 0 || got.x2 != 800 || got.y2 != 1600 {
		t.Fatalf("unexpected mapped swipe: %v", got)
	}
}

func TestInputRejectsOutsideFramebuffer(t *testing.T) {
	m, posts := recordingPost(t)
	m.windowBounds = func() (windowBounds, error) { return windowBounds{X: 0, Y: 0, Width: 400, Height: 800}, nil }
	m.recordCaptureSize(200, 400)

	err := m.Input(Input{Action: "tap", X: 250, Y: 10})
	if err == nil || !strings.Contains(err.Error(), "outside the simulator frame") {
		t.Fatalf("expected out-of-frame rejection, got %v", err)
	}
	if len(*posts) != 0 {
		t.Fatal("out-of-frame input must not be posted")
	}
}

func TestInputRequiresCaptureSize(t *testing.T) {
	m, posts := recordingPost(t)
	m.windowBounds = func() (windowBounds, error) { return windowBounds{X: 0, Y: 0, Width: 400, Height: 800}, nil }

	err := m.Input(Input{Action: "tap", X: 10, Y: 10})
	if err == nil || !strings.Contains(err.Error(), "capture size is unknown") {
		t.Fatalf("expected unknown-size error, got %v", err)
	}
	if len(*posts) != 0 {
		t.Fatal("input without capture size must not be posted")
	}
}

func TestInputRequiresBootedSimulator(t *testing.T) {
	m := NewWithRunner(func(name string, args ...string) ([]byte, error) {
		if join(args) == "simctl list devices -j" {
			return []byte(`{"devices":{"runtime":[{"udid":"device-1","state":"Shutdown"}]}}`), nil
		}
		return nil, nil
	})
	m.device = Status{Available: true, DeviceID: "device-1", State: "Shutdown"}
	if err := m.Input(Input{Action: "home"}); err == nil || !strings.Contains(err.Error(), "not booted") {
		t.Fatalf("expected not-booted error, got %v", err)
	}
}

func TestInputHomeRotateDispatch(t *testing.T) {
	m, posts := recordingPost(t)
	for _, action := range []string{"home", "rotateLeft", "rotateRight", "lock", "text", "key"} {
		input := Input{Action: action, Text: "hi", KeyCode: 36}
		if err := m.Input(input); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	got := *posts
	if len(got) != 6 {
		t.Fatalf("expected 6 posts, got %d", len(got))
	}
	if got[0].action != "home" || got[1].action != "rotateLeft" || got[2].action != "rotateRight" || got[3].action != "lock" {
		t.Fatalf("shortcut dispatch order wrong: %v", got)
	}
	if got[4].text != "hi" || got[5].code != 36 {
		t.Fatalf("text/key payloads not forwarded: %v", got)
	}
}

func TestInputDefaultsToFocusBeforeTextAndKey(t *testing.T) {
	// The real wiring wraps text/key in focusBefore so keys land in
	// Simulator.app; the injected post path must not change, but the same
	// actions must still reach it with their payloads intact.
	m, posts := recordingPost(t)
	for _, action := range []string{"text", "key"} {
		if err := m.Input(Input{Action: action, Text: "hello", KeyCode: 36}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	got := *posts
	if len(got) != 2 || got[0].text != "hello" || got[1].code != 36 {
		t.Fatalf("text/key payloads not forwarded: %v", got)
	}
}

func TestInputUnsupportedAction(t *testing.T) {
	m := newBootedManager()
	if err := m.Input(Input{Action: "nope"}); err == nil || !strings.Contains(err.Error(), "unsupported iOS input action") {
		t.Fatalf("expected unsupported action error, got %v", err)
	}
}
