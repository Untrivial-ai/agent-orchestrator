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
	session      domain.SessionID
	credentials  devicesvc.Credentials
	command      devicesvc.Command
	setupCommand devicesvc.SetupCommand
	emptyList    bool
}

func (f *fakeLocalDeviceService) SetupStatus(_ context.Context, session domain.SessionID, credentials devicesvc.Credentials) ([]domain.DeviceSetup, error) {
	f.session, f.credentials = session, credentials
	return []domain.DeviceSetup{{Platform: domain.DevicePlatformIOS, State: domain.DeviceSetupIdle}}, nil
}

func (f *fakeLocalDeviceService) ExecuteSetup(_ context.Context, session domain.SessionID, credentials devicesvc.Credentials, command devicesvc.SetupCommand) (domain.DeviceSetup, error) {
	f.session, f.credentials, f.setupCommand = session, credentials, command
	return domain.DeviceSetup{Platform: command.Platform, State: domain.DeviceSetupQueued}, nil
}

func (f *fakeLocalDeviceService) Status(_ context.Context, session domain.SessionID, credentials devicesvc.Credentials) (devicesvc.Status, error) {
	f.session, f.credentials = session, credentials
	return devicesvc.Status{Capabilities: []domain.DevicePlatformCapability{{Platform: domain.DevicePlatformIOS, Available: true}}}, nil
}

func (f *fakeLocalDeviceService) List(_ context.Context, session domain.SessionID, credentials devicesvc.Credentials) (devicesvc.Inventory, error) {
	f.session, f.credentials = session, credentials
	if f.emptyList {
		return devicesvc.Inventory{}, nil
	}
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

func TestLocalDevicesControllerStartsManagedSetupWithExplicitLicense(t *testing.T) {
	service := &fakeLocalDeviceService{}
	router := chi.NewRouter()
	(&LocalDevicesController{Svc: service}).Register(router)
	request := httptest.NewRequest(http.MethodPost, "/devices/setup", strings.NewReader(`{"sessionId":"s1","platform":"android","action":"start","licenseAccepted":true}`))
	request.Header.Set(deviceCapabilityHeader, "agent-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.setupCommand.Platform != domain.DevicePlatformAndroid || !service.setupCommand.LicenseAccepted {
		t.Fatalf("status = %d, command = %#v, body = %s", response.Code, service.setupCommand, response.Body.String())
	}
}

func TestLocalDevicesControllerEncodesEmptyInventoryAsArray(t *testing.T) {
	router := chi.NewRouter()
	(&LocalDevicesController{Svc: &fakeLocalDeviceService{emptyList: true}}).Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/devices?sessionId=s1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"devices":[]`) {
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

func TestDeviceStreamRouteAllowsOnlyPlatformSpecificHelperPaths(t *testing.T) {
	tests := []struct {
		name       string
		access     devicesvc.StreamAccess
		channel    string
		wantSuffix string
		websocket  bool
		allowed    bool
	}{
		{name: "ios mjpeg", access: devicesvc.StreamAccess{Origin: "http://127.0.0.1:4000", DeviceID: "ios/device", Platform: domain.DevicePlatformIOS}, channel: "mjpeg", wantSuffix: "/vendor/serve-sim/helper/ios%2Fdevice/stream.mjpeg", allowed: true},
		{name: "ios input", access: devicesvc.StreamAccess{Origin: "http://127.0.0.1:4000", DeviceID: "ios device", Platform: domain.DevicePlatformIOS}, channel: "input", wantSuffix: "/vendor/serve-sim/helper/ws?device=ios+device", websocket: true, allowed: true},
		{name: "android input", access: devicesvc.StreamAccess{Origin: "http://127.0.0.1:4000", DeviceID: "emulator-5554", Platform: domain.DevicePlatformAndroid}, channel: "input", wantSuffix: "/vendor/serve-emu/ws?device=emulator-5554&frame-meta=1", websocket: true, allowed: true},
		{name: "android arbitrary http", access: devicesvc.StreamAccess{Origin: "http://127.0.0.1:4000", DeviceID: "emulator-5554", Platform: domain.DevicePlatformAndroid}, channel: "api", allowed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, websocket, allowed := deviceStreamRoute(test.access, test.channel)
			if allowed != test.allowed || websocket != test.websocket || (test.allowed && !strings.HasSuffix(got, test.wantSuffix)) {
				t.Fatalf("route = %q, websocket = %v, allowed = %v", got, websocket, allowed)
			}
		})
	}
}
