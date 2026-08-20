package controllers

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/iossdk"
	"github.com/aoagents/agent-orchestrator/backend/internal/iossimulator"
)

// IOSDeviceController exposes the iOS Simulator HTTP API.
type IOSDeviceController struct {
	Simulator *iossimulator.Manager
	Sessions  *iossimulator.SessionRegistry
}

func (c *IOSDeviceController) manager(r *http.Request) *iossimulator.Manager {
	if c.Sessions != nil {
		if id := r.URL.Query().Get("sessionId"); id != "" {
			return c.Sessions.For(id)
		}
	}
	return c.Simulator
}

// Devices lists selectable simulator devices without changing boot state.
func (c *IOSDeviceController) Devices(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	if m == nil {
		envelope.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "iOS Simulator is not wired"})
		return
	}
	devices, err := m.ListDevices()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "IOS_SIMULATOR_DEVICES", err.Error(), nil)
		return
	}
	response := make([]SimulatorDeviceResponse, len(devices))
	for i, device := range devices {
		response[i] = SimulatorDeviceResponse{DeviceID: device.DeviceID, Name: device.Name, State: device.State, Runtime: device.Runtime}
	}
	envelope.WriteJSON(w, http.StatusOK, response)
}

// Status reports the detected iOS toolchain.
func (c *IOSDeviceController) Status(w http.ResponseWriter, r *http.Request) {
	envelope.WriteJSON(w, http.StatusOK, iosStatusResponse(iossdk.DetectToolchain()))
}

// Recheck refreshes the detected iOS toolchain status.
func (c *IOSDeviceController) Recheck(w http.ResponseWriter, r *http.Request) {
	envelope.WriteJSON(w, http.StatusOK, iosStatusResponse(iossdk.DetectToolchain()))
}

// FetchRuntime explains how to acquire an iOS runtime.
func (c *IOSDeviceController) FetchRuntime(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AcceptAppleID bool `json:"acceptAppleID"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, FetchRuntimeResponse{Success: false, Message: "AO cannot download Xcode. Install Xcode from the Mac App Store or Apple Developer downloads, then recheck the toolchain."})
}

// SimulatorStatus reports the managed simulator state.
func (c *IOSDeviceController) SimulatorStatus(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	if m == nil {
		envelope.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "iOS Simulator is not wired"})
		return
	}
	envelope.WriteJSON(w, http.StatusOK, simulatorStatusResponse(m.Status()))
}

// StartSimulator boots the managed simulator.
func (c *IOSDeviceController) StartSimulator(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	if m == nil {
		envelope.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "iOS Simulator is not wired"})
		return
	}
	var status iossimulator.Status
	var err error
	if deviceID := r.URL.Query().Get("deviceId"); deviceID != "" {
		status, err = m.StartDevice(deviceID)
	} else {
		status, err = m.Start()
	}
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "IOS_SIMULATOR_START", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, simulatorStatusResponse(status))
}

// StopSimulator shuts down the managed simulator.
func (c *IOSDeviceController) StopSimulator(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	if m == nil {
		envelope.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "iOS Simulator is not wired"})
		return
	}
	status, err := m.Stop()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "IOS_SIMULATOR_STOP", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, simulatorStatusResponse(status))
}

// Screenshot captures the managed simulator display.
func (c *IOSDeviceController) Screenshot(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	if m == nil {
		envelope.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "iOS Simulator is not wired"})
		return
	}
	data, err := m.Screenshot()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "IOS_SIMULATOR_SCREENSHOT", err.Error(), nil)
		return
	}
	width, height := m.Frames().Size()
	envelope.WriteJSON(w, http.StatusOK, SimulatorScreenshotResponse{Data: base64.StdEncoding.EncodeToString(data), MimeType: "image/png", Width: width, Height: height})
}

// Permissions reports macOS permissions relevant to simulator control.
func (c *IOSDeviceController) Permissions(w http.ResponseWriter, r *http.Request) {
	status := iossimulator.PermissionsStatus()
	envelope.WriteJSON(w, http.StatusOK, SimulatorPermissionsResponse{ScreenRecording: status.ScreenRecording, Accessibility: status.Accessibility, Supported: status.Supported})
}

// Input sends tap, swipe, text, or key input to the simulator.
func (c *IOSDeviceController) Input(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	if m == nil {
		envelope.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "iOS Simulator is not wired"})
		return
	}
	var request SimulatorInputRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	err := m.Input(iossimulator.Input{Action: request.Action, X: request.X, Y: request.Y, X2: request.X2, Y2: request.Y2, Text: request.Text, KeyCode: request.KeyCode})
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "IOS_SIMULATOR_INPUT", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SimulatorInputResponse{Accepted: true})
}

// Stream streams live simulator frames over WebSocket. The backend runs one
// shared ScreenCaptureKit process (see iossimulator.FrameSource); this handler
// only fans frames out to the connected client. It is registered outside the
// REST timeout group so the socket can stay open indefinitely. Frames carry
// their framebuffer pixel size; error frames keep the panel's connection state
// distinguishable from a dead socket while the capture helper restarts.
func (c *IOSDeviceController) Stream(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	if m == nil {
		http.Error(w, "iOS Simulator is not wired", http.StatusNotImplemented)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "stream ended") }()
	// The first message is intentionally JSON and all H.264 frames after it
	// are binary. This lets WebCodecs initialize once while retaining the PNG
	// JSON fallback for older helpers.
	status := m.Status()
	if err := wsjson.Write(context.Background(), conn, map[string]any{
		"type": "hello", "sessionId": r.URL.Query().Get("sessionId"), "udid": status.DeviceID,
		"width": status.ScreenWidth, "height": status.ScreenHeight, "scale": 1,
	}); err != nil {
		return
	}

	frames, unsubscribe := m.Frames().Subscribe()
	defer unsubscribe()

	stall := time.NewTicker(2 * time.Second)
	defer stall.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame, ok := <-frames:
			if !ok {
				_ = wsjson.Write(context.Background(), conn, map[string]string{"error": "capture stopped"})
				return
			}
			if frame.Codec == "h264" {
				// Binary packet: u32 width, u32 height, Annex-B access unit.
				// Width/height come from a u32 header, but guard the cast to
				// keep the conversion provably in range.
				if frame.Width < 0 || frame.Height < 0 || uint64(frame.Width) > math.MaxUint32 || uint64(frame.Height) > math.MaxUint32 {
					continue
				}
				packet := make([]byte, 8+len(frame.Data))
				binary.BigEndian.PutUint32(packet[:4], uint32(frame.Width))
				binary.BigEndian.PutUint32(packet[4:8], uint32(frame.Height))
				copy(packet[8:], frame.Data)
				if err := conn.Write(context.Background(), websocket.MessageBinary, packet); err != nil {
					return
				}
				continue
			}
			if err := wsjson.Write(context.Background(), conn, SimulatorScreenshotResponse{Data: base64.StdEncoding.EncodeToString(frame.Data), MimeType: "image/png", Width: frame.Width, Height: frame.Height}); err != nil {
				return
			}
		case <-stall.C:
			// No frame for a while: the helper is restarting, the device is
			// off, or the window is gone. Keep the socket alive and surface
			// the reason so the panel can show a disconnected state instead of
			// a frozen frame.
			reason := "capture stalled"
			if lastErr := m.Frames().LastError(); lastErr != nil {
				reason = lastErr.Error()
			}
			_ = wsjson.Write(context.Background(), conn, map[string]string{"error": reason})
		}
	}
}

// InstallApp installs an app bundle on the managed simulator.
func (c *IOSDeviceController) InstallApp(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	var q SimulatorAppRequest
	if json.NewDecoder(r.Body).Decode(&q) != nil || q.AppPath == "" {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_APP", "appPath is required", nil)
		return
	}
	if m == nil {
		http.Error(w, "not wired", http.StatusNotImplemented)
		return
	}
	if err := m.Install(q.AppPath); err != nil {
		envelope.WriteAPIError(w, r, 409, "conflict", "IOS_INSTALL", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, 200, SimulatorInputResponse{Accepted: true})
}

// LaunchApp launches an installed app by bundle identifier.
func (c *IOSDeviceController) LaunchApp(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	var q SimulatorAppRequest
	if json.NewDecoder(r.Body).Decode(&q) != nil || q.BundleID == "" {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_BUNDLE_ID", "bundleId is required", nil)
		return
	}
	if m == nil {
		http.Error(w, "not wired", http.StatusNotImplemented)
		return
	}
	if err := m.Launch(q.BundleID); err != nil {
		envelope.WriteAPIError(w, r, 409, "conflict", "IOS_LAUNCH", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, 200, SimulatorInputResponse{Accepted: true})
}

// TerminateApp stops an app by bundle identifier.
func (c *IOSDeviceController) TerminateApp(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	var q SimulatorAppRequest
	if json.NewDecoder(r.Body).Decode(&q) != nil || q.BundleID == "" {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_BUNDLE_ID", "bundleId is required", nil)
		return
	}
	if m == nil {
		http.Error(w, "not wired", http.StatusNotImplemented)
		return
	}
	if err := m.Terminate(q.BundleID); err != nil {
		envelope.WriteAPIError(w, r, 409, "conflict", "IOS_TERMINATE", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, 200, SimulatorInputResponse{Accepted: true})
}

// BuildApp builds, installs, and optionally launches an iOS app.
func (c *IOSDeviceController) BuildApp(w http.ResponseWriter, r *http.Request) {
	m := c.manager(r)
	var q SimulatorBuildRequest
	if json.NewDecoder(r.Body).Decode(&q) != nil || q.Scheme == "" {
		envelope.WriteAPIError(w, r, 400, "bad_request", "INVALID_BUILD", "scheme is required", nil)
		return
	}
	app, err := iossimulator.BuildApp(q.Project, q.Workspace, q.Scheme, q.DerivedData)
	if err != nil {
		envelope.WriteAPIError(w, r, 409, "conflict", "IOS_BUILD", err.Error(), nil)
		return
	}
	if m == nil {
		http.Error(w, "not wired", http.StatusNotImplemented)
		return
	}
	if err := m.Install(app); err != nil {
		envelope.WriteAPIError(w, r, 409, "conflict", "IOS_INSTALL", err.Error(), nil)
		return
	}
	if q.BundleID != "" {
		if err := m.Launch(q.BundleID); err != nil {
			envelope.WriteAPIError(w, r, 409, "conflict", "IOS_LAUNCH", err.Error(), nil)
			return
		}
	}
	envelope.WriteJSON(w, 200, SimulatorBuildResponse{AppPath: app, Accepted: true})
}

func simulatorStatusResponse(status iossimulator.Status) SimulatorStatusResponse {
	return SimulatorStatusResponse{Available: status.Available, DeviceID: status.DeviceID, Name: status.Name, State: status.State, Error: status.Error, ScreenWidth: status.ScreenWidth, ScreenHeight: status.ScreenHeight}
}

func iosStatusResponse(status iossdk.ToolchainStatus) StatusResponse {
	res := StatusResponse{XcodeDetected: status.XcodeDetected, CLTOnly: status.CLTOnly, SimctlAvailable: status.SimctlAvailable, DefaultRuntimeAvailable: status.DefaultRuntimeAvailable}
	if !status.XcodeDetected {
		res.GuidanceAppStoreURL = iossdk.DefaultGuidance.AppStoreURL
		res.GuidanceDeveloperURL = iossdk.DefaultGuidance.DeveloperURL
		res.GuidanceWhyMissing = iossdk.DefaultGuidance.WhyMissing
	}
	return res
}

// Register mounts the iOS Simulator routes on a Chi router. Long-lived
// surfaces such as the frame stream stay out of this REST group (see
// RegisterStream) so the REST timeout middleware cannot kill them.
func (c *IOSDeviceController) Register(r chi.Router) {
	r.Get("/ios-device/devices", c.Devices)
	r.Get("/ios-device/toolchain/status", c.Status)
	r.Post("/ios-device/toolchain/recheck", c.Recheck)
	r.Post("/ios-device/toolchain/fetch-runtime", c.FetchRuntime)
	r.Get("/ios-device/status", c.SimulatorStatus)
	r.Post("/ios-device/start", c.StartSimulator)
	r.Post("/ios-device/stop", c.StopSimulator)
	r.Get("/ios-device/screenshot", c.Screenshot)
	r.Get("/ios-device/permissions", c.Permissions)
	r.Post("/ios-device/input", c.Input)
	r.Post("/ios-device/app/install", c.InstallApp)
	r.Post("/ios-device/app/launch", c.LaunchApp)
	r.Post("/ios-device/app/terminate", c.TerminateApp)
	r.Post("/ios-device/app/build", c.BuildApp)
}

// RegisterStream mounts the long-lived simulator frame stream directly on the
// root router, mirroring the other stream surfaces that bypass the REST
// timeout middleware.
func (c *IOSDeviceController) RegisterStream(r chi.Router) {
	r.Get("/ios-device/stream", c.Stream)
}
