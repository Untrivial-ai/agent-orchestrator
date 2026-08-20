//go:build !darwin || !cgo

package iossimulator

// Permissions reports whether macOS has granted simulator control permissions.
type Permissions struct {
	ScreenRecording bool `json:"screenRecording"`
	Accessibility   bool `json:"accessibility"`
	Supported       bool `json:"supported"`
}

// PermissionsStatus reports unavailable permissions on unsupported platforms.
func PermissionsStatus() Permissions     { return Permissions{} }
func tap(x, y float64) error             { return unsupportedError{} }
func text(value string) error            { return unsupportedError{} }
func key(code int) error                 { return unsupportedError{} }
func swipe(x1, y1, x2, y2 float64) error { return unsupportedError{} }
func home() error                        { return unsupportedError{} }
func lock() error                        { return unsupportedError{} }
func rotateLeft() error                  { return unsupportedError{} }
func rotateRight() error                 { return unsupportedError{} }

type unsupportedError struct{}

func (unsupportedError) Error() string { return "iOS Simulator input is only supported on macOS" }
