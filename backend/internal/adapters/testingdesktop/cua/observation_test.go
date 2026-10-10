package cua

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestObservationBoundsTextAndCaptureIdentity(t *testing.T) {
	f := newFixture(t)
	f.width, f.height = 2640, 1560
	f.bounds.Width, f.bounds.Height = 1320, 780
	on, off := true, false
	field := capturedElement{Role: "AXTextField", Token: "private-field-token", Label: strings.Repeat("x", 300),
		Value: json.RawMessage(`"field value"`), Enabled: &on, Focused: &off, Frame: &pixelBounds{X: 1320, Y: 780, Width: 660, Height: 390}}
	f.elements = []capturedElement{field,
		{Role: "AXButton", Token: "offscreen", Frame: &pixelBounds{X: 3000, Y: 0, Width: 20, Height: 20}},
		{Role: "AXButton", Token: "hidden", Visible: &off, Frame: &pixelBounds{Width: 20, Height: 20}}}
	shot, err := f.adapter.Screenshot(context.Background(), f.target)
	if err != nil || len(shot.Elements) != 1 || !shot.Truncated {
		t.Fatal("observation projection", shot, err)
	}
	e := shot.Elements[0]
	if e.Frame.X != float64(shot.Frame.Width)/2 || e.Frame.Y != float64(shot.Frame.Height)/2 ||
		e.Frame.Width != float64(shot.Frame.Width)/4 || e.Value != "field value" || len(e.Label) != 256 || e.Enabled == nil || !*e.Enabled || e.Focused == nil || *e.Focused {
		t.Fatal("AX frame does not use returned pixels", e, shot.Frame)
	}
	data, _ := json.Marshal(shot)
	if strings.Contains(string(data), "private-field-token") || strings.Contains(string(data), "private-provider-capture") {
		t.Fatal("provider receipt leaked", string(data))
	}
	for _, call := range f.runner.calls {
		if call.args[3] == "get_window_state" && (!strings.Contains(call.args[4], `"include_accessibility_tree":true`) || !strings.Contains(call.args[4], `"max_elements":300`)) {
			t.Fatal("pixels and AX not captured together", call)
		}
	}
}

func TestObservationElementLimit(t *testing.T) {
	f := newFixture(t)
	for range 301 {
		f.elements = append(f.elements, capturedElement{Role: "AXButton", Token: "token", Frame: &pixelBounds{Width: 20, Height: 20}})
	}
	shot, err := f.adapter.Screenshot(context.Background(), f.target)
	if err != nil || len(shot.Elements) != 300 || !shot.Truncated {
		t.Fatal("unbounded AX", len(shot.Elements), shot.Truncated, err)
	}
}

func TestClickCaptureIDMatchesPinnedProviderContract(t *testing.T) {
	for _, mode := range []DeliveryMode{Foreground} {
		for _, tc := range []struct {
			name    string
			element bool
			button  domain.TestMouseButton
		}{
			{"element_default", true, ""},
			{"element_left", true, domain.TestMouseButtonLeft},
			{"element_right", true, domain.TestMouseButtonRight},
			{"element_middle", true, domain.TestMouseButtonMiddle},
			{"pixel_default", false, ""},
			{"pixel_left", false, domain.TestMouseButtonLeft},
			{"pixel_right", false, domain.TestMouseButtonRight},
			{"pixel_middle", false, domain.TestMouseButtonMiddle},
		} {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				f := newFixture(t)
				f.adapter.cfg.DeliveryMode = mode
				f.elements = []capturedElement{{Role: "AXButton", Token: "settings-token", Label: "Settings", Frame: &pixelBounds{X: 100, Y: 200, Width: 40, Height: 60}}}
				shot, err := f.adapter.Screenshot(context.Background(), f.target)
				if err != nil {
					t.Fatal(err)
				}
				shot.Frame.ScreenshotID = "receipt"
				request := domain.TestClickRequest{ScreenshotID: "receipt", X: 400, Y: 200, Button: tc.button}
				if tc.element {
					request.ElementID, request.X, request.Y = shot.Elements[0].ElementID, 0, 0
				}
				before := len(f.runner.calls)
				action, err := f.adapter.Click(context.Background(), f.target, shot.Frame, request)
				tokenOnly := tc.element && tc.button != domain.TestMouseButtonMiddle
				wantPath := "pointer"
				if tokenOnly {
					wantPath = "ax"
				}
				if err != nil || !action.Delivered || action.RequestedInputPath != wantPath || action.InputPath != "unknown" {
					t.Fatalf("provider contract rejected click or changed delivery metadata: %+v %v", action, err)
				}
				var clicks []map[string]any
				for _, call := range f.runner.calls[before:] {
					if call.args[3] == "click" {
						var args map[string]any
						if err := json.Unmarshal([]byte(call.args[4]), &args); err != nil {
							t.Fatal(err)
						}
						clicks = append(clicks, args)
					}
				}
				if len(clicks) != 1 {
					t.Fatalf("expected one click dispatch, got %d", len(clicks))
				}
				args := clicks[0]
				if args["pid"] != float64(f.target.ElectronPID) || args["window_id"] != float64(456) || args["session"] != f.adapter.bindings[f.target.ID].session || args["scope"] != "window" || args["delivery_mode"] != string(mode) {
					t.Fatal("click lost its exact target or configured policy", args)
				}
				if (tc.button == "" && args["button"] != nil) || (tc.button != "" && args["button"] != string(tc.button)) {
					t.Fatal("click changed the requested button", args)
				}
				if tokenOnly {
					if args["element_token"] != "settings-token" || args["capture_id"] != nil || args["x"] != nil || args["y"] != nil {
						t.Fatal("AX click must use only its fresh token, without pixel capture admission", args)
					}
				} else {
					x, y := float64(400), float64(200)
					if tc.element {
						x, y = 120, 230
					}
					if args["capture_id"] != "private-provider-capture" || args["element_token"] != nil || args["x"] != x || args["y"] != y {
						t.Fatal("pixel click lost capture admission or its original pixel/element-center coordinates", args)
					}
				}
				before = len(f.runner.calls)
				if _, err := f.adapter.Click(context.Background(), f.target, shot.Frame, request); !errors.Is(err, ErrRefused) {
					t.Fatal("click did not consume its screenshot receipt", err)
				}
				for _, call := range f.runner.calls[before:] {
					if call.args[3] == "click" {
						t.Fatal("consumed click was dispatched again")
					}
				}
			})
		}
	}
}

func TestElementInputIsCaptureBoundAndConsumed(t *testing.T) {
	for _, kind := range []string{"foreign", "old-capture", "foreign-window", "consumed", "moved", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.elements = []capturedElement{{Role: "AXButton", Token: "private-button", Frame: &pixelBounds{X: 10, Y: 10, Width: 100, Height: 50}}}
			shot, err := f.adapter.Screenshot(context.Background(), f.target)
			if err != nil {
				t.Fatal(err)
			}
			frame := shot.Frame
			frame.ScreenshotID = "receipt"
			id := shot.Elements[0].ElementID
			switch kind {
			case "foreign":
				id = "foreign"
			case "old-capture":
				frame = f.screenshot(t)
			case "foreign-window":
				frame.Target.WindowID = "999"
			case "consumed":
				action, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, ElementID: id})
				if err != nil || !action.Delivered || action.RequestedInputPath != "ax" {
					t.Fatal(action, err)
				}
			case "moved":
				f.bounds.X++
			case "disabled":
				off := false
				r := f.adapter.bindings[f.target.ID].receipt
				e := r.addressable[id]
				e.Enabled = &off
				r.addressable[id] = e
			}
			before := len(f.runner.calls)
			_, err = f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, ElementID: id})
			if !errors.Is(err, ErrRefused) {
				t.Fatal("invalid element accepted", err)
			}
			for _, call := range f.runner.calls[before:] {
				if call.args[3] == "click" {
					t.Fatal("refused element dispatched", call)
				}
			}
		})
	}
}

func TestCoordinateTypingPreservesPointerAndElementTypingUsesAX(t *testing.T) {
	for _, element := range []bool{false, true} {
		f := newFixture(t)
		f.adapter.cfg.DeliveryMode = Foreground
		f.elements = []capturedElement{{Role: "AXTextField", Token: "field-token", Frame: &pixelBounds{X: 100, Y: 100, Width: 400, Height: 200}}}
		shot, err := f.adapter.Screenshot(context.Background(), f.target)
		if err != nil {
			t.Fatal(err)
		}
		shot.Frame.ScreenshotID = "receipt"
		request := domain.TestTypeRequest{ScreenshotID: "receipt", X: 200, Y: 150, Text: "hello"}
		wantPath := "pointer+keyboard"
		if element {
			request.ElementID, request.X, request.Y = shot.Elements[0].ElementID, 0, 0
			wantPath = "ax+keyboard"
		}
		action, err := f.adapter.Type(context.Background(), f.target, shot.Frame, request)
		if err != nil || action.RequestedInputPath != wantPath || action.InputPath != "unknown" {
			t.Fatal(action, err)
		}
		var args map[string]any
		_ = json.Unmarshal([]byte(f.runner.calls[len(f.runner.calls)-1].args[4]), &args)
		if element && (args["element_token"] != "field-token" || args["x"] != nil) {
			t.Fatal(args)
		}
		if !element && (args["element_token"] != nil || args["x"] != float64(200) || args["y"] != float64(150)) {
			t.Fatal(args)
		}
	}
}
