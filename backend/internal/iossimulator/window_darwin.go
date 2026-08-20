//go:build darwin

package iossimulator

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type windowBounds struct{ X, Y, Width, Height float64 }

// focusSimulator makes Simulator.app the active application so its device
// shortcuts (Home, rotate) land on it. The app is already running at this
// point; `open -a` activates the existing instance without launching a second
// one. Capture deliberately does not depend on this — only the shortcut
// actions call it.
func focusSimulator() error {
	out, err := exec.Command("open", "-a", "Simulator").Output()
	if err != nil {
		return fmt.Errorf("activate Simulator.app: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// lastFocusTime throttles Simulator.app activation so text/key bursts do not
// re-front the app (and sleep 150 ms) on every keystroke.
var lastFocusTime time.Time

// focusBefore activates Simulator.app before running post. Text and keyboard
// keys are delivered through Simulator.app, so without this they would land in
// whoever is frontmost. Re-activation is throttled to roughly once per burst;
// the first activation sleeps briefly because Simulator needs a moment before
// it will accept posted events.
func focusBefore(post func() error) error {
	if time.Since(lastFocusTime) > 2*time.Second {
		lastFocusTime = time.Now()
		err := focusSimulator()
		if err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return post()
}

func simulatorWindowBounds() (windowBounds, error) {
	script := `tell application "System Events" to tell process "Simulator" to set r to {position of window 1, size of window 1} as text`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return windowBounds{}, fmt.Errorf("accessibility permission may be missing: %w", err)
	}
	parts := strings.FieldsFunc(string(out), func(r rune) bool { return r == ',' || r == '}' || r == '{' || r == ' ' || r == '\n' })
	if len(parts) < 4 {
		return windowBounds{}, fmt.Errorf("unexpected Simulator window bounds %q", out)
	}
	values := make([]float64, 4)
	for i := range values {
		values[i], err = strconv.ParseFloat(strings.TrimSpace(parts[i]), 64)
		if err != nil {
			return windowBounds{}, err
		}
	}
	return windowBounds{X: values[0], Y: values[1], Width: values[2], Height: values[3]}, nil
}
