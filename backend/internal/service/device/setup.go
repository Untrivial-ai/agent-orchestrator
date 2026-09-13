package device

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SetupCommand is the complete renderer/CLI managed-setup action surface.
type SetupCommand struct {
	Platform        domain.DevicePlatform
	Action          string
	LicenseAccepted bool
}

// SetupStatus returns a stable entry for each supported platform.
func (s *Service) SetupStatus(ctx context.Context, sessionID domain.SessionID, creds Credentials) ([]domain.DeviceSetup, error) {
	if err := s.authorize(ctx, sessionID, creds); err != nil {
		return nil, err
	}
	result := make([]domain.DeviceSetup, 0, 2)
	for _, platform := range []domain.DevicePlatform{domain.DevicePlatformIOS, domain.DevicePlatformAndroid} {
		status, err := s.platformSetupStatus(ctx, platform)
		if err != nil {
			return nil, apierr.Internal("DEVICE_SETUP_STATUS_FAILED", "Managed device setup status is unavailable")
		}
		result = append(result, status)
	}
	return result, nil
}

// ExecuteSetup starts, retries, or cancels one fixed managed platform setup.
func (s *Service) ExecuteSetup(ctx context.Context, sessionID domain.SessionID, creds Credentials, command SetupCommand) (domain.DeviceSetup, error) {
	if err := s.authorize(ctx, sessionID, creds); err != nil {
		return domain.DeviceSetup{}, err
	}
	if command.Platform != domain.DevicePlatformIOS && command.Platform != domain.DevicePlatformAndroid {
		return domain.DeviceSetup{}, apierr.Invalid("INVALID_ARGUMENT", "platform must be ios or android", nil)
	}
	switch strings.ToLower(strings.TrimSpace(command.Action)) {
	case "cancel":
		s.mu.Lock()
		cancel := s.setupCancels[command.Platform]
		s.mu.Unlock()
		if cancel == nil {
			return domain.DeviceSetup{}, apierr.Conflict("DEVICE_SETUP_NOT_ACTIVE", "There is no active setup to cancel", nil)
		}
		cancel()
		status, _ := s.platformSetupStatus(ctx, command.Platform)
		return status, nil
	case "start", "retry":
		if !command.LicenseAccepted {
			return domain.DeviceSetup{}, apierr.Invalid("DEVICE_SETUP_LICENSE_REQUIRED", "Accept the vendor license terms before setup", nil)
		}
		return s.startSetup(ctx, command.Platform)
	default:
		return domain.DeviceSetup{}, apierr.Invalid("DEVICE_SETUP_ACTION_UNSUPPORTED", "setup action must be start, retry, or cancel", nil)
	}
}

func (s *Service) startSetup(ctx context.Context, platform domain.DevicePlatform) (domain.DeviceSetup, error) {
	if s.setupRuntime == nil {
		return domain.DeviceSetup{}, apierr.Unavailable("DEVICE_SETUP_UNAVAILABLE", "Managed device setup is unavailable")
	}
	s.mu.Lock()
	if s.setupCancels[platform] != nil {
		s.mu.Unlock()
		return domain.DeviceSetup{}, apierr.Conflict("DEVICE_SETUP_ACTIVE", "Setup is already running for this platform", nil)
	}
	s.mu.Unlock()
	plan, err := s.setupRuntime.SetupPlan(ctx, platform)
	if err != nil {
		return domain.DeviceSetup{}, mapSetupError(err)
	}
	if plan.Ready {
		status := setupFromPlan(platform, plan)
		status.State, status.Progress, status.LicenseAccepted = domain.DeviceSetupSucceeded, 100, true
		_ = s.saveSetup(context.Background(), status, time.Now(), ptrTime(time.Now()))
		return status, nil
	}
	if plan.State == domain.DeviceSetupAwaitingAction {
		status := setupFromPlan(platform, plan)
		status.LicenseAccepted, status.Retryable = true, true
		_ = s.saveSetup(context.Background(), status, time.Now(), nil)
		return status, nil
	}
	started := time.Now().UTC()
	status := setupFromPlan(platform, plan)
	status.State, status.Stage, status.Progress, status.Message = domain.DeviceSetupQueued, "queued", 0, "Setup queued"
	status.LicenseAccepted, status.Cancelable = true, true
	// Setup deliberately outlives the initiating HTTP request while retaining
	// its values for logging and tracing.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	if s.setupCancels[platform] != nil {
		s.mu.Unlock()
		cancel()
		return domain.DeviceSetup{}, apierr.Conflict("DEVICE_SETUP_ACTIVE", "Setup is already running for this platform", nil)
	}
	s.setupCancels[platform] = cancel
	s.setupJobs[platform] = status
	s.mu.Unlock()
	if err := s.saveSetup(context.Background(), status, started, nil); err != nil {
		s.mu.Lock()
		delete(s.setupCancels, platform)
		delete(s.setupJobs, platform)
		s.mu.Unlock()
		cancel()
		return domain.DeviceSetup{}, apierr.Internal("DEVICE_SETUP_PERSIST_FAILED", "AO could not persist the setup job")
	}
	s.setupWG.Add(1)
	//nolint:gosec // Managed setup jobs deliberately continue after the initiating request ends.
	go s.runSetup(jobCtx, platform, started, status)
	return status, nil
}

func (s *Service) runSetup(ctx context.Context, platform domain.DevicePlatform, started time.Time, initial domain.DeviceSetup) {
	defer s.setupWG.Done()
	last := initial
	persisted := initial
	report := func(update ports.DeviceSetupProgress) {
		if update.Progress < 0 {
			update.Progress = 0
		}
		if update.Progress > 100 {
			update.Progress = 100
		}
		last.State, last.Stage, last.Message, last.Progress = update.State, update.Stage, update.Message, update.Progress
		last.DownloadedBytes, last.TotalBytes, last.RequiredBytes, last.AvailableBytes = update.DownloadedBytes, update.TotalBytes, update.RequiredBytes, update.AvailableBytes
		last.InstalledVersion, last.Cancelable = update.InstalledVersion, true
		if last.State == persisted.State && last.Stage == persisted.Stage && last.Progress == persisted.Progress && last.DownloadedBytes-persisted.DownloadedBytes < 8<<20 {
			return
		}
		_ = s.saveSetup(context.Background(), last, started, nil)
		persisted = last
	}
	err := s.setupRuntime.Install(ctx, platform, report)
	finished := time.Now().UTC()
	last.Cancelable, last.Retryable = false, false
	if errors.Is(ctx.Err(), context.Canceled) {
		last.State, last.Message, last.ErrorCode, last.Error, last.Retryable = domain.DeviceSetupCanceled, "Setup canceled", "SETUP_CANCELED", "Setup was canceled. Retry to resume.", true
	} else if err != nil {
		last.State, last.Progress, last.Retryable = domain.DeviceSetupFailed, min(last.Progress, 99), true
		var setupErr *ports.DeviceSetupRuntimeError
		if errors.As(err, &setupErr) {
			last.ErrorCode, last.Error, last.ActionURL = setupErr.Code, setupErr.Message, setupErr.ActionURL
		} else {
			last.ErrorCode, last.Error = "DEVICE_SETUP_FAILED", "Managed device setup failed. Retry to resume."
		}
	} else {
		last.State, last.Stage, last.Message, last.Progress = domain.DeviceSetupSucceeded, "complete", "Setup complete", 100
	}
	s.mu.Lock()
	delete(s.setupCancels, platform)
	s.mu.Unlock()
	_ = s.saveSetup(context.Background(), last, started, &finished)
}

func (s *Service) platformSetupStatus(ctx context.Context, platform domain.DevicePlatform) (domain.DeviceSetup, error) {
	s.mu.Lock()
	current, memoryOK := s.setupJobs[platform]
	s.mu.Unlock()
	if memoryOK && isActiveSetup(current.State) {
		return current, nil
	}
	if s.setupStore != nil {
		record, ok, err := s.setupStore.GetDeviceSetupJob(ctx, string(platform))
		if err != nil {
			return domain.DeviceSetup{}, err
		}
		if ok {
			current = setupFromRecord(record)
			memoryOK = true
		}
	}
	if s.setupRuntime == nil {
		return domain.DeviceSetup{Platform: platform, State: domain.DeviceSetupFailed, ErrorCode: "DEVICE_SETUP_UNAVAILABLE", Error: "Managed setup is unavailable"}, nil
	}
	plan, err := s.setupRuntime.SetupPlan(ctx, platform)
	if err != nil {
		var setupErr *ports.DeviceSetupRuntimeError
		if errors.As(err, &setupErr) {
			return domain.DeviceSetup{Platform: platform, State: domain.DeviceSetupFailed, ErrorCode: setupErr.Code, Error: setupErr.Message, ActionURL: setupErr.ActionURL, Retryable: true}, nil
		}
		return domain.DeviceSetup{}, err
	}
	if plan.Ready {
		status := setupFromPlan(platform, plan)
		status.State, status.Progress = domain.DeviceSetupSucceeded, 100
		return status, nil
	}
	if memoryOK && current.State != domain.DeviceSetupSucceeded {
		current.RequiredBytes, current.AvailableBytes, current.LicenseURL = plan.RequiredBytes, plan.AvailableBytes, plan.LicenseURL
		if plan.State == domain.DeviceSetupAwaitingAction {
			current.State, current.Message, current.ActionURL = plan.State, plan.Message, plan.ActionURL
		}
		return current, nil
	}
	return setupFromPlan(platform, plan), nil
}

func (s *Service) saveSetup(ctx context.Context, status domain.DeviceSetup, started time.Time, finished *time.Time) error {
	s.mu.Lock()
	s.setupJobs[status.Platform] = status
	s.mu.Unlock()
	if s.setupStore == nil {
		return nil
	}
	return s.setupStore.UpsertDeviceSetupJob(ctx, ports.DeviceSetupJobRecord{Platform: string(status.Platform), State: string(status.State), Stage: status.Stage, Message: status.Message,
		Progress: status.Progress, DownloadedBytes: status.DownloadedBytes, TotalBytes: status.TotalBytes, RequiredBytes: status.RequiredBytes, AvailableBytes: status.AvailableBytes,
		LicenseURL: status.LicenseURL, LicenseAccepted: status.LicenseAccepted, ActionURL: status.ActionURL, ErrorCode: status.ErrorCode, Error: status.Error,
		InstalledVersion: status.InstalledVersion, StartedAt: started, FinishedAt: finished, UpdatedAt: time.Now().UTC()})
}

func setupFromPlan(platform domain.DevicePlatform, plan ports.DeviceSetupPlan) domain.DeviceSetup {
	return domain.DeviceSetup{Platform: platform, State: plan.State, Message: plan.Message, RequiredBytes: plan.RequiredBytes, AvailableBytes: plan.AvailableBytes, LicenseURL: plan.LicenseURL, ActionURL: plan.ActionURL, InstalledVersion: plan.InstalledVersion, Retryable: plan.State == domain.DeviceSetupAwaitingAction}
}
func setupFromRecord(record ports.DeviceSetupJobRecord) domain.DeviceSetup {
	return domain.DeviceSetup{Platform: domain.DevicePlatform(record.Platform), State: domain.DeviceSetupState(record.State), Stage: record.Stage, Message: record.Message, Progress: record.Progress, DownloadedBytes: record.DownloadedBytes, TotalBytes: record.TotalBytes, RequiredBytes: record.RequiredBytes, AvailableBytes: record.AvailableBytes, LicenseURL: record.LicenseURL, LicenseAccepted: record.LicenseAccepted, ActionURL: record.ActionURL, ErrorCode: record.ErrorCode, Error: record.Error, InstalledVersion: record.InstalledVersion, Cancelable: isActiveSetup(domain.DeviceSetupState(record.State)), Retryable: record.State == string(domain.DeviceSetupFailed) || record.State == string(domain.DeviceSetupCanceled) || record.State == string(domain.DeviceSetupInterrupted)}
}
func isActiveSetup(state domain.DeviceSetupState) bool {
	return state == domain.DeviceSetupQueued || state == domain.DeviceSetupDownloading || state == domain.DeviceSetupInstalling || state == domain.DeviceSetupCreating || state == domain.DeviceSetupVerifying
}
func ptrTime(value time.Time) *time.Time { return &value }
func mapSetupError(err error) error {
	var setupErr *ports.DeviceSetupRuntimeError
	if errors.As(err, &setupErr) {
		if setupErr.Code == "INVALID_ARGUMENT" {
			return apierr.Invalid(setupErr.Code, setupErr.Message, nil)
		}
		if setupErr.Code == "DISK_SPACE_REQUIRED" {
			return apierr.Conflict(setupErr.Code, setupErr.Message, nil)
		}
		return apierr.Unavailable(setupErr.Code, setupErr.Message)
	}
	return apierr.Internal("DEVICE_SETUP_FAILED", "Managed device setup failed")
}
