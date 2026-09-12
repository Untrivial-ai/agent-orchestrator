package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	devicesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/device"
)

type fakeLocalDeviceService struct {
	session     domain.SessionID
	credentials devicesvc.Credentials
	command     devicesvc.Command
}

func (f *fakeLocalDeviceService) Status(_ context.Context, session domain.SessionID, credentials devicesvc.Credentials) (devicesvc.Status, error) {
	f.session, f.credentials = session, credentials
	return devicesvc.Status{Capabilities: []domain.DevicePlatformCapability{{Platform: domain.DevicePlatformIOS, Available: true}}}, nil
}

func (f *fakeLocalDeviceService) List(_ context.Context, session domain.SessionID, credentials devicesvc.Credentials) (devicesvc.Inventory, error) {
	f.session, f.credentials = session, credentials
	return devicesvc.Inventory{Devices: []domain.Device{{ID: "ios-1", Name: "iPhone", Platform: domain.DevicePlatformIOS}}}, nil
}

func (f *fakeLocalDeviceService) Execute(_ context.Context, session domain.SessionID, credentials devicesvc.Credentials, command devicesvc.Command) (devicesvc.Result, error) {
	f.session, f.credentials, f.command = session, credentials, command
	return devicesvc.Result{Action: command.Action, Value: json.RawMessage(`{"pngBase64":"cG5n"}`)}, nil
}

func TestLocalDevicesControllerCarriesScopedCredentialsAndTypedCommands(t *testing.T) {
	service := &fakeLocalDeviceService{}
	router := chi.NewRouter()
	(&LocalDevicesController{Svc: service}).Register(router)

	request := httptest.NewRequest(http.MethodPost, "/devices/commands", strings.NewReader(`{"sessionId":"s1","action":"tap","x":12,"y":34}`))
	request.Header.Set(deviceCapabilityHeader, "agent-token")
	request.Header.Set(desktopDeviceCapabilityHeader, "desktop-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.session != "s1" || service.credentials.Agent != "agent-token" || service.credentials.Desktop != "desktop-token" {
		t.Fatalf("authorization context = %q %#v", service.session, service.credentials)
	}
	if service.command.Action != "tap" || service.command.X == nil || *service.command.X != 12 || service.command.Y == nil || *service.command.Y != 34 {
		t.Fatalf("command = %#v", service.command)
	}
	if !strings.Contains(response.Body.String(), `"pngBase64":"cG5n"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestLocalDevicesControllerRejectsMalformedJSON(t *testing.T) {
	router := chi.NewRouter()
	(&LocalDevicesController{Svc: &fakeLocalDeviceService{}}).Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/devices/commands", strings.NewReader("{")))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_JSON") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestLocalDevicesControllerRejectsUnknownCommandFields(t *testing.T) {
	router := chi.NewRouter()
	(&LocalDevicesController{Svc: &fakeLocalDeviceService{}}).Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/devices/commands", strings.NewReader(`{"sessionId":"s1","action":"home","argv":["dangerous"]}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_JSON") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
