package controllers

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	devicesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/device"
)

const (
	deviceCapabilityHeader        = "X-AO-Device-Capability"
	desktopDeviceCapabilityHeader = "X-AO-Desktop-Device-Capability"
)

// LocalDeviceService is the loopback local virtual-device surface.
type LocalDeviceService interface {
	Status(context.Context, domain.SessionID, devicesvc.Credentials) (devicesvc.Status, error)
	List(context.Context, domain.SessionID, devicesvc.Credentials) (devicesvc.Inventory, error)
	Execute(context.Context, domain.SessionID, devicesvc.Credentials, devicesvc.Command) (devicesvc.Result, error)
	SetupStatus(context.Context, domain.SessionID, devicesvc.Credentials) ([]domain.DeviceSetup, error)
	ExecuteSetup(context.Context, domain.SessionID, devicesvc.Credentials, devicesvc.SetupCommand) (domain.DeviceSetup, error)
}

// LocalDevicesController exposes session-scoped iOS Simulator and Android Emulator operations.
type LocalDevicesController struct{ Svc LocalDeviceService }

type localDeviceStreamService interface {
	ResolveStream(string) (devicesvc.StreamAccess, bool)
}

// Register mounts the bounded local device API.
func (c *LocalDevicesController) Register(r chi.Router) {
	r.Get("/devices/status", c.status)
	r.Get("/devices", c.list)
	r.Post("/devices/commands", c.execute)
	r.Get("/devices/setup", c.setupStatus)
	r.Post("/devices/setup", c.executeSetup)
}

// RegisterStream mounts the long-lived, capability-scoped media proxy outside
// the ordinary REST timeout. Only fixed stream and input routes are mapped.
func (c *LocalDevicesController) RegisterStream(r chi.Router) {
	r.Get("/devices/stream/{ticket}/{channel}", c.stream)
}

func (c *LocalDevicesController) stream(w http.ResponseWriter, r *http.Request) {
	streams, ok := c.Svc.(localDeviceStreamService)
	if !ok {
		http.NotFound(w, r)
		return
	}
	access, ok := streams.ResolveStream(chi.URLParam(r, "ticket"))
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusUnauthorized, "unauthorized", "DEVICE_STREAM_EXPIRED", "Device stream access expired", nil)
		return
	}
	upstream, websocketRoute, ok := deviceStreamRoute(access, chi.URLParam(r, "channel"))
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "DEVICE_STREAM_CHANNEL_NOT_FOUND", "Device stream channel was not found", nil)
		return
	}
	if websocketRoute {
		proxyDeviceWebSocket(w, r, upstream)
		return
	}
	proxyDeviceHTTP(w, r, upstream)
}

func deviceStreamRoute(access devicesvc.StreamAccess, channel string) (string, bool, bool) {
	device := url.PathEscape(access.DeviceID)
	switch access.Platform {
	case domain.DevicePlatformIOS:
		base := access.Origin + "/vendor/serve-sim/helper/" + device
		switch channel {
		case "mjpeg":
			return base + "/stream.mjpeg", false, true
		case "avcc":
			return base + "/stream.avcc", false, true
		case "config":
			return base + "/config", false, true
		case "health":
			return base + "/health", false, true
		case "input":
			return strings.Replace(access.Origin, "http://", "ws://", 1) + "/vendor/serve-sim/helper/ws?device=" + url.QueryEscape(access.DeviceID), true, true
		}
	case domain.DevicePlatformAndroid:
		if channel == "input" {
			return strings.Replace(access.Origin, "http://", "ws://", 1) + "/vendor/serve-emu/ws?device=" + url.QueryEscape(access.DeviceID) + "&frame-meta=1", true, true
		}
	}
	return "", false, false
}

func proxyDeviceHTTP(w http.ResponseWriter, r *http.Request, upstream string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, http.NoBody)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadGateway, "bad_gateway", "DEVICE_STREAM_INVALID", "Invalid device stream", nil)
		return
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadGateway, "bad_gateway", "DEVICE_STREAM_UNAVAILABLE", "Device stream unavailable", nil)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		envelope.WriteAPIError(w, r, http.StatusBadGateway, "bad_gateway", "DEVICE_STREAM_UNAVAILABLE", "Device stream unavailable", nil)
		return
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func proxyDeviceWebSocket(w http.ResponseWriter, r *http.Request, upstream string) {
	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "device stream closed") }()
	server, response, err := websocket.Dial(r.Context(), upstream, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		_ = client.Close(websocket.StatusTryAgainLater, "device helper unavailable")
		return
	}
	defer func() { _ = server.Close(websocket.StatusNormalClosure, "device stream closed") }()
	errCh := make(chan error, 2)
	copyMessages := func(dst, src *websocket.Conn) {
		for {
			kind, reader, err := src.Reader(r.Context())
			if err != nil {
				errCh <- err
				return
			}
			writer, err := dst.Writer(r.Context(), kind)
			if err == nil {
				_, err = io.Copy(writer, reader)
				closeErr := writer.Close()
				if err == nil {
					err = closeErr
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}
	go copyMessages(server, client)
	go copyMessages(client, server)
	<-errCh
}

func (c *LocalDevicesController) setupStatus(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/devices/setup")
		return
	}
	sessionID := domain.SessionID(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	setups, err := c.Svc.SetupStatus(r.Context(), sessionID, deviceCredentials(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if setups == nil {
		setups = []domain.DeviceSetup{}
	}
	envelope.WriteJSON(w, http.StatusOK, DeviceSetupResponse{SessionID: sessionID, Setups: setups})
}

func (c *LocalDevicesController) executeSetup(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/devices/setup")
		return
	}
	var in DeviceSetupCommandRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	setup, err := c.Svc.ExecuteSetup(r.Context(), in.SessionID, deviceCredentials(r), devicesvc.SetupCommand{Platform: in.Platform, Action: in.Action, LicenseAccepted: in.LicenseAccepted})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, DeviceSetupCommandResponse{SessionID: in.SessionID, Setup: setup})
}

func (c *LocalDevicesController) status(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/devices/status")
		return
	}
	sessionID := domain.SessionID(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	result, err := c.Svc.Status(r.Context(), sessionID, deviceCredentials(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, DeviceStatusResponse{
		SessionID: sessionID, Capabilities: result.Capabilities, Attachment: result.Attachment,
	})
}

func (c *LocalDevicesController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/devices")
		return
	}
	sessionID := domain.SessionID(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	result, err := c.Svc.List(r.Context(), sessionID, deviceCredentials(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if result.Devices == nil {
		result.Devices = []domain.Device{}
	}
	envelope.WriteJSON(w, http.StatusOK, DeviceListResponse{
		SessionID: sessionID, Devices: result.Devices, Errors: result.Errors,
	})
}

func (c *LocalDevicesController) execute(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/devices/commands")
		return
	}
	var in DeviceCommandRequest
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	result, err := c.Svc.Execute(r.Context(), in.SessionID, deviceCredentials(r), devicesvc.Command{
		Action: in.Action, DeviceID: in.DeviceID, Platform: in.Platform, InteractiveOnly: in.InteractiveOnly,
		Ref: in.Ref, X: in.X, Y: in.Y, X1: in.X1, Y1: in.Y1, X2: in.X2, Y2: in.Y2,
		Text: in.Text, Key: in.Key, Confirmed: in.Confirmed,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, DeviceCommandResponse{
		SessionID: in.SessionID, Action: result.Action, Attachment: result.Attachment, Result: result.Value,
	})
}

func deviceCredentials(r *http.Request) devicesvc.Credentials {
	return devicesvc.Credentials{
		Agent: r.Header.Get(deviceCapabilityHeader), Desktop: r.Header.Get(desktopDeviceCapabilityHeader),
	}
}
