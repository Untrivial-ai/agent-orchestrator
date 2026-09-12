package ports

import (
	"context"
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DeviceRuntime is AO's helper-neutral boundary around local virtual-device tools.
type DeviceRuntime interface {
	Capabilities(context.Context) []domain.DevicePlatformCapability
	List(context.Context, domain.DevicePlatform) ([]domain.Device, error)
	Execute(context.Context, DeviceRuntimeRequest) (json.RawMessage, error)
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
