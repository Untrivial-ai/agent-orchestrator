// Package browser owns authorization and dispatch for session-scoped browser
// commands. HTTP controllers remain transport-only adapters.
package browser

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

var actions = map[string]struct{}{
	"open": {}, "snapshot": {}, "act": {}, "click": {}, "dblclick": {}, "focus": {}, "fill": {}, "type": {}, "press": {},
	"hover": {}, "highlight": {}, "unhighlight": {}, "scrollintoview": {}, "drag": {}, "tabs": {}, "tab-new": {},
	"tab-select": {}, "tab-close": {}, "scroll": {}, "select": {}, "check": {},
	"uncheck": {}, "get": {}, "wait": {}, "screenshot": {}, "network-start": {},
	"network-status": {}, "network-list": {}, "network-stop": {}, "network-clear": {},
	"console": {}, "errors": {}, "frame": {}, "dialog": {},
	"devtools-open": {}, "devtools-close": {},
}

type sessionReader interface {
	Get(ctx context.Context, id domain.SessionID) (domain.Session, error)
}

type runtime interface {
	Status(sessionID domain.SessionID, surface string) (browserruntime.Status, string)
	Execute(ctx context.Context, sessionID domain.SessionID, surface, action string, args map[string]interface{}) (browserruntime.Result, error)
}

// Service validates worker ownership and lifecycle state before dispatching to
// the Electron runtime.
type Service struct {
	sessions  sessionReader
	runtime   runtime
	authority *Authority
}

// New creates a browser service.
func New(sessions sessionReader, runtime runtime, authority *Authority) *Service {
	return &Service{sessions: sessions, runtime: runtime, authority: authority}
}

// Status returns transport state after validating the session owner.
func (s *Service) Status(ctx context.Context, sessionID domain.SessionID, capability, surface string) (browserruntime.Status, string, error) {
	if err := s.authorize(ctx, sessionID, capability); err != nil {
		return browserruntime.Status{}, "", err
	}
	surface, err := normalizeSurface(surface)
	if err != nil {
		return browserruntime.Status{}, "", err
	}
	status, transport := s.runtime.Status(sessionID, surface)
	return status, transport, nil
}

// Execute validates ownership and dispatches one supported action.
func (s *Service) Execute(
	ctx context.Context,
	sessionID domain.SessionID,
	capability string,
	surface string,
	action string,
	args map[string]interface{},
) (browserruntime.Result, string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if err := s.authorize(ctx, sessionID, capability); err != nil {
		return browserruntime.Result{}, action, err
	}
	surface, err := normalizeSurface(surface)
	if err != nil {
		return browserruntime.Result{}, action, err
	}
	if _, ok := actions[action]; !ok {
		return browserruntime.Result{}, action, apierr.Invalid(
			"BROWSER_ACTION_UNSUPPORTED",
			"Unsupported browser action",
			nil,
		)
	}
	result, err := s.runtime.Execute(ctx, sessionID, surface, action, args)
	return result, action, err
}

func normalizeSurface(surface string) (string, error) {
	surface = strings.ToLower(strings.TrimSpace(surface))
	if surface == "" {
		return "auto", nil
	}
	if surface != "auto" && surface != "desktop" && surface != "mobile" {
		return "", apierr.Invalid("BROWSER_SURFACE_INVALID", "Browser surface must be auto, desktop, or mobile", nil)
	}
	return surface, nil
}

func (s *Service) authorize(ctx context.Context, sessionID domain.SessionID, capability string) error {
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.IsTerminated {
		return apierr.Conflict("SESSION_TERMINATED", "Session is terminated", nil)
	}
	if s.authority == nil || !s.authority.Valid(
		sessionID,
		strings.TrimSpace(capability),
		session.Metadata.BrowserCapabilityVerifier,
	) {
		return apierr.Forbidden("BROWSER_CAPABILITY_INVALID", "Browser capability is invalid")
	}
	return nil
}
