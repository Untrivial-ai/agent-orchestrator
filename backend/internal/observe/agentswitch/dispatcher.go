package agentswitch

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	dispatcherLeaseDuration = 30 * time.Second
	dispatcherPollInterval  = time.Second
	dispatcherSettleTimeout = 5 * time.Second
)

// Dispatcher construction errors identify missing required dependencies.
var (
	ErrDispatcherStoreRequired    = errors.New("agent switch failure dispatcher store is required")
	ErrDispatcherObserverRequired = errors.New("agent switch failure dispatcher observer is required")
	ErrDispatcherPolicyRequired   = errors.New("agent switch failure dispatcher policy is required")
	errDispatcherShutdown         = errors.New("agent switch failure dispatcher shutdown")
)

// DispatcherConfig supplies the durable queue, delivery observer, policy gate,
// and optional scheduling hooks used by a Dispatcher.
type DispatcherConfig struct {
	Store    ports.AgentSwitchFailureOutboxStore
	Observer ports.AgentSwitchFailureObserver
	Policy   PolicyCoordinator
	Clock    func() time.Time
	NewToken func() string
	Jitter   func(time.Duration) time.Duration
	Logger   *slog.Logger
}

// Dispatcher performs one durable claim, gated provider call, and matching
// settlement at a time. It has no session or saga mutation dependency.
type Dispatcher struct {
	store    ports.AgentSwitchFailureOutboxStore
	observer ports.AgentSwitchFailureObserver
	policy   PolicyCoordinator
	clock    func() time.Time
	newToken func() string
	jitter   func(time.Duration) time.Duration
	logger   *slog.Logger

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
	mu        sync.Mutex
	started   bool
	stopping  bool
	claimStop context.CancelFunc
	active    context.CancelCauseFunc
}

// NewDispatcher validates config and returns an idle failure-event dispatcher.
func NewDispatcher(config DispatcherConfig) (*Dispatcher, error) {
	if config.Store == nil {
		return nil, ErrDispatcherStoreRequired
	}
	if config.Observer == nil {
		return nil, ErrDispatcherObserverRequired
	}
	if config.Policy == nil {
		return nil, ErrDispatcherPolicyRequired
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.NewToken == nil {
		config.NewToken = uuid.NewString
	}
	if config.Jitter == nil {
		config.Jitter = defaultDispatcherJitter
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Dispatcher{
		store: config.Store, observer: config.Observer, policy: config.Policy,
		clock: config.Clock, newToken: config.NewToken, jitter: config.Jitter, logger: config.Logger,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}, nil
}

// Start launches the singleton loop and returns a channel closed after all
// active delivery and settlement work has stopped.
func (d *Dispatcher) Start(lifetime context.Context) <-chan struct{} {
	d.startOnce.Do(func() {
		claimContext, cancelClaims := context.WithCancel(lifetime)
		d.mu.Lock()
		d.started = true
		d.claimStop = cancelClaims
		d.mu.Unlock()
		go d.run(claimContext)
	})
	return d.done
}

// Wake asks the dispatcher to check durable work without blocking an emitter.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Stop prevents new claims. An active provider call retains its own five-second
// bound and may settle normally; only expiration of ctx cancels and releases it.
func (d *Dispatcher) Stop(ctx context.Context) error {
	d.stopOnce.Do(func() {
		d.mu.Lock()
		d.stopping = true
		cancelClaims := d.claimStop
		started := d.started
		d.mu.Unlock()
		close(d.stop)
		if cancelClaims != nil {
			cancelClaims()
		}
		if !started {
			d.startOnce.Do(func() { close(d.done) })
		}
	})
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		d.mu.Lock()
		cancelActive := d.active
		d.mu.Unlock()
		if cancelActive != nil {
			cancelActive(errDispatcherShutdown)
		}
		<-d.done
		return ctx.Err()
	}
}

func (d *Dispatcher) run(ctx context.Context) {
	defer close(d.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stop:
			return
		default:
		}
		if d.runOne(ctx) {
			continue
		}
		timer := time.NewTimer(dispatcherPollInterval)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-d.stop:
			stopTimer(timer)
			return
		case <-d.wake:
			stopTimer(timer)
		case <-timer.C:
		}
	}
}

// runOne returns true when it acquired a row. The loop immediately drains the
// next row after a completed cycle and polls only when no row was claimable.
func (d *Dispatcher) runOne(ctx context.Context) bool {
	policyErr := d.policy.Synchronize(ctx)
	now := d.clock().UTC()
	if _, err := d.store.ExpireAgentSwitchFailurePayloads(ctx, now); err != nil {
		d.logger.Warn("expire agent switch failure payloads", "error", err)
		return false
	}
	if policyErr != nil {
		d.logger.Warn("synchronize agent switch failure policy before claim", "error", policyErr)
		return false
	}
	authorization := d.policy.Authorization()
	if !authorization.Enabled || d.isStopping() {
		return false
	}
	claim, claimed, err := d.store.ClaimAgentSwitchFailure(ctx, ports.AgentSwitchFailureClaimRequest{
		Authorization: authorization, DeliveryEpoch: d.policy.DeliveryEpoch(), LeaseToken: d.newToken(),
		Now: now, LeaseExpiresAt: now.Add(dispatcherLeaseDuration),
	})
	if err != nil {
		d.logger.Warn("claim agent switch failure", "error", err)
		return false
	}
	if !claimed {
		return false
	}
	d.deliver(claim)
	return true
}

func (d *Dispatcher) deliver(claim ports.AgentSwitchFailureClaim) {
	gateContext, leaveGate, entered := d.policy.EnterDelivery(context.Background(), claim.ConsentGeneration, claim.DeliveryEpoch)
	if !entered {
		return
	}
	defer leaveGate()
	callContext, cancelCall := context.WithCancelCause(gateContext)
	d.setActive(cancelCall)
	defer d.clearActive()

	now := d.clock().UTC()
	if !now.Before(claim.ExpiresAt) {
		_, _ = d.store.ExpireAgentSwitchFailurePayloads(context.Background(), now)
		return
	}
	if d.isStopping() {
		cancelCall(errDispatcherShutdown)
		d.settle(claim, ports.DeliveryResult{Outcome: ports.DeliveryShutdownCancelled})
		return
	}
	if callContext.Err() != nil {
		d.settleCancellation(callContext, claim)
		return
	}
	began, err := d.store.BeginAgentSwitchFailureAttempt(callContext, ports.AgentSwitchFailureAttempt{
		ID: claim.ID, LeaseToken: claim.LeaseToken, ConsentGeneration: claim.ConsentGeneration,
		DeliveryEpoch: claim.DeliveryEpoch, DestinationFingerprint: claim.DestinationFingerprint, Now: now,
	})
	if err != nil {
		if callContext.Err() != nil {
			d.settleCancellation(callContext, claim)
		} else {
			d.logger.Warn("begin agent switch failure attempt", "error", err)
		}
		return
	}
	if !began {
		if !now.Before(claim.ExpiresAt) {
			_, _ = d.store.ExpireAgentSwitchFailurePayloads(context.Background(), now)
			return
		}
		d.settle(claim, ports.DeliveryResult{Outcome: ports.DeliveryPolicyCancelled})
		return
	}
	if callContext.Err() != nil {
		d.settleCancellation(callContext, claim)
		return
	}

	result := d.observer.ObserveAgentSwitchFailure(callContext, claim.Event)
	if callContext.Err() != nil {
		result = dispatcherCancellationResult(callContext)
	}
	if result.Outcome == ports.DeliveryPolicyCancelled {
		// Synchronize/apply-policy owns the atomic purge. A matching settlement
		// would only race the purge and cannot strengthen opt-out.
		return
	}
	d.settle(claim, result)
}

func (d *Dispatcher) settleCancellation(callContext context.Context, claim ports.AgentSwitchFailureClaim) {
	result := dispatcherCancellationResult(callContext)
	if result.Outcome != ports.DeliveryPolicyCancelled {
		d.settle(claim, result)
	}
}

func (d *Dispatcher) settle(claim ports.AgentSwitchFailureClaim, result ports.DeliveryResult) {
	settledAt := d.clock().UTC()
	if result.Outcome == "" {
		result = ports.DeliveryResult{Outcome: ports.DeliveryPermanentFailure, Class: ports.DeliveryErrorLocalInvariant}
	}
	if result.RetryNotBefore.After(claim.ExpiresAt) {
		result.RetryNotBefore = claim.ExpiresAt
	}
	nextAvailable := settledAt
	if result.Outcome == ports.DeliveryTransientFailure {
		nextAvailable = settledAt.Add(dispatcherRetryDelay(claim.AttemptCount+1, d.jitter))
		if result.RetryNotBefore.After(nextAvailable) {
			nextAvailable = result.RetryNotBefore
		}
		if nextAvailable.After(claim.ExpiresAt) {
			nextAvailable = claim.ExpiresAt
		}
		if result.ThrottleScope != ports.DeliveryThrottleNone {
			// A headerless 429 still throttles the destination. Bind that scope to
			// the same durable retry deadline so another row cannot bypass it.
			result.RetryNotBefore = nextAvailable
		}
	}
	settleContext, cancel := context.WithTimeout(context.Background(), dispatcherSettleTimeout)
	defer cancel()
	changed, err := d.store.SettleAgentSwitchFailureDelivery(settleContext, ports.AgentSwitchFailureSettlement{
		ID: claim.ID, LeaseToken: claim.LeaseToken, ConsentGeneration: claim.ConsentGeneration,
		DeliveryEpoch: claim.DeliveryEpoch, DestinationFingerprint: claim.DestinationFingerprint,
		SettledAt: settledAt, NextAvailableAt: nextAvailable, Result: result,
	})
	if err != nil {
		d.logger.Warn("settle agent switch failure delivery", "error", err)
	} else if !changed {
		d.logger.Debug("agent switch failure settlement lost lease", "event_id", claim.ID)
	}
}

func (d *Dispatcher) isStopping() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopping
}

func (d *Dispatcher) setActive(cancel context.CancelCauseFunc) {
	d.mu.Lock()
	d.active = cancel
	stopping := d.stopping
	d.mu.Unlock()
	if stopping {
		cancel(errDispatcherShutdown)
	}
}

func (d *Dispatcher) clearActive() {
	d.mu.Lock()
	// At most one call exists, so clearing cannot detach another delivery.
	d.active = nil
	d.mu.Unlock()
}

func dispatcherCancellationResult(ctx context.Context) ports.DeliveryResult {
	if errors.Is(context.Cause(ctx), errDispatcherShutdown) {
		return ports.DeliveryResult{Outcome: ports.DeliveryShutdownCancelled}
	}
	return ports.DeliveryResult{Outcome: ports.DeliveryPolicyCancelled}
}

func dispatcherRetryDelay(attempt int64, jitter func(time.Duration) time.Duration) time.Duration {
	bases := [...]time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute, 6 * time.Hour}
	index := attempt - 1
	if index < 0 {
		index = 0
	}
	if index >= int64(len(bases)) {
		index = int64(len(bases) - 1)
	}
	base := bases[index]
	delay := jitter(base)
	if delay < base/2 {
		return base / 2
	}
	if delay > base {
		return base
	}
	return delay
}

func defaultDispatcherJitter(base time.Duration) time.Duration {
	half := base / 2
	// #nosec G404 -- retry jitter is not used for secrets or access control.
	return half + time.Duration(rand.Int64N(int64(base-half)+1))
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
