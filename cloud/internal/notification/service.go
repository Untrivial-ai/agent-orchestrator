package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
)

var ErrInvalidPayload = errors.New("invalid notification payload")

const (
	defaultLeaseDuration  = 30 * time.Second
	defaultProcessTimeout = 20 * time.Second
	defaultWakeInterval   = time.Second
	maxAttempts           = 10
	maxBackoff            = 5 * time.Minute
)

type Store interface {
	ClaimNotificationEvent(context.Context, string, time.Duration) (domain.NotificationIngress, bool, error)
	CompleteNotificationEvent(context.Context, string, string, string) error
	RetryNotificationEvent(context.Context, string, string, string, string, time.Time, bool) error
	CreateNotificationFromIngress(context.Context, domain.NotificationIngress, domain.Notification) (domain.Notification, bool, error)
}

type Config struct {
	Owner          string
	LeaseDuration  time.Duration
	ProcessTimeout time.Duration
	WakeInterval   time.Duration
	Now            func() time.Time
	Jitter         func(time.Duration) time.Duration
	Logger         *slog.Logger
}

type Service struct {
	store          Store
	owner          string
	leaseDuration  time.Duration
	processTimeout time.Duration
	wakeInterval   time.Duration
	now            func() time.Time
	jitter         func(time.Duration) time.Duration
	logger         *slog.Logger
	wake           chan struct{}
}

func NewService(store Store, config Config) *Service {
	if config.Owner == "" {
		config.Owner = "notification-" + uuid.NewString()
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = defaultLeaseDuration
	}
	if config.ProcessTimeout <= 0 {
		config.ProcessTimeout = defaultProcessTimeout
	}
	if config.WakeInterval <= 0 {
		config.WakeInterval = defaultWakeInterval
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Jitter == nil {
		config.Jitter = func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(max)))
		}
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Service{
		store: store, owner: config.Owner, leaseDuration: config.LeaseDuration,
		processTimeout: config.ProcessTimeout, wakeInterval: config.WakeInterval,
		now: config.Now, jitter: config.Jitter, logger: config.Logger,
		wake: make(chan struct{}, 1),
	}
}

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.wakeInterval)
	defer ticker.Stop()

	s.Wake()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
		for {
			processed, err := s.processOne(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.logger.Error("process cloud notification", "error", err)
				}
				break
			}
			if !processed {
				break
			}
		}
	}
}

func (s *Service) ProcessOne(ctx context.Context) error {
	_, err := s.processOne(ctx)
	return err
}

func (s *Service) processOne(ctx context.Context) (bool, error) {
	ingress, found, err := s.store.ClaimNotificationEvent(ctx, s.owner, s.leaseDuration)
	if err != nil || !found {
		return false, err
	}
	ingress.LeaseOwner = s.owner
	processContext, cancel := context.WithTimeout(ctx, s.processTimeout)
	defer cancel()

	if ingress.RecipientUserID == "" {
		return true, s.store.CompleteNotificationEvent(processContext, ingress.OrgID, ingress.ID, s.owner)
	}
	notification, err := BuildNotification(ingress)
	if err != nil {
		retryErr := s.store.RetryNotificationEvent(
			processContext, ingress.OrgID, ingress.ID, s.owner, err.Error(), s.now(), true,
		)
		if retryErr != nil {
			return true, errors.Join(err, retryErr)
		}
		return true, err
	}
	if _, _, err := s.store.CreateNotificationFromIngress(processContext, ingress, notification); err != nil {
		terminal := ingress.AttemptCount >= maxAttempts
		retryAt := s.now()
		if !terminal {
			retryAt = retryAt.Add(s.backoff(ingress.AttemptCount))
		}
		retryErr := s.store.RetryNotificationEvent(
			processContext, ingress.OrgID, ingress.ID, s.owner, err.Error(), retryAt, terminal,
		)
		if retryErr != nil {
			return true, errors.Join(err, retryErr)
		}
		return true, err
	}
	return true, nil
}

func (s *Service) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := time.Second << min(attempt-1, 8)
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff + s.jitter(backoff/4)
}

func BuildNotification(ingress domain.NotificationIngress) (domain.Notification, error) {
	var payload struct {
		ActivityID string `json:"activityId"`
		Message    string `json:"message"`
	}
	if len(ingress.Event.Payload) == 0 || json.Unmarshal(ingress.Event.Payload, &payload) != nil {
		return domain.Notification{}, ErrInvalidPayload
	}
	payload.ActivityID = strings.TrimSpace(payload.ActivityID)
	payload.Message = strings.TrimSpace(payload.Message)

	notification := domain.Notification{
		OrgID: ingress.OrgID, RecipientUserID: ingress.RecipientUserID,
		ProjectID: ingress.ProjectID, SessionID: ingress.SessionID,
		Source: "cloud", Type: string(ingress.Event.Type), Body: payload.Message,
		Status: string(domain.NotificationStatusUnread), EventID: ingress.Event.EventID,
		Metadata: ingress.Event.Payload,
	}
	switch ingress.Event.Type {
	case domain.NotificationTypeNeedsInput:
		if payload.ActivityID == "" {
			return domain.Notification{}, ErrInvalidPayload
		}
		notification.Title = "Agent needs input"
		notification.DedupeKey = fmt.Sprintf("needs-input:%s:%s", ingress.SessionID, payload.ActivityID)
	case domain.NotificationTypeAgentFailed:
		notification.Title = "Agent stopped with an error"
		notification.DedupeKey = fmt.Sprintf("agent-failed:%s:%d", ingress.SessionID, ingress.WorkerEpoch)
	case domain.NotificationTypeAgentCompleted:
		if payload.ActivityID == "" {
			return domain.Notification{}, ErrInvalidPayload
		}
		notification.Title = "Agent completed its work"
		notification.DedupeKey = fmt.Sprintf("agent-completed:%s:%d:%s", ingress.SessionID, ingress.WorkerEpoch, payload.ActivityID)
	default:
		return domain.Notification{}, ErrInvalidPayload
	}
	return notification, nil
}
