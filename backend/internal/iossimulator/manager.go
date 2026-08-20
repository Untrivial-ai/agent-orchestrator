// Package iossimulator manages AO's single shared iOS Simulator through
// Apple's xcrun simctl command-line interface.
package iossimulator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// CommandRunner executes an external simulator command.
type CommandRunner func(name string, args ...string) ([]byte, error)

// Status describes the managed simulator state.
type Status struct {
	Available    bool   `json:"available"`
	DeviceID     string `json:"deviceId,omitempty"`
	Name         string `json:"name,omitempty"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
	ScreenWidth  int    `json:"screenWidth,omitempty"`
	ScreenHeight int    `json:"screenHeight,omitempty"`
}

// Device is a simulator available to AO, including devices it did not create.
type Device struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Runtime  string `json:"runtime,omitempty"`
}

// Manager owns one iOS Simulator instance. A manager is intentionally scoped
// to an AO session by SessionRegistry; this prevents input and frame streams
// from one worker being routed to another worker's device.
type Manager struct {
	mu               sync.Mutex
	run              CommandRunner
	device           Status
	screenshotWidth  int
	screenshotHeight int
	lastRestart      time.Time
	restartAttempts  int
	frames           *FrameSource
	windowBounds     func() (windowBounds, error)
	post             func(Input) error
	postPtr          func(action string, x, y, x2, y2 float64) error
	transport        InputTransport
}

// SetInputTransport installs direct simulator HID injection for this manager.
// It is deliberately injectable so CI can use a fake and future Xcode HID
// implementations do not leak into the HTTP or renderer layers.
func (m *Manager) SetInputTransport(transport InputTransport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transport = transport
}

// SessionRegistry keeps simulator ownership explicit. The registry does not
// shut down devices when a manager is discarded: callers may attach to a
// pre-existing simulator and must not affect devices they did not create.
type SessionRegistry struct {
	mu      sync.Mutex
	items   map[string]*Manager
	newFunc func() *Manager
}

// NewSessionRegistry builds a registry that lazily creates per-session
// managers via newFunc (defaulting to New).
func NewSessionRegistry(newFunc func() *Manager) *SessionRegistry {
	if newFunc == nil {
		newFunc = New
	}
	return &SessionRegistry{items: make(map[string]*Manager), newFunc: newFunc}
}

// For returns the manager for sessionID, creating it on first use.
func (r *SessionRegistry) For(sessionID string) *Manager {
	if sessionID == "" {
		return r.newFunc()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if manager := r.items[sessionID]; manager != nil {
		return manager
	}
	manager := r.newFunc()
	r.items[sessionID] = manager
	return manager
}

// NativeScreenshot invokes the optional ScreenCaptureKit helper in single-shot
// mode. The helper path is supplied by packaging/runtime wiring; keeping it
// optional preserves the simctl screenshot fallback for development and
// non-macOS builds. The captured frame's size is recorded so input mapping has
// an authoritative framebuffer to scale against.
func (m *Manager) NativeScreenshot() ([]byte, error) {
	helper := captureHelperPath()
	if helper == "" {
		return nil, fmt.Errorf("AO_IOS_CAPTURE_HELPER is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The helper path is deliberately configurable for development and packaging.
	data, err := exec.CommandContext(ctx, helper, "--once").Output() // #nosec G702 -- helper is an explicit local AO configuration.
	if err != nil {
		return nil, fmt.Errorf("ScreenCaptureKit helper: %w", err)
	}
	if width, height, ok := pngDimensions(data); ok {
		m.recordCaptureSize(width, height)
	}
	return data, nil
}

// Frames exposes the shared simulator capture stream. Every WebSocket viewer
// subscribes through it, so the daemon runs exactly one ScreenCaptureKit
// process for all subscribers.
func (m *Manager) Frames() *FrameSource { return m.frames }

// Install installs an app bundle on the managed simulator.
func (m *Manager) Install(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.device.DeviceID == "" {
		return fmt.Errorf("iOS Simulator is not started")
	}
	_, err := m.run("xcrun", "simctl", "install", m.device.DeviceID, path)
	return err
}

// Launch starts an installed app by bundle identifier.
func (m *Manager) Launch(bundle string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.device.DeviceID == "" {
		return fmt.Errorf("iOS Simulator is not started")
	}
	_, err := m.run("xcrun", "simctl", "launch", m.device.DeviceID, bundle)
	return err
}

// Terminate stops an app by bundle identifier.
func (m *Manager) Terminate(bundle string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.device.DeviceID == "" {
		return fmt.Errorf("iOS Simulator is not started")
	}
	_, err := m.run("xcrun", "simctl", "terminate", m.device.DeviceID, bundle)
	return err
}

// Screenshot captures the managed simulator display as PNG data.
func (m *Manager) Screenshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.device.DeviceID == "" {
		return nil, fmt.Errorf("iOS Simulator is not started")
	}
	data, err := m.run("xcrun", "simctl", "io", m.device.DeviceID, "screenshot", "-")
	if err != nil {
		return nil, fmt.Errorf("capture simulator screenshot: %w", err)
	}
	if width, height, ok := pngDimensions(data); ok {
		m.recordCaptureSize(width, height)
	}
	return data, nil
}

// pngDimensions extracts the pixel size of a PNG frame without a full decode.
func pngDimensions(data []byte) (int, int, bool) {
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

// recordCaptureSize keeps the framebuffer size used by the input mapping in
// sync with every capture path (native helper, simctl screenshot, frame stream).
func (m *Manager) recordCaptureSize(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.screenshotWidth, m.screenshotHeight = width, height
	if m.device.State == "Booted" {
		m.device.ScreenWidth, m.device.ScreenHeight = width, height
	}
}

// New creates a manager using the real command runner.
func New() *Manager { return NewWithRunner(defaultRunner) }

// NewWithRunner creates a manager with an injected command runner.
func NewWithRunner(run CommandRunner) *Manager {
	if run == nil {
		run = defaultRunner
	}
	return &Manager{
		run:          run,
		frames:       NewH264FrameSource(captureHelperPath()),
		windowBounds: simulatorWindowBounds,
		post:         defaultPostInput,
		postPtr:      defaultPostPointer,
	}
}

// ListDevices returns all available iOS simulator devices without changing
// session ownership or boot state.
func (m *Manager) ListDevices() ([]Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out, err := m.run("xcrun", "simctl", "list", "devices", "-j")
	if err != nil {
		return nil, fmt.Errorf("list simulator devices: %w", err)
	}
	var payload struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("decode simulator devices: %w", err)
	}
	devices := make([]Device, 0)
	for runtime, entries := range payload.Devices {
		for _, d := range entries {
			devices = append(devices, Device{DeviceID: d.UDID, Name: d.Name, State: d.State, Runtime: runtime})
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	return devices, nil
}

// StartDevice attaches this manager to an explicit UDID and boots it. It is
// the session-safe alternative to Start, which creates AO's default device.
func (m *Manager) StartDevice(deviceID string) (Status, error) {
	if strings.TrimSpace(deviceID) == "" {
		return Status{}, fmt.Errorf("device id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.deviceState(deviceID)
	if err != nil {
		return Status{}, err
	}
	m.device = Status{Available: true, DeviceID: deviceID, State: state}
	if state != "Booted" {
		if _, err := m.run("xcrun", "simctl", "boot", deviceID); err != nil && !strings.Contains(err.Error(), "already booted") {
			return m.device, err
		}
		if _, err := m.run("xcrun", "simctl", "bootstatus", deviceID, "-b"); err != nil {
			return m.device, fmt.Errorf("wait for simulator boot: %w", err)
		}
	}
	m.device.State = "Booted"
	m.device.ScreenWidth, m.device.ScreenHeight = m.captureSize()
	return m.device, nil
}

func defaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Status returns the current managed simulator state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.device.DeviceID == "" {
		return Status{State: "uninitialized"}
	}
	state, err := m.deviceState(m.device.DeviceID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			m.device.State = "stale"
			m.device.Error = err.Error()
			return m.device
		}
		m.device.Error = err.Error()
		return m.device
	}
	m.device.State = state
	if state == "Booted" {
		_ = m.ensureSimulatorProcess()
		m.device.ScreenWidth, m.device.ScreenHeight = m.captureSize()
	}
	m.device.Error = ""
	return m.device
}

// captureSize returns the authoritative framebuffer size: the live frame
// stream wins, with the last screenshot as the fallback.
func (m *Manager) captureSize() (int, int) {
	if width, height := m.frames.Size(); width > 0 && height > 0 {
		return width, height
	}
	return m.screenshotWidth, m.screenshotHeight
}

// Start creates, boots, and supervises the managed simulator.
func (m *Manager) Start() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.device.DeviceID == "" {
		device, err := m.ensureDevice()
		if err != nil {
			return Status{}, err
		}
		m.device = device
	}
	state, err := m.deviceState(m.device.DeviceID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			m.device = Status{}
			device, createErr := m.ensureDevice()
			if createErr != nil {
				return Status{}, createErr
			}
			m.device = device
			state = "Shutdown"
		} else {
			return m.device, err
		}
	}
	if state != "Booted" {
		if _, err := m.run("xcrun", "simctl", "boot", m.device.DeviceID); err != nil && !strings.Contains(err.Error(), "already booted") {
			return m.device, err
		}
		// Simulator.app owns the visible window; simctl only boots the device.
		_, _ = m.run("open", "-a", "Simulator")
		if _, err := m.run("xcrun", "simctl", "bootstatus", m.device.DeviceID, "-b"); err != nil {
			return m.device, fmt.Errorf("wait for simulator boot: %w", err)
		}
	}
	if err := m.ensureSimulatorProcess(); err != nil {
		return m.device, err
	}
	m.device.State = "Booted"
	m.device.ScreenWidth, m.device.ScreenHeight = m.captureSize()
	return m.device, nil
}

func (m *Manager) ensureSimulatorProcess() error {
	if _, err := m.run("pgrep", "-x", "Simulator"); err == nil {
		m.restartAttempts = 0
		return nil
	}
	backoff := time.Duration(1<<minInt(m.restartAttempts, 4)) * time.Second
	if !m.lastRestart.IsZero() && time.Since(m.lastRestart) < backoff {
		return fmt.Errorf("simulator.app restart backoff active")
	}
	m.lastRestart = time.Now()
	m.restartAttempts++
	if _, err := m.run("open", "-a", "Simulator"); err != nil {
		return fmt.Errorf("restart Simulator.app: %w", err)
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Stop shuts down the managed simulator device.
func (m *Manager) Stop() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.device.DeviceID == "" {
		return Status{State: "stopped"}, nil
	}
	state, err := m.deviceState(m.device.DeviceID)
	if err != nil {
		return m.device, err
	}
	if state == "Booted" {
		if _, err := m.run("xcrun", "simctl", "shutdown", m.device.DeviceID); err != nil {
			return m.device, err
		}
	}
	m.device.State = "Shutdown"
	return m.device, nil
}

func (m *Manager) ensureDevice() (Status, error) {
	devtypes, err := m.run("xcrun", "simctl", "list", "devicetypes", "-j")
	if err != nil {
		return Status{}, fmt.Errorf("list simulator device types: %w", err)
	}
	runtimes, err := m.run("xcrun", "simctl", "list", "runtimes", "-j")
	if err != nil {
		return Status{}, fmt.Errorf("list simulator runtimes: %w", err)
	}
	typeID, typeName, err := newestIPhoneType(devtypes)
	if err != nil {
		return Status{}, err
	}
	runtimeID, err := newestIOSRuntime(runtimes)
	if err != nil {
		return Status{}, err
	}
	name := "AO iPhone"
	out, err := m.run("xcrun", "simctl", "create", name, typeID, runtimeID)
	if err != nil {
		return Status{}, fmt.Errorf("create simulator: %w", err)
	}
	return Status{Available: true, DeviceID: strings.TrimSpace(string(out)), Name: typeName, State: "Shutdown"}, nil
}

func (m *Manager) deviceState(id string) (string, error) {
	out, err := m.run("xcrun", "simctl", "list", "devices", "-j")
	if err != nil {
		return "", fmt.Errorf("list simulator devices: %w", err)
	}
	var payload struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", fmt.Errorf("decode simulator devices: %w", err)
	}
	for _, devices := range payload.Devices {
		for _, device := range devices {
			if device.UDID == id {
				return device.State, nil
			}
		}
	}
	return "", fmt.Errorf("simulator %s not found", id)
}

func newestIPhoneType(out []byte) (string, string, error) {
	var payload struct {
		Devicetypes []struct {
			Identifier string `json:"identifier"`
			Name       string `json:"name"`
		} `json:"devicetypes"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", "", fmt.Errorf("decode device types: %w", err)
	}
	var matches []struct{ id, name string }
	for _, d := range payload.Devicetypes {
		if strings.HasPrefix(d.Name, "iPhone") && !strings.Contains(d.Name, "Pro") {
			matches = append(matches, struct{ id, name string }{d.Identifier, d.Name})
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no iPhone simulator device type installed")
	}
	for _, match := range matches {
		if match.name == "iPhone 17" {
			return match.id, match.name, nil
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].name > matches[j].name })
	return matches[0].id, matches[0].name, nil
}

func newestIOSRuntime(out []byte) (string, error) {
	var payload struct {
		Runtimes []struct {
			Identifier string `json:"identifier"`
			Name       string `json:"name"`
			Available  bool   `json:"isAvailable"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", fmt.Errorf("decode simulator runtimes: %w", err)
	}
	var matches []string
	for _, r := range payload.Runtimes {
		if r.Available && strings.HasPrefix(r.Identifier, "com.apple.CoreSimulator.SimRuntime.iOS-") {
			matches = append(matches, r.Identifier)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no available iOS simulator runtime installed")
	}
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}
