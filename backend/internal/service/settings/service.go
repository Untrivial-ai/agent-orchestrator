// Package settings owns AO's daemon-side user preferences.
//
// It exists so every spawn surface — desktop, mobile, `ao spawn`, headless —
// resolves one value. A renderer-held preference would look correct in Settings
// while disagreeing with the CLI, which is worse than having no control.
package settings

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Store is the durable preference surface.
type Store interface {
	GetAppSettings(ctx context.Context) (Snapshot, error)
	SetDefaultSessionMode(ctx context.Context, mode domain.SessionMode, now time.Time) error
	SetCloudOffering(ctx context.Context, enabled bool, now time.Time) error
}

// Snapshot is the current preference set.
type Snapshot struct {
	DefaultSessionMode domain.SessionMode
	// CloudOffering is the user's cloud toggle (Settings, Developer Mode).
	CloudOffering          bool
	ChatHibernationEnabled bool
	UpdatedAt              time.Time
}

// Offering reports which AO offerings this daemon exposes to clients. It is
// resolved once at boot from daemon config: offering gates are a deployment
// decision, not a user preference, so they ride alongside the preferences the
// settings endpoint serves rather than living in the durable store.
type Offering struct {
	// Client is the deployment's client identity (AO_CLIENT); empty when unset.
	Client string
	// LocalEnabled reports whether the local offering is available.
	LocalEnabled bool
	// CloudForced reports the AO_CLOUD_OFFERING env override: when set the
	// cloud offering is on regardless of the user's toggle (dev/CI escape
	// hatch). The normal path is the persisted Settings toggle.
	CloudForced bool
	// CloudControlPlaneURL is the cloud control plane base URL; empty when
	// no control plane is configured.
	CloudControlPlaneURL string
	// TrackerIntakeEnabled reports the AO_TRACKER_INTAKE gate. Clients need it
	// to avoid offering a per-project intake control the daemon will ignore.
	TrackerIntakeEnabled bool
}

// OfferingFromConfig derives the offering gates from daemon config.
func OfferingFromConfig(cfg config.Config) Offering {
	return Offering{
		Client:               cfg.Client,
		LocalEnabled:         cfg.LocalOffering,
		CloudForced:          cfg.CloudOffering,
		CloudControlPlaneURL: cfg.CloudControlPlaneURL,
		TrackerIntakeEnabled: cfg.TrackerIntake,
	}
}

// CloudEnabled resolves the effective cloud gate for one settings snapshot:
// the user's toggle (or the env override), and a configured control plane.
// A missing control-plane URL fails closed instead of surfacing broken cloud
// UI.
func (o Offering) CloudEnabled(snapshot Snapshot) bool {
	return (o.CloudForced || snapshot.CloudOffering) && o.CloudControlPlaneURL != ""
}

// ChatCapability reports which harnesses can run in chat mode, so the UI can warn
// that choosing chat narrows the agents available rather than letting the user
// discover it at spawn time.
type ChatCapability interface {
	SupportsChat(harness domain.AgentHarness) bool
}

// Service reads and writes preferences.
type Service struct {
	store           Store
	chat            ChatCapability
	offering        Offering
	now             func() time.Time
	chatHibernation atomic.Bool
}

// New builds the service.
func New(store Store, chat ChatCapability, offering Offering, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: store, chat: chat, offering: offering, now: now}
}

// Offering reports the boot-resolved offering gates.
func (s *Service) Offering() Offering {
	return s.offering
}

// Get returns the current preferences.
func (s *Service) Get(ctx context.Context) (Snapshot, error) {
	snapshot, err := s.store.GetAppSettings(ctx)
	snapshot.ChatHibernationEnabled = s.chatHibernation.Load()
	return snapshot, err
}

// DefaultSessionMode resolves the default for a spawn that named no mode. A read
// failure falls back to the compatibility default rather than failing the spawn:
// an unreadable preference should not stop work.
func (s *Service) DefaultSessionMode(ctx context.Context) domain.SessionMode {
	snapshot, err := s.store.GetAppSettings(ctx)
	if err != nil {
		return domain.DefaultSessionMode
	}
	return domain.NormalizeSessionMode(snapshot.DefaultSessionMode)
}

// SetDefaultSessionMode changes the default for sessions created afterwards.
//
// It deliberately does not touch existing sessions or their controllers. This
// is a preference for future sessions, not an implicit migration of the present.
func (s *Service) SetDefaultSessionMode(ctx context.Context, mode domain.SessionMode) (Snapshot, error) {
	if !mode.Valid() {
		return Snapshot{}, fmt.Errorf("%w: %q", ports.ErrChatUnsupported, mode)
	}
	if err := s.store.SetDefaultSessionMode(ctx, mode, s.now()); err != nil {
		return Snapshot{}, err
	}
	return s.Get(ctx)
}

// SetCloudOffering flips the user's cloud toggle. The effect is immediate for
// new reads; nothing about running sessions changes.
func (s *Service) SetCloudOffering(ctx context.Context, enabled bool) (Snapshot, error) {
	if err := s.store.SetCloudOffering(ctx, enabled, s.now()); err != nil {
		return Snapshot{}, err
	}
	return s.Get(ctx)
}

// ChatHibernationEnabled is false on every daemon boot until Developer Mode
// enables it through the settings route.
func (s *Service) ChatHibernationEnabled() bool { return s.chatHibernation.Load() }

// SetChatHibernationEnabled changes the in-memory hibernation gate.
func (s *Service) SetChatHibernationEnabled(ctx context.Context, enabled bool) (Snapshot, error) {
	if !enabled {
		// Disabling must take effect even if reading the response later fails.
		s.chatHibernation.Store(false)
	}
	snapshot, err := s.store.GetAppSettings(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if enabled {
		s.chatHibernation.Store(true)
	}
	snapshot.ChatHibernationEnabled = enabled
	return snapshot, nil
}

// ChatHarnesses lists the harnesses that can run in chat mode today.
func (s *Service) ChatHarnesses(candidates []domain.AgentHarness) []domain.AgentHarness {
	if s.chat == nil {
		return nil
	}
	var out []domain.AgentHarness
	for _, harness := range candidates {
		if s.chat.SupportsChat(harness) {
			out = append(out, harness)
		}
	}
	return out
}
