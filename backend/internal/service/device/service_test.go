package device

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeSessionReader struct{}

func (fakeSessionReader) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	return domain.Session{SessionRecord: domain.SessionRecord{
		ID: id, Metadata: domain.SessionMetadata{BrowserCapabilityVerifier: "verifier-" + string(id)},
	}}, nil
}

type fakeAuthority struct{}

func (fakeAuthority) Valid(id domain.SessionID, token, verifier string) bool {
	return token == "token-"+string(id) && verifier == "verifier-"+string(id)
}

type fakeDeviceRuntime struct {
	requests    []ports.DeviceRuntimeRequest
	listed      []domain.DevicePlatform
	prepared    []domain.DeviceAttachment
	fail        error
	prepareFail error
	attachValue json.RawMessage
	devices     []domain.Device
}

func (f *fakeDeviceRuntime) Capabilities(context.Context) []domain.DevicePlatformCapability {
	return []domain.DevicePlatformCapability{
		{Platform: domain.DevicePlatformIOS, Available: true},
		{Platform: domain.DevicePlatformAndroid, Code: "ANDROID_SDK_REQUIRED", Message: "Install Android SDK"},
	}
}

func (f *fakeDeviceRuntime) List(_ context.Context, platform domain.DevicePlatform) ([]domain.Device, error) {
	f.listed = append(f.listed, platform)
	if f.devices != nil {
		return f.devices, nil
	}
	return []domain.Device{{ID: "ios-1", Name: "iPhone 17", Platform: domain.DevicePlatformIOS, Kind: domain.DeviceKindSimulator}}, nil
}

func (f *fakeDeviceRuntime) Execute(_ context.Context, request ports.DeviceRuntimeRequest) (json.RawMessage, error) {
	f.requests = append(f.requests, request)
	if f.fail != nil {
		return nil, f.fail
	}
	if request.Action == "attach" && len(f.attachValue) != 0 {
		return f.attachValue, nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (f *fakeDeviceRuntime) HubOrigin(context.Context) (string, error) {
	return "http://127.0.0.1:43210", nil
}

func (f *fakeDeviceRuntime) PrepareStream(_ context.Context, platform domain.DevicePlatform, deviceID string) error {
	f.prepared = append(f.prepared, domain.DeviceAttachment{DeviceID: deviceID, Platform: platform})
	return f.prepareFail
}

func TestServiceRequiresSessionCapabilityOrDesktopCredential(t *testing.T) {
	runtime := &fakeDeviceRuntime{}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "desktop-secret")
	if _, err := service.Status(context.Background(), "s1", Credentials{Agent: "wrong"}); apiErrorCode(err) != "DEVICE_CAPABILITY_INVALID" {
		t.Fatalf("wrong capability error = %v", err)
	}
	if _, err := service.Status(context.Background(), "s1", Credentials{Agent: "token-s1"}); err != nil {
		t.Fatalf("agent capability: %v", err)
	}
	if _, err := service.Status(context.Background(), "s1", Credentials{Desktop: "desktop-secret"}); err != nil {
		t.Fatalf("desktop capability: %v", err)
	}
}

func TestServiceListReturnsNonNilCollections(t *testing.T) {
	runtime := &fakeDeviceRuntime{}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "")
	inventory, err := service.List(context.Background(), "s1", Credentials{Agent: "token-s1"})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Devices == nil || inventory.Errors == nil {
		t.Fatalf("inventory collections must be non-nil: %#v", inventory)
	}
}

func TestServiceEnforcesExclusiveAttachmentAndReleasesOnClose(t *testing.T) {
	runtime := &fakeDeviceRuntime{}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "")
	open := Command{Action: "open", DeviceID: "ios-1", Platform: domain.DevicePlatformIOS}
	first, err := service.Execute(context.Background(), "s1", Credentials{Agent: "token-s1"}, open)
	if err != nil || first.Attachment == nil || first.Attachment.DeviceID != "ios-1" {
		t.Fatalf("open result = %#v, err = %v", first, err)
	}
	if _, err := service.Execute(context.Background(), "s2", Credentials{Agent: "token-s2"}, open); apiErrorCode(err) != "DEVICE_BUSY" {
		t.Fatalf("second open error = %v", err)
	}
	inventory, err := service.List(context.Background(), "s2", Credentials{Agent: "token-s2"})
	if err != nil || len(inventory.Devices) != 1 || !inventory.Devices[0].Busy || len(inventory.Errors) != 1 {
		t.Fatalf("inventory = %#v, err = %v", inventory, err)
	}
	if err := service.DetachSession(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), "s2", Credentials{Agent: "token-s2"}, open); err != nil {
		t.Fatalf("open after release: %v", err)
	}
	if runtime.requests[0].Action != "attach" || runtime.requests[1].Action != "detach" || runtime.requests[2].Action != "attach" {
		t.Fatalf("runtime requests = %#v", runtime.requests)
	}
}

func TestServiceOpenOnlyDiscoversSelectedPlatform(t *testing.T) {
	runtime := &fakeDeviceRuntime{}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "")
	_, err := service.Execute(context.Background(), "s1", Credentials{Agent: "token-s1"}, Command{
		Action: "open", DeviceID: "ios-1", Platform: domain.DevicePlatformIOS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.listed) != 1 || runtime.listed[0] != domain.DevicePlatformIOS {
		t.Fatalf("listed platforms = %#v", runtime.listed)
	}
}

func TestServiceUsesRuntimeDeviceIDForAndroidStreamAndLease(t *testing.T) {
	runtime := &fakeDeviceRuntime{
		attachValue: json.RawMessage(`{"attached":true,"deviceId":"emulator-5554"}`),
		devices: []domain.Device{{
			ID: "AO_Pixel_API_36", Name: "AO Pixel API 36", Platform: domain.DevicePlatformAndroid, Kind: domain.DeviceKindEmulator,
		}},
	}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "")
	result, err := service.Execute(context.Background(), "s1", Credentials{Agent: "token-s1"}, Command{
		Action: "open", DeviceID: "AO_Pixel_API_36", Platform: domain.DevicePlatformAndroid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attachment == nil || result.Attachment.DeviceID != "emulator-5554" {
		t.Fatalf("attachment = %#v", result.Attachment)
	}
	if len(runtime.prepared) != 1 || runtime.prepared[0].DeviceID != "emulator-5554" {
		t.Fatalf("prepared streams = %#v", runtime.prepared)
	}
	if _, err := service.Execute(context.Background(), "s1", Credentials{Agent: "token-s1"}, Command{Action: "close"}); err != nil {
		t.Fatal(err)
	}
	if _, busy := service.owners["emulator-5554"]; busy {
		t.Fatal("runtime device lease remained after close")
	}
}

func TestServiceIssuesAndRevokesStreamCapabilities(t *testing.T) {
	runtime := &fakeDeviceRuntime{}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "")
	credentials := Credentials{Agent: "token-s1"}
	opened, err := service.Execute(context.Background(), "s1", credentials, Command{Action: "open", DeviceID: "ios-1", Platform: domain.DevicePlatformIOS})
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		StreamBasePath string `json:"streamBasePath"`
	}
	if err := json.Unmarshal(opened.Value, &value); err != nil || value.StreamBasePath == "" {
		t.Fatalf("stream result = %s, err = %v", opened.Value, err)
	}
	ticket := value.StreamBasePath[strings.LastIndex(value.StreamBasePath, "/")+1:]
	if access, ok := service.ResolveStream(ticket); !ok || access.DeviceID != "ios-1" || access.Origin != "http://127.0.0.1:43210" {
		t.Fatalf("stream access = %#v, ok = %v", access, ok)
	}
	if len(runtime.prepared) != 1 || runtime.prepared[0].DeviceID != "ios-1" || runtime.prepared[0].Platform != domain.DevicePlatformIOS {
		t.Fatalf("prepared streams = %#v", runtime.prepared)
	}
	if _, err := service.Execute(context.Background(), "s1", credentials, Command{Action: "close"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.ResolveStream(ticket); ok {
		t.Fatal("stream capability remained valid after close")
	}
}

func TestServiceReleasesNewAttachmentWhenStreamPreparationFails(t *testing.T) {
	runtime := &fakeDeviceRuntime{
		prepareFail: &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "stream unavailable"},
		attachValue: json.RawMessage(`{"attached":true,"deviceId":"runtime-ios-1"}`),
	}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "")
	open := Command{Action: "open", DeviceID: "ios-1", Platform: domain.DevicePlatformIOS}
	if _, err := service.Execute(context.Background(), "s1", Credentials{Agent: "token-s1"}, open); apiErrorCode(err) != "DEVICE_RUNTIME_UNAVAILABLE" {
		t.Fatalf("first open error = %v", err)
	}
	if len(runtime.requests) != 2 || runtime.requests[0].Action != "attach" || runtime.requests[1].Action != "detach" {
		t.Fatalf("rollback requests = %#v", runtime.requests)
	}
	if _, busy := service.owners["runtime-ios-1"]; busy {
		t.Fatal("runtime device lease remained after stream failure")
	}
	runtime.prepareFail = nil
	if _, err := service.Execute(context.Background(), "s2", Credentials{Agent: "token-s2"}, open); err != nil {
		t.Fatalf("lease remained after stream failure: %v", err)
	}
}

func TestServiceValidatesActionsAndMapsRuntimeFailures(t *testing.T) {
	runtime := &fakeDeviceRuntime{}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "")
	credentials := Credentials{Agent: "token-s1"}
	if _, err := service.Execute(context.Background(), "s1", credentials, Command{Action: "shutdown"}); apiErrorCode(err) != "DEVICE_SHUTDOWN_CONFIRMATION_REQUIRED" {
		t.Fatalf("shutdown error = %v", err)
	}
	if _, err := service.Execute(context.Background(), "s1", credentials, Command{Action: "open", DeviceID: "ios-1", Platform: domain.DevicePlatformIOS}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), "s1", credentials, Command{Action: "key", Key: "escape"}); apiErrorCode(err) != "DEVICE_ACTION_UNSUPPORTED" {
		t.Fatalf("key error = %v", err)
	}
	if _, err := service.Execute(context.Background(), "s1", credentials, Command{Action: "launch"}); apiErrorCode(err) != "INVALID_ARGUMENT" {
		t.Fatalf("empty launch error = %v", err)
	}
	if _, err := service.Execute(context.Background(), "s1", credentials, Command{Action: "launch", Text: "Calendar"}); err != nil {
		t.Fatalf("launch error = %v", err)
	}
	if got := runtime.requests[len(runtime.requests)-1]; got.Action != "launch" || got.Text != "Calendar" || got.DeviceID != "ios-1" {
		t.Fatalf("launch runtime request = %#v", got)
	}
	runtime.fail = &ports.DeviceRuntimeError{Code: "DEVICE_NOT_FOUND", Message: "not found"}
	if _, err := service.Execute(context.Background(), "s1", credentials, Command{Action: "home"}); apiErrorCode(err) != "DEVICE_NOT_FOUND" {
		t.Fatalf("mapped runtime error = %v", err)
	}
}

func TestDetachSessionReleasesAOLeaseWhenHelperCleanupFails(t *testing.T) {
	runtime := &fakeDeviceRuntime{}
	service := New(fakeSessionReader{}, runtime, fakeAuthority{}, "")
	open := Command{Action: "open", DeviceID: "ios-1", Platform: domain.DevicePlatformIOS}
	if _, err := service.Execute(context.Background(), "s1", Credentials{Agent: "token-s1"}, open); err != nil {
		t.Fatal(err)
	}
	runtime.fail = &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "gone"}
	if err := service.DetachSession(context.Background(), "s1"); apiErrorCode(err) != "DEVICE_RUNTIME_UNAVAILABLE" {
		t.Fatalf("detach error = %v", err)
	}
	runtime.fail = nil
	if _, err := service.Execute(context.Background(), "s2", Credentials{Agent: "token-s2"}, open); err != nil {
		t.Fatalf("lease remained after teardown failure: %v", err)
	}
}

func apiErrorCode(err error) string {
	var target *apierr.Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}
