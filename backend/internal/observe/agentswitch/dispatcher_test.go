package agentswitch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDispatcherRetriesSameEventAfterAcceptedResponseLost(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	claim := dispatcherTestClaim(now)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{claim, claim}}
	observer := &dispatcherObserverFake{results: []ports.DeliveryResult{
		{Outcome: ports.DeliveryTransientFailure, Class: ports.DeliveryResponseLost},
		{Outcome: ports.DeliveryAccepted, Class: ports.DeliveryErrorNone},
	}}
	d := mustDispatcher(t, DispatcherConfig{
		Store: store, Observer: observer, Policy: newDispatcherPolicyFake(), Clock: func() time.Time { return now },
		NewToken: func() string { return "lease-token" }, Jitter: func(base time.Duration) time.Duration { return base },
	})

	if worked := d.runOne(context.Background()); !worked {
		t.Fatal("first cycle did not claim work")
	}
	if worked := d.runOne(context.Background()); !worked {
		t.Fatal("retry cycle did not claim work")
	}
	if len(observer.events) != 2 {
		t.Fatalf("observer calls = %d, want 2", len(observer.events))
	}
	if observer.events[0].EventID != observer.events[1].EventID {
		t.Fatalf("EventID changed across retry: %q then %q", observer.events[0].EventID, observer.events[1].EventID)
	}
	if !bytes.Equal(observer.events[0].CanonicalEventJSON, observer.events[1].CanonicalEventJSON) {
		t.Fatal("immutable canonical event bytes changed across retry")
	}
}

func TestDispatcherClaimUsesThirtySecondLeaseAndAttemptBeginsAfterGate(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{dispatcherTestClaim(now)}}
	policy := newDispatcherPolicyFake()
	d := mustDispatcher(t, DispatcherConfig{
		Store: store, Observer: &dispatcherObserverFake{results: []ports.DeliveryResult{{Outcome: ports.DeliveryAccepted}}},
		Policy: policy, Clock: func() time.Time { return now }, NewToken: func() string { return "lease-token" },
	})

	d.runOne(context.Background())
	if len(store.claimRequests) != 1 {
		t.Fatalf("claim requests = %d, want 1", len(store.claimRequests))
	}
	request := store.claimRequests[0]
	if got := request.LeaseExpiresAt.Sub(request.Now); got != 30*time.Second {
		t.Fatalf("lease duration = %s, want 30s", got)
	}
	if len(store.attempts) != 1 {
		t.Fatalf("begin attempts = %d, want 1", len(store.attempts))
	}
	if !policy.entered {
		t.Fatal("final attempt began before entering the delivery gate")
	}
}

func TestDispatcherDoesNotBeginCallAtTTLBoundary(t *testing.T) {
	start := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	now := start
	claim := dispatcherTestClaim(start)
	claim.ExpiresAt = start.Add(time.Second)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{claim}}
	policy := newDispatcherPolicyFake()
	policy.onEnter = func() { now = claim.ExpiresAt }
	observer := &dispatcherObserverFake{}
	d := mustDispatcher(t, DispatcherConfig{
		Store: store, Observer: observer, Policy: policy, Clock: func() time.Time { return now }, NewToken: func() string { return "lease-token" },
	})

	d.runOne(context.Background())
	if len(store.attempts) != 0 {
		t.Fatal("dispatcher ran final attempt CAS at or after expiry")
	}
	if len(observer.events) != 0 {
		t.Fatal("dispatcher invoked observer at or after expiry")
	}
	if len(store.expirations) < 2 || !store.expirations[len(store.expirations)-1].Equal(claim.ExpiresAt) {
		t.Fatalf("expiry maintenance calls = %v", store.expirations)
	}
}

func TestDispatcherSettlesCallThatBeganBeforeTTLAfterTTL(t *testing.T) {
	start := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	now := start
	claim := dispatcherTestClaim(start)
	claim.ExpiresAt = start.Add(time.Second)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{claim}}
	observer := &dispatcherObserverFake{observe: func(context.Context, domain.AgentSwitchFailureEvent) ports.DeliveryResult {
		now = claim.ExpiresAt.Add(time.Second)
		return ports.DeliveryResult{Outcome: ports.DeliveryAccepted, Class: ports.DeliveryErrorNone}
	}}
	d := mustDispatcher(t, DispatcherConfig{
		Store: store, Observer: observer, Policy: newDispatcherPolicyFake(), Clock: func() time.Time { return now }, NewToken: func() string { return "lease-token" },
	})

	d.runOne(context.Background())
	if len(store.attempts) != 1 || len(observer.events) != 1 {
		t.Fatalf("pre-expiry attempt/observer calls = %d/%d, want 1/1", len(store.attempts), len(observer.events))
	}
	if len(store.settlements) != 1 || store.settlements[0].Result.Outcome != ports.DeliveryAccepted {
		t.Fatalf("post-expiry accepted settlement = %+v", store.settlements)
	}
}

func TestDispatcherCallsObserverOnlyAfterWinningFinalAttemptCAS(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{dispatcherTestClaim(now)}, beginChanged: boolPointer(false)}
	observer := &dispatcherObserverFake{}
	d := mustDispatcher(t, DispatcherConfig{
		Store: store, Observer: observer, Policy: newDispatcherPolicyFake(), Clock: func() time.Time { return now }, NewToken: func() string { return "lease-token" },
	})

	d.runOne(context.Background())
	if len(observer.events) != 0 {
		t.Fatal("observer ran after losing final attempt CAS")
	}
	if len(store.settlements) != 1 || store.settlements[0].Result.Outcome != ports.DeliveryPolicyCancelled {
		t.Fatalf("lost CAS settlement = %+v", store.settlements)
	}
}

func TestDispatcherPersistsAcceptedResponseThrottleWithAcknowledgement(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	retryAt := now.Add(45 * time.Minute)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{dispatcherTestClaim(now)}}
	d := mustDispatcher(t, DispatcherConfig{
		Store: store,
		Observer: &dispatcherObserverFake{results: []ports.DeliveryResult{{
			Outcome: ports.DeliveryAccepted, Class: ports.DeliveryErrorNone,
			RetryNotBefore: retryAt, ThrottleScope: ports.DeliveryThrottleAll,
		}}},
		Policy: newDispatcherPolicyFake(), Clock: func() time.Time { return now }, NewToken: func() string { return "lease-token" },
	})

	d.runOne(context.Background())
	if len(store.settlements) != 1 {
		t.Fatalf("settlements = %d, want 1", len(store.settlements))
	}
	got := store.settlements[0].Result
	if got.Outcome != ports.DeliveryAccepted || got.ThrottleScope != ports.DeliveryThrottleAll || !got.RetryNotBefore.Equal(retryAt) {
		t.Fatalf("accepted settlement lost throttle: %+v", got)
	}
}

func TestDispatcherBindsTransientThrottleToLaterRetryDeadlineAndTTL(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		providerRetry time.Time
		expiresAt     time.Time
		want          time.Time
	}{
		{name: "headerless throttle uses local retry", expiresAt: now.Add(time.Hour), want: now.Add(5 * time.Second)},
		{name: "later provider retry wins", providerRetry: now.Add(20 * time.Second), expiresAt: now.Add(time.Hour), want: now.Add(20 * time.Second)},
		{name: "payload TTL caps throttle", expiresAt: now.Add(2 * time.Second), want: now.Add(2 * time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim := dispatcherTestClaim(now)
			claim.ExpiresAt = tc.expiresAt
			store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{claim}}
			d := mustDispatcher(t, DispatcherConfig{
				Store: store,
				Observer: &dispatcherObserverFake{results: []ports.DeliveryResult{{
					Outcome: ports.DeliveryTransientFailure, Class: ports.DeliveryErrorRateLimited,
					RetryNotBefore: tc.providerRetry, ThrottleScope: ports.DeliveryThrottleAll,
				}}},
				Policy: newDispatcherPolicyFake(), Clock: func() time.Time { return now },
				NewToken: func() string { return "lease-token" }, Jitter: func(base time.Duration) time.Duration { return base },
			})

			if worked := d.runOne(context.Background()); !worked {
				t.Fatal("dispatcher did not claim throttled delivery")
			}
			if len(store.settlements) != 1 {
				t.Fatalf("settlements = %d, want 1", len(store.settlements))
			}
			settlement := store.settlements[0]
			if !settlement.NextAvailableAt.Equal(tc.want) || !settlement.Result.RetryNotBefore.Equal(tc.want) || settlement.Result.ThrottleScope != ports.DeliveryThrottleAll {
				t.Fatalf("throttled settlement = %+v, want deadline %s", settlement, tc.want)
			}
		})
	}
}

func TestDispatcherEqualJitterBoundsAndRetrySchedule(t *testing.T) {
	bases := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute, 6 * time.Hour, 6 * time.Hour}
	for attempt, wantBase := range bases {
		got := dispatcherRetryDelay(int64(attempt+1), func(base time.Duration) time.Duration {
			if base != wantBase {
				t.Fatalf("attempt %d base = %s, want %s", attempt+1, base, wantBase)
			}
			return base / 4
		})
		if got != wantBase/2 {
			t.Fatalf("attempt %d clamped jitter = %s, want %s", attempt+1, got, wantBase/2)
		}
		got = dispatcherRetryDelay(int64(attempt+1), func(base time.Duration) time.Duration { return base * 2 })
		if got != wantBase {
			t.Fatalf("attempt %d upper jitter = %s, want %s", attempt+1, got, wantBase)
		}
	}
}

func TestDispatcherPolicyCancellationLeavesPurgeOwnershipToCoordinator(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{dispatcherTestClaim(now)}}
	policy := newDispatcherPolicyFake()
	observer := &dispatcherObserverFake{observe: func(ctx context.Context, _ domain.AgentSwitchFailureEvent) ports.DeliveryResult {
		policy.cancelCalls()
		<-ctx.Done()
		return ports.DeliveryResult{Outcome: ports.DeliveryTransientFailure, Class: ports.DeliveryErrorNetwork}
	}}
	d := mustDispatcher(t, DispatcherConfig{
		Store: store, Observer: observer, Policy: policy, Clock: func() time.Time { return now }, NewToken: func() string { return "lease-token" },
	})

	d.runOne(context.Background())
	if len(store.settlements) != 0 {
		t.Fatalf("policy cancellation settled a row owned by purge: %+v", store.settlements)
	}
}

func TestDispatcherRejectsNoopObserver(t *testing.T) {
	_, err := NewDispatcher(DispatcherConfig{Store: &dispatcherStoreFake{}, Policy: newDispatcherPolicyFake()})
	if !errors.Is(err, ErrDispatcherObserverRequired) {
		t.Fatalf("NewDispatcher error = %v, want ErrDispatcherObserverRequired", err)
	}
}

func TestDispatcherStopDeadlineCancelsAndReleasesActiveLease(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{dispatcherTestClaim(now)}}
	started := make(chan struct{})
	observer := &dispatcherObserverFake{observe: func(ctx context.Context, _ domain.AgentSwitchFailureEvent) ports.DeliveryResult {
		close(started)
		<-ctx.Done()
		return ports.DeliveryResult{Outcome: ports.DeliveryTransientFailure, Class: ports.DeliveryErrorNetwork}
	}}
	d := mustDispatcher(t, DispatcherConfig{
		Store: store, Observer: observer, Policy: newDispatcherPolicyFake(), Clock: func() time.Time { return now }, NewToken: func() string { return "lease-token" },
	})
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	done := d.Start(lifetime)
	<-started
	cancelLifetime()
	stopCtx, cancelStop := context.WithCancel(context.Background())
	cancelStop()
	if err := d.Stop(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop error = %v, want context.Canceled", err)
	}
	<-done
	if len(store.settlements) != 1 || store.settlements[0].Result.Outcome != ports.DeliveryShutdownCancelled {
		t.Fatalf("shutdown settlement = %+v", store.settlements)
	}
}

func TestDispatcherOrdinaryStopLetsActiveCallFinishAndSettle(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 15, 30, 0, time.UTC)
	store := &dispatcherStoreFake{claims: []ports.AgentSwitchFailureClaim{dispatcherTestClaim(now)}}
	started := make(chan struct{})
	finish := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	observer := &dispatcherObserverFake{observe: func(ctx context.Context, _ domain.AgentSwitchFailureEvent) ports.DeliveryResult {
		close(started)
		select {
		case <-ctx.Done():
			cancelled <- struct{}{}
		case <-finish:
		}
		return ports.DeliveryResult{Outcome: ports.DeliveryAccepted, Class: ports.DeliveryErrorNone}
	}}
	d := mustDispatcher(t, DispatcherConfig{
		Store: store, Observer: observer, Policy: newDispatcherPolicyFake(), Clock: func() time.Time { return now }, NewToken: func() string { return "lease-token" },
	})
	d.Start(context.Background())
	<-started
	stopResult := make(chan error, 1)
	go func() { stopResult <- d.Stop(context.Background()) }()
	close(finish)
	if err := <-stopResult; err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
		t.Fatal("ordinary shutdown cancelled the bounded provider call")
	default:
	}
	if len(store.settlements) != 1 || store.settlements[0].Result.Outcome != ports.DeliveryAccepted {
		t.Fatalf("ordinary shutdown settlement = %+v", store.settlements)
	}
}

func mustDispatcher(t *testing.T, config DispatcherConfig) *Dispatcher {
	t.Helper()
	config.Logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	d, err := NewDispatcher(config)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func dispatcherTestClaim(now time.Time) ports.AgentSwitchFailureClaim {
	const eventID = "0123456789abcdef0123456789abcdef"
	return ports.AgentSwitchFailureClaim{
		ID: eventID,
		Event: domain.AgentSwitchFailureEvent{
			EventID: eventID, EnvelopeEncodingVersion: 1,
			CanonicalEventJSON: []byte(`{"event_id":"` + eventID + `"}`),
		},
		LeaseToken: "lease-token", ConsentGeneration: "generation", DeliveryEpoch: 7,
		DestinationFingerprint: "destination", ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
}

type dispatcherStoreFake struct {
	mu            sync.Mutex
	claims        []ports.AgentSwitchFailureClaim
	claimRequests []ports.AgentSwitchFailureClaimRequest
	attempts      []ports.AgentSwitchFailureAttempt
	settlements   []ports.AgentSwitchFailureSettlement
	expirations   []time.Time
	beginChanged  *bool
}

func (s *dispatcherStoreFake) ForceDisableAgentSwitchFailurePolicy(context.Context, time.Time) error {
	return nil
}
func (s *dispatcherStoreFake) ApplyAgentSwitchFailurePolicy(context.Context, ports.AgentSwitchFailurePolicy) error {
	return nil
}
func (s *dispatcherStoreFake) PurgeAgentSwitchFailurePayloads(context.Context) (int64, error) {
	return 0, nil
}
func (s *dispatcherStoreFake) EnrollCurrentAgentSwitchRecoveryMarkers(context.Context, ports.AgentSwitchFailureRecoveryEnrollment) (int64, error) {
	return 0, nil
}
func (s *dispatcherStoreFake) ClaimAgentSwitchFailure(_ context.Context, request ports.AgentSwitchFailureClaimRequest) (ports.AgentSwitchFailureClaim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimRequests = append(s.claimRequests, request)
	if len(s.claims) == 0 {
		return ports.AgentSwitchFailureClaim{}, false, nil
	}
	claim := s.claims[0]
	s.claims = s.claims[1:]
	claim.LeaseToken = request.LeaseToken
	claim.ConsentGeneration = request.Authorization.ConsentGeneration
	claim.DeliveryEpoch = request.DeliveryEpoch
	return claim, true, nil
}
func (s *dispatcherStoreFake) BeginAgentSwitchFailureAttempt(_ context.Context, attempt ports.AgentSwitchFailureAttempt) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, attempt)
	return s.beginChanged == nil || *s.beginChanged, nil
}
func (s *dispatcherStoreFake) SettleAgentSwitchFailureDelivery(_ context.Context, settlement ports.AgentSwitchFailureSettlement) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlements = append(s.settlements, settlement)
	return true, nil
}
func (s *dispatcherStoreFake) ExpireAgentSwitchFailurePayloads(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expirations = append(s.expirations, now)
	return 0, nil
}
func (s *dispatcherStoreFake) ResolveAgentSwitchFailureReceipts(context.Context, ports.AgentSwitchFailureReceiptResolution) (int64, error) {
	return 0, nil
}
func (s *dispatcherStoreFake) AgentSwitchFailureBacklog(context.Context, time.Time) (ports.AgentSwitchFailureBacklog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ports.AgentSwitchFailureBacklog{Pending: int64(len(s.claims))}, nil
}

type dispatcherPolicyFake struct {
	mu      sync.Mutex
	auth    domain.AgentSwitchReportingAuthorization
	epoch   int64
	entered bool
	onEnter func()
	calls   []context.CancelFunc
}

func newDispatcherPolicyFake() *dispatcherPolicyFake {
	return &dispatcherPolicyFake{auth: domain.AgentSwitchReportingAuthorization{Enabled: true, ConsentGeneration: "generation", DestinationFingerprint: "destination"}, epoch: 7}
}
func (p *dispatcherPolicyFake) ForceDisabled(context.Context) error { return nil }
func (p *dispatcherPolicyFake) Synchronize(context.Context) error   { return nil }
func (p *dispatcherPolicyFake) PrepareDisable(context.Context) (ports.AgentSwitchFailurePolicyAcknowledgement, error) {
	p.cancelCalls()
	return ports.AgentSwitchFailurePolicyAcknowledgement{Authorization: p.Authorization(), GateDrained: true}, nil
}
func (p *dispatcherPolicyFake) ApplyPolicy(context.Context, string, bool) (ports.AgentSwitchFailurePolicyAcknowledgement, error) {
	return ports.AgentSwitchFailurePolicyAcknowledgement{Authorization: p.Authorization()}, nil
}
func (p *dispatcherPolicyFake) Authorization() domain.AgentSwitchReportingAuthorization {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.auth
}
func (p *dispatcherPolicyFake) DeliveryEpoch() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.epoch
}
func (p *dispatcherPolicyFake) EnterDelivery(parent context.Context, generation string, epoch int64) (context.Context, func(), bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.auth.Enabled || generation != p.auth.ConsentGeneration || epoch != p.epoch {
		return parent, func() {}, false
	}
	p.entered = true
	if p.onEnter != nil {
		p.onEnter()
	}
	ctx, cancel := context.WithCancel(parent)
	p.calls = append(p.calls, cancel)
	return ctx, cancel, true
}
func (p *dispatcherPolicyFake) CloseAndDrain(context.Context) error {
	p.cancelCalls()
	return nil
}
func (p *dispatcherPolicyFake) cancelCalls() {
	p.mu.Lock()
	calls := append([]context.CancelFunc(nil), p.calls...)
	p.calls = nil
	p.auth.Enabled = false
	p.epoch++
	p.mu.Unlock()
	for _, cancel := range calls {
		cancel()
	}
}

type dispatcherObserverFake struct {
	mu      sync.Mutex
	results []ports.DeliveryResult
	events  []domain.AgentSwitchFailureEvent
	observe func(context.Context, domain.AgentSwitchFailureEvent) ports.DeliveryResult
}

func (o *dispatcherObserverFake) ObserveAgentSwitchFailure(ctx context.Context, event domain.AgentSwitchFailureEvent) ports.DeliveryResult {
	o.mu.Lock()
	event.CanonicalEventJSON = append([]byte(nil), event.CanonicalEventJSON...)
	o.events = append(o.events, event)
	observe := o.observe
	if observe == nil && len(o.results) > 0 {
		result := o.results[0]
		o.results = o.results[1:]
		o.mu.Unlock()
		return result
	}
	o.mu.Unlock()
	if observe != nil {
		return observe(ctx, event)
	}
	return ports.DeliveryResult{Outcome: ports.DeliveryPermanentFailure, Class: ports.DeliveryErrorLocalInvariant}
}

func boolPointer(value bool) *bool { return &value }
