package iossimulator

import "fmt"

// Input describes a tap, swipe, text, key, or device-button event for the
// simulator. Tap and swipe coordinates are expressed in device framebuffer
// pixels — the same coordinate space as the frames streamed to the panel — so
// the frontend can map pointer events through its own scaling/letterboxing and
// the backend never guesses about CSS sizes.
type Input struct {
	Action  string  `json:"action"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	X2      float64 `json:"x2"`
	Y2      float64 `json:"y2"`
	Text    string  `json:"text"`
	KeyCode int     `json:"keyCode"`
}

// defaultPostInput dispatches non-pointer actions to the platform input layer.
// Text and keys are delivered through Simulator.app, so they are preceded by a
// (throttled) activation of the app — otherwise the keys would land in
// whoever is frontmost, not the simulator.
func defaultPostInput(input Input) error {
	switch input.Action {
	case "text":
		return focusBefore(func() error { return text(input.Text) })
	case "key":
		return focusBefore(func() error { return key(input.KeyCode) })
	case "home":
		return home()
	case "lock":
		return lock()
	case "rotateLeft":
		return rotateLeft()
	case "rotateRight":
		return rotateRight()
	default:
		return fmt.Errorf("unsupported iOS input action %q", input.Action)
	}
}

// defaultPostPointer dispatches pointer actions to the platform input layer.
// All coordinates are global screen pixels by this point.
func defaultPostPointer(action string, x, y, x2, y2 float64) error {
	switch action {
	case "tap":
		return tap(x, y)
	case "swipe":
		return swipe(x, y, x2, y2)
	default:
		return fmt.Errorf("unsupported pointer action %q", action)
	}
}

// Input sends an event to the managed simulator.
func (m *Manager) Input(input Input) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.device.DeviceID == "" {
		return fmt.Errorf("iOS Simulator is not started")
	}
	state, err := m.deviceState(m.device.DeviceID)
	if err != nil {
		return err
	}
	if state != "Booted" {
		return fmt.Errorf("iOS Simulator is not booted")
	}
	if m.transport != nil {
		return m.transport.Send(input)
	}
	switch input.Action {
	case "tap", "swipe":
		return m.postPointer(input)
	case "text", "key", "home", "lock", "rotateLeft", "rotateRight":
		if m.post == nil {
			return fmt.Errorf("iOS Simulator input is not wired")
		}
		return m.post(input)
	default:
		return fmt.Errorf("unsupported iOS input action %q", input.Action)
	}
}

// postPointer maps framebuffer coordinates onto the Simulator window's screen
// bounds and posts a CG mouse event. This window lookup is the single place
// the daemon touches Simulator.app geometry; everything upstream — the panel,
// the capture stream, the wire contract — speaks framebuffer pixels, so the
// mapping stays correct at any panel size and letterboxing.
func (m *Manager) postPointer(input Input) error {
	width, height := m.captureSize()
	if width <= 0 || height <= 0 {
		return fmt.Errorf("simulator capture size is unknown; capture the screen once before sending input")
	}
	if input.X < 0 || input.X > float64(width) || input.Y < 0 || input.Y > float64(height) {
		return fmt.Errorf("input coordinates outside the simulator frame (%d x %d)", width, height)
	}
	if input.Action == "swipe" {
		if input.X2 < 0 || input.X2 > float64(width) || input.Y2 < 0 || input.Y2 > float64(height) {
			return fmt.Errorf("swipe end coordinates outside the simulator frame (%d x %d)", width, height)
		}
	}
	if m.windowBounds == nil {
		return fmt.Errorf("iOS Simulator window mapping is not wired")
	}
	bounds, err := m.windowBounds()
	if err != nil {
		return fmt.Errorf("find Simulator window: %w", err)
	}
	x := bounds.X + input.X/float64(width)*bounds.Width
	y := bounds.Y + input.Y/float64(height)*bounds.Height
	if m.postPtr == nil {
		return fmt.Errorf("iOS Simulator input is not wired")
	}
	switch input.Action {
	case "tap":
		return m.postPtr(input.Action, x, y, 0, 0)
	case "swipe":
		x2 := bounds.X + input.X2/float64(width)*bounds.Width
		y2 := bounds.Y + input.Y2/float64(height)*bounds.Height
		return m.postPtr(input.Action, x, y, x2, y2)
	default:
		return fmt.Errorf("unsupported pointer action %q", input.Action)
	}
}
