package cua

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestInputRequiresExactFocusedWindow(t *testing.T) {
	for _, reason := range []string{"foreign pid", "foreign window", "not focused", "unknown key window", "foreign key window", "foreign foreground pid", "inactive process", "unknown front process", "provider failure"} {
		t.Run(reason, func(t *testing.T) {
			f := newFixture(t)
			frame := f.screenshot(t)
			provider := f.runner.hook
			f.runner.hook = func(executable string, args []string) (Output, error) {
				if len(args) == 5 && args[3] == "bring_to_front" {
					if reason == "provider failure" {
						return Output{}, errors.New("activation failed")
					}
					out, _ := provider(executable, args)
					var result map[string]any
					if err := json.Unmarshal(out.Stdout, &result); err != nil {
						t.Fatal(err)
					}
					observed := result["observed"].(map[string]any)
					switch reason {
					case "foreign pid":
						result["pid"] = 999
					case "foreign window":
						result["window_id"] = 999
					case "not focused":
						result["exact_window_effect"].(map[string]any)["focused"] = false
					case "unknown key window":
						observed["focused_window_id"] = nil
					case "foreign key window":
						observed["focused_window_id"] = 999
					case "foreign foreground pid":
						observed["workspace_frontmost_pid"] = 999
					case "inactive process":
						result["process_activated"] = false
					case "unknown front process":
						observed["front_process_matches_target"] = nil
					}
					return jsonOutput(result), nil
				}
				return provider(executable, args)
			}
			before := len(f.runner.calls)
			_, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100})
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "window_not_focused") {
				t.Fatalf("focus reason lost: %v", err)
			}
			for _, call := range f.runner.calls[before:] {
				if len(call.args) == 5 && call.args[3] == "click" {
					t.Fatal("unfocused input dispatched")
				}
			}
		})
	}
}

func TestRecordingControlOnlyBlocksItsCoveredPoint(t *testing.T) {
	exitErr := exec.Command("/bin/sh", "-c", "exit 1").Run()
	if exitErr == nil {
		t.Fatal("expected provider partial exit")
	}
	for _, covering := range []bool{false, true} {
		t.Run(map[bool]string{false: "elsewhere", true: "covers click"}[covering], func(t *testing.T) {
			f := newFixture(t)
			frame := f.screenshot(t)
			provider := f.runner.hook
			f.runner.hook = func(executable string, args []string) (Output, error) {
				if executable == "/usr/bin/osascript" {
					alpha := 1.0
					bounds := domain.TestWindowBounds{X: 70, Y: 60, Width: 66, Height: 20}
					if covering {
						bounds.Y = 130
					}
					return jsonOutput(windowSnapshot{Windows: []screenWindow{{ID: 999, PID: 123, Owner: "Electron recording control", Layer: 27, Alpha: &alpha, Bounds: &bounds}, {ID: 456, PID: 123, Owner: "Electron", Alpha: &alpha, Bounds: &f.bounds}}}), nil
				}
				out, err := provider(executable, args)
				if len(args) == 5 && args[3] == "bring_to_front" {
					var result map[string]any
					if err := json.Unmarshal(out.Stdout, &result); err != nil {
						t.Fatal(err)
					}
					result["activated"], result["status"], result["code"] = false, "partial", "bring_to_front_exact_window_unverified"
					result["exact_window_effect"].(map[string]any)["frontmost_ordinary"] = false
					result["exact_window_effect"].(map[string]any)["verified"] = false
					return jsonOutput(result), exitErr
				}
				return out, err
			}
			before := len(f.runner.calls)
			_, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100})
			if covering {
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "window_point_covered") || !strings.Contains(err.Error(), "PID 123 window 999 layer 27 bounds (70,130,66,20)") {
					t.Fatal("cover identity lost", err)
				}
			} else if err != nil {
				t.Fatal("non-covering control blocked owned focused input", err)
			}
			inputs := 0
			for _, call := range f.runner.calls[before:] {
				if len(call.args) == 5 && call.args[3] == "click" {
					inputs++
				}
			}
			if covering && inputs != 0 || !covering && inputs != 1 {
				t.Fatal("wrong dispatch count", inputs)
			}
		})
	}
}

func TestPointGuardRejectsRecordedLockScreenAndUnknownMetadata(t *testing.T) {
	data, err := os.ReadFile("testdata/window-list-locked.json")
	if err != nil {
		t.Fatal(err)
	}
	var recorded []screenWindow
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	if err := ownedPoint(recorded, nil, f.target, f.bounds, 120, 140); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "loginwindow PID 412") || !strings.Contains(err.Error(), "layer 2004 bounds (0,0,1440,900)") {
		t.Fatal("high-layer locked screen admitted", err)
	}
	for _, data := range []string{`[]`, `[{"kCGWindowAlpha":1}]`, `[{"kCGWindowBounds":{"X":0,"Y":0,"Width":1440,"Height":900}}]`} {
		var windows []screenWindow
		if err := json.Unmarshal([]byte(data), &windows); err != nil {
			t.Fatal(err)
		}
		if err := ownedPoint(windows, nil, f.target, f.bounds, 120, 140); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "window_hit_test_unknown") {
			t.Fatal("unknown metadata admitted", err)
		}
	}
	alpha, zero := 1.0, 0.0
	transparent := screenWindow{ID: 999, PID: 999, Layer: 1000, Alpha: &zero, Bounds: &f.bounds}
	owned := screenWindow{ID: 456, PID: 123, Alpha: &alpha, Bounds: &f.bounds}
	if err := ownedPoint([]screenWindow{transparent, owned}, nil, f.target, f.bounds, 120, 140); err != nil {
		t.Fatal("transparent overlay blocked target", err)
	}
	elsewhere := domain.TestWindowBounds{X: 70, Y: 60, Width: 66, Height: 20}
	if err := ownedPoint([]screenWindow{{Bounds: &elsewhere}, owned}, nil, f.target, f.bounds, 120, 140); err != nil {
		t.Fatal("opacity outside the click point blocked target", err)
	}
}

func TestBackgroundInputIsRefused(t *testing.T) {
	f := newFixture(t)
	frame := f.screenshot(t)
	f.adapter.cfg.DeliveryMode = Background
	before := len(f.runner.calls)
	_, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "foreground_required") || len(f.runner.calls) != before {
		t.Fatal("background input was attempted", err)
	}
}

func TestRecordedDockWindowOnlyCoversReservedDisplayArea(t *testing.T) {
	data, err := os.ReadFile("testdata/window-list-dock.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot windowSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	target := domain.TestTargetIdentity{ElectronPID: 31838, WindowID: "8461"}
	bounds := *snapshot.Windows[2].Bounds
	for _, point := range []struct {
		name string
		y    float64
		want bool
	}{{"Settings in usable area", 784, true}, {"Dock area", 840, false}, {"menu area", 10, false}} {
		t.Run(point.name, func(t *testing.T) {
			err := ownedPoint(snapshot.Windows, snapshot.Displays, target, bounds, 179.5, point.y)
			if point.want {
				if err != nil {
					t.Fatal("full-display Dock window blocked the usable area", err)
				}
			} else if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "Dock PID 612 window 7 layer 20 bounds (0,0,1440,900)") {
				t.Fatal("reserved-area refusal lost the exact covering window", err)
			}
		})
	}
}

func TestScreenshotMovesOnlyBoundWindowToCurrentSpace(t *testing.T) {
	f := newFixture(t)
	b := f.adapter.bindings[f.target.ID]
	moved := false
	provider := f.runner.hook
	f.runner.hook = func(executable string, args []string) (Output, error) {
		if len(args) == 5 && args[3] == "bring_to_front" {
			var input map[string]any
			if err := json.Unmarshal([]byte(args[4]), &input); err != nil {
				t.Fatal(err)
			}
			if input["pid"] != float64(123) || input["window_id"] != float64(456) {
				t.Fatal("activation escaped binding", input)
			}
			moved = true
		}
		if len(args) == 5 && args[3] == "list_windows" {
			return jsonOutput(map[string]any{"windows": []window{{PID: 123, ID: 456, Bounds: f.bounds, OnScreen: true, OnCurrentSpace: boolPointer(moved), CurrentSpaceID: 7}}}), nil
		}
		return provider(executable, args)
	}
	if _, err := f.adapter.Screenshot(context.Background(), b.target); err != nil || !moved {
		t.Fatalf("Space preparation: %v", err)
	}
}

func TestInputRefusesWindowChangedDuringPreparation(t *testing.T) {
	f := newFixture(t)
	frame := f.screenshot(t)
	provider := f.runner.hook
	f.runner.hook = func(executable string, args []string) (Output, error) {
		out, err := provider(executable, args)
		if len(args) == 5 && args[3] == "bring_to_front" {
			f.bounds.X++
		}
		return out, err
	}
	_, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "window_changed") {
		t.Fatalf("changed geometry admitted input: %v", err)
	}
}

func TestWindowSpaceMustBeProved(t *testing.T) {
	f := newFixture(t)
	provider := f.runner.hook
	f.runner.hook = func(executable string, args []string) (Output, error) {
		if len(args) == 5 && args[3] == "list_windows" {
			return jsonOutput(map[string]any{"windows": []window{{PID: 123, ID: 456, Bounds: f.bounds, OnScreen: true}}}), nil
		}
		return provider(executable, args)
	}
	_, err := f.adapter.Screenshot(context.Background(), f.target)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "window_space_unknown") {
		t.Fatalf("unknown Space accepted: %v", err)
	}
	for _, call := range f.runner.calls {
		if len(call.args) == 5 && call.args[3] == "bring_to_front" {
			var args map[string]any
			_ = json.Unmarshal([]byte(call.args[4]), &args)
			if args["pid"] != float64(123) {
				t.Fatal("foreign window activated")
			}
		}
	}
}

func TestScreenshotPlacesOnlyOwnedOffScreenWindow(t *testing.T) {
	f := newFixture(t)
	f.hidden = true
	f.bounds.X, f.bounds.Y = -3000, -3000
	provider := f.runner.hook
	moved := false
	f.runner.hook = func(executable string, args []string) (Output, error) {
		if len(args) == 5 && args[3] == "get_screen_size" {
			return jsonOutput(map[string]any{"width": 1440, "height": 900}), nil
		}
		if len(args) == 5 && args[3] == "set_window_frame" {
			var input map[string]any
			if err := json.Unmarshal([]byte(args[4]), &input); err != nil {
				t.Fatal(err)
			}
			if input["pid"] != float64(123) || input["window_id"] != float64(456) || input["x"] != float64(40) || input["y"] != float64(60) || input["width"] != f.bounds.Width || input["height"] != f.bounds.Height {
				t.Fatal("placement escaped the exact bound window", input)
			}
			f.bounds.X, f.bounds.Y, f.hidden = 40, 60, false
			moved = true
			return jsonOutput(map[string]any{"effect": "confirmed"}), nil
		}
		return provider(executable, args)
	}
	shot, err := f.adapter.Screenshot(context.Background(), f.target)
	if err != nil || !moved || shot.Frame.Bounds != f.bounds {
		t.Fatal("off-screen window was not placed before its new capture", err)
	}
}

func TestSpaceActivationFailureNeverCaptures(t *testing.T) {
	f := newFixture(t)
	provider := f.runner.hook
	cause := errors.New("owned window unavailable on current Space")
	f.runner.hook = func(executable string, args []string) (Output, error) {
		if len(args) == 5 && args[3] == "bring_to_front" {
			return Output{Stderr: []byte("exact window activation failed")}, cause
		}
		if len(args) == 5 && args[3] == "list_windows" {
			return jsonOutput(map[string]any{"windows": []window{{PID: 123, ID: 456, Bounds: f.bounds, OnScreen: true, OnCurrentSpace: boolPointer(false), CurrentSpaceID: 7}}}), nil
		}
		if len(args) == 5 && args[3] == "get_window_state" {
			t.Fatal("failed Space activation continued to capture")
		}
		return provider(executable, args)
	}
	_, err := f.adapter.Screenshot(context.Background(), f.target)
	if !errors.Is(err, ErrRefused) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "window_other_space") || !strings.Contains(err.Error(), "exact window activation failed") {
		t.Fatal("Space refusal lost exact reason", err)
	}
}

func TestSpaceReadbackRejectsSpaceSwitch(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "did not move", true: "switched Space"}[changed], func(t *testing.T) {
			f := newFixture(t)
			provider := f.runner.hook
			activated := false
			f.runner.hook = func(executable string, args []string) (Output, error) {
				if len(args) == 5 && args[3] == "bring_to_front" {
					activated = true
				}
				if len(args) == 5 && args[3] == "list_windows" {
					id := uint64(7)
					if changed && activated {
						id = 8
					}
					return jsonOutput(map[string]any{"windows": []window{{PID: 123, ID: 456, Bounds: f.bounds, OnScreen: true, OnCurrentSpace: boolPointer(changed && activated), CurrentSpaceID: id}}}), nil
				}
				if len(args) == 5 && args[3] == "get_window_state" {
					t.Fatal("unproven Space captured")
				}
				return provider(executable, args)
			}
			if _, err := f.adapter.Screenshot(context.Background(), f.target); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "window_other_space") {
				t.Fatal("unproven Space accepted", err)
			}
		})
	}
}

func TestFocusPreparationRetainsProviderAndCancellationCause(t *testing.T) {
	f := newFixture(t)
	provider := f.runner.hook
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cause := errors.New("exact-window activation failed")
	f.runner.hook = func(executable string, args []string) (Output, error) {
		if len(args) == 5 && args[3] == "bring_to_front" {
			cancel()
			return Output{}, cause
		}
		return provider(executable, args)
	}
	_, err := f.adapter.Screenshot(ctx, f.target)
	if !errors.Is(err, ErrRefused) || !errors.Is(err, ErrProvider) || !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "window_not_focused") {
		t.Fatal("frontmost refusal lost provider or cancellation cause", err)
	}
}
