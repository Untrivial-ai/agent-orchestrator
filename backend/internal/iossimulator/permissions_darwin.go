//go:build darwin && cgo

package iossimulator

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>
#include <CoreGraphics/CoreGraphics.h>
#include <unistd.h>

static int ao_screen_recording(void) { return CGPreflightScreenCaptureAccess() ? 1 : 0; }
static int ao_accessibility(void) { return AXIsProcessTrusted() ? 1 : 0; }
static int ao_tap(double x, double y) {
    CGPoint p = CGPointMake(x, y);
    CGEventRef down = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDown, p, kCGMouseButtonLeft);
    CGEventRef up = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseUp, p, kCGMouseButtonLeft);
    if (!down || !up) { if (down) CFRelease(down); if (up) CFRelease(up); return 0; }
    CGEventPost(kCGHIDEventTap, down); CGEventPost(kCGHIDEventTap, up);
    CFRelease(down); CFRelease(up); return 1;
}
static int ao_swipe(double x1, double y1, double x2, double y2) {
    CGPoint a = CGPointMake(x1, y1), b = CGPointMake(x2, y2);
    CGEventRef down = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDown, a, kCGMouseButtonLeft);
    CGEventRef drag = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDragged, b, kCGMouseButtonLeft);
    CGEventRef up = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseUp, b, kCGMouseButtonLeft);
    if (!down || !drag || !up) { if (down) CFRelease(down); if (drag) CFRelease(drag); if (up) CFRelease(up); return 0; }
    CGEventPost(kCGHIDEventTap, down); usleep(16000); CGEventPost(kCGHIDEventTap, drag); CGEventPost(kCGHIDEventTap, up);
    CFRelease(down); CFRelease(drag); CFRelease(up); return 1;
}
static int ao_key(int code) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)code, true);
    CGEventRef up = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)code, false);
    if (!down || !up) { if (down) CFRelease(down); if (up) CFRelease(up); return 0; }
    CGEventPost(kCGHIDEventTap, down); CGEventPost(kCGHIDEventTap, up); CFRelease(down); CFRelease(up); return 1;
}
static int ao_key_with_flags(int64_t flags, int code) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)code, true);
    CGEventRef up = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)code, false);
    if (!down || !up) { if (down) CFRelease(down); if (up) CFRelease(up); return 0; }
    CGEventSetFlags(down, flags); CGEventSetFlags(up, flags);
    CGEventPost(kCGHIDEventTap, down); usleep(12000); CGEventPost(kCGHIDEventTap, up);
    CFRelease(down); CFRelease(up); return 1;
}
static int ao_text(const char *value, int length) {
    CGEventRef event = CGEventCreateKeyboardEvent(NULL, 0, true);
    if (!event) return 0;
    UniChar *chars = (UniChar *)value;
    CGEventKeyboardSetUnicodeString(event, (size_t)length, chars);
    CGEventPost(kCGHIDEventTap, event); CFRelease(event); return 1;
}
*/
import "C" //nolint:gocritic // cgo requires the C pseudo-package and unsafe import.

import (
	"fmt"
	"time"

	//nolint:gocritic // cgo requires unsafe for passing text buffers.
	"unsafe"
)

// Permissions reports whether macOS has granted simulator control permissions.
type Permissions struct {
	ScreenRecording bool `json:"screenRecording"`
	Accessibility   bool `json:"accessibility"`
	Supported       bool `json:"supported"`
}

// PermissionsStatus reports the current macOS simulator permissions.
func PermissionsStatus() Permissions {
	return Permissions{ScreenRecording: C.ao_screen_recording() != 0, Accessibility: C.ao_accessibility() != 0, Supported: true}
}
func tap(x, y float64) error {
	if C.ao_tap(C.double(x), C.double(y)) == 0 {
		return fmt.Errorf("unable to create mouse event")
	}
	return nil
}
func swipe(x1, y1, x2, y2 float64) error {
	if C.ao_swipe(C.double(x1), C.double(y1), C.double(x2), C.double(y2)) == 0 {
		return fmt.Errorf("unable to create swipe event")
	}
	return nil
}
func key(code int) error {
	if C.ao_key(C.int(code)) == 0 {
		return fmt.Errorf("unable to create keyboard event")
	}
	return nil
}
func text(value string) error {
	data := []byte(value)
	if len(data) == 0 {
		return nil
	}
	if C.ao_text((*C.char)(unsafe.Pointer(&data[0])), C.int(len(data))) == 0 {
		return fmt.Errorf("unable to create text event")
	}
	return nil
}

// CG event flag masks (CoreGraphics) and macOS virtual key codes used by the
// Simulator device-shortcut actions. Simulator maps Cmd+Shift+H to Home and
// Cmd+Left/Right to rotate.
const (
	commandFlag = 1 << 20 // kCGEventFlagMaskCommand
	shiftFlag   = 1 << 17 // kCGEventFlagMaskShift

	keyH          = 4
	keyL          = 37
	keyLeftArrow  = 123
	keyRightArrow = 124
)

// postSimulatorShortcut brings Simulator.app to the front (its device
// shortcuts are delivered to the active application) and posts the shortcut.
// The activation is momentary: capture never depends on the window being
// visible, only these button shortcuts need Simulator active.
func postSimulatorShortcut(flags int64, code int) error {
	err := focusSimulator()
	if err != nil {
		return fmt.Errorf("focus Simulator: %w", err)
	}
	// Simulator needs a moment to become the active application; without the
	// pause the shortcut lands on whoever was frontmost before we activated it.
	time.Sleep(150 * time.Millisecond)
	if C.ao_key_with_flags(C.int64_t(flags), C.int(code)) == 0 {
		return fmt.Errorf("unable to create keyboard event")
	}
	return nil
}

func home() error {
	return postSimulatorShortcut(commandFlag|shiftFlag, keyH)
}

// lock locks the managed device. Simulator.app maps ⌘L to Device > Lock,
// delivered as a CG keyboard shortcut to the active application.
func lock() error {
	return postSimulatorShortcut(commandFlag, keyL)
}

func rotateLeft() error {
	return postSimulatorShortcut(commandFlag, keyLeftArrow)
}

func rotateRight() error {
	return postSimulatorShortcut(commandFlag, keyRightArrow)
}
