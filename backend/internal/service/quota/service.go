package quota

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"golang.org/x/sync/singleflight"
)

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
	refresher QuotaRefresher
	refreshes singleflight.Group
	collects  singleflight.Group
	reads     map[string]cachedRateLimits
}

type cachedRateLimits struct {
	limits     ports.ChatRateLimits
	observedAt time.Time
}

type QuotaRefresher interface {
	RefreshQuota(context.Context, domain.QuotaProviderID, domain.QuotaAccountID) (domain.QuotaSnapshot, error)
}

func New(store Store) *Service {
	return &Service{store: store, reads: make(map[string]cachedRateLimits)}
}

// SetRefresher late-binds the live chat service, breaking the construction cycle:
// chat writes provider events to this service, while manual refresh needs a live
// provider conversation owned by chat.
func (s *Service) SetRefresher(refresher QuotaRefresher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresher = refresher
}

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

func (s *Service) Alerts(ctx context.Context, since time.Time, limit int64) ([]domain.QuotaAlert, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("quota store is unavailable")
	}
	return s.store.ListQuotaAlerts(ctx, since, limit)
}

func (s *Service) List(ctx context.Context) ([]domain.QuotaSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("quota store is unavailable")
	}
	return s.store.ListQuotaSnapshots(ctx)
}

func (s *Service) Get(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID) (domain.QuotaSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return domain.QuotaSnapshot{}, false, fmt.Errorf("quota store is unavailable")
	}
	return s.store.GetQuotaSnapshot(ctx, provider, accountID)
}

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
	return result.(ports.ChatRateLimits), nil
}

func (s *Service) cachedRead(key string, now time.Time) (ports.ChatRateLimits, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached, ok := s.reads[key]
	return cached.limits, ok && now.Sub(cached.observedAt) < FreshWindow
}

func (s *Service) Refresh(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID) (domain.QuotaSnapshot, error) {
	if s == nil || s.store == nil {
		return domain.QuotaSnapshot{}, fmt.Errorf("quota store is unavailable")
	}
	s.mu.Lock()
	refresher := s.refresher
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
	return result.(domain.QuotaSnapshot), nil
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
