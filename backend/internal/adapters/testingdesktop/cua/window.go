package cua

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// prepareWindow fences the exact PID/window, Space, visibility and key focus.
// Windows elsewhere above the target do not disprove its focus.
func (a *Adapter) prepareWindow(ctx context.Context, b *binding) (window, error) {
	if err := a.checkScreenUnlocked(ctx); err != nil {
		return window{}, err
	}
	w, err := a.liveWindow(ctx, b)
	if err != nil {
		return window{}, err
	}
	if w.OnCurrentSpace == nil {
		return window{}, refuse("window_space_unknown", "provider did not prove the owned window's Space")
	}
	otherSpace, currentSpace := !*w.OnCurrentSpace, w.CurrentSpaceID
	if otherSpace && currentSpace == 0 {
		return window{}, refuse("window_space_unknown", "provider did not prove the current Space ID")
	}
	if !w.OnScreen && !otherSpace {
		if err := a.placeOnScreen(ctx, b, w); err != nil {
			return window{}, err
		}
	}
	if err := a.focusWindow(ctx, b); err != nil {
		code := "window_not_focused"
		if otherSpace {
			code = "window_other_space"
		}
		return window{}, &Error{Code: code, Detail: err.Error(), cause: errors.Join(ErrRefused, err)}
	}
	w, err = a.liveWindow(ctx, b)
	if err != nil {
		return window{}, err
	}
	if w.OnCurrentSpace == nil || !*w.OnCurrentSpace || otherSpace && w.CurrentSpaceID != currentSpace {
		return window{}, refuse("window_other_space", "owned window did not move to the original current Space")
	}
	if !w.OnScreen {
		return window{}, refuse("window_off_screen", "owned window is not visible on screen")
	}
	return w, nil
}

// Cua's older verified flag also requires global ordinary-window ordering.
// The exact activated process and AX key-window readback are the input property.
// Accept only its documented partial exit when these independent facts hold.
func (a *Adapter) focusWindow(ctx context.Context, b *binding) error {
	if err := a.checkDriver(ctx); err != nil {
		return err
	}
	args := a.targetArgs(b)
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	out, runErr := a.run(ctx, a.binary(), "--socket", a.socket(), "call", "bring_to_front", string(encoded))
	var result struct {
		PID              int    `json:"pid"`
		WindowID         int    `json:"window_id"`
		Code             string `json:"code"`
		Status           string `json:"status"`
		Activated        bool   `json:"activated"`
		IsError          bool   `json:"isError"`
		ProcessActivated bool   `json:"process_activated"`
		RequestAccepted  bool   `json:"request_accepted"`
		Exact            struct {
			Focused bool `json:"focused"`
		} `json:"exact_window_effect"`
		Observed struct {
			FocusedWindowID     *int  `json:"focused_window_id"`
			WorkspacePID        *int  `json:"workspace_frontmost_pid"`
			FrontProcessMatches *bool `json:"front_process_matches_target"`
		} `json:"observed"`
	}
	decodeErr := json.Unmarshal(out.Stdout, &result)
	var exitErr *exec.ExitError
	partial := result.Code == "bring_to_front_exact_window_unverified" && result.Status == "partial" &&
		(runErr == nil || errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1)
	if decodeErr != nil || ctx.Err() != nil || result.IsError || runErr != nil && !partial {
		return &Error{Code: "window_not_focused", Detail: providerCallDiagnostic(ctx, "bring_to_front", args, "exact window activation failed", runErr, decodeErr), cause: errors.Join(ErrRefused, ErrProvider, runErr, decodeErr, ctx.Err())}
	}
	verified := result.Activated && result.Code == "bring_to_front_exact_window_verified" && result.Status == "activated"
	if result.PID != b.target.ElectronPID || strconv.Itoa(result.WindowID) != b.target.WindowID ||
		!result.RequestAccepted || !result.ProcessActivated || !result.Exact.Focused ||
		result.Observed.FocusedWindowID == nil || *result.Observed.FocusedWindowID != result.WindowID ||
		result.Observed.WorkspacePID == nil || *result.Observed.WorkspacePID != result.PID ||
		result.Observed.FrontProcessMatches == nil || !*result.Observed.FrontProcessMatches ||
		!verified && !partial {
		observed, _ := json.Marshal(result.Observed)
		detail := fmt.Sprintf("Cua did not prove owned PID %d and key window %s: returned PID %d window %d, request_accepted=%t, process_activated=%t, focused=%t, observed=%s, code=%s", b.target.ElectronPID, b.target.WindowID, result.PID, result.WindowID, result.RequestAccepted, result.ProcessActivated, result.Exact.Focused, observed, result.Code)
		return refuse("window_not_focused", providerDiagnosticText(detail, args, providerDiagnosticLimit))
	}
	return nil
}

// CGWindowListCopyWindowInfo returns all layers in front-to-back order. JXA is
// built into macOS; no compiler, helper binary, display capture or AX walk runs.
const windowListScript = `ObjC.import("CoreGraphics"); ObjC.import("AppKit");
var screens=$.NSScreen.screens, displays=[];
for(var i=0;i<screens.count;i++) {
  var screen=screens.objectAtIndex(i), frame=screen.frame, visible=screen.visibleFrame;
  displays.push({frame:{x:frame.origin.x,y:frame.origin.y,width:frame.size.width,height:frame.size.height},
    visibleFrame:{x:visible.origin.x,y:visible.origin.y,width:visible.size.width,height:visible.size.height}});
}
JSON.stringify({windows:ObjC.deepUnwrap(ObjC.castRefToObject($.CGWindowListCopyWindowInfo(1, 0))), displays:displays})`

type screenDisplay struct {
	Frame        domain.TestWindowBounds `json:"frame"`
	VisibleFrame domain.TestWindowBounds `json:"visibleFrame"`
}

type windowSnapshot struct {
	Windows  []screenWindow  `json:"windows"`
	Displays []screenDisplay `json:"displays"`
}

type screenWindow struct {
	ID     int                      `json:"kCGWindowNumber"`
	PID    int                      `json:"kCGWindowOwnerPID"`
	Owner  string                   `json:"kCGWindowOwnerName"`
	Layer  int                      `json:"kCGWindowLayer"`
	Alpha  *float64                 `json:"kCGWindowAlpha"`
	Bounds *domain.TestWindowBounds `json:"kCGWindowBounds"`
}

func (a *Adapter) checkClickPoint(ctx context.Context, b *binding, r *captureReceipt, point pixel) error {
	out, err := a.run(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", windowListScript)
	if err != nil {
		return &Error{Code: "window_hit_test_unknown", Detail: providerCallDiagnostic(ctx, "WindowServer window list", nil, string(out.Stderr), err, nil), cause: errors.Join(ErrRefused, err, ctx.Err())}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var snapshot windowSnapshot
	if err := json.Unmarshal(out.Stdout, &snapshot); err != nil {
		return &Error{Code: "window_hit_test_unknown", Detail: providerCallDiagnostic(ctx, "WindowServer window list", nil, "invalid all-layer window list", nil, err), cause: errors.Join(ErrRefused, err)}
	}
	// Convert original capture pixels to global native display points once.
	x := r.frame.Bounds.X + float64(point.x)/r.frame.Scale
	y := r.frame.Bounds.Y + float64(point.y)/r.frame.Scale
	return ownedPoint(snapshot.Windows, snapshot.Displays, b.target, r.frame.Bounds, x, y)
}

func ownedPoint(windows []screenWindow, displays []screenDisplay, target domain.TestTargetIdentity, bounds domain.TestWindowBounds, x, y float64) error {
	for _, w := range windows {
		if w.Alpha != nil && *w.Alpha == 0 {
			continue
		}
		if w.Bounds == nil {
			return refuse("window_hit_test_unknown", "WindowServer did not prove a window's bounds")
		}
		f := *w.Bounds
		if f.Width == 0 || f.Height == 0 {
			continue
		}
		if !validBounds(f) {
			return refuse("window_hit_test_unknown", "WindowServer returned invalid window bounds")
		}
		if x < f.X || y < f.Y || x >= f.X+f.Width || y >= f.Y+f.Height {
			continue
		}
		if w.Alpha == nil || *w.Alpha < 0 || *w.Alpha > 1 {
			return refuse("window_hit_test_unknown", "WindowServer did not prove the containing window's opacity")
		}
		if dockUsablePoint(w, displays, x, y) {
			continue
		}
		if w.PID != target.ElectronPID || strconv.Itoa(w.ID) != target.WindowID {
			return refuse("window_point_covered", fmt.Sprintf("point (%g,%g) is covered by %s PID %d window %d layer %d bounds (%g,%g,%g,%g)", x, y, providerDiagnosticText(w.Owner, nil, 128), w.PID, w.ID, w.Layer, f.X, f.Y, f.Width, f.Height))
		}
		if f != bounds {
			return refuse("window_changed", "owned WindowServer bounds differ from the input screenshot")
		}
		return nil
	}
	return refuse("window_hit_test_unknown", "no on-screen window contains the input point")
}

// Dock paints its bar inside a full-display window. Only that exact shape may
// pass input through the usable area. Cocoa screen frames use bottom-left Y;
// WindowServer uses the primary display's top-left global origin.
func dockUsablePoint(w screenWindow, displays []screenDisplay, x, y float64) bool {
	if w.Owner != "Dock" || w.Layer != 20 || len(displays) == 0 || !validBounds(displays[0].Frame) {
		return false
	}
	top := displays[0].Frame.Y + displays[0].Frame.Height
	for _, display := range displays {
		frame, usable := display.Frame, display.VisibleFrame
		frame.Y = top - frame.Y - frame.Height
		usable.Y = top - usable.Y - usable.Height
		if frame != *w.Bounds || !validBounds(usable) || usable.X < frame.X || usable.Y < frame.Y ||
			usable.X+usable.Width > frame.X+frame.Width || usable.Y+usable.Height > frame.Y+frame.Height {
			continue
		}
		return x >= usable.X && x < usable.X+usable.Width && y >= usable.Y && y < usable.Y+usable.Height
	}
	return false
}

// For an off-screen frame, move only this window into the primary display's
// usable rectangle. This occurs before capturing, so no old receipt is reused.
func (a *Adapter) placeOnScreen(ctx context.Context, b *binding, w window) error {
	var screen struct{ Width, Height float64 }
	if err := a.call(ctx, "get_screen_size", map[string]any{"session": b.session}, &screen); err != nil {
		return err
	}
	if screen.Width <= 80 || screen.Height <= 120 {
		return refuse("window_display_unknown", "provider did not prove usable display dimensions")
	}
	args := a.targetArgs(b)
	args["x"], args["y"] = 40, 60
	args["width"], args["height"] = min(w.Bounds.Width, screen.Width-80), min(w.Bounds.Height, screen.Height-120)
	var moved struct {
		Effect string `json:"effect"`
	}
	if err := a.call(ctx, "set_window_frame", args, &moved); err != nil {
		return err
	}
	if moved.Effect != "confirmed" {
		return refuse("window_off_screen", "owned window could not be placed on screen")
	}
	return nil
}
