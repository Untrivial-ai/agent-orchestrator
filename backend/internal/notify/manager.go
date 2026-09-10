// Package notify owns notification write-side production and live dashboard fan-out.
package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Store is the write-side notification persistence boundary.
type Store interface {
	CreateNotification(ctx context.Context, rec domain.NotificationRecord) (domain.NotificationRecord, bool, error)
	ResolveSessionNotifications(
		ctx context.Context,
		id domain.SessionID,
		typ domain.NotificationType,
		at time.Time,
	) ([]domain.NotificationRecord, error)
	ResolvePRNotifications(
		ctx context.Context,
		prURL string,
		typ domain.NotificationType,
		at time.Time,
	) ([]domain.NotificationRecord, error)
	ReconcileResolvedNotifications(ctx context.Context, at time.Time) ([]domain.NotificationRecord, error)
}

// Publisher pushes notification events to live dashboard subscribers.
type Publisher interface {
	Publish(ctx context.Context, event domain.NotificationEvent) error
}

// Intent is the lifecycle-to-notification producer contract.
type Intent = ports.NotificationIntent

// Resolution is the lifecycle-to-notification resolution contract.
type Resolution = ports.NotificationResolution

// Manager validates lifecycle intents, enriches them into stored rows, persists
// unread notifications, and publishes newly inserted rows to live subscribers.
type Manager struct {
	store     Store
	publisher Publisher
	barrier   sync.Locker
	clock     func() time.Time
	newID     func() string
}

// Deps configures a Manager.
type Deps struct {
	Store     Store
	Publisher Publisher
	// Barrier serializes notification persistence and publication with clear-all.
	Barrier sync.Locker
	Clock   func() time.Time
	NewID   func() string
}

// New constructs a write-side notification manager.
func New(d Deps) *Manager {
	m := &Manager{store: d.Store, publisher: d.Publisher, barrier: d.Barrier, clock: d.Clock, newID: d.NewID}
	if m.barrier == nil {
		m.barrier = &sync.Mutex{}
	}
	if m.clock == nil {
		m.clock = time.Now
	}
	if m.newID == nil {
		m.newID = func() string { return "ntf_" + uuid.NewString() }
	}
	return m
}

// Notify stores one notification intent and publishes it after persistence.
// Duplicate unread rows are treated as a clean no-op.
func (m *Manager) Notify(ctx context.Context, intent Intent) error {
	if m == nil || m.store == nil {
		return errors.New("notify: store is required")
	}
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = m.clock().UTC()
	}
	rec, err := enrich(intent)
	if err != nil {
		return fmt.Errorf("notify enrich: %w", err)
	}
	rec.ID = m.newID()
	m.barrier.Lock()
	defer m.barrier.Unlock()
	created, inserted, err := m.store.CreateNotification(ctx, rec)
	if err != nil {
		return fmt.Errorf("notify store: %w", err)
	}
	if !inserted || m.publisher == nil {
		return nil
	}
	if err := m.publisher.Publish(ctx, domain.NotificationEvent{Kind: domain.NotificationCreated, Record: created}); err != nil {
		return fmt.Errorf("notify publish: %w", err)
	}
	return nil
}

// Resolve closes every open notification the resolution matches and publishes
// the rows it changed. Resolution is always derived from a lifecycle fact —
// the session got its input, the PR stopped waiting on a merge — never from a
// user action, so there is no manual counterpart to this call.
func (m *Manager) Resolve(ctx context.Context, res Resolution) error {
	if m == nil || m.store == nil {
		return errors.New("notify: store is required")
	}
	if !res.Type.Valid() {
		return fmt.Errorf("notify resolve: %w", domain.ErrInvalidNotificationType)
	}
	m.barrier.Lock()
	defer m.barrier.Unlock()
	at := res.ResolvedAt
	if at.IsZero() {
		at = m.clock().UTC()
	}
	var (
		resolved []domain.NotificationRecord
		err      error
	)
	if res.PRURL != "" {
		resolved, err = m.store.ResolvePRNotifications(ctx, res.PRURL, res.Type, at)
	} else {
		if res.SessionID == "" {
			return errors.New("notify resolve: session id or pr url is required")
		}
		resolved, err = m.store.ResolveSessionNotifications(ctx, res.SessionID, res.Type, at)
	}
	if err != nil {
		return fmt.Errorf("notify resolve: %w", err)
	}
	return m.publishResolved(ctx, resolved)
}

// Reconcile closes notifications whose resolving transition happened while the
// daemon was down. Lifecycle only observes live transitions, so without this a
// restart could strand a needs-input row as unresolved forever.
func (m *Manager) Reconcile(ctx context.Context) error {
	if m == nil || m.store == nil {
		return errors.New("notify: store is required")
	}
	m.barrier.Lock()
	defer m.barrier.Unlock()
	resolved, err := m.store.ReconcileResolvedNotifications(ctx, m.clock().UTC())
	if err != nil {
		return fmt.Errorf("notify reconcile: %w", err)
	}
	return m.publishResolved(ctx, resolved)
}

func (m *Manager) publishResolved(ctx context.Context, resolved []domain.NotificationRecord) error {
	if m.publisher == nil {
		return nil
	}
	for _, rec := range resolved {
		event := domain.NotificationEvent{Kind: domain.NotificationResolved, Record: rec}
		if err := m.publisher.Publish(ctx, event); err != nil {
			return fmt.Errorf("notify publish resolved: %w", err)
		}
	}
	return nil
}
