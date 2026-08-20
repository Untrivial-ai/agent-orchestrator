//go:build !darwin

package iossimulator

import "fmt"

type windowBounds struct{ X, Y, Width, Height float64 }

// focusSimulator is referenced by the macOS-only shortcut path
// (postSimulatorShortcut in permissions_darwin.go); on other platforms it is
// a compilation-parity stub with no caller.
func focusSimulator() error { //nolint:unused // non-darwin parity stub
	return fmt.Errorf("simulator activation is only supported on macOS")
}

// focusBefore is the non-darwin compilation-parity stub for the text/key
// delivery path in defaultPostInput.
func focusBefore(post func() error) error { //nolint:unused // non-darwin parity stub
	return fmt.Errorf("simulator activation is only supported on macOS")
}

func simulatorWindowBounds() (windowBounds, error) {
	return windowBounds{}, fmt.Errorf("simulator window mapping is only supported on macOS")
}
