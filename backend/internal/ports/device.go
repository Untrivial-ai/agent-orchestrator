package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DeviceRuntime is AO's helper-neutral boundary around local virtual-device tools.
type DeviceRuntime interface {
	Capabilities(context.Context) []domain.DevicePlatformCapability
	List(context.Context, domain.DevicePlatform) ([]domain.Device, error)
	Execute(context.Context, DeviceRuntimeRequest) (json.RawMessage, error)
}

// DeviceSetupProgress is emitted by a platform installer at durable stage boundaries.
type DeviceSetupProgress struct {
	State            domain.DeviceSetupState
	Stage            string
	Message          string
	Progress         int
	DownloadedBytes  int64
	TotalBytes       int64
	RequiredBytes    int64
	AvailableBytes   int64
	InstalledVersion string
}

// DeviceSetupPlan is a side-effect-free platform readiness and install plan.
type DeviceSetupPlan struct {
	Ready            bool
	State            domain.DeviceSetupState
	Message          string
	RequiredBytes    int64
	AvailableBytes   int64
	LicenseURL       string
	ActionURL        string
	InstalledVersion string
}

// DeviceSetupRuntime installs vendor tools without exposing shell execution to callers.
type DeviceSetupRuntime interface {
	SetupPlan(context.Context, domain.DevicePlatform) (DeviceSetupPlan, error)
	Install(context.Context, domain.DevicePlatform, func(DeviceSetupProgress)) error
}

// DeviceSetupRuntimeError is a redacted installer failure safe for the API.
type DeviceSetupRuntimeError struct {
	Code       string
	Message    string
	ActionURL  string
	Actionable bool
}

func (e *DeviceSetupRuntimeError) Error() string { return e.Message }

// DeviceSetupJobRecord is the persistence-neutral representation of setup progress.
type DeviceSetupJobRecord struct {
	Platform         string
	State            string
	Stage            string
	Message          string
	Progress         int
	DownloadedBytes  int64
	TotalBytes       int64
	RequiredBytes    int64
	AvailableBytes   int64
	LicenseURL       string
	LicenseAccepted  bool
	ActionURL        string
	ErrorCode        string
	Error            string
	InstalledVersion string
	StartedAt        time.Time
	FinishedAt       *time.Time
	UpdatedAt        time.Time
}

// DeviceSetupJobStore persists one managed setup job per platform.
type DeviceSetupJobStore interface {
	UpsertDeviceSetupJob(context.Context, DeviceSetupJobRecord) error
	GetDeviceSetupJob(context.Context, string) (DeviceSetupJobRecord, bool, error)
	InterruptActiveDeviceSetupJobs(context.Context, time.Time) error
}

// DeviceRuntimeRequest is constructed only by the device service. Its fields
// are serialized to AO's allowlisted helper runner; clients cannot provide
// command lines, paths, environment, or helper routes.
type DeviceRuntimeRequest struct {
	Action          string                `json:"action"`
	Session         string                `json:"session,omitempty"`
	Platform        domain.DevicePlatform `json:"platform,omitempty"`
	DeviceID        string                `json:"deviceId,omitempty"`
	InteractiveOnly bool                  `json:"interactiveOnly,omitempty"`
	Ref             string                `json:"ref,omitempty"`
	X               *int                  `json:"x,omitempty"`
	Y               *int                  `json:"y,omitempty"`
	X1              *int                  `json:"x1,omitempty"`
	Y1              *int                  `json:"y1,omitempty"`
	X2              *int                  `json:"x2,omitempty"`
	Y2              *int                  `json:"y2,omitempty"`
	Text            string                `json:"text,omitempty"`
	Key             string                `json:"key,omitempty"`
}

// DeviceRuntimeError is a redacted, helper-neutral runtime failure.
type DeviceRuntimeError struct {
	Code    string
	Message string
}

func (e *DeviceRuntimeError) Error() string { return e.Message }
