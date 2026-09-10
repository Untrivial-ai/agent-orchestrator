// Package agentswitch owns the daemon-side consent mirror and delivery gate for
// agent-switch failure reporting. The desktop JSON file is the durable
// authority; SQLite is only its transaction-bound mirror.
package agentswitch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// PolicyFileName is the durable desktop telemetry-policy authority filename.
const (
	PolicyFileName                      = "telemetry_policy.json"
	agentSwitchFailureProductionEnabled = domain.AgentSwitchFailureProductionEnabled
)

// Policy synchronization errors describe rejected authority state or cleanup.
var (
	ErrPolicyHintMismatch   = errors.New("telemetry policy hint does not match durable authority")
	ErrPolicyUnavailable    = errors.New("telemetry policy authority is unavailable or unsafe")
	ErrPolicyCleanupPending = errors.New("telemetry policy disable cleanup is pending")
)

// PolicyStore persists the transaction-bound daemon mirror of telemetry policy.
type PolicyStore interface {
	ConfigureAgentSwitchFailureEventMetadata(context.Context, domain.AgentSwitchEventMetadata) error
	ForceDisableAgentSwitchFailurePolicy(context.Context, time.Time) error
	ApplyAgentSwitchFailurePolicy(context.Context, ports.AgentSwitchFailurePolicy) error
	PurgeAgentSwitchFailurePayloads(context.Context) (int64, error)
	EnrollCurrentAgentSwitchRecoveryMarkers(context.Context, ports.AgentSwitchFailureRecoveryEnrollment) (int64, error)
}

// PolicyOptions supplies authority location, release gates, metadata, and hooks.
type PolicyOptions struct {
	AuthorityReader         ports.AgentSwitchFailureAuthorityReader
	TelemetryEvents         bool
	TelemetryEventsExplicit bool
	DestinationFingerprint  string
	StreamKillSwitched      bool
	ProductionEnabled       *bool
	Metadata                domain.AgentSwitchEventMetadata
	Now                     func() time.Time
	NewBootToken            func() string
	OnEventsChanged         func(bool)
	ProviderDrain           func(context.Context) error
}

// PolicyCoordinator synchronizes durable consent and gates provider deliveries.
type PolicyCoordinator interface {
	ForceDisabled(context.Context) error
	Synchronize(context.Context) error
	PrepareDisable(context.Context) (ports.AgentSwitchFailurePolicyAcknowledgement, error)
	ApplyPolicy(context.Context, string, bool) (ports.AgentSwitchFailurePolicyAcknowledgement, error)
	Authorization() domain.AgentSwitchReportingAuthorization
	DeliveryEpoch() int64
	EnterDelivery(context.Context, string, int64) (context.Context, func(), bool)
	CloseAndDrain(context.Context) error
}

// Coordinator is the concurrency-safe PolicyCoordinator implementation.
type Coordinator struct {
	store         PolicyStore
	options       PolicyOptions
	bootToken     string
	metadataReady bool

	operationMu     sync.Mutex
	mu              sync.Mutex
	authorization   domain.AgentSwitchReportingAuthorization
	eventsEnabled   bool
	deliveryEpoch   int64
	disablePrepared bool
	gateClosed      bool
	gateDrained     bool
	purgeGeneration string
	purgeConfirmed  bool
	nextCall        uint64
	calls           map[uint64]context.CancelFunc
	callWG          sync.WaitGroup
}

// NewPolicyCoordinator constructs a fail-closed coordinator for store and options.
func NewPolicyCoordinator(store PolicyStore, options PolicyOptions) *Coordinator {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewBootToken == nil {
		options.NewBootToken = newBootToken
	}
	coordinator := &Coordinator{store: store, options: options, calls: make(map[uint64]context.CancelFunc)}
	coordinator.bootToken = options.NewBootToken()
	if store != nil {
		coordinator.metadataReady = store.ConfigureAgentSwitchFailureEventMetadata(context.Background(), options.Metadata) == nil
	}
	return coordinator
}

// ForceDisabled closes and drains delivery before disabling the durable mirror.
func (c *Coordinator) ForceDisabled(ctx context.Context) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	if err := c.closeGateAndDrain(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.disablePrepared = false
	c.purgeGeneration = ""
	c.purgeConfirmed = false
	c.mu.Unlock()
	if c.store == nil {
		return errors.New("agent switch policy store is unavailable")
	}
	return c.store.ForceDisableAgentSwitchFailurePolicy(ctx, c.options.Now().UTC())
}

// Synchronize applies the current durable desktop authority to the daemon mirror.
func (c *Coordinator) Synchronize(ctx context.Context) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	authority := c.readAuthority(ctx)
	_, err := c.synchronizeAuthority(ctx, authority)
	return err
}

// PrepareDisable closes the delivery gate and waits for active calls to drain.
func (c *Coordinator) PrepareDisable(ctx context.Context) (ports.AgentSwitchFailurePolicyAcknowledgement, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.mu.Lock()
	c.disablePrepared = true
	c.eventsEnabled = false
	c.mu.Unlock()
	c.notifyEventsChanged(false)
	if err := c.closeGateAndDrain(ctx); err != nil {
		return ports.AgentSwitchFailurePolicyAcknowledgement{}, err
	}
	return ports.AgentSwitchFailurePolicyAcknowledgement{
		Authorization: c.Authorization(), GateDrained: true,
	}, nil
}

// ApplyPolicy validates a desktop hint and synchronizes its durable authority.
func (c *Coordinator) ApplyPolicy(ctx context.Context, generation string, eventsEnabled bool) (ports.AgentSwitchFailurePolicyAcknowledgement, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	authority := c.readAuthority(ctx)
	if !authority.valid || authority.generation != generation || authority.eventsEnabled != eventsEnabled {
		_ = c.closeGateAndDrain(ctx)
		return ports.AgentSwitchFailurePolicyAcknowledgement{}, ErrPolicyHintMismatch
	}
	c.mu.Lock()
	disablePrepared := c.disablePrepared
	c.mu.Unlock()
	if disablePrepared && authority.eventsEnabled {
		_ = c.closeGateAndDrain(ctx)
		return ports.AgentSwitchFailurePolicyAcknowledgement{}, ErrPolicyCleanupPending
	}
	return c.synchronizeAuthority(ctx, authority)
}

// Authorization returns the current immutable reporting authorization snapshot.
func (c *Coordinator) Authorization() domain.AgentSwitchReportingAuthorization {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authorization
}

// DeliveryEpoch returns the epoch required to enter the current delivery gate.
func (c *Coordinator) DeliveryEpoch() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deliveryEpoch
}

// EventsEnabled reports whether the synchronized policy enables telemetry events.
func (c *Coordinator) EventsEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventsEnabled
}

// EnterDelivery synchronizes policy and conditionally enters the matching gate.
func (c *Coordinator) EnterDelivery(parent context.Context, generation string, epoch int64) (context.Context, func(), bool) {
	if err := c.Synchronize(parent); err != nil {
		return parent, func() {}, false
	}
	c.mu.Lock()
	if c.gateClosed || !c.authorization.Enabled || c.authorization.ConsentGeneration != generation || c.deliveryEpoch != epoch {
		c.mu.Unlock()
		return parent, func() {}, false
	}
	callContext, cancel := context.WithCancel(parent)
	callID := atomic.AddUint64(&c.nextCall, 1)
	c.calls[callID] = cancel
	c.callWG.Add(1)
	c.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.calls, callID)
			c.mu.Unlock()
			cancel()
			c.callWG.Done()
		})
	}
	return callContext, release, true
}

// CloseAndDrain closes the gate, cancels active calls, and waits for their exit.
func (c *Coordinator) CloseAndDrain(ctx context.Context) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.mu.Lock()
	c.disablePrepared = true
	c.eventsEnabled = false
	c.mu.Unlock()
	c.notifyEventsChanged(false)
	return c.closeGateAndDrain(ctx)
}

// StartWatcher periodically synchronizes policy until ctx is canceled.
func (c *Coordinator) StartWatcher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.Synchronize(ctx)
			}
		}
	}()
}

type authorityRead struct {
	valid         bool
	eventsEnabled bool
	generation    string
}

func (c *Coordinator) readAuthority(ctx context.Context) authorityRead {
	if c.options.AuthorityReader == nil {
		return authorityRead{}
	}
	snapshot, err := c.options.AuthorityReader.ReadAgentSwitchFailureAuthority(ctx)
	if err != nil {
		return authorityRead{}
	}
	if !snapshot.Present {
		return authorityRead{
			valid: true, eventsEnabled: c.options.TelemetryEventsExplicit && c.options.TelemetryEvents,
			generation: c.bootToken,
		}
	}
	return authorityRead{
		valid: true, eventsEnabled: snapshot.EventsEnabled && c.options.TelemetryEventsExplicit && c.options.TelemetryEvents,
		generation: snapshot.ConsentGeneration,
	}
}

func (c *Coordinator) desiredAuthorization(authority authorityRead) domain.AgentSwitchReportingAuthorization {
	authorization := domain.AgentSwitchReportingAuthorization{ConsentGeneration: authority.generation}
	productionEnabled := agentSwitchFailureProductionEnabled
	if c.options.ProductionEnabled != nil {
		productionEnabled = *c.options.ProductionEnabled
	}
	if authority.valid && authority.eventsEnabled && c.metadataReady && productionEnabled && !c.options.StreamKillSwitched && c.options.DestinationFingerprint != "" {
		authorization.Enabled = true
		authorization.DestinationFingerprint = c.options.DestinationFingerprint
	}
	return authorization
}

func (c *Coordinator) synchronizeAuthority(ctx context.Context, authority authorityRead) (ports.AgentSwitchFailurePolicyAcknowledgement, error) {
	desired := c.desiredAuthorization(authority)
	c.mu.Lock()
	disablePrepared := c.disablePrepared
	c.eventsEnabled = authority.valid && authority.eventsEnabled && !disablePrepared
	eventsEnabled := c.eventsEnabled
	current := c.authorization
	purgeConfirmed := c.purgeConfirmed && c.purgeGeneration == desired.ConsentGeneration
	c.mu.Unlock()
	c.notifyEventsChanged(eventsEnabled)
	if disablePrepared && authority.valid && authority.eventsEnabled {
		if err := c.closeGateAndDrain(ctx); err != nil {
			return ports.AgentSwitchFailurePolicyAcknowledgement{}, err
		}
		return ports.AgentSwitchFailurePolicyAcknowledgement{
			Authorization: c.Authorization(), GateDrained: true,
		}, nil
	}
	if current == desired && (desired.Enabled || purgeConfirmed) {
		if !desired.Enabled {
			if err := c.closeGateAndDrain(ctx); err != nil {
				return ports.AgentSwitchFailurePolicyAcknowledgement{}, err
			}
		}
		acknowledgement := c.policyAcknowledgement(desired)
		if !authority.valid {
			return acknowledgement, ErrPolicyUnavailable
		}
		return acknowledgement, nil
	}
	if err := c.closeGateAndDrain(ctx); err != nil {
		return ports.AgentSwitchFailurePolicyAcknowledgement{}, err
	}
	if c.store == nil {
		return ports.AgentSwitchFailurePolicyAcknowledgement{}, errors.New("agent switch policy store is unavailable")
	}
	if err := c.store.ApplyAgentSwitchFailurePolicy(ctx, ports.AgentSwitchFailurePolicy{Authorization: desired, UpdatedAt: c.options.Now().UTC()}); err != nil {
		c.invalidatePurgeProof()
		return ports.AgentSwitchFailurePolicyAcknowledgement{}, err
	}
	if desired.Enabled {
		if _, err := c.store.EnrollCurrentAgentSwitchRecoveryMarkers(ctx, ports.AgentSwitchFailureRecoveryEnrollment{Authorization: desired, EnrolledAt: c.options.Now().UTC()}); err != nil {
			fallback := domain.AgentSwitchReportingAuthorization{ConsentGeneration: desired.ConsentGeneration}
			fallbackErr := c.store.ApplyAgentSwitchFailurePolicy(ctx, ports.AgentSwitchFailurePolicy{Authorization: fallback, UpdatedAt: c.options.Now().UTC()})
			c.mu.Lock()
			c.authorization = fallback
			c.eventsEnabled = false
			c.disablePrepared = true
			c.purgeGeneration = fallback.ConsentGeneration
			c.purgeConfirmed = fallbackErr == nil
			c.mu.Unlock()
			c.notifyEventsChanged(false)
			if fallbackErr != nil {
				return ports.AgentSwitchFailurePolicyAcknowledgement{}, errors.Join(err, fallbackErr)
			}
			return ports.AgentSwitchFailurePolicyAcknowledgement{}, err
		}
	}
	c.mu.Lock()
	c.authorization = desired
	c.disablePrepared = false
	if desired.Enabled {
		c.gateClosed = false
		c.gateDrained = false
		c.purgeGeneration = ""
		c.purgeConfirmed = false
	} else {
		c.purgeGeneration = desired.ConsentGeneration
		c.purgeConfirmed = true
	}
	c.mu.Unlock()
	acknowledgement := c.policyAcknowledgement(desired)
	if !authority.valid {
		return acknowledgement, ErrPolicyUnavailable
	}
	return acknowledgement, nil
}

func (c *Coordinator) closeGateAndDrain(ctx context.Context) error {
	c.mu.Lock()
	if !c.gateClosed {
		c.deliveryEpoch++
		c.gateClosed = true
		c.gateDrained = false
	}
	c.authorization.Enabled = false
	for _, cancel := range c.calls {
		cancel()
	}
	if c.gateDrained {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	done := make(chan struct{})
	go func() { c.callWG.Wait(); close(done) }()
	select {
	case <-done:
		if c.options.ProviderDrain != nil {
			if err := c.options.ProviderDrain(ctx); err != nil {
				return err
			}
		}
		c.mu.Lock()
		c.gateDrained = true
		c.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) policyAcknowledgement(authorization domain.AgentSwitchReportingAuthorization) ports.AgentSwitchFailurePolicyAcknowledgement {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ports.AgentSwitchFailurePolicyAcknowledgement{
		Authorization:  authorization,
		GateDrained:    !authorization.Enabled && c.gateClosed && c.gateDrained,
		PurgeConfirmed: !authorization.Enabled && c.purgeConfirmed && c.purgeGeneration == authorization.ConsentGeneration,
	}
}

func (c *Coordinator) invalidatePurgeProof() {
	c.mu.Lock()
	c.purgeGeneration = ""
	c.purgeConfirmed = false
	c.mu.Unlock()
}

func (c *Coordinator) notifyEventsChanged(enabled bool) {
	if c.options.OnEventsChanged != nil {
		c.options.OnEventsChanged(enabled)
	}
}

func newBootToken() string {
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return fmt.Sprintf("headless-%d", time.Now().UnixNano())
	}
	return "headless-" + hex.EncodeToString(tokenBytes[:])
}

var _ PolicyCoordinator = (*Coordinator)(nil)
