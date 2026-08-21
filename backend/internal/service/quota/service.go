package quota

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Store is the durable quota persistence boundary used by Service.
type Store interface {
	PersistQuotaObservation(context.Context, domain.QuotaSnapshot, []domain.QuotaAlert) error
	ListQuotaSnapshots(context.Context) ([]domain.QuotaSnapshot, error)
	GetQuotaSnapshot(context.Context, domain.QuotaProviderID, domain.QuotaAccountID) (domain.QuotaSnapshot, bool, error)
	ListQuotaHistory(context.Context, domain.QuotaProviderID, domain.QuotaAccountID, time.Time, int64) ([]domain.QuotaHistoryPoint, error)
	CompactQuotaHistory(context.Context, time.Time) (int64, error)
	RecordQuotaRefreshFailure(context.Context, domain.QuotaProviderID, domain.QuotaAccountID, string) error
	ListQuotaAlerts(context.Context, time.Time, int64) ([]domain.QuotaAlert, error)
}

// Service merges provider observations into account-level durable state.
type Service struct {
	store     Store
	mu        sync.Mutex
	refresher Refresher
	providers map[string]providerRefresher
	refreshes singleflight.Group
	collects  singleflight.Group
	reads     map[string]cachedRateLimits
}

type providerRefresher struct {
	provider  domain.QuotaProviderID
	accountID domain.QuotaAccountID
	refresher Refresher
}

type cachedRateLimits struct {
	limits     ports.ChatRateLimits
	observedAt time.Time
}

// Refresher performs a provider-native on-demand quota read.
type Refresher interface {
	RefreshQuota(context.Context, domain.QuotaProviderID, domain.QuotaAccountID) (domain.QuotaSnapshot, error)
}

// New creates a quota service backed by store.
func New(store Store) *Service {
	return &Service{store: store, providers: make(map[string]providerRefresher), reads: make(map[string]cachedRateLimits)}
}

// SetRefresher late-binds the live chat service, breaking the construction cycle:
// chat writes provider events to this service, while manual refresh needs a live
// provider conversation owned by chat.
func (s *Service) SetRefresher(refresher Refresher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresher = refresher
}

// RegisterRefresher adds a daemon-owned reader for an account that can be
// refreshed even when no live Chat conversation exists.
func (s *Service) RegisterRefresher(provider domain.QuotaProviderID, accountID domain.QuotaAccountID, refresher Refresher) {
	if s == nil || refresher == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers[quotaKey(provider, accountID)] = providerRefresher{provider: provider, accountID: accountID, refresher: refresher}
}

// RecordQuotaSnapshot merges and persists a provider quota observation.
func (s *Service) RecordQuotaSnapshot(ctx context.Context, update domain.QuotaSnapshot) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("quota store is unavailable")
	}
	update = domain.NormalizeQuotaSnapshot(update)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok, err := s.store.GetQuotaSnapshot(ctx, update.Provider, update.AccountID)
	if err != nil {
		return err
	}
	if ok {
		update = Merge(current, update)
	}
	return s.store.PersistQuotaObservation(ctx, update, TransitionAlerts(current, update))
}

// Alerts lists quota alerts created at or after since.
func (s *Service) Alerts(ctx context.Context, since time.Time, limit int64) ([]domain.QuotaAlert, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("quota store is unavailable")
	}
	return s.store.ListQuotaAlerts(ctx, since, limit)
}

// List returns the latest durable snapshot for every provider account.
func (s *Service) List(ctx context.Context) ([]domain.QuotaSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("quota store is unavailable")
	}
	return s.store.ListQuotaSnapshots(ctx)
}

// Get returns the latest durable snapshot for one provider account.
func (s *Service) Get(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID) (domain.QuotaSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return domain.QuotaSnapshot{}, false, fmt.Errorf("quota store is unavailable")
	}
	return s.store.GetQuotaSnapshot(ctx, provider, accountID)
}

// History lists durable quota observations for one provider account.
func (s *Service) History(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID, since time.Time, limit int64) ([]domain.QuotaHistoryPoint, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("quota store is unavailable")
	}
	return s.store.ListQuotaHistory(ctx, provider, accountID, since, limit)
}

// CollectRateLimits performs at most one provider request per account at a
// time. A fresh successful read is reused across conversations so sessions do
// not multiply provider traffic.
func (s *Service) CollectRateLimits(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID, read ports.QuotaReadFunc) (ports.ChatRateLimits, error) {
	if read == nil {
		return ports.ChatRateLimits{}, ports.ErrQuotaRefreshUnsupported
	}
	key := string(provider) + "\x00" + string(accountID)
	if cached, ok := s.cachedRead(key, time.Now().UTC()); ok {
		return cached, nil
	}
	result, err, _ := s.collects.Do(key, func() (any, error) {
		if cached, ok := s.cachedRead(key, time.Now().UTC()); ok {
			return cached, nil
		}
		limits, err := read(ctx)
		if err != nil {
			return nil, err
		}
		if limits.Quota == nil {
			return nil, fmt.Errorf("provider quota read returned no account snapshot")
		}
		snapshot := domain.NormalizeQuotaSnapshot(*limits.Quota)
		if snapshot.Provider != provider || snapshot.AccountID != accountID {
			return nil, fmt.Errorf("provider quota identity changed during read")
		}
		if err := s.RecordQuotaSnapshot(ctx, snapshot); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.reads[key] = cachedRateLimits{limits: limits, observedAt: snapshot.ObservedAt}
		s.mu.Unlock()
		return limits, nil
	})
	if err != nil {
		return ports.ChatRateLimits{}, err
	}
	limits, ok := result.(ports.ChatRateLimits)
	if !ok {
		return ports.ChatRateLimits{}, fmt.Errorf("unexpected collected quota result type %T", result)
	}
	return limits, nil
}

func (s *Service) cachedRead(key string, now time.Time) (ports.ChatRateLimits, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached, ok := s.reads[key]
	return cached.limits, ok && now.Sub(cached.observedAt) < FreshWindow
}

// Refresh requests and persists a fresh snapshot for one provider account.
func (s *Service) Refresh(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID) (domain.QuotaSnapshot, error) {
	if s == nil || s.store == nil {
		return domain.QuotaSnapshot{}, fmt.Errorf("quota store is unavailable")
	}
	s.mu.Lock()
	refresher := s.refresher
	if registered, ok := s.providers[quotaKey(provider, accountID)]; ok {
		refresher = registered.refresher
	}
	s.mu.Unlock()
	if refresher == nil {
		return domain.QuotaSnapshot{}, ports.ErrQuotaRefreshUnsupported
	}
	key := string(provider) + "\x00" + string(accountID)
	result, err, _ := s.refreshes.Do(key, func() (any, error) {
		snapshot, readErr := refresher.RefreshQuota(ctx, provider, accountID)
		if readErr != nil {
			return nil, readErr
		}
		if recordErr := s.RecordQuotaSnapshot(ctx, snapshot); recordErr != nil {
			return nil, recordErr
		}
		return snapshot, nil
	})
	if err != nil {
		_ = s.store.RecordQuotaRefreshFailure(context.WithoutCancel(ctx), provider, accountID, err.Error())
		return domain.QuotaSnapshot{}, err
	}
	snapshot, ok := result.(domain.QuotaSnapshot)
	if !ok {
		return domain.QuotaSnapshot{}, fmt.Errorf("unexpected refreshed quota result type %T", result)
	}
	return snapshot, nil
}

// RefreshAll refreshes stale daemon-owned accounts and then returns every
// durable snapshot.
func (s *Service) RefreshAll(ctx context.Context) ([]domain.QuotaSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("quota store is unavailable")
	}
	s.RefreshRegisteredIfStale(ctx)
	return s.store.ListQuotaSnapshots(ctx)
}

// RefreshRegisteredIfStale refreshes daemon-owned accounts whose last good
// observation is outside FreshWindow. It is suitable for startup, periodic,
// page-entry, and completion triggers without multiplying provider traffic.
func (s *Service) RefreshRegisteredIfStale(ctx context.Context) {
	s.mu.Lock()
	accounts := make([]providerRefresher, 0, len(s.providers))
	for _, registered := range s.providers {
		accounts = append(accounts, registered)
	}
	s.mu.Unlock()
	for _, account := range accounts {
		snapshot, ok, err := s.store.GetQuotaSnapshot(ctx, account.provider, account.accountID)
		if err == nil && ok && time.Since(snapshot.ObservedAt) < FreshWindow {
			continue
		}
		_, _ = s.Refresh(ctx, account.provider, account.accountID)
	}
}

// StartAutoRefresh performs an initial daemon-owned read and repeats it at the
// requested interval until ctx is cancelled.
func (s *Service) StartAutoRefresh(ctx context.Context, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RefreshRegisteredIfStale(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RefreshRegisteredIfStale(ctx)
			}
		}
	}()
	return done
}

func quotaKey(provider domain.QuotaProviderID, accountID domain.QuotaAccountID) string {
	return string(provider) + "\x00" + string(accountID)
}

// StartMaintenance compacts durable history immediately and once per day. It
// returns a channel that closes after ctx is cancelled and the worker exits.
func (s *Service) StartMaintenance(ctx context.Context, onError func(error)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		compact := func() {
			if _, err := s.store.CompactQuotaHistory(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil && onError != nil {
				onError(err)
			}
		}
		compact()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				compact()
			}
		}
	}()
	return done
}
