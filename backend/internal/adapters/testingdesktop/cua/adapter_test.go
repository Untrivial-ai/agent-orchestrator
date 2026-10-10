package cua

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type invocation struct {
	executable string
	args, env  []string
}

type fakeRunner struct {
	calls []invocation
	hook  func(string, []string) (Output, error)
}

func (f *fakeRunner) Run(_ context.Context, executable string, args, env []string) (Output, error) {
	f.calls = append(f.calls, invocation{executable: executable, args: append([]string(nil), args...), env: append([]string(nil), env...)})
	return f.hook(executable, args)
}

func jsonOutput(value any) Output {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return Output{Stdout: data}
}

func shortRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ao-cua-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

type fixture struct {
	adapter       *Adapter
	runner        *fakeRunner
	target        domain.TestTargetIdentity
	bounds        domain.TestWindowBounds
	born          time.Time
	changed       bool
	foreign       bool
	hidden        bool
	providerError bool
	elements      []capturedElement
	width, height int
	captureData   []byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{born: time.Date(2026, 10, 6, 1, 2, 3, 456000, time.UTC), bounds: domain.TestWindowBounds{X: 70, Y: 90, Width: 640, Height: 400}, width: 1280, height: 800}
	f.target = domain.TestTargetIdentity{ID: "target", LaunchID: "launch", Generation: 1, ElectronPID: 123, ElectronStartedAt: f.born, DataDir: "/scratch/target"}
	f.runner = &fakeRunner{}
	f.runner.hook = func(_ string, args []string) (Output, error) {
		if len(args) < 5 || args[2] != "call" {
			t.Fatalf("unexpected command %v", args)
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(args[4]), &input); err != nil {
			t.Fatal(err)
		}
		switch args[3] {
		case "start_session":
			return jsonOutput(map[string]any{"active": true, "revived": false}), nil
		case "list_windows":
			pid := 123
			if f.foreign {
				pid = 999
			}
			return jsonOutput(map[string]any{"windows": []window{{PID: pid, ID: 456, Layer: 0, Bounds: f.bounds, OnScreen: !f.hidden}}}), nil
		case "get_window_state":
			path, ok := input["screenshot_out_file"].(string)
			if !ok {
				t.Fatal("missing output path")
			}
			var data bytes.Buffer
			if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, f.width, f.height))); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			f.captureData = data.Bytes()
			return jsonOutput(captureState{PID: 123, WindowID: 456, Bounds: f.bounds, Width: f.width, Height: f.height, Scale: 2, Capture: "private-provider-capture", Elements: f.elements}), nil
		case "click", "type_text", "press_key":
			if args[3] == "click" && input["capture_id"] != nil &&
				(input["x"] == nil || input["y"] == nil || input["element_token"] != nil) {
				return jsonOutput(map[string]any{"code": "invalid_arguments", "summary": "capture_id requires pixel coordinates without an element token"}), errors.New("exit status 1")
			}
			if f.providerError {
				return jsonOutput(map[string]any{"code": "ax_window_unresolved", "effect": "refused", "summary": "exact window unavailable"}), errors.New("exit 1")
			}
			return jsonOutput(map[string]any{"effect": "unverifiable", "summary": "events posted"}), nil
		default:
			t.Fatalf("unexpected tool %s", args[3])
			return Output{}, nil
		}
	}
	var err error
	f.adapter, err = New(Config{DataDir: shortRoot(t), Runner: f.runner, ProcessStartedAt: func(_ context.Context, _ int) (time.Time, error) {
		if f.changed {
			return f.born.Add(time.Microsecond), nil
		}
		return f.born, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.adapter.root, "captures"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.adapter.pidFile(), []byte("99"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.adapter.driver = driverIdentity{pid: 99, started: f.born}
	f.adapter.now = func() time.Time { return f.born.Add(time.Hour) }
	f.target, err = f.adapter.BindWindow(context.Background(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) screenshot(t *testing.T) domain.TestDesktopFrame {
	t.Helper()
	shot, err := f.adapter.Screenshot(context.Background(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	shot.Frame.ScreenshotID = "evidence-screenshot-1"
	return shot.Frame
}

func TestWindowInjectionAndNativePixels(t *testing.T) {
	f := newFixture(t)
	frame := f.screenshot(t)
	if frame.Width != 1280 || frame.Height != 800 || frame.Scale != 2 || frame.Bounds != f.bounds || frame.CaptureHandle == "" {
		t.Fatalf("wrong frame: %+v", frame)
	}
	result, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 400, Y: 200})
	if err != nil || !result.Delivered {
		t.Fatalf("input result %+v: %v", result, err)
	}
	last := f.runner.calls[len(f.runner.calls)-1]
	var input map[string]any
	if err := json.Unmarshal([]byte(last.args[4]), &input); err != nil {
		t.Fatal(err)
	}
	if input["x"] != float64(400) || input["y"] != float64(200) || input["capture_id"] != "private-provider-capture" {
		t.Fatalf("pixels were rescaled or capture lost: %v", input)
	}
	// Cua's source converts these pixels once: x/2=200 native points.
	for _, call := range f.runner.calls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.args[4]), &args); err != nil {
			t.Fatal(err)
		}
		if call.args[3] == "start_session" {
			if len(args) != 1 || args["session"] == "" {
				t.Fatalf("session lifecycle call must name only its owned label: %v", args)
			}
			continue
		}
		if args["pid"] != float64(123) || args["session"] == "" {
			t.Fatalf("missing pinned PID/session: %v", args)
		}
		if call.args[3] != "list_windows" && args["window_id"] != float64(456) {
			t.Fatalf("missing bound window: %v", args)
		}
		if args["scope"] == "desktop" || strings.Contains(call.args[3], "desktop") {
			t.Fatal("desktop scope escaped")
		}
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(frame.CaptureHandle)) {
		t.Fatal("adapter receipt leaked to worker JSON")
	}
}

func TestRefusalsBeforeProviderCall(t *testing.T) {
	cases := []struct {
		name, code string
		mutate     func(*fixture, *domain.TestDesktopFrame, *domain.TestClickRequest)
	}{
		{"negative", "coordinate_outside_window", func(_ *fixture, _ *domain.TestDesktopFrame, r *domain.TestClickRequest) { r.X = -1 }},
		{"right_edge", "coordinate_outside_window", func(_ *fixture, frame *domain.TestDesktopFrame, r *domain.TestClickRequest) { r.X = frame.Width }},
		{"bottom_edge", "coordinate_outside_window", func(_ *fixture, frame *domain.TestDesktopFrame, r *domain.TestClickRequest) { r.Y = frame.Height }},
		{"foreign_id", "screenshot_mismatch", func(_ *fixture, _ *domain.TestDesktopFrame, r *domain.TestClickRequest) { r.ScreenshotID = "foreign" }},
		{"forged_receipt", "screenshot_mismatch", func(_ *fixture, frame *domain.TestDesktopFrame, _ *domain.TestClickRequest) {
			frame.CaptureHandle = "forged"
		}},
		{"forged_bounds", "screenshot_mismatch", func(_ *fixture, frame *domain.TestDesktopFrame, _ *domain.TestClickRequest) { frame.Bounds.X++ }},
		{"stale", "screenshot_stale", func(f *fixture, _ *domain.TestDesktopFrame, _ *domain.TestClickRequest) {
			f.adapter.now = func() time.Time { return f.born.Add(time.Hour + 31*time.Second) }
		}},
		{"reused_pid", "process_changed", func(f *fixture, _ *domain.TestDesktopFrame, _ *domain.TestClickRequest) { f.changed = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			frame := f.screenshot(t)
			request := domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100}
			tc.mutate(f, &frame, &request)
			before := len(f.runner.calls)
			_, err := f.adapter.Click(context.Background(), f.target, frame, request)
			var failure *Error
			if !errors.Is(err, ErrRefused) || !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("got %v, expected %s", err, tc.code)
			}
			if len(f.runner.calls) != before {
				t.Fatal("validation reached Cua")
			}
		})
	}
}

func TestChangedWindowAndForeignOwnerRefuseInput(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		f := newFixture(t)
		frame := f.screenshot(t)
		if foreign {
			f.foreign = true
		} else {
			f.bounds.X++
		}
		before := len(f.runner.calls)
		_, err := f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100, Text: "must not dispatch"})
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("expected refusal: %v", err)
		}
		for _, call := range f.runner.calls[before:] {
			if call.args[3] != "list_windows" {
				t.Fatalf("input escaped after window change: %v", call.args)
			}
		}
	}
}

func TestConsumedReceiptAndProviderError(t *testing.T) {
	f := newFixture(t)
	frame := f.screenshot(t)
	f.providerError = true
	_, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100})
	var failure *Error
	if !errors.Is(err, ErrProvider) || !errors.As(err, &failure) || failure.Code != "ax_window_unresolved" {
		t.Fatalf("lost provider refusal: %v", err)
	}
	before := len(f.runner.calls)
	_, err = f.adapter.Key(context.Background(), f.target, frame, domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"enter"}})
	if !errors.Is(err, ErrRefused) || len(f.runner.calls) != before {
		t.Fatalf("uncertain input reused capture: %v", err)
	}
}

func TestPixelTypeAndKeyInjection(t *testing.T) {
	f := newFixture(t)
	frame := f.screenshot(t)
	_, err := f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 400, Y: 200, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	last := f.runner.calls[len(f.runner.calls)-1]
	if last.args[3] != "type_text" || !strings.Contains(last.args[4], `"x":400`) || strings.Contains(last.args[4], "element_token") {
		t.Fatal("type did not use pixel focus")
	}
	frame = f.screenshot(t)
	_, err = f.adapter.Key(context.Background(), f.target, frame, domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"command", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	last = f.runner.calls[len(f.runner.calls)-1]
	if last.args[3] != "press_key" || !strings.Contains(last.args[4], `"modifiers":["cmd"]`) {
		t.Fatalf("wrong key chord: %v", last.args)
	}
}

func TestForegroundKeyUsesFreshTypedFieldAndForegroundClick(t *testing.T) {
	f := newFixture(t)
	f.adapter.cfg.DeliveryMode = Foreground
	f.elements = []capturedElement{{Role: "AXTextField", Token: "first-field", Frame: &pixelBounds{X: 100, Y: 100, Width: 500, Height: 200}}}
	frame := f.screenshot(t)
	_, err := f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 400, Y: 200, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	f.elements = []capturedElement{{Role: "AXTextField", Token: "fresh-field", Frame: &pixelBounds{X: 100, Y: 100, Width: 500, Height: 200}}}
	frame = f.screenshot(t)
	_, err = f.adapter.Key(context.Background(), f.target, frame, domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"return"}})
	if err != nil {
		t.Fatal(err)
	}
	last := f.runner.calls[len(f.runner.calls)-1]
	if !strings.Contains(last.args[4], `"element_token":"fresh-field"`) || !strings.Contains(last.args[4], `"delivery_mode":"foreground"`) {
		t.Fatalf("key lacks exact fresh field: %v", last.args)
	}
	frame = f.screenshot(t)
	result, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100})
	if err != nil {
		t.Fatal(err)
	}
	last = f.runner.calls[len(f.runner.calls)-1]
	if !strings.Contains(last.args[4], `"delivery_mode":"foreground"`) || !strings.HasPrefix(result.Detail, "delivery_mode=foreground;") {
		t.Fatal("click did not follow configured foreground policy")
	}
}

func TestScreenshotPreviewAndCoordinateEdges(t *testing.T) {
	for _, portrait := range []bool{false, true} {
		for _, tool := range []string{"click", "type"} {
			for _, farEdge := range []bool{false, true} {
				name := tool + map[bool]string{false: "/landscape", true: "/portrait"}[portrait] + map[bool]string{false: "/first", true: "/last"}[farEdge]
				t.Run(name, func(t *testing.T) {
					f := newFixture(t)
					f.width, f.height = 2640, 1560
					if portrait {
						f.width, f.height = f.height, f.width
					}
					f.bounds.Width, f.bounds.Height = float64(f.width)/2, float64(f.height)/2
					shot, err := f.adapter.Screenshot(context.Background(), f.target)
					if err != nil {
						t.Fatal(err)
					}
					w, h := 1568, 927
					if portrait {
						w, h = h, w
					}
					geometry, err := png.DecodeConfig(bytes.NewReader(shot.Data))
					if err != nil || geometry.Width != w || geometry.Height != h || shot.Frame.Width != w || shot.Frame.Height != h {
						t.Fatalf("preview metadata disagrees: %+v %+v %v", geometry, shot.Frame, err)
					}
					if shot.Original == nil || !bytes.Equal(shot.Original.Data, f.captureData) || shot.Original.Frame.Width != f.width || shot.Original.Frame.Height != f.height {
						t.Fatal("original PNG evidence was lost or rescaled")
					}
					frame := shot.Frame
					frame.ScreenshotID = "preview-evidence"
					point, want := pixel{}, pixel{}
					if farEdge {
						point, want = pixel{w - 1, h - 1}, pixel{f.width - 1, f.height - 1}
					}
					if tool == "click" {
						_, err = f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: point.x, Y: point.y})
					} else {
						_, err = f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: point.x, Y: point.y, Text: "edge"})
					}
					if err != nil {
						t.Fatal(err)
					}
					var args map[string]any
					last := f.runner.calls[len(f.runner.calls)-1]
					if err := json.Unmarshal([]byte(last.args[4]), &args); err != nil {
						t.Fatal(err)
					}
					if args["x"] != float64(want.x) || args["y"] != float64(want.y) || args["delivery_mode"] != "background" {
						t.Fatalf("preview pixels did not map once onto capture edges: %v", args)
					}
					if tool == "click" && args["capture_id"] != "private-provider-capture" {
						t.Fatal("preview click lost original capture admission")
					}
				})
			}
		}
	}
}

func TestPreviewTypingResolvesOriginalAXCoordinates(t *testing.T) {
	f := newFixture(t)
	f.adapter.cfg.DeliveryMode = Foreground
	f.width, f.height = 2640, 1560
	f.bounds.Width, f.bounds.Height = 1320, 780
	f.elements = []capturedElement{{Role: "AXTextField", Token: "clone-url", Frame: &pixelBounds{X: 1200, Y: 650, Width: 300, Height: 100}}}
	shot, err := f.adapter.Screenshot(context.Background(), f.target)
	if err != nil || len(shot.Elements) != 1 {
		t.Fatal("missing observed field", err)
	}
	frame := shot.Frame
	frame.ScreenshotID = "evidence-screenshot-1"
	_, err = f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, ElementID: shot.Elements[0].ElementID, Text: "typed-by-investigator"})
	if err != nil {
		t.Fatal(err)
	}
	last := f.runner.calls[len(f.runner.calls)-1]
	if !strings.Contains(last.args[4], `"element_token":"clone-url"`) || strings.Contains(last.args[4], `"x":`) {
		t.Fatalf("preview point missed original AX field: %v", last.args)
	}
	f.elements[0].Token = "fresh-clone-url"
	frame = f.screenshot(t)
	_, err = f.adapter.Key(context.Background(), f.target, frame, domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"return"}})
	if err != nil || !strings.Contains(f.runner.calls[len(f.runner.calls)-1].args[4], `"element_token":"fresh-clone-url"`) {
		t.Fatalf("preview typing lost original remembered focus: %v", err)
	}
}

func TestForegroundKeyRefusesMissingTypedField(t *testing.T) {
	f := newFixture(t)
	f.adapter.cfg.DeliveryMode = Foreground
	f.elements = []capturedElement{{Role: "AXTextField", Token: "first-field", Frame: &pixelBounds{X: 100, Y: 100, Width: 500, Height: 200}}}
	frame := f.screenshot(t)
	_, err := f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 400, Y: 200, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	f.elements = nil
	frame = f.screenshot(t)
	before := len(f.runner.calls)
	_, err = f.adapter.Key(context.Background(), f.target, frame, domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"return"}})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("missing exact field accepted: %v", err)
	}
	for _, call := range f.runner.calls[before:] {
		if call.args[3] == "press_key" {
			t.Fatal("unresolved field dispatched a key")
		}
	}
}

func TestForegroundTypingWithoutAXFieldUsesGuardedPixels(t *testing.T) {
	f := newFixture(t)
	f.adapter.cfg.DeliveryMode = Foreground
	f.width, f.height = 2640, 1560
	f.bounds.Width, f.bounds.Height = 1320, 780
	frame := f.screenshot(t)
	result, err := f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 784, Y: 416, Text: "typed-by-investigator"})
	if err != nil || !result.Delivered {
		t.Fatalf("foreground pixel typing failed: %+v %v", result, err)
	}
	last := f.runner.calls[len(f.runner.calls)-1]
	var args map[string]any
	if err := json.Unmarshal([]byte(last.args[4]), &args); err != nil {
		t.Fatal(err)
	}
	if last.args[3] != "type_text" || args["x"] != float64(1320) || args["y"] != float64(700) || args["pid"] != float64(123) || args["window_id"] != float64(456) || args["delivery_mode"] != "foreground" || args["element_token"] != nil {
		t.Fatalf("pixel typing lost exact target or coordinates: %v", args)
	}
	if _, err := f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 400, Y: 200, Text: "do not repeat"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("pixel typing reused its frame: %v", err)
	}
}

func TestPreviewRejectsOutsideAndForgedDimensions(t *testing.T) {
	for _, boundary := range []string{"negative", "right", "bottom", "forged"} {
		t.Run(boundary, func(t *testing.T) {
			f := newFixture(t)
			f.width, f.height = 2640, 1560
			f.bounds.Width, f.bounds.Height = 1320, 780
			frame := f.screenshot(t)
			point := pixel{}
			switch boundary {
			case "negative":
				point.x = -1
			case "right":
				point.x = frame.Width
			case "bottom":
				point.y = frame.Height
			case "forged":
				frame.Width, frame.Height = f.width, f.height
			}
			before := len(f.runner.calls)
			_, err := f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: point.x, Y: point.y, Text: "refuse"})
			if !errors.Is(err, ErrRefused) || len(f.runner.calls) != before {
				t.Fatalf("invalid preview reached provider: %v", err)
			}
		})
	}
}

func TestLifecycleOwnsOnlyCustomDaemon(t *testing.T) {
	root := shortRoot(t)
	born := time.Now().UTC()
	alive := false
	fake := &fakeRunner{}
	var a *Adapter
	var socket net.Listener
	fake.hook = func(executable string, args []string) (Output, error) {
		switch executable {
		case "/usr/bin/codesign":
			return Output{Stderr: []byte("Identifier=com.trycua.driver\nTeamIdentifier=YCK386LBJ7\n")}, nil
		case "/bin/launchctl":
			return Output{}, nil
		case "/usr/bin/open":
			alive = true
			if err := os.WriteFile(a.pidFile(), []byte("99"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			socket, err = net.Listen("unix", a.socket())
			if err != nil {
				t.Fatal(err)
			}
			return Output{}, nil
		default:
			if reflect.DeepEqual(args, []string{"--version"}) {
				return Output{Stdout: []byte("cua-driver 0.34.0\n")}, nil
			}
			if len(args) == 5 && args[2] == "call" {
				return jsonOutput(map[string]bool{"accessibility": true, "screen_recording": true}), nil
			}
			if len(args) == 7 && args[4] == "stop" && args[5] == "--expected-pid" && args[6] == "99" {
				alive = false
				if err := socket.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(a.pidFile()); err != nil {
					t.Fatal(err)
				}
				return Output{}, nil
			}
			t.Fatalf("unexpected command %s %v", executable, args)
			return Output{}, nil
		}
	}
	var err error
	a, err = New(Config{DataDir: root, Runner: fake, ProcessStartedAt: func(_ context.Context, pid int) (time.Time, error) {
		if pid != 99 || !alive {
			return time.Time{}, errors.New("missing")
		}
		return born, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_INHERITED_SECRET", "must-remove")
	t.Setenv("CUA_DRIVER_RS_MCP_HTTP_PORT", "9876")
	t.Setenv("CUA_DRIVER_ENVELOPE_HTTP_PORT", "9877")
	if err := a.ensureDriver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.calls {
		for _, entry := range call.env {
			if strings.HasPrefix(entry, "AO_") || strings.HasPrefix(entry, "CUA_DRIVER_RS_MCP_HTTP_PORT=") || strings.HasPrefix(entry, "CUA_DRIVER_ENVELOPE_HTTP_PORT=") {
				t.Fatalf("unsafe inherited env: %s", entry)
			}
		}
		if call.executable == "/usr/bin/open" {
			joined := strings.Join(call.args, " ")
			for _, needed := range []string{"-n -g -a /Applications/CuaDriver.app", "CUA_DRIVER_RS_UPDATE_CHECK=0", a.socket(), a.pidFile(), filepath.Join(a.root, "tmp"), filepath.Join(a.root, "home")} {
				if !strings.Contains(joined, needed) {
					t.Fatalf("launch omitted %s: %s", needed, joined)
				}
			}
		}
	}
}

func TestKeyChord(t *testing.T) {
	key, modifiers, err := keyChord([]string{"control", "shift", "enter"})
	if err != nil || key != "return" || !reflect.DeepEqual(modifiers, []string{"ctrl", "shift"}) {
		t.Fatalf("chord %s %v %v", key, modifiers, err)
	}
	for _, keys := range [][]string{{}, {"cmd"}, {"a", "b"}, {"cmd", "command", "a"}, {"launch_other_app"}} {
		if _, _, err := keyChord(keys); err == nil {
			t.Fatalf("invalid chord accepted: %v", keys)
		}
	}
}

func TestForeignTargetAndDriverReuse(t *testing.T) {
	f := newFixture(t)
	frame := f.screenshot(t)
	foreign := f.target
	foreign.WindowID = strconv.Itoa(999)
	before := len(f.runner.calls)
	if _, err := f.adapter.Screenshot(context.Background(), foreign); !errors.Is(err, ErrRefused) {
		t.Fatalf("foreign target: %v", err)
	}
	if err := os.WriteFile(f.adapter.pidFile(), []byte("100"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100})
	if !errors.Is(err, ErrRefused) || len(f.runner.calls) != before {
		t.Fatalf("foreign daemon reached provider: %v", err)
	}
}
