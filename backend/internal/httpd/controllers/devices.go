package controllers

import (
	"context"
	"net/http"
	"strings"

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
}

// LocalDevicesController exposes session-scoped iOS Simulator and Android Emulator operations.
type LocalDevicesController struct{ Svc LocalDeviceService }

// Register mounts the bounded local device API.
func (c *LocalDevicesController) Register(r chi.Router) {
	r.Get("/devices/status", c.status)
	r.Get("/devices", c.list)
	r.Post("/devices/commands", c.execute)
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
