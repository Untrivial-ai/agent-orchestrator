// Package interfacereconcile converges a durable interface handoff to a single
// committed session interface. HTTP handlers create the durable intent; every
// worker-facing stop/start happens here, driven by a narrow Store + Driver
// interface, so a slow worker can never stall an API request or the browser.
package interfacereconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/google/uuid"
)

// Store is the durable state the coordinator converges.
type Store interface {
	ClaimCoordinatedInterfaceTransitions(ctx context.Context, owner string, limit int, lease time.Duration) ([]postgres.CoordinatedInterfaceTransition, error)
	RenewCoordinatedInterfaceClaim(ctx context.Context, owner, transitionID string, lease time.Duration) error
	AdvanceCoordinatedInterfaceTransition(ctx context.Context, owner, transitionID string, from, to domain.SessionInterfaceTransitionPhase, nativeConversationID, errorCode, errorDetail string) error
	CommitCoordinatedSessionInterface(ctx context.Context, owner, orgID, transitionID string, interfaceValue domain.SessionInterface) (bool, error)
	ReleaseCoordinatedInterfaceClaim(ctx context.Context, owner, transitionID string) error
	EnqueueSessionInterfaceTransitionMessage(ctx context.Context, transitionID, clientMessageID, message string) error
}

// WorkerDriver is the worker-facing side of a handoff. Because the control
// plane is stateless and the worker owns the agent process, each phase step is
// dispatched to the worker and its completion is awaited through a bounded
// lease rather than an in-memory goroutine.
type WorkerDriver interface {
	// PreflightTarget validates that the target controller can start. It returns
	// a fail-closed reason code when the target is unsupported.
	PreflightTarget(ctx context.Context, transition postgres.CoordinatedInterfaceTransition) error
	// InspectSource reports whether the source controller is quiescent. Drain
	// waits for true; interrupt sends an explicit cancellation first.
	InspectSource(ctx context.Context, transition postgres.CoordinatedInterfaceTransition) (SourceInspection, error)
	// InterruptSource cancels an in-flight turn on the source controller.
	InterruptSource(ctx context.Context, transition postgres.CoordinatedInterfaceTransition) error
	// StopSource stops the source controller conclusively.
	StopSource(ctx context.Context, transition postgres.CoordinatedInterfaceTransition) error
	// ResolveNativeConversationID resolves the provider-native conversation
	// identity shared by both controllers.
	ResolveNativeConversationID(ctx context.Context, transition postgres.CoordinatedInterfaceTransition) (string, error)
	// StartTarget starts the target controller and commits it as active.
	StartTarget(ctx context.Context, transition postgres.CoordinatedInterfaceTransition, nativeConversationID string) error
}

// SourceInspection is the drain quiescence verdict for a source controller.
type SourceInspection struct {
	Idle                 bool `json:"idle"`
	WaitingForInput      bool `json:"waitingForInput"`
	DecisionPending      bool `json:"decisionPending"`
	DraftPresent         bool `json:"draftPresent"`
	QuiescenceUnverified bool `json:"quiescenceUnverified"`
}

// Options configures a Coordinator. Zero values fall back to defaults.
type Options struct {
	Interval time.Duration
	// StepTimeout bounds one phase advance. It must exceed a worker's slowest
	// command so a healthy handoff is never abandoned mid-flight.
	StepTimeout time.Duration
	// MaxConcurrent bounds handoffs processed per tick.
	MaxConcurrent int
	// MaxPendingRetries bounds how many ticks a pending worker command may be
	// retried before the handoff fails. A worker that never completes a command
	// is assumed to be unreachable rather than looping forever.
	MaxPendingRetries int
	Logger            *slog.Logger
}

const (
	defaultInterval      = 2 * time.Second
	defaultStepTimeout   = 45 * time.Second
	defaultMaxConcurrent = 4
	defaultLease         = 30 * time.Second
	defaultMaxRetries    = 10
)

// Coordinator converges durable interface handoffs.
type Coordinator struct {
	store   Store
	driver  WorkerDriver
	options Options
	owner   string
	lease   time.Duration
	log     *slog.Logger
	retries map[string]int
}

// New creates an interface handoff coordinator.
func New(store Store, driver WorkerDriver, options Options) *Coordinator {
	if options.Interval <= 0 {
		options.Interval = defaultInterval
	}
	if options.StepTimeout <= 0 {
		options.StepTimeout = defaultStepTimeout
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = defaultMaxConcurrent
	}
	if options.MaxPendingRetries <= 0 {
		options.MaxPendingRetries = defaultMaxRetries
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Coordinator{
		store:   store,
		driver:  driver,
		options: options,
		owner:   uuid.NewString(),
		lease:   defaultLease,
		log:     options.Logger,
		retries: make(map[string]int),
	}
}

// Run converges interface handoffs until ctx is canceled.
func (c *Coordinator) Run(ctx context.Context) error {
	if err := c.ReconcileOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		c.log.Error("initial interface reconciliation failed", "err", err)
	}
	ticker := time.NewTicker(c.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.ReconcileOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				c.log.Error("interface reconciliation failed", "err", err)
			}
		}
	}
}

// ReconcileOnce performs a single pass. Run calls it on a ticker; tests call it
// directly so a phase assertion never depends on wall-clock timing.
func (c *Coordinator) ReconcileOnce(ctx context.Context) error {
	transitions, err := c.store.ClaimCoordinatedInterfaceTransitions(
		ctx, c.owner, c.options.MaxConcurrent, c.lease,
	)
	if err != nil {
		return err
	}
	for _, transition := range transitions {
		transition := transition
		if err := c.reconcile(ctx, &transition); err != nil {
			if errors.Is(err, errCoordinationLost) {
				c.log.Warn("interface transition claim lost to another coordinator",
					"transition_id", transition.ID, "session_id", transition.SessionID)
				continue
			}
			c.log.Warn("interface transition failed",
				"transition_id", transition.ID, "session_id", transition.SessionID, "err", err)
		}
	}
	return nil
}

// errCoordinationLost means the claim was stolen or the row moved on; no
// recovery action is owed and the row is already owned by someone else.
var errCoordinationLost = errors.New("interface transition coordination lost")

// errPendingWorkerCommand means a worker command is still in flight. It is not
// a failure: the coordinator releases the claim and re-claims on a later tick,
// resuming from the durable phase row. No terminal phase is written.
var errPendingWorkerCommand = errors.New("interface transition worker command pending")

func (c *Coordinator) reconcile(ctx context.Context, transition *postgres.CoordinatedInterfaceTransition) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Renew the claim while a bounded worker operation is in flight. A lost
	// claim aborts the handoff rather than risking two writers.
	renewed := make(chan error, 1)
	go func() {
		interval := c.lease / 3
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				renewed <- nil
				return
			case <-ticker.C:
				err := c.store.RenewCoordinatedInterfaceClaim(runCtx, c.owner, transition.ID, c.lease)
				if err != nil {
					renewed <- err
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-renewed
		_ = c.store.ReleaseCoordinatedInterfaceClaim(ctx, c.owner, transition.ID)
	}()

	// Every durable phase marks the *next* operation to perform. On a retry or
	// coordinator restart we therefore resume at that phase instead of replaying
	// earlier worker commands. The transition only advances after the preceding
	// operation succeeds, so source stopping, native-id resolution, committing,
	// and target start are never repeated once their following checkpoint is
	// durable.
	for {
		switch transition.Phase {
		case domain.SessionInterfaceTransitionRequested:
			if err := c.advance(runCtx, transition, domain.SessionInterfaceTransitionPreflighting, "", ""); err != nil {
				return err
			}
		case domain.SessionInterfaceTransitionPreflighting:
			if err := c.driver.PreflightTarget(runCtx, *transition); err != nil {
				return c.retryOrFail(*transition, "TARGET_PREFLIGHT_FAILED", err)
			}
			if err := c.advance(runCtx, transition, domain.SessionInterfaceTransitionDraining, "", ""); err != nil {
				return err
			}
		case domain.SessionInterfaceTransitionDraining:
			if err := c.drain(runCtx, *transition); err != nil {
				return c.retryOrFail(*transition, "SOURCE_DRAIN_FAILED", err)
			}
			if err := c.advance(runCtx, transition, domain.SessionInterfaceTransitionSourceStopping, "", ""); err != nil {
				return err
			}
		case domain.SessionInterfaceTransitionSourceStopping:
			if err := c.driver.StopSource(runCtx, *transition); err != nil {
				return c.retryOrFail(*transition, "SOURCE_STOP_FAILED", err)
			}
			if err := c.advance(runCtx, transition, domain.SessionInterfaceTransitionSourceStopped, "", ""); err != nil {
				return err
			}
		case domain.SessionInterfaceTransitionSourceStopped:
			nativeID, err := c.driver.ResolveNativeConversationID(runCtx, *transition)
			if err != nil {
				return c.retryOrFail(*transition, "NATIVE_ID_RESOLUTION_FAILED", err)
			}
			committed, err := c.store.CommitCoordinatedSessionInterface(
				runCtx, c.owner, transition.OrgID, transition.ID, transition.TargetInterface,
			)
			if err != nil {
				if errors.Is(err, postgres.ErrTransitionStale) {
					return errCoordinationLost
				}
				return c.fail(*transition, "SESSION_COMMIT_FAILED", err)
			}
			if !committed {
				return c.fail(*transition, "SESSION_NOT_FOUND", errors.New("session changed before interface commit"))
			}
			if err := c.advance(runCtx, transition, domain.SessionInterfaceTransitionTargetStarting, nativeID, ""); err != nil {
				return err
			}
		case domain.SessionInterfaceTransitionTargetStarting:
			if err := c.driver.StartTarget(runCtx, *transition, transition.NativeConversationID); err != nil {
				if !isRetryable(err) {
					// The session is committed to the new interface. The target failed to
					// start, so mark recovery instead of failing: a user must never be left
					// with no controller.
					return c.recover(*transition, "TARGET_START_FAILED", err)
				}
				return c.retryOrFail(*transition, "TARGET_START_FAILED", err)
			}
			if err := c.advance(runCtx, transition, domain.SessionInterfaceTransitionActivating, transition.NativeConversationID, ""); err != nil {
				return err
			}
		case domain.SessionInterfaceTransitionActivating:
			return c.advance(runCtx, transition, domain.SessionInterfaceTransitionCompleted, "", "")
		default:
			return nil
		}
	}
}

// retryOrFail releases a pending retryable worker command, or fails the
// transition once it has been retried too many times. Target-start failures
// already committed to the target interface recover instead of failing.
func (c *Coordinator) retryOrFail(
	transition postgres.CoordinatedInterfaceTransition,
	errorCode string,
	err error,
) error {
	if !isRetryable(err) {
		return c.fail(transition, errorCode, err)
	}
	c.retries[transition.ID]++
	if c.retries[transition.ID] <= c.options.MaxPendingRetries {
		c.log.Warn("interface transition retrying pending worker command",
			"transition_id", transition.ID,
			"session_id", transition.SessionID,
			"attempt", c.retries[transition.ID],
		)
		return err
	}
	delete(c.retries, transition.ID)
	// A pending command that already committed the session to the target
	// interface must recover (never leave a session without a controller);
	// pre-commit commands fail closed.
	if transition.Phase == domain.SessionInterfaceTransitionTargetStarting ||
		transition.Phase == domain.SessionInterfaceTransitionActivating {
		return c.recover(transition, errorCode, fmt.Errorf(
			"worker never completed the interface command after %d attempts: %w",
			c.options.MaxPendingRetries, err,
		))
	}
	return c.fail(transition, errorCode, fmt.Errorf(
		"worker never completed the interface command after %d attempts: %w",
		c.options.MaxPendingRetries, err,
	))
}

// isRetryable reports an error that must release the claim and retry on a later
// tick rather than fail the interface transition. Pending worker commands and
// lost coordination both fall through to a clean re-claim from the durable row.
func isRetryable(err error) bool {
	return errors.Is(err, errPendingWorkerCommand) || errors.Is(err, errCoordinationLost)
}

// drain waits for the source controller to be quiescent. An interrupt policy
// cancels in-flight work first.
func (c *Coordinator) drain(ctx context.Context, transition postgres.CoordinatedInterfaceTransition) error {
	if transition.Policy == domain.SessionInterfaceTransitionInterrupt {
		if err := c.driver.InterruptSource(ctx, transition); err != nil {
			return err
		}
	}
	for {
		inspection, err := c.driver.InspectSource(ctx, transition)
		if err != nil {
			return err
		}
		if inspection.DecisionPending {
			return errors.New("source controller is waiting for a decision; answer it in the source interface")
		}
		if inspection.DraftPresent {
			return errors.New("source controller has unsent text; submit or clear it in the source interface")
		}
		if inspection.Idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.options.Interval):
		}
	}
}

func (c *Coordinator) advance(
	ctx context.Context,
	transition *postgres.CoordinatedInterfaceTransition,
	to domain.SessionInterfaceTransitionPhase,
	nativeID, detail string,
) error {
	stepCtx, cancel := context.WithTimeout(ctx, c.options.StepTimeout)
	defer cancel()
	err := c.store.AdvanceCoordinatedInterfaceTransition(
		stepCtx,
		c.owner,
		transition.ID,
		transition.Phase,
		to,
		nativeID,
		"",
		detail,
	)
	if errors.Is(err, postgres.ErrTransitionStale) {
		return errCoordinationLost
	}
	if err != nil {
		return fmt.Errorf("advance interface transition to %s: %w", to, err)
	}
	transition.Phase = to
	transition.NativeConversationID = nativeID
	return nil
}

func (c *Coordinator) fail(
	transition postgres.CoordinatedInterfaceTransition,
	errorCode string,
	cause error,
) error {
	if transition.Phase == domain.SessionInterfaceTransitionTargetStarting {
		return c.recover(transition, errorCode, cause)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.options.StepTimeout)
	defer cancel()
	err := c.store.AdvanceCoordinatedInterfaceTransition(
		ctx, c.owner, transition.ID, transition.Phase,
		domain.SessionInterfaceTransitionFailed, transition.NativeConversationID,
		errorCode, cause.Error(),
	)
	if errors.Is(err, postgres.ErrTransitionStale) {
		return errCoordinationLost
	}
	return err
}

// recover marks a committed-interface target failure as recovery_required so a
// session is never left with no controller. It runs under a fresh context so a
// drained or canceled request context cannot skip the durable write.
func (c *Coordinator) recover(
	transition postgres.CoordinatedInterfaceTransition,
	errorCode string,
	cause error,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.options.StepTimeout)
	defer cancel()
	err := c.store.AdvanceCoordinatedInterfaceTransition(
		ctx, c.owner, transition.ID, transition.Phase,
		domain.SessionInterfaceTransitionRecovery, transition.NativeConversationID,
		errorCode, cause.Error(),
	)
	if errors.Is(err, postgres.ErrTransitionStale) {
		return errCoordinationLost
	}
	return err
}
