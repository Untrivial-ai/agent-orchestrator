package device

import (
	"context"
	"encoding/json"
	"errors"
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
	requests []ports.DeviceRuntimeRequest
	fail     error
}

func (f *fakeDeviceRuntime) Capabilities(context.Context) []domain.DevicePlatformCapability {
	return []domain.DevicePlatformCapability{
		{Platform: domain.DevicePlatformIOS, Available: true},
		{Platform: domain.DevicePlatformAndroid, Code: "ANDROID_SDK_REQUIRED", Message: "Install Android SDK"},
	}
}

func (f *fakeDeviceRuntime) List(context.Context, domain.DevicePlatform) ([]domain.Device, error) {
	return []domain.Device{{ID: "ios-1", Name: "iPhone 17", Platform: domain.DevicePlatformIOS, Kind: domain.DeviceKindSimulator}}, nil
}

func (f *fakeDeviceRuntime) Execute(_ context.Context, request ports.DeviceRuntimeRequest) (json.RawMessage, error) {
	f.requests = append(f.requests, request)
	if f.fail != nil {
		return nil, f.fail
	}
	return json.RawMessage(`{"ok":true}`), nil
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
