package device

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeManagedRuntime struct {
	*fakeDeviceRuntime
	plan    ports.DeviceSetupPlan
	started chan struct{}
	release chan struct{}
}

func (f *fakeManagedRuntime) SetupPlan(context.Context, domain.DevicePlatform) (ports.DeviceSetupPlan, error) {
	return f.plan, nil
}
func (f *fakeManagedRuntime) Install(ctx context.Context, _ domain.DevicePlatform, report func(ports.DeviceSetupProgress)) error {
	report(ports.DeviceSetupProgress{State: domain.DeviceSetupDownloading, Stage: "download", Progress: 42})
	if f.started != nil {
		close(f.started)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release:
		return nil
	}
}

type memorySetupStore struct {
	mu   sync.Mutex
	jobs map[string]ports.DeviceSetupJobRecord
}

func (s *memorySetupStore) UpsertDeviceSetupJob(_ context.Context, job ports.DeviceSetupJobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.Platform] = job
	return nil
}
func (s *memorySetupStore) GetDeviceSetupJob(_ context.Context, platform string) (ports.DeviceSetupJobRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[platform]
	return job, ok, nil
}
func (s *memorySetupStore) InterruptActiveDeviceSetupJobs(_ context.Context, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, job := range s.jobs {
		if job.State == string(domain.DeviceSetupDownloading) {
			job.State = string(domain.DeviceSetupInterrupted)
			job.FinishedAt = &at
			s.jobs[key] = job
		}
	}
	return nil
}

func TestManagedSetupRequiresLicensePersistsProgressAndCancels(t *testing.T) {
	runtime := &fakeManagedRuntime{fakeDeviceRuntime: &fakeDeviceRuntime{}, plan: ports.DeviceSetupPlan{State: domain.DeviceSetupIdle, RequiredBytes: 100, AvailableBytes: 200, LicenseURL: "https://terms.example"}, started: make(chan struct{}), release: make(chan struct{})}
	store := &memorySetupStore{jobs: make(map[string]ports.DeviceSetupJobRecord)}
	service := NewWithDeps(fakeSessionReader{}, runtime, fakeAuthority{}, "", Deps{SetupRuntime: runtime, SetupStore: store})
	credentials := Credentials{Agent: "token-s1"}
	if _, err := service.ExecuteSetup(context.Background(), "s1", credentials, SetupCommand{Platform: domain.DevicePlatformAndroid, Action: "start"}); apiErrorCode(err) != "DEVICE_SETUP_LICENSE_REQUIRED" {
		t.Fatalf("license error = %v", err)
	}
	started, err := service.ExecuteSetup(context.Background(), "s1", credentials, SetupCommand{Platform: domain.DevicePlatformAndroid, Action: "start", LicenseAccepted: true})
	if err != nil || started.State != domain.DeviceSetupQueued {
		t.Fatalf("start = %#v, %v", started, err)
	}
	<-runtime.started
	statuses, err := service.SetupStatus(context.Background(), "s1", credentials)
	if err != nil || statuses[1].State != domain.DeviceSetupDownloading || statuses[1].Progress != 42 {
		t.Fatalf("status = %#v, %v", statuses, err)
	}
	if _, err := service.ExecuteSetup(context.Background(), "s1", credentials, SetupCommand{Platform: domain.DevicePlatformAndroid, Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		statuses, _ = service.SetupStatus(context.Background(), "s1", credentials)
		if statuses[1].State == domain.DeviceSetupCanceled {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if statuses[1].State != domain.DeviceSetupCanceled || !statuses[1].Retryable {
		t.Fatalf("canceled = %#v", statuses[1])
	}
}

func TestSetupStatusDetectsCompletedXcodeHandoff(t *testing.T) {
	runtime := &fakeManagedRuntime{fakeDeviceRuntime: &fakeDeviceRuntime{}, plan: ports.DeviceSetupPlan{
		State: domain.DeviceSetupIdle, RequiredBytes: 100, AvailableBytes: 200,
	}}
	store := &memorySetupStore{jobs: map[string]ports.DeviceSetupJobRecord{
		string(domain.DevicePlatformIOS): {
			Platform: string(domain.DevicePlatformIOS), State: string(domain.DeviceSetupAwaitingAction),
			LicenseAccepted: true, ActionURL: "https://developer.apple.com/xcode/",
		},
	}}
	service := NewWithDeps(fakeSessionReader{}, runtime, fakeAuthority{}, "", Deps{SetupRuntime: runtime, SetupStore: store})

	status, err := service.platformSetupStatus(context.Background(), domain.DevicePlatformIOS)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != domain.DeviceSetupIdle || !status.LicenseAccepted || status.ActionURL != "" {
		t.Fatalf("handoff status = %#v", status)
	}
}
