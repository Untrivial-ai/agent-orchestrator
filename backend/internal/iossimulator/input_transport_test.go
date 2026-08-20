package iossimulator

import "testing"

func TestInputTransportIsDirectAndInjectable(t *testing.T) {
	called := false
	transport := IndigoTransport{SendFunc: func(input Input) error {
		called = input.Action == "tap"
		return nil
	}}
	m := newBootedManager()
	m.SetInputTransport(transport)
	m.recordCaptureSize(100, 200)
	if err := m.Input(Input{Action: "tap", X: 10, Y: 20}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected direct HID transport to receive input")
	}
}
