package iossimulator

import "fmt"

// InputTransport isolates simulator HID injection from the daemon protocol.
// IndigoTransport is the Xcode 26 implementation point; DtuHidTransport is
// reserved for Xcode 27 Device Hub. Neither transport uses CGEvent or a mouse
// cursor, so the renderer and HTTP API do not need to change when Apple’s HID
// endpoint changes.
type InputTransport interface {
	Send(Input) error
	Name() string
}

// IndigoTransport injects HID via the Indigo protocol (Xcode 26).
type IndigoTransport struct{ SendFunc func(Input) error }

// Name reports the transport identifier.
func (t IndigoTransport) Name() string { return "indigo" }

// Send forwards an input event through the transport.
func (t IndigoTransport) Send(input Input) error {
	if t.SendFunc == nil {
		return fmt.Errorf("IndigoHID transport is unavailable")
	}
	return t.SendFunc(input)
}

// DtuHidTransport injects HID via the Device Hub protocol (Xcode 27).
type DtuHidTransport struct{ SendFunc func(Input) error }

// Name reports the transport identifier.
func (t DtuHidTransport) Name() string { return "dtu-hid" }

// Send forwards an input event through the transport.
func (t DtuHidTransport) Send(input Input) error {
	if t.SendFunc == nil {
		return fmt.Errorf("Device Hub HID transport is unavailable")
	}
	return t.SendFunc(input)
}
