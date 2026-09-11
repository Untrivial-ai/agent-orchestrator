// Package reconcile converges durable sandbox intent with provider reality.
// HTTP handlers record intent only; every slow provider call happens here, so a
// degraded provider can never stall an API request or a browser.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/google/uuid"
)

// Store is the durable state the reconciler converges.
type Store interface {
	ClaimSandboxes(ctx context.Context, owner string, limit int, lease time.Duration) ([]domain.Sandbox, error)
	RenewSandboxClaim(ctx context.Context, owner, orgID, sessionID string, lease time.Duration) error
	UpdateSandboxObservation(ctx context.Context, owner, orgID, sessionID, providerEnvironmentID, observedState, lastError string, reconcileAfter time.Time) error
	RecordSandboxFailure(ctx context.Context, owner, orgID, sessionID, providerEnvironmentID, lastError string) error
	ReleaseSandboxClaim(ctx context.Context, owner, orgID, sessionID string, reconcileAfter time.Time) error
	IssueAccessTicket(ctx context.Context, orgID, sessionID, purpose string, scopes []string, ttl time.Duration) (string, error)
	AppendSessionEvent(ctx context.Context, orgID, sessionID, eventType string, payload json.RawMessage) (domain.ClientEvent, error)
	MarkSandboxDeletionRequested(ctx context.Context, owner, orgID, sessionID string) error
	CompleteSandboxDeletion(ctx context.Context, owner, orgID, sessionID string) error
	DisconnectSessionWorkers(ctx context.Context, orgID, sessionID string) error
}

// Resolver selects the provider that owns one sandbox's compute.
type Resolver interface {
	Resolve(context.Context, domain.Sandbox) (sandbox.Provider, error)
}

// Options configures a Reconciler. Zero values fall back to the defaults below.
type Options struct {
	// PublicURL is the origin a worker dials back to.
	PublicURL string
	// TerminalStreamEnabled tells provisioned workers to hold persistent
	// terminal streams to the control plane.
	TerminalStreamEnabled bool
	// WorkerBinary is uploaded into sandboxes whose provider supports it.
	WorkerBinary []byte
	// WorkerDestination is where that binary lands inside the sandbox.
	WorkerDestination string
	// WorkerHelperBinary is the session-local AO CLI used by harness hooks.
	WorkerHelperBinary []byte
	// WorkerHelperDestination is where the AO CLI lands inside the sandbox.
	WorkerHelperDestination string
	// WorkerUser is the unprivileged account used to run hosted workers.
	WorkerUser string
	// AllowAnonymousCheckout lets a worker clone a public repository directly,
	// with no GitHub App grant, when the checkout broker denies a grant. The
	// docker provider always allows this for local development; this extends it
	// to real providers (e.g. NodeOps) for public-repo projects that have not
	// been connected through the GitHub App yet.
	AllowAnonymousCheckout bool
	// Interval is the reconcile tick.
	Interval time.Duration
	// StartupTimeout is the budget from Create to the first worker heartbeat.
	// It must exceed the provider's p95 cold start: an under-set deadline
	// produces a recreate storm where every cycle replaces a sandbox that was
	// still booting.
	StartupTimeout time.Duration
	// TerminalStartupTimeout is the hard ceiling on repairing a worker that has
	// never checked in. Past it the sandbox is terminated rather than
	// re-bootstrapped forever, which is what stops a permanently broken session
	// (for example a private repository with no GitHub App grant) from burning
	// compute in an endless repair storm. Must comfortably exceed StartupTimeout
	// so a slow-but-healthy cold start is never mistaken for a dead worker.
	TerminalStartupTimeout time.Duration
	// HeartbeatTimeout is how long a silent worker is tolerated before repair.
	HeartbeatTimeout time.Duration
	// DeletionDeadline bounds how long the reconciler keeps re-requesting a
	// deletion the provider will not converge (an unreclaimable box, e.g. a
	// NodeOps VM stuck in "failed" that the API refuses to destroy). Past it the
	// reconciler releases the row and logs the orphan rather than looping every
	// tick forever. Generous on purpose: a healthy deletion converges in
	// seconds, so anything still churning past this is genuinely stuck.
	DeletionDeadline time.Duration
	// BatchSize is the maximum number of sandboxes claimed per tick.
	BatchSize int
	// LeaseDuration bounds exclusive ownership of one reconciliation. It is
	// renewed while provider calls are in flight.
	LeaseDuration time.Duration
	// MaxConcurrentOperations caps slow provider operations per control-plane
	// replica. Keeping this bounded lets a login wake every session promptly
	// without overwhelming the provider API.
	MaxConcurrentOperations int
	// MaxConcurrentOperationsPerProvider caps provider work for one provider
	// within a reconciliation pass.
	MaxConcurrentOperationsPerProvider int
	// MaxConcurrentOperationsPerOrganization caps provider work for one
	// organization within a reconciliation pass.
	MaxConcurrentOperationsPerOrganization int
	// Logger receives lifecycle events.
	Logger *slog.Logger
}

// Reconciler defaults, tuned for a decentralized provider whose provisioning
// latency is variable by design.
const (
	DefaultInterval                = 2 * time.Second
	DefaultStartupTimeout          = 180 * time.Second
	DefaultTerminalStartupTimeout  = 10 * time.Minute
	DefaultHeartbeatTimeout        = time.Minute
	DefaultDeletionDeadline        = 15 * time.Minute
	DefaultBatchSize               = 20
	DefaultMaxConcurrentOperations = 4
	defaultLease                   = 30 * time.Second
	defaultWorkerDestination       = "/usr/local/bin/ao-worker"
	defaultWorkerHelperDestination = "/usr/local/bin/ao"
	defaultWorkerUser              = "ao-worker"
	bootstrapTicketTTL             = 10 * time.Minute
	capacityRetryBackoff           = 15 * time.Second
	dockerBootCrashWindow          = 20 * time.Second
	// Inline wait after Create for the sandbox to reach running so the worker
	// launches within the same reconcile claim (NodeOps takes ~2s). Bounded well
	// under the claim lease; a slower provider falls back to tick-driven
	// supervision.
	inlineRunningWait = 6 * time.Second
	inlineRunningPoll = 300 * time.Millisecond
)

// Reconciler converges durable sandbox intent with provider state.
type Reconciler struct {
	store     Store
	providers Resolver
	options   Options
	owner     string
	lease     time.Duration
	log       *slog.Logger
}

// New creates a sandbox reconciler.
func New(store Store, providers Resolver, options Options) *Reconciler {
	if options.Interval <= 0 {
		options.Interval = DefaultInterval
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = DefaultStartupTimeout
	}
	if options.TerminalStartupTimeout <= 0 {
		options.TerminalStartupTimeout = DefaultTerminalStartupTimeout
	}
	// A terminal ceiling below the startup deadline would kill healthy cold
	// starts, so clamp it up to at least the startup timeout.
	if options.TerminalStartupTimeout < options.StartupTimeout {
		options.TerminalStartupTimeout = options.StartupTimeout
	}
	if options.HeartbeatTimeout <= 0 {
		options.HeartbeatTimeout = DefaultHeartbeatTimeout
	}
	if options.DeletionDeadline <= 0 {
		options.DeletionDeadline = DefaultDeletionDeadline
	}
	if options.BatchSize <= 0 {
		options.BatchSize = DefaultBatchSize
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultLease
	}
	if options.MaxConcurrentOperations <= 0 {
		options.MaxConcurrentOperations = DefaultMaxConcurrentOperations
	}
	if options.MaxConcurrentOperationsPerProvider <= 0 {
		options.MaxConcurrentOperationsPerProvider = options.MaxConcurrentOperations
	}
	if options.MaxConcurrentOperationsPerOrganization <= 0 {
		options.MaxConcurrentOperationsPerOrganization = options.MaxConcurrentOperations
	}
	if strings.TrimSpace(options.WorkerDestination) == "" {
		options.WorkerDestination = defaultWorkerDestination
	}
	if strings.TrimSpace(options.WorkerHelperDestination) == "" {
		options.WorkerHelperDestination = defaultWorkerHelperDestination
	}
	if strings.TrimSpace(options.WorkerUser) == "" {
		options.WorkerUser = defaultWorkerUser
	}
	options.PublicURL = strings.TrimRight(options.PublicURL, "/")
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Reconciler{
		store:     store,
		providers: providers,
		options:   options,
		owner:     uuid.NewString(),
		lease:     options.LeaseDuration,
		log:       options.Logger,
	}
}

// Run reconciles sandboxes until ctx is canceled.
func (r *Reconciler) Run(ctx context.Context) error {
	if err := r.ReconcileOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		r.log.Error("initial sandbox reconciliation failed", "err", err)
	}
	ticker := time.NewTicker(r.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.ReconcileOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.log.Error("sandbox reconciliation failed", "err", err)
			}
		}
	}
}

// ReconcileOnce performs a single pass. Run calls it on a ticker; tests call it
// directly so a lifecycle assertion never depends on wall-clock timing.
func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	sandboxes, err := r.store.ClaimSandboxes(ctx, r.owner, r.options.BatchSize, r.lease)
	if err != nil {
		return err
	}
	gate := newReconcileGate(
		r.options.MaxConcurrentOperations,
		r.options.MaxConcurrentOperationsPerProvider,
		r.options.MaxConcurrentOperationsPerOrganization,
	)
	var group sync.WaitGroup
	for _, record := range sandboxes {
		record := record
		group.Add(1)
		go func() {
			defer group.Done()
			if err := r.reconcileQueuedClaim(ctx, gate, record); err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				r.log.Warn("sandbox reconciliation attempt failed",
					"session_id", record.SessionID,
					"provider_id", record.ProviderEnvironmentID,
					"err", err,
				)
			}
		}()
	}
	group.Wait()
	return nil
}

// reconcileQueuedClaim retains a claimed row while it waits for a bounded
// provider slot. A batch may contain more sandboxes than can safely execute at
// once; without this renewal a row near the end could lose its lease and be
// duplicated by another replica before it ever reaches the provider.
func (r *Reconciler) reconcileQueuedClaim(
	ctx context.Context,
	gate *reconcileGate,
	record domain.Sandbox,
) error {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	renewalErr := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := r.lease / 3
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-waitCtx.Done():
				return
			case <-ticker.C:
				if err := r.store.RenewSandboxClaim(
					waitCtx,
					r.owner,
					record.OrgID,
					record.SessionID,
					r.lease,
				); err != nil {
					renewalErr <- err
					cancel()
					return
				}
			}
		}
	}()

	err := gate.acquire(waitCtx, record)
	cancel()
	<-done
	if err != nil {
		select {
		case renewErr := <-renewalErr:
			return renewErr
		default:
			return err
		}
	}
	defer gate.release(record)
	select {
	case renewErr := <-renewalErr:
		return renewErr
	default:
	}
	return r.reconcileClaim(ctx, record)
}

type reconcileGate struct {
	global       chan struct{}
	providerSize int
	orgSize      int

	mu        sync.Mutex
	providers map[string]chan struct{}
	orgs      map[string]chan struct{}
}

func newReconcileGate(global, provider, org int) *reconcileGate {
	return &reconcileGate{
		global:       make(chan struct{}, global),
		providerSize: provider,
		orgSize:      org,
		providers:    make(map[string]chan struct{}),
		orgs:         make(map[string]chan struct{}),
	}
}

func (g *reconcileGate) acquire(ctx context.Context, record domain.Sandbox) error {
	provider := g.provider(record.Provider)
	organization := g.organization(record.OrgID)
	acquired := make([]chan struct{}, 0, 3)
	for _, slot := range []chan struct{}{g.global, provider, organization} {
		select {
		case slot <- struct{}{}:
			acquired = append(acquired, slot)
		case <-ctx.Done():
			for index := len(acquired) - 1; index >= 0; index-- {
				<-acquired[index]
			}
			return ctx.Err()
		}
	}
	return nil
}

func (g *reconcileGate) release(record domain.Sandbox) {
	<-g.organization(record.OrgID)
	<-g.provider(record.Provider)
	<-g.global
}

func (g *reconcileGate) provider(key string) chan struct{} {
	if strings.TrimSpace(key) == "" {
		key = "unknown"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if slot := g.providers[key]; slot != nil {
		return slot
	}
	slot := make(chan struct{}, g.providerSize)
	g.providers[key] = slot
	return slot
}

func (g *reconcileGate) organization(key string) chan struct{} {
	if strings.TrimSpace(key) == "" {
		key = "unknown"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if slot := g.orgs[key]; slot != nil {
		return slot
	}
	slot := make(chan struct{}, g.orgSize)
	g.orgs[key] = slot
	return slot
}

func (r *Reconciler) reconcileClaim(ctx context.Context, record domain.Sandbox) error {
	// A row may have waited behind an earlier slow item in this batch. Renew it
	// synchronously before touching the provider so an expired claim can never
	// start a duplicate operation while another replica owns the row.
	if err := r.store.RenewSandboxClaim(
		ctx,
		r.owner,
		record.OrgID,
		record.SessionID,
		r.lease,
	); err != nil {
		return err
	}

	operationCtx, cancel := context.WithCancelCause(ctx)
	renewed := make(chan error, 1)
	go func() {
		interval := r.lease / 3
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-operationCtx.Done():
				renewed <- nil
				return
			case <-ticker.C:
				err := r.store.RenewSandboxClaim(
					operationCtx,
					r.owner,
					record.OrgID,
					record.SessionID,
					r.lease,
				)
				if err != nil {
					cancel(err)
					renewed <- err
					return
				}
			}
		}
	}()

	err := r.reconcileSandbox(operationCtx, record)
	cancel(nil)
	renewErr := <-renewed
	if err != nil {
		if renewErr != nil && !errors.Is(err, context.Canceled) {
			return errors.Join(err, renewErr)
		}
		if renewErr != nil {
			return renewErr
		}
		return err
	}
	return nil
}

func (r *Reconciler) reconcileSandbox(ctx context.Context, record domain.Sandbox) error {
	provider, err := r.providers.Resolve(ctx, record)
	if err != nil {
		return r.fail(ctx, record, err)
	}

	if record.DesiredState == domain.SandboxDesiredDeleted {
		return r.reconcileDeletion(ctx, record, provider)
	}

	// A terminated sandbox is parked: repairs have been abandoned, so do not
	// probe or resume its compute (a probe would resume an auto-paused VM and
	// restart the very repair storm termination stopped). It stays down until a
	// deleted desired state, handled above, cleans it up.
	if record.ObservedState == domain.SandboxObservedTerminated {
		return r.observe(ctx, record, record.ProviderEnvironmentID,
			domain.SandboxObservedTerminated, record.LastError, 24*time.Hour)
	}

	if record.ProviderEnvironmentID == "" {
		return r.provision(ctx, record, provider)
	}

	environment, err := provider.Get(ctx, sandbox.ID(record.ProviderEnvironmentID))
	if errors.Is(err, sandbox.ErrNotFound) {
		if record.DesiredState == domain.SandboxDesiredRunning {
			return r.observe(ctx, record, "", domain.SandboxObservedRequested,
				"provider environment disappeared", 2*time.Second)
		}
		return r.observe(ctx, record, "", domain.SandboxObservedDeleted, "", 24*time.Hour)
	}
	if err != nil {
		// A failed probe is not evidence of death. Record the error, keep the
		// provider ID, and try again — a provider outage must leave healthy
		// sessions alone.
		return r.fail(ctx, record, err)
	}

	// A worker that never checked in is repaired in place until a hard ceiling.
	// Past it the session is permanently broken (a private repository with no
	// GitHub App grant, a crash-looping harness), so stop repairing it: an
	// endless re-bootstrap storm otherwise keeps resetting the provider's
	// auto-pause timer and billing forever. This is deliberately gated on
	// "never checked in" so a once-healthy worker that goes silent is still
	// repaired normally.
	if record.DesiredState == domain.SandboxDesiredRunning &&
		record.WorkerLastSeenAt == nil &&
		r.terminalStartupDeadlineElapsed(record) {
		return r.terminate(ctx, record, environment, provider)
	}

	if record.DesiredState == domain.SandboxDesiredPaused ||
		record.DesiredState == domain.SandboxDesiredStopped {
		switch environment.State {
		case sandbox.StateStopped, sandbox.StatePaused, sandbox.StateDeleted:
		default:
			if err := provider.Stop(ctx, environment.ID); err != nil {
				return r.fail(ctx, record, err)
			}
		}
		return r.observe(ctx, record, string(environment.ID), domain.SandboxObservedStopped, "", 30*time.Second)
	}

	switch environment.State {
	case sandbox.StateDeleted:
		return r.observe(ctx, record, "", domain.SandboxObservedRequested,
			"provider environment was destroyed", 2*time.Second)
	case sandbox.StateDeleting:
		return r.observe(ctx, record, string(environment.ID), domain.SandboxObservedDeleting, "", 2*time.Second)
	case sandbox.StateStopped, sandbox.StatePaused:
		if record.Provider == sandbox.ProviderDocker {
			// A stopped sandbox already observed as stopped was intentionally
			// paused. It must be recreated immediately on wake; otherwise a
			// user who resumes within the boot-crash window is misclassified as
			// a failed worker. A newly bootstrapping or previously running worker
			// that stops inside the window is still a deterministic boot failure.
			if (record.ObservedState != domain.SandboxObservedStopped || record.LastError != "") &&
				time.Since(record.UpdatedAt) < dockerBootCrashWindow {
				if err := r.store.DisconnectSessionWorkers(ctx, record.OrgID, record.SessionID); err != nil {
					r.log.Warn("disconnect crashed worker", "session_id", record.SessionID, "err", err)
				}
				return r.fail(ctx, record, fmt.Errorf(
					"worker exited %s after being (re)created, before the boot-crash window elapsed",
					time.Since(record.UpdatedAt).Round(time.Millisecond),
				))
			}
			recreator, ok := provider.(sandbox.Recreator)
			if !ok {
				return r.fail(
					ctx, record,
					errors.New("docker provider cannot recreate a stopped worker"),
				)
			}
			// A Docker worker receives a single-use bootstrap ticket in its
			// container environment. Starting the same container would replay
			// that spent ticket, so repair must create a fresh container while
			// retaining the separately managed workspace volume.
			return r.recreate(ctx, record, environment, recreator)
		}
		// Restoring in place is always preferable to replacing compute: a
		// resumed sandbox keeps its filesystem, and on providers that snapshot
		// memory it keeps the running worker too. Recreate is repair, not the
		// normal path out of an idle auto-pause.
		if err := r.restore(ctx, environment, provider); err != nil {
			if recreator, ok := provider.(sandbox.Recreator); ok {
				r.log.Warn("restore failed, replacing sandbox",
					"session_id", record.SessionID, "provider_id", environment.ID, "err", err)
				return r.recreate(ctx, record, environment, recreator)
			}
			return r.fail(ctx, record, err)
		}
		// A NodeOps resume retains the sandbox filesystem, including the
		// previously uploaded worker binary. Keep the distinct restoring state
		// until the provider confirms it is running, so that next probe can
		// refresh the worker in place rather than waiting for the startup timeout
		// to repair a stale release.
		return r.observe(ctx, record, string(environment.ID), domain.SandboxObservedRestoring, "", 2*time.Second)
	case sandbox.StateRunning:
		return r.superviseRunning(ctx, record, environment, provider)
	case sandbox.StateProvisioning:
		// A provider can acknowledge a resume and then remain in its own
		// transitional state forever. There is no safe in-place action AO can
		// take while that happens: a recreate would discard the paused VM's
		// filesystem. Bound the wait, preserve that VM, and retry with normal
		// backoff until the provider reports it runnable again.
		if record.ObservedState == domain.SandboxObservedFailed ||
			(record.ObservedState == domain.SandboxObservedProvisioning &&
				record.WorkerLastSeenAt == nil &&
				r.startupDeadlineElapsed(record)) {
			return r.fail(ctx, record, r.providerStartupTimeoutError())
		}
		return r.observe(ctx, record, string(environment.ID), domain.SandboxObservedProvisioning, "", 5*time.Second)
	default:
		// Any state this control plane does not recognize is treated as
		// not-yet-ready. Guessing "running" would suppress the startup
		// deadline and strand the session in silence.
		return r.observe(ctx, record, string(environment.ID), domain.SandboxObservedProvisioning, "", 5*time.Second)
	}
}

func (r *Reconciler) reconcileDeletion(
	ctx context.Context,
	record domain.Sandbox,
	provider sandbox.Provider,
) error {
	if record.ProviderEnvironmentID == "" {
		// Create may have succeeded just before a replica crashed, leaving no
		// durable provider id. Prove absence by the session correlation key
		// before releasing quota; otherwise that orphan would keep billing.
		environment, found, err := provider.FindBySession(ctx, record.SessionID)
		if err != nil {
			return r.fail(ctx, record, err)
		}
		if !found {
			return r.store.CompleteSandboxDeletion(ctx, r.owner, record.OrgID, record.SessionID)
		}
		record.ProviderEnvironmentID = string(environment.ID)
	}

	environment, err := provider.Get(ctx, sandbox.ID(record.ProviderEnvironmentID))
	switch {
	case errors.Is(err, sandbox.ErrNotFound):
		return r.store.CompleteSandboxDeletion(ctx, r.owner, record.OrgID, record.SessionID)
	case err != nil:
		return r.fail(ctx, record, err)
	case environment.State == sandbox.StateDeleted:
		return r.store.CompleteSandboxDeletion(ctx, r.owner, record.OrgID, record.SessionID)
	}

	// The box has not converged to gone. Bound the attempt: some providers cannot
	// reclaim a box in certain states (a NodeOps VM stuck in "failed" accepts
	// DELETE with 200 but never transitions, so Get keeps reporting it), which
	// would otherwise re-request Delete every tick forever. Stamp the first
	// attempt, then past a deadline release the row and log the orphan for
	// provider-side cleanup rather than loop indefinitely.
	firstAttempt := record.DeletionRequestedAt == nil
	if firstAttempt {
		if err := r.store.MarkSandboxDeletionRequested(ctx, r.owner, record.OrgID, record.SessionID); err != nil {
			r.log.Warn("mark sandbox deletion requested",
				"session_id", record.SessionID, "err", err)
		}
	} else if time.Since(*record.DeletionRequestedAt) >= r.options.DeletionDeadline {
		r.log.Warn("abandoning unreclaimable sandbox after deletion deadline",
			"session_id", record.SessionID,
			"provider", record.Provider,
			"provider_id", record.ProviderEnvironmentID,
			"provider_state", environment.State,
			"deadline", r.options.DeletionDeadline,
		)
		return r.store.CompleteSandboxDeletion(ctx, r.owner, record.OrgID, record.SessionID)
	}

	if environment.State == sandbox.StateDeleting {
		return r.observe(
			ctx,
			record,
			string(environment.ID),
			domain.SandboxObservedDeleting,
			"",
			2*time.Second,
		)
	}

	// Log once per deletion (first attempt), not every retry tick: DELETE is
	// re-issued each tick until the box converges, but the intent is the same.
	if firstAttempt {
		r.log.Info("requesting sandbox deletion",
			"session_id", record.SessionID,
			"provider", record.Provider,
			"provider_id", record.ProviderEnvironmentID,
		)
	}
	if err := provider.Delete(ctx, environment.ID); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return r.fail(ctx, record, err)
	}
	// DELETE acceptance is not deletion confirmation. Keep the provider id and
	// quota allocation until a later Get observes deleted or not found.
	return r.observe(
		ctx,
		record,
		string(environment.ID),
		domain.SandboxObservedDeleting,
		"",
		2*time.Second,
	)
}

func (r *Reconciler) restore(
	ctx context.Context,
	environment sandbox.Environment,
	provider sandbox.Provider,
) error {
	if environment.State == sandbox.StatePaused {
		return provider.Resume(ctx, environment.ID)
	}
	return provider.Start(ctx, environment.ID)
}

func (r *Reconciler) superviseRunning(
	ctx context.Context,
	record domain.Sandbox,
	environment sandbox.Environment,
	provider sandbox.Provider,
) error {
	if record.ObservedState == domain.SandboxObservedRestoring {
		return r.refreshRestoredWorker(ctx, record, environment, provider)
	}

	startupExpired := (record.ObservedState == domain.SandboxObservedProvisioning ||
		record.ObservedState == domain.SandboxObservedBootstrapping) &&
		record.WorkerLastSeenAt == nil &&
		r.startupDeadlineElapsed(record)
	heartbeatExpired := record.WorkerLastSeenAt != nil &&
		time.Since(*record.WorkerLastSeenAt) >= r.options.HeartbeatTimeout

	// First worker install: the sandbox is now running (network ready), so this
	// is the earliest point a worker can actually reach the control plane and
	// clone the repository. Bootstrapping here instead of at provision time is
	// what lets the first attempt check in, rather than launching on a not-yet-
	// networked VM and forcing a full startup-deadline wait before repair.
	if record.ObservedState == domain.SandboxObservedProvisioning && record.WorkerLastSeenAt == nil {
		if bootstrapper, ok := provider.(sandbox.Bootstrapper); ok && len(r.options.WorkerBinary) > 0 {
			spec, err := r.workerSpec(ctx, record)
			if err != nil {
				return r.fail(ctx, record, err)
			}
			r.log.Info("bootstrapping worker in running sandbox",
				"session_id", record.SessionID,
				"provider", record.Provider,
				"provider_id", environment.ID,
			)
			if err := bootstrapper.BootstrapWorker(ctx, environment.ID, sandbox.WorkerBootstrap{
				Binary:            r.options.WorkerBinary,
				Destination:       r.options.WorkerDestination,
				HelperBinary:      r.options.WorkerHelperBinary,
				HelperDestination: r.options.WorkerHelperDestination,
				User:              r.options.WorkerUser,
				Environment:       spec.Environment,
			}); err != nil {
				return r.fail(ctx, record, err)
			}
			return r.observe(ctx, record, string(environment.ID),
				domain.SandboxObservedBootstrapping, "", r.options.Interval)
		}
	}

	if record.ObservedState == domain.SandboxObservedFailed || startupExpired || heartbeatExpired {
		// Repairing in place is cheaper than replacing compute, so prefer the
		// bootstrapper when the provider exposes one.
		if bootstrapper, ok := provider.(sandbox.Bootstrapper); ok && len(r.options.WorkerBinary) > 0 {
			spec, err := r.workerSpec(ctx, record)
			if err != nil {
				return r.fail(ctx, record, err)
			}
			r.log.Info("reinstalling worker in live sandbox",
				"session_id", record.SessionID,
				"provider", record.Provider,
				"provider_id", environment.ID,
				"reason", repairReason(record, startupExpired, heartbeatExpired),
			)
			if err := bootstrapper.BootstrapWorker(ctx, environment.ID, sandbox.WorkerBootstrap{
				Binary:            r.options.WorkerBinary,
				Destination:       r.options.WorkerDestination,
				HelperBinary:      r.options.WorkerHelperBinary,
				HelperDestination: r.options.WorkerHelperDestination,
				User:              r.options.WorkerUser,
				Environment:       spec.Environment,
			}); err != nil {
				return r.fail(ctx, record, err)
			}
			return r.observe(ctx, record, string(environment.ID),
				domain.SandboxObservedBootstrapping, "", r.options.Interval)
		}
		if recreator, ok := provider.(sandbox.Recreator); ok {
			return r.recreate(ctx, record, environment, recreator)
		}
	}

	state := record.ObservedState
	if state != domain.SandboxObservedRunning {
		state = domain.SandboxObservedBootstrapping
	}
	return r.observe(ctx, record, string(environment.ID), state, "", 5*time.Second)
}

// refreshRestoredWorker installs the release worker into a resumed sandbox
// exactly once. NodeOps resumes a preserved root filesystem, so without this
// step a session paused before a deployment can continue running an older
// worker binary after it wakes.
func (r *Reconciler) refreshRestoredWorker(
	ctx context.Context,
	record domain.Sandbox,
	environment sandbox.Environment,
	provider sandbox.Provider,
) error {
	bootstrapper, supportsBootstrap := provider.(sandbox.Bootstrapper)
	if !supportsBootstrap || len(r.options.WorkerBinary) == 0 {
		return r.observe(ctx, record, string(environment.ID),
			domain.SandboxObservedBootstrapping, "", r.options.Interval)
	}

	spec, err := r.workerSpec(ctx, record)
	if err != nil {
		return r.fail(ctx, record, err)
	}
	r.log.Info("refreshing worker after sandbox restore",
		"session_id", record.SessionID,
		"provider", record.Provider,
		"provider_id", environment.ID,
	)
	if err := bootstrapper.BootstrapWorker(ctx, environment.ID, sandbox.WorkerBootstrap{
		Binary:            r.options.WorkerBinary,
		Destination:       r.options.WorkerDestination,
		HelperBinary:      r.options.WorkerHelperBinary,
		HelperDestination: r.options.WorkerHelperDestination,
		User:              r.options.WorkerUser,
		Environment:       spec.Environment,
	}); err != nil {
		return r.fail(ctx, record, err)
	}
	return r.observe(ctx, record, string(environment.ID),
		domain.SandboxObservedBootstrapping, "", r.options.Interval)
}

func (r *Reconciler) startupDeadlineElapsed(record domain.Sandbox) bool {
	startedAt := record.UpdatedAt
	if record.StartupStartedAt != nil {
		startedAt = *record.StartupStartedAt
	}
	return time.Since(startedAt) >= r.options.StartupTimeout
}

// terminalStartupDeadlineElapsed reports whether a worker that has never checked
// in has been under repair longer than the terminal ceiling. It measures from
// StartupStartedAt, which the store preserves across in-place re-bootstraps, so
// the ceiling bounds the whole repair storm rather than a single attempt.
func (r *Reconciler) terminalStartupDeadlineElapsed(record domain.Sandbox) bool {
	startedAt := record.UpdatedAt
	if record.StartupStartedAt != nil {
		startedAt = *record.StartupStartedAt
	}
	return time.Since(startedAt) >= r.options.TerminalStartupTimeout
}

// terminate abandons a permanently broken session. It best-effort stops the
// compute so billing halts immediately rather than waiting for the provider's
// auto-pause, then records a terminal observation the reconcile entry parks. It
// never writes desired state: tearing the record down is the control plane's
// call, made when a user deletes the session.
func (r *Reconciler) terminate(
	ctx context.Context,
	record domain.Sandbox,
	environment sandbox.Environment,
	provider sandbox.Provider,
) error {
	r.log.Warn("terminating sandbox after startup ceiling",
		"session_id", record.SessionID,
		"provider", record.Provider,
		"provider_id", environment.ID,
		"ceiling", r.options.TerminalStartupTimeout,
	)
	if err := r.store.DisconnectSessionWorkers(ctx, record.OrgID, record.SessionID); err != nil {
		r.log.Warn("disconnect terminated worker", "session_id", record.SessionID, "err", err)
	}
	switch environment.State {
	case sandbox.StateStopped, sandbox.StatePaused, sandbox.StateDeleted, sandbox.StateDeleting:
	default:
		if err := provider.Stop(ctx, environment.ID); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
			r.log.Warn("stop terminated sandbox", "session_id", record.SessionID, "err", err)
		}
	}
	message := fmt.Sprintf(
		"The session's worker never started within %s and has been stopped. This usually means the repository could not be checked out (for example a private repository not connected through the GitHub App).",
		r.options.TerminalStartupTimeout,
	)
	return r.observe(ctx, record, string(environment.ID),
		domain.SandboxObservedTerminated, message, 24*time.Hour)
}

func (r *Reconciler) providerStartupTimeoutError() error {
	return fmt.Errorf(
		"The NodeOps VM did not become ready within %s. AO kept the existing VM and will retry.",
		r.options.StartupTimeout,
	)
}

func repairReason(record domain.Sandbox, startupExpired, heartbeatExpired bool) string {
	switch {
	case record.ObservedState == domain.SandboxObservedFailed:
		return "previous attempt failed"
	case startupExpired:
		return "worker never checked in before the startup deadline"
	case heartbeatExpired:
		return "worker heartbeat stopped"
	default:
		return "unknown"
	}
}

func (r *Reconciler) provision(
	ctx context.Context,
	record domain.Sandbox,
	provider sandbox.Provider,
) error {
	// Dedupe guard: a reconciler that crashed between Create and the durable
	// write must adopt the sandbox it already made, never create a second.
	existing, found, err := provider.FindBySession(ctx, record.SessionID)
	if err != nil {
		return r.fail(ctx, record, err)
	}
	if found {
		return r.observe(ctx, record, string(existing.ID), domain.SandboxObservedProvisioning, "", time.Second)
	}

	spec, err := r.workerSpec(ctx, record)
	if err != nil {
		return r.fail(ctx, record, err)
	}
	payload, _ := json.Marshal(map[string]string{"provider": record.Provider})
	if _, err := r.store.AppendSessionEvent(
		ctx, record.OrgID, record.SessionID, "sandbox.provisioning", payload,
	); err != nil {
		r.log.Warn("append sandbox.provisioning event failed",
			"session_id", record.SessionID, "err", err)
	}

	r.log.Info("provisioning sandbox", "session_id", record.SessionID, "provider", record.Provider)
	startedAt := time.Now()
	environment, err := provider.Create(ctx, spec)
	if err != nil {
		if errors.Is(err, sandbox.ErrAtCapacity) {
			r.log.Info("provider at capacity; will retry",
				"session_id", record.SessionID, "provider", record.Provider)
			return r.observe(ctx, record, record.ProviderEnvironmentID,
				domain.SandboxObservedProvisioning, "waiting for provider capacity", capacityRetryBackoff)
		}
		return r.fail(ctx, record, err)
	}
	r.log.Info("sandbox provisioned",
		"session_id", record.SessionID,
		"provider", record.Provider,
		"provider_id", environment.ID,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)

	// The worker is bootstrapped once the sandbox is actually running, not at
	// create time. A provider like NodeOps accepts Create before the VM's
	// network is up, and a worker launched that early cannot reach the control
	// plane or clone the repository, so it never sends its first heartbeat and
	// the reconciler would wait out the whole startup deadline before repairing
	// it. NodeOps reaches running in about two seconds, so a short bounded wait
	// here lets the worker launch inside this same reconcile claim instead of
	// paying two more tick round-trips (observe provisioning, re-claim, observe
	// running) first. A provider slower than the budget falls back to the
	// supervise-on-running path unchanged.
	if bootstrapper, ok := provider.(sandbox.Bootstrapper); ok && len(r.options.WorkerBinary) > 0 {
		deadline := time.Now().Add(inlineRunningWait)
		for environment.State != sandbox.StateRunning && time.Now().Before(deadline) && ctx.Err() == nil {
			time.Sleep(inlineRunningPoll)
			refreshed, err := provider.Get(ctx, sandbox.ID(environment.ID))
			if err != nil {
				break
			}
			environment = refreshed
		}
		if environment.State == sandbox.StateRunning {
			r.log.Info("bootstrapping worker in freshly provisioned sandbox",
				"session_id", record.SessionID,
				"provider", record.Provider,
				"provider_id", environment.ID,
			)
			if err := bootstrapper.BootstrapWorker(ctx, environment.ID, sandbox.WorkerBootstrap{
				Binary:            r.options.WorkerBinary,
				Destination:       r.options.WorkerDestination,
				HelperBinary:      r.options.WorkerHelperBinary,
				HelperDestination: r.options.WorkerHelperDestination,
				User:              r.options.WorkerUser,
				Environment:       spec.Environment,
			}); err != nil {
				return r.fail(ctx, record, err)
			}
			return r.observe(ctx, record, string(environment.ID),
				domain.SandboxObservedBootstrapping, "", r.options.Interval)
		}
	}
	return r.observe(ctx, record, string(environment.ID), domain.SandboxObservedProvisioning, "", 2*time.Second)
}

func (r *Reconciler) recreate(
	ctx context.Context,
	record domain.Sandbox,
	environment sandbox.Environment,
	recreator sandbox.Recreator,
) error {
	// Fresh compute needs a fresh ticket: the previous one was single-use and
	// has already been consumed.
	spec, err := r.workerSpec(ctx, record)
	if err != nil {
		return r.fail(ctx, record, err)
	}
	r.log.Info("recreating sandbox with fresh worker credentials",
		"session_id", record.SessionID,
		"provider", record.Provider,
		"provider_id", environment.ID,
	)
	recreated, err := recreator.Recreate(ctx, environment.ID, spec)
	if err != nil {
		return r.fail(ctx, record, err)
	}
	if bootstrapper, ok := recreator.(sandbox.Bootstrapper); ok && len(r.options.WorkerBinary) > 0 {
		if err := bootstrapper.BootstrapWorker(ctx, recreated.ID, sandbox.WorkerBootstrap{
			Binary:            r.options.WorkerBinary,
			Destination:       r.options.WorkerDestination,
			HelperBinary:      r.options.WorkerHelperBinary,
			HelperDestination: r.options.WorkerHelperDestination,
			User:              r.options.WorkerUser,
			Environment:       spec.Environment,
		}); err != nil {
			return r.fail(ctx, record, err)
		}
	}
	return r.observe(ctx, record, string(recreated.ID), domain.SandboxObservedBootstrapping, "", 2*time.Second)
}

// nodeOpsProfile is the subset of the stored resource profile the provider
// needs. Reading the shape from the durable row rather than from configuration
// means a config change never disturbs an in-flight session.
type nodeOpsProfile struct {
	NodeOps struct {
		DefaultShape     string `json:"defaultShape"`
		DefaultRootFS    string `json:"defaultRootFs"`
		Ingress          string `json:"ingress"`
		AutoPauseSeconds int    `json:"autoPauseSeconds"`
	} `json:"nodeOps"`
}

func (r *Reconciler) workerSpec(ctx context.Context, record domain.Sandbox) (sandbox.Spec, error) {
	ticket, err := r.store.IssueAccessTicket(
		ctx,
		record.OrgID,
		record.SessionID,
		"worker_bootstrap",
		[]string{
			"worker:connect",
			"worker:event",
			"worker:turn:claim",
			"worker:turn:poll",
			"worker:turn:complete",
			"worker:credential:read",
			"worker:git",
			"worker:orchestrate",
			"worker:transport",
		},
		bootstrapTicketTTL,
	)
	if err != nil {
		return sandbox.Spec{}, err
	}

	var profile nodeOpsProfile
	if len(record.ResourceProfile) > 0 {
		_ = json.Unmarshal(record.ResourceProfile, &profile)
	}
	workerEnvironment := map[string]string{
		"AO_CLOUD_PUBLIC_URL":       r.options.PublicURL,
		"AO_CLOUD_SESSION_ID":       record.SessionID,
		"AO_WORKER_BOOTSTRAP_TOKEN": ticket,
		"AO_WORKSPACE_DIR":          "/workspace/repository",
		"AO_DATA_DIR":               "/workspace/.ao/worker",
		"HOME":                      "/workspace/.ao/home",
		"CLAUDE_CONFIG_DIR":         "/workspace/.ao/home/.claude",
		"CODEX_HOME":                "/workspace/.ao/home/.codex",
		"DISABLE_AUTOUPDATER":       "1",
	}
	if record.Provider == sandbox.ProviderDocker || r.options.AllowAnonymousCheckout {
		workerEnvironment["AO_CLOUD_ALLOW_ANONYMOUS_GITHUB_CHECKOUT"] = "true"
	}
	if r.options.TerminalStreamEnabled {
		workerEnvironment["AO_CLOUD_TERMINAL_STREAM"] = "1"
	}
	return sandbox.Spec{
		Name:             "ao-" + record.SessionID,
		SessionID:        record.SessionID,
		OrgID:            record.OrgID,
		ResourceProfile:  domain.ResourceProfile{CPU: 4, Memory: 8, Disk: 10},
		Shape:            profile.NodeOps.DefaultShape,
		RootFS:           profile.NodeOps.DefaultRootFS,
		Ingress:          profile.NodeOps.Ingress,
		AutoPauseSeconds: profile.NodeOps.AutoPauseSeconds,
		Environment:      workerEnvironment,
		Labels: map[string]string{
			"ao.session_id": record.SessionID,
			"ao.org_id":     record.OrgID,
			"ao.managed":    "true",
		},
		AutoDeleteMinutes: 7 * 24 * 60,
	}, nil
}

func (r *Reconciler) fail(ctx context.Context, record domain.Sandbox, cause error) error {
	updateErr := r.store.RecordSandboxFailure(
		ctx, r.owner, record.OrgID, record.SessionID, record.ProviderEnvironmentID, cause.Error(),
	)
	if updateErr != nil && !errors.Is(updateErr, postgres.ErrSandboxLeaseLost) {
		return errors.Join(cause, updateErr)
	}
	return cause
}

func (r *Reconciler) observe(
	ctx context.Context,
	record domain.Sandbox,
	providerID, state, lastError string,
	after time.Duration,
) error {
	return r.store.UpdateSandboxObservation(
		ctx,
		r.owner,
		record.OrgID,
		record.SessionID,
		providerID,
		state,
		lastError,
		time.Now().Add(after),
	)
}
