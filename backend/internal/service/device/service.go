// Package device owns authorization, attachment leases, validation, and
// helper-neutral error mapping for local virtual devices.
package device

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const maxTextLength = 4096

type sessionReader interface {
	Get(context.Context, domain.SessionID) (domain.Session, error)
}

type capabilityAuthority interface {
	Valid(domain.SessionID, string, string) bool
}

// Credentials carries either a session agent capability or the private
// Electron-main desktop capability. The renderer never receives the latter.
type Credentials struct {
	Agent   string
	Desktop string
}

// Status is the live, derived device feature state for one AO session.
type Status struct {
	Capabilities []domain.DevicePlatformCapability `json:"capabilities"`
	Attachment   *domain.DeviceAttachment          `json:"attachment,omitempty"`
}

// Inventory keeps platform failures independent so Android can work without
// Xcode and iOS can work without an Android SDK.
type Inventory struct {
	Devices []domain.Device                   `json:"devices"`
	Errors  []domain.DevicePlatformCapability `json:"errors,omitempty"`
}

// Command is the typed public action contract accepted by Service.Execute.
type Command struct {
	Action          string
	DeviceID        string
	Platform        domain.DevicePlatform
	InteractiveOnly bool
	Ref             string
	X, Y            *int
	X1, Y1, X2, Y2  *int
	Text            string
	Key             string
	Confirmed       bool
}

// Result contains a normalized action result and the current attachment.
type Result struct {
	Action     string                   `json:"action"`
	Attachment *domain.DeviceAttachment `json:"attachment,omitempty"`
	Value      json.RawMessage          `json:"value,omitempty"`
}

// Service owns daemon-scoped AO attachment policy.
type Service struct {
	sessions     sessionReader
	runtime      ports.DeviceRuntime
	authority    capabilityAuthority
	desktopToken string
	mu           sync.Mutex
	bySession    map[domain.SessionID]domain.DeviceAttachment
	owners       map[string]domain.SessionID
	operations   map[domain.SessionID]*sync.Mutex
}

// New creates the device service.
func New(sessions sessionReader, runtime ports.DeviceRuntime, authority capabilityAuthority, desktopToken string) *Service {
	return &Service{
		sessions: sessions, runtime: runtime, authority: authority, desktopToken: desktopToken,
		bySession: make(map[domain.SessionID]domain.DeviceAttachment), owners: make(map[string]domain.SessionID),
		operations: make(map[domain.SessionID]*sync.Mutex),
	}
}

// Status returns live capabilities and the caller's attachment.
func (s *Service) Status(ctx context.Context, sessionID domain.SessionID, creds Credentials) (Status, error) {
	if err := s.authorize(ctx, sessionID, creds); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	attachment, ok := s.bySession[sessionID]
	s.mu.Unlock()
	result := Status{Capabilities: s.runtime.Capabilities(ctx)}
	if ok {
		attachmentCopy := attachment
		result.Attachment = &attachmentCopy
	}
	return result, nil
}

// List returns available devices and marks devices controlled by another AO session as busy.
func (s *Service) List(ctx context.Context, sessionID domain.SessionID, creds Credentials) (Inventory, error) {
	if err := s.authorize(ctx, sessionID, creds); err != nil {
		return Inventory{}, err
	}
	return s.list(ctx, sessionID), nil
}

func (s *Service) list(ctx context.Context, sessionID domain.SessionID) Inventory {
	result := Inventory{}
	for _, capability := range s.runtime.Capabilities(ctx) {
		if !capability.Available {
			result.Errors = append(result.Errors, capability)
			continue
		}
		devices, err := s.runtime.List(ctx, capability.Platform)
		if err != nil {
			result.Errors = append(result.Errors, runtimeCapabilityError(capability.Platform, err))
			continue
		}
		result.Devices = append(result.Devices, devices...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range result.Devices {
		owner, busy := s.owners[result.Devices[i].ID]
		result.Devices[i].Busy = busy && owner != sessionID
	}
	return result
}

// Execute validates and runs one allowlisted action.
func (s *Service) Execute(ctx context.Context, sessionID domain.SessionID, creds Credentials, command Command) (Result, error) {
	if err := s.authorize(ctx, sessionID, creds); err != nil {
		return Result{}, err
	}
	operation := s.operationLock(sessionID)
	operation.Lock()
	defer operation.Unlock()
	action := strings.ToLower(strings.TrimSpace(command.Action))
	switch action {
	case "open":
		return s.open(ctx, sessionID, command)
	case "close":
		return s.close(ctx, sessionID)
	case "shutdown":
		if !command.Confirmed {
			return Result{}, apierr.Invalid("DEVICE_SHUTDOWN_CONFIRMATION_REQUIRED", "Device shutdown requires explicit confirmation", nil)
		}
		return s.shutdown(ctx, sessionID, command)
	case "screenshot", "ui-tree", "tap", "swipe", "fill", "type", "key", "back", "home":
		return s.runAttached(ctx, sessionID, action, command)
	default:
		return Result{}, apierr.Invalid("DEVICE_ACTION_UNSUPPORTED", "Unsupported device action", nil)
	}
}

// DetachSession releases a session attachment during lifecycle teardown.
func (s *Service) DetachSession(ctx context.Context, sessionID domain.SessionID) error {
	operation := s.operationLock(sessionID)
	operation.Lock()
	defer operation.Unlock()
	attachment, ok := s.attachment(sessionID)
	if !ok {
		return nil
	}
	_, err := s.runtime.Execute(ctx, ports.DeviceRuntimeRequest{Action: "detach", Session: helperSession(sessionID)})
	// Session termination is authoritative for AO's in-memory lease even if the
	// external helper has already disappeared or cannot acknowledge cleanup.
	s.release(sessionID, attachment.DeviceID)
	if err != nil {
		return mapRuntimeError(err)
	}
	return nil
}

func (s *Service) operationLock(sessionID domain.SessionID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation := s.operations[sessionID]
	if operation == nil {
		operation = &sync.Mutex{}
		s.operations[sessionID] = operation
	}
	return operation
}

func (s *Service) open(ctx context.Context, sessionID domain.SessionID, command Command) (Result, error) {
	deviceID := strings.TrimSpace(command.DeviceID)
	if deviceID == "" || (command.Platform != domain.DevicePlatformIOS && command.Platform != domain.DevicePlatformAndroid) {
		return Result{}, apierr.Invalid("INVALID_ARGUMENT", "open requires a deviceId and platform", nil)
	}
	inventory := s.list(ctx, sessionID)
	var selected *domain.Device
	for i := range inventory.Devices {
		if inventory.Devices[i].ID == deviceID && inventory.Devices[i].Platform == command.Platform {
			selected = &inventory.Devices[i]
			break
		}
	}
	if selected == nil {
		return Result{}, apierr.NotFound("DEVICE_NOT_FOUND", "The selected virtual device was not found")
	}
	s.mu.Lock()
	if existing, ok := s.bySession[sessionID]; ok {
		s.mu.Unlock()
		if existing.DeviceID == deviceID {
			return Result{Action: actionName("open"), Attachment: &existing}, nil
		}
		return Result{}, apierr.Conflict("DEVICE_ATTACHMENT_EXISTS", "Close the current device before opening another", nil)
	}
	if owner, ok := s.owners[deviceID]; ok && owner != sessionID {
		s.mu.Unlock()
		return Result{}, apierr.Conflict("DEVICE_BUSY", "The selected virtual device is already controlled by another AO session", nil)
	}
	attachment := domain.DeviceAttachment{SessionID: string(sessionID), DeviceID: deviceID, Platform: selected.Platform, Name: selected.Name}
	s.bySession[sessionID] = attachment
	s.owners[deviceID] = sessionID
	s.mu.Unlock()

	value, err := s.runtime.Execute(ctx, ports.DeviceRuntimeRequest{
		Action: "attach", Session: helperSession(sessionID), Platform: selected.Platform, DeviceID: deviceID,
	})
	if err != nil {
		s.release(sessionID, deviceID)
		return Result{}, mapRuntimeError(err)
	}
	return Result{Action: actionName("open"), Attachment: &attachment, Value: value}, nil
}

func (s *Service) close(ctx context.Context, sessionID domain.SessionID) (Result, error) {
	attachment, ok := s.attachment(sessionID)
	if !ok {
		return Result{}, apierr.Conflict("DEVICE_ATTACHMENT_REQUIRED", "Open a device first", nil)
	}
	value, err := s.runtime.Execute(ctx, ports.DeviceRuntimeRequest{Action: "detach", Session: helperSession(sessionID)})
	if err != nil {
		return Result{}, mapRuntimeError(err)
	}
	s.release(sessionID, attachment.DeviceID)
	return Result{Action: actionName("close"), Value: value}, nil
}

func (s *Service) shutdown(ctx context.Context, sessionID domain.SessionID, command Command) (Result, error) {
	attachment, attached := s.attachment(sessionID)
	deviceID := strings.TrimSpace(command.DeviceID)
	platform := command.Platform
	if deviceID == "" && attached {
		deviceID, platform = attachment.DeviceID, attachment.Platform
	}
	if deviceID == "" || (platform != domain.DevicePlatformIOS && platform != domain.DevicePlatformAndroid) {
		return Result{}, apierr.Invalid("INVALID_ARGUMENT", "shutdown requires an attached device or deviceId and platform", nil)
	}
	value, err := s.runtime.Execute(ctx, ports.DeviceRuntimeRequest{
		Action: "shutdown", Session: helperSession(sessionID), Platform: platform, DeviceID: deviceID,
	})
	if err != nil {
		return Result{}, mapRuntimeError(err)
	}
	if attached && attachment.DeviceID == deviceID {
		s.release(sessionID, deviceID)
	}
	return Result{Action: actionName("shutdown"), Value: value}, nil
}

func (s *Service) runAttached(ctx context.Context, sessionID domain.SessionID, action string, command Command) (Result, error) {
	attachment, ok := s.attachment(sessionID)
	if !ok {
		return Result{}, apierr.Conflict("DEVICE_ATTACHMENT_REQUIRED", "Open a device first", nil)
	}
	request := ports.DeviceRuntimeRequest{
		Action: action, Session: helperSession(sessionID), Platform: attachment.Platform, DeviceID: attachment.DeviceID,
		InteractiveOnly: command.InteractiveOnly, Ref: strings.TrimSpace(command.Ref),
		X: command.X, Y: command.Y, X1: command.X1, Y1: command.Y1, X2: command.X2, Y2: command.Y2,
		Text: command.Text, Key: command.Key,
	}
	if err := validateAction(request); err != nil {
		return Result{}, err
	}
	value, err := s.runtime.Execute(ctx, request)
	if err != nil {
		return Result{}, mapRuntimeError(err)
	}
	return Result{Action: actionName(action), Attachment: &attachment, Value: value}, nil
}

func (s *Service) authorize(ctx context.Context, sessionID domain.SessionID, creds Credentials) error {
	if sessionID == "" {
		return apierr.Invalid("SESSION_ID_REQUIRED", "sessionId is required", nil)
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.IsTerminated {
		return apierr.Conflict("SESSION_TERMINATED", "Session is terminated", nil)
	}
	desktopOK := creds.Desktop != "" && s.desktopToken != "" && subtle.ConstantTimeCompare([]byte(creds.Desktop), []byte(s.desktopToken)) == 1
	agentOK := s.authority != nil && s.authority.Valid(sessionID, strings.TrimSpace(creds.Agent), session.Metadata.BrowserCapabilityVerifier)
	if !desktopOK && !agentOK {
		return apierr.Forbidden("DEVICE_CAPABILITY_INVALID", "Device capability is invalid")
	}
	return nil
}

func (s *Service) attachment(sessionID domain.SessionID) (domain.DeviceAttachment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attachment, ok := s.bySession[sessionID]
	return attachment, ok
}

func (s *Service) release(sessionID domain.SessionID, deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bySession, sessionID)
	if s.owners[deviceID] == sessionID {
		delete(s.owners, deviceID)
	}
}

func validateAction(request ports.DeviceRuntimeRequest) error {
	if len(request.Text) > maxTextLength {
		return apierr.Invalid("INVALID_ARGUMENT", "text must be at most 4096 characters", nil)
	}
	validCoordinate := func(value *int) bool { return value != nil && *value >= 0 && *value <= 100000 }
	switch request.Action {
	case "tap", "fill":
		if request.Ref == "" && (!validCoordinate(request.X) || !validCoordinate(request.Y)) {
			return apierr.Invalid("INVALID_ARGUMENT", request.Action+" requires a ref or x and y coordinates", nil)
		}
	case "swipe":
		if !validCoordinate(request.X1) || !validCoordinate(request.Y1) || !validCoordinate(request.X2) || !validCoordinate(request.Y2) {
			return apierr.Invalid("INVALID_ARGUMENT", "swipe requires valid endpoint coordinates", nil)
		}
	case "key":
		key := strings.ToLower(strings.TrimSpace(request.Key))
		if key != "enter" && key != "return" {
			return apierr.Invalid("DEVICE_ACTION_UNSUPPORTED", "Only Enter and Return keys are currently supported", nil)
		}
	}
	return nil
}

func mapRuntimeError(err error) error {
	var runtimeErr *ports.DeviceRuntimeError
	if !errors.As(err, &runtimeErr) {
		return apierr.Internal("DEVICE_RUNTIME_UNAVAILABLE", "The local device runtime failed")
	}
	switch runtimeErr.Code {
	case "INVALID_ARGUMENT":
		return apierr.Invalid(runtimeErr.Code, runtimeErr.Message, nil)
	case "DEVICE_NOT_FOUND":
		return apierr.NotFound(runtimeErr.Code, runtimeErr.Message)
	case "DEVICE_BUSY":
		return apierr.Conflict(runtimeErr.Code, runtimeErr.Message, nil)
	case "DEVICE_ACTION_UNSUPPORTED":
		return apierr.NotImplemented(runtimeErr.Code, runtimeErr.Message)
	default:
		return apierr.Unavailable(runtimeErr.Code, runtimeErr.Message)
	}
}

func runtimeCapabilityError(platform domain.DevicePlatform, err error) domain.DevicePlatformCapability {
	code, message := "DEVICE_RUNTIME_UNAVAILABLE", "The local device runtime could not list this platform"
	var runtimeErr *ports.DeviceRuntimeError
	if errors.As(err, &runtimeErr) {
		code, message = runtimeErr.Code, runtimeErr.Message
	}
	return domain.DevicePlatformCapability{Platform: platform, Code: code, Message: message}
}

func helperSession(sessionID domain.SessionID) string { return "ao-" + string(sessionID) }
func actionName(action string) string                 { return action }
