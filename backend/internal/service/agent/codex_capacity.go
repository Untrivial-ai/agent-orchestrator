package agent

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	codexCapacityDisplayTTL  = 2 * time.Minute
	codexCapacityReadTimeout = 10 * time.Second
)

type capacityReadCall struct {
	done              chan struct{}
	startedReceivedAt time.Time
}

type accountCapacityState struct {
	snapshot    domain.CodexCapacitySnapshot
	invalidated bool
	failures    int
	nextRetryAt time.Time
	call        *capacityReadCall
	generation  uint64
	receivedAt  time.Time
}

type codexCapacityCoordinator struct {
	ctx     context.Context
	manager *codexAccountManager
	logger  *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	accounts map[string]*accountCapacityState
}

func newCodexCapacityCoordinator(manager *codexAccountManager) *codexCapacityCoordinator {
	return &codexCapacityCoordinator{
		ctx: manager.ctx, manager: manager, logger: manager.logger, now: manager.now,
		accounts: make(map[string]*accountCapacityState),
	}
}

func uncheckedCodexCapacity() domain.CodexCapacitySnapshot {
	return domain.CodexCapacitySnapshot{
		State: domain.CodexCapacityUnknown, Freshness: domain.AgentReadinessStale,
		ReasonCode: domain.CodexCapacityReasonNotChecked, Reason: "Codex capacity has not been checked yet.",
		AdditionalBuckets: []domain.CodexCapacityBucket{},
	}
}

func unavailableCodexCapacity() domain.CodexCapacitySnapshot {
	snapshot := uncheckedCodexCapacity()
	snapshot.ReasonCode = domain.CodexCapacityReasonAccountUnavailable
	snapshot.Reason = "This Codex account is unavailable, so capacity cannot be checked."
	return snapshot
}

func (c *codexCapacityCoordinator) snapshot(accountID string) domain.CodexCapacitySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.accounts[accountID]; state != nil {
		return state.snapshot
	}
	return uncheckedCodexCapacity()
}

func (c *codexCapacityCoordinator) ensureStateLocked(accountID string) *accountCapacityState {
	state := c.accounts[accountID]
	if state == nil {
		state = &accountCapacityState{snapshot: uncheckedCodexCapacity(), invalidated: true}
		c.accounts[accountID] = state
	}
	return state
}

func (c *codexCapacityCoordinator) ensure(ctx context.Context, records []codexAccountRecord, capabilities domain.CodexAccountCapabilities) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(records))
	for _, record := range records {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ensureOne(ctx, record, capabilities, false); err != nil {
				errCh <- err
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *codexCapacityCoordinator) ensureOne(ctx context.Context, record codexAccountRecord, capabilities domain.CodexAccountCapabilities, bypassBackoff bool) (domain.CodexCapacitySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.CodexCapacitySnapshot{}, err
	}
	if record.Snapshot.Status != domain.CodexAccountStatusValid {
		return c.replace(record.Snapshot.ID, unavailableCodexCapacity(), "account_unavailable"), nil
	}
	if snapshot, handled := c.authGate(record, capabilities); handled {
		return snapshot, nil
	}

	c.mu.Lock()
	state := c.ensureStateLocked(record.Snapshot.ID)
	now := c.now()
	fresh := state.snapshot.CheckedAt != nil && now.Sub(*state.snapshot.CheckedAt) < codexCapacityDisplayTTL
	if !state.invalidated && fresh {
		snapshot := state.snapshot
		c.mu.Unlock()
		c.logger.Debug("Codex account capacity cache hit", "account_id", record.Snapshot.ID, "source", record.Snapshot.Source, "trigger", "display", "cache", "hit")
		return snapshot, nil
	}
	if !bypassBackoff && !state.nextRetryAt.IsZero() && now.Before(state.nextRetryAt) {
		snapshot := state.snapshot
		nextRetryAt := state.nextRetryAt
		c.mu.Unlock()
		c.logger.Debug("Codex account capacity retry deferred", "account_id", record.Snapshot.ID, "source", record.Snapshot.Source, "trigger", "display", "cache", "retry_delay", "next_retry_at", nextRetryAt)
		return snapshot, nil
	}
	if state.call != nil {
		call := state.call
		c.mu.Unlock()
		c.logger.Debug("joined Codex account capacity read", "account_id", record.Snapshot.ID, "source", record.Snapshot.Source, "trigger", "display", "cache", "join")
		select {
		case <-call.done:
			return c.snapshot(record.Snapshot.ID), nil
		case <-ctx.Done():
			return domain.CodexCapacitySnapshot{}, ctx.Err()
		}
	}
	call := &capacityReadCall{done: make(chan struct{}), startedReceivedAt: state.receivedAt}
	state.call = call
	checking := state.snapshot
	checking.Freshness = domain.AgentReadinessChecking
	checking.ReasonCode = domain.CodexCapacityReasonChecking
	checking.Reason = "Codex capacity is being checked."
	attemptedAt := now
	checking.AttemptedAt = &attemptedAt
	state.snapshot = checking
	c.mu.Unlock()
	c.publish(record.Snapshot.ID, &checking)
	c.logger.Info("started Codex account capacity read", "account_id", record.Snapshot.ID, "source", record.Snapshot.Source, "trigger", "display", "cache", "new")
	go c.runRead(record, call, attemptedAt)
	select {
	case <-call.done:
		return c.snapshot(record.Snapshot.ID), nil
	case <-ctx.Done():
		return domain.CodexCapacitySnapshot{}, ctx.Err()
	}
}

func (c *codexCapacityCoordinator) authGate(record codexAccountRecord, capabilities domain.CodexAccountCapabilities) (domain.CodexCapacitySnapshot, bool) {
	switch capabilities.CapacityRead.State {
	case domain.CodexCapabilityUnsupported:
		return c.replace(record.Snapshot.ID, staticCodexCapacity(domain.CodexCapacityUnsupported, domain.CodexCapacityReasonUnsupported, "Codex subscription capacity is not supported by this Codex version."), "unsupported"), true
	case domain.CodexCapabilityUnknown, "":
		return c.preserveFailure(record.Snapshot.ID, domain.CodexCapacityReasonCheckInconclusive, "Codex capacity could not be checked."), true
	}
	auth := record.Snapshot.Authentication
	if auth.State == domain.AgentAuthenticationUnauthorized && auth.Freshness == domain.AgentReadinessFresh {
		return c.replace(record.Snapshot.ID, staticCodexCapacity(domain.CodexCapacityUnknown, domain.CodexCapacityReasonSkippedSignedOut, "Sign in to Codex to see subscription capacity."), "signed_out"), true
	}
	if auth.State == domain.AgentAuthenticationNotApplicable || record.Snapshot.AuthMethod == domain.CodexAuthMethodAPIKey || record.Snapshot.AuthMethod == domain.CodexAuthMethodOther {
		return c.replace(record.Snapshot.ID, staticCodexCapacity(domain.CodexCapacityUnsupported, domain.CodexCapacityReasonUnsupported, "Subscription capacity is not available for this Codex authentication method."), "unsupported_auth"), true
	}
	if auth.State != domain.AgentAuthenticationAuthorized || record.Snapshot.AuthMethod != domain.CodexAuthMethodChatGPT {
		return c.preserveFailure(record.Snapshot.ID, domain.CodexCapacityReasonSkippedAuthUnknown, "Confirm Codex authentication before checking capacity."), true
	}
	return domain.CodexCapacitySnapshot{}, false
}

func staticCodexCapacity(state domain.CodexCapacityState, code, reason string) domain.CodexCapacitySnapshot {
	return domain.CodexCapacitySnapshot{State: state, Freshness: domain.AgentReadinessFresh, ReasonCode: code, Reason: reason, AdditionalBuckets: []domain.CodexCapacityBucket{}}
}

func (c *codexCapacityCoordinator) runRead(record codexAccountRecord, call *capacityReadCall, attemptedAt time.Time) {
	if c.manager.factory == nil {
		c.finishFailure(record.Snapshot.ID, attemptedAt, domain.CodexCapacityReasonCheckFailed, "Codex capacity check failed.", call)
		return
	}
	select {
	case c.manager.processes <- struct{}{}:
		defer func() { <-c.manager.processes }()
	case <-c.ctx.Done():
		c.finishFailure(record.Snapshot.ID, attemptedAt, domain.CodexCapacityReasonCheckFailed, "Codex capacity check stopped.", call)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, codexCapacityReadTimeout)
	defer cancel()
	account := c.manager.accountContext(record)
	releaseGlobal, err := c.manager.acquireGlobalRead(ctx, account)
	if err != nil {
		c.finishFailure(record.Snapshot.ID, attemptedAt, domain.CodexCapacityReasonCheckFailed, "Codex capacity check stopped.", call)
		return
	}
	defer releaseGlobal()
	client, err := c.manager.factory.Open(ctx, account)
	if err != nil {
		c.finishFailure(record.Snapshot.ID, attemptedAt, domain.CodexCapacityReasonCheckFailed, "Codex capacity check failed.", call)
		return
	}
	defer func() { _ = client.Close() }()
	observation, err := client.ReadCapacity(ctx)
	if err != nil {
		code, reason := domain.CodexCapacityReasonCheckFailed, "Codex capacity check failed."
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code, reason = domain.CodexCapacityReasonCheckTimeout, "Codex capacity check timed out."
		}
		c.finishFailure(record.Snapshot.ID, attemptedAt, code, reason, call)
		return
	}
	observation.Partial = false
	c.finishSuccess(record.Snapshot.ID, observation, attemptedAt, call, "direct")
}

func (c *codexCapacityCoordinator) finishSuccess(accountID string, observation ports.CodexCapacityObservation, attemptedAt time.Time, call *capacityReadCall, source string) {
	receivedAt := c.now()
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = receivedAt
	}
	snapshot := capacitySnapshotFromObservation(observation, attemptedAt, receivedAt)
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if !state.receivedAt.After(receivedAt) {
		state.snapshot = snapshot
		state.receivedAt = receivedAt
		state.invalidated = false
		state.failures = 0
		state.nextRetryAt = time.Time{}
		state.generation++
	}
	if call != nil && state.call == call {
		state.call = nil
		close(call.done)
	}
	generation := state.generation
	result := state.snapshot
	c.mu.Unlock()
	c.publish(accountID, &result)
	c.scheduleResetInvalidation(accountID, generation, result)
	c.logger.Info("Codex account capacity updated", "account_id", accountID, "trigger", "capacity", "source", source, "duration_ms", receivedAt.Sub(attemptedAt).Milliseconds(), "outcome", result.State, "classification", map[bool]string{true: "partial", false: "full"}[observation.Partial])
}

func (c *codexCapacityCoordinator) finishFailure(accountID string, attemptedAt time.Time, code, reason string, call *capacityReadCall) {
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if state.receivedAt.After(call.startedReceivedAt) {
		if state.call == call {
			state.call = nil
			close(call.done)
		}
		c.mu.Unlock()
		return
	}
	if state.snapshot.CheckedAt != nil {
		state.snapshot.Freshness = domain.AgentReadinessStale
	} else {
		state.snapshot = uncheckedCodexCapacity()
	}
	state.snapshot.AttemptedAt = &attemptedAt
	state.snapshot.ReasonCode, state.snapshot.Reason = code, reason
	state.invalidated = true
	state.failures++
	if state.failures <= len(defaultReadinessRetryDelays) {
		state.nextRetryAt = c.now().Add(defaultReadinessRetryDelays[state.failures-1])
	}
	nextRetryAt := state.nextRetryAt
	if state.call == call {
		state.call = nil
		close(call.done)
	}
	result := state.snapshot
	c.mu.Unlock()
	c.publish(accountID, &result)
	c.logger.Info("Codex account capacity read completed", "account_id", accountID, "trigger", "capacity", "source", "direct", "duration_ms", c.now().Sub(attemptedAt).Milliseconds(), "outcome", result.State, "failure_category", code, "next_retry_at", nextRetryAt)
}

func capacitySnapshotFromObservation(observation ports.CodexCapacityObservation, attemptedAt, checkedAt time.Time) domain.CodexCapacitySnapshot {
	snapshot := domain.CodexCapacitySnapshot{
		State: domain.CodexCapacityUnknown, Freshness: domain.AgentReadinessFresh,
		Plan: observation.Plan, Overall: observation.Overall,
		AdditionalBuckets: append([]domain.CodexCapacityBucket(nil), observation.AdditionalBuckets...),
		ResetCredits:      observation.ResetCredits,
		ReasonCode:        domain.CodexCapacityReasonCheckInconclusive, Reason: "Codex did not report a trustworthy overall capacity window.",
	}
	if snapshot.AdditionalBuckets == nil {
		snapshot.AdditionalBuckets = []domain.CodexCapacityBucket{}
	}
	observedAt := observation.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = checkedAt.UTC()
	}
	attemptedAt, checkedAt = attemptedAt.UTC(), checkedAt.UTC()
	snapshot.ObservedAt, snapshot.CheckedAt, snapshot.AttemptedAt = &observedAt, &checkedAt, &attemptedAt
	if observation.Overall == nil {
		return snapshot
	}
	if observation.Overall.Reached == domain.CodexCapacityReached {
		snapshot.State, snapshot.ReasonCode, snapshot.Reason = domain.CodexCapacityExhausted, domain.CodexCapacityReasonExhausted, "Codex reports that this account has reached its limit."
	}
	var best *domain.CodexCapacityWindow
	for _, window := range []*domain.CodexCapacityWindow{observation.Overall.Primary, observation.Overall.Secondary} {
		if window != nil && (best == nil || window.UsedPercent > best.UsedPercent) {
			best = window
		}
	}
	if best == nil {
		return snapshot
	}
	used := best.UsedPercent
	remaining := 100 - used
	snapshot.UsedPercent, snapshot.RemainingPercent, snapshot.ResetsAt = &used, &remaining, best.ResetsAt
	if snapshot.State == domain.CodexCapacityExhausted || used >= 100 {
		snapshot.State, snapshot.ReasonCode, snapshot.Reason = domain.CodexCapacityExhausted, domain.CodexCapacityReasonExhausted, "Codex reports that this account has reached its limit."
	} else if used >= 75 {
		snapshot.State, snapshot.ReasonCode, snapshot.Reason = domain.CodexCapacityNearLimit, domain.CodexCapacityReasonNearLimit, "Codex reports that this account is near its limit."
	} else {
		snapshot.State, snapshot.ReasonCode, snapshot.Reason = domain.CodexCapacityAvailable, domain.CodexCapacityReasonAvailable, "Codex reports capacity is available for this account."
	}
	return snapshot
}

func (c *codexCapacityCoordinator) updateFromEvent(accountID string, observation ports.CodexCapacityObservation) {
	if accountID == "" {
		return
	}
	receivedAt := c.now()
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if state.receivedAt.After(receivedAt) {
		c.mu.Unlock()
		return
	}
	merged := mergeCapacityObservation(state.snapshot, observation, receivedAt)
	state.snapshot = merged
	state.receivedAt = receivedAt
	state.invalidated = false
	state.failures = 0
	state.nextRetryAt = time.Time{}
	state.generation++
	generation := state.generation
	c.mu.Unlock()
	c.publish(accountID, &merged)
	c.scheduleResetInvalidation(accountID, generation, merged)
	c.logger.Info("Codex account capacity updated", "account_id", accountID, "trigger", "provider_event", "source", "event", "outcome", merged.State, "classification", "partial")
}

func mergeCapacityObservation(current domain.CodexCapacitySnapshot, observation ports.CodexCapacityObservation, receivedAt time.Time) domain.CodexCapacitySnapshot {
	if !observation.Partial || current.CheckedAt == nil {
		return capacitySnapshotFromObservation(observation, receivedAt, receivedAt)
	}
	merged := ports.CodexCapacityObservation{Plan: current.Plan, Overall: current.Overall, AdditionalBuckets: current.AdditionalBuckets, ResetCredits: current.ResetCredits, ObservedAt: receivedAt, Partial: true}
	if observation.Plan != nil {
		merged.Plan = observation.Plan
	}
	if observation.Overall != nil {
		if merged.Overall == nil {
			mergedOverall := *observation.Overall
			merged.Overall = &mergedOverall
		} else {
			mergedOverall := *merged.Overall
			if observation.Overall.LimitID != "" {
				mergedOverall.LimitID = observation.Overall.LimitID
			}
			if observation.Overall.DisplayName != nil {
				mergedOverall.DisplayName = observation.Overall.DisplayName
			}
			if observation.Overall.Primary != nil {
				mergedOverall.Primary = observation.Overall.Primary
			}
			if observation.Overall.Secondary != nil {
				mergedOverall.Secondary = observation.Overall.Secondary
			}
			if observation.Overall.Reached != domain.CodexCapacityReachUnknown {
				mergedOverall.Reached = observation.Overall.Reached
			}
			merged.Overall = &mergedOverall
		}
	}
	if observation.ResetCredits != nil {
		merged.ResetCredits = observation.ResetCredits
	}
	result := capacitySnapshotFromObservation(merged, receivedAt, receivedAt)
	return result
}

func (c *codexCapacityCoordinator) acceptDirect(accountID string, observation ports.CodexCapacityObservation, attemptedAt time.Time) {
	c.finishSuccess(accountID, observation, attemptedAt, nil, "reset_credit")
}

func (c *codexCapacityCoordinator) invalidateAfterReset(accountID string) {
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	state.snapshot.ResetCredits = nil
	state.snapshot.Freshness = domain.AgentReadinessStale
	state.snapshot.ReasonCode = domain.CodexCapacityReasonInvalidated
	state.snapshot.Reason = "A Codex usage-limit reset was applied; capacity should be checked again."
	state.invalidated = true
	state.nextRetryAt = time.Time{}
	state.generation++
	snapshot := state.snapshot
	c.mu.Unlock()
	c.publish(accountID, &snapshot)
}

func (c *codexCapacityCoordinator) preserveFailure(accountID, code, reason string) domain.CodexCapacitySnapshot {
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if state.snapshot.CheckedAt != nil {
		state.snapshot.Freshness = domain.AgentReadinessStale
	} else {
		state.snapshot = uncheckedCodexCapacity()
	}
	state.snapshot.ReasonCode, state.snapshot.Reason = code, reason
	state.invalidated = true
	result := state.snapshot
	c.mu.Unlock()
	c.publish(accountID, &result)
	return result
}

func (c *codexCapacityCoordinator) replace(accountID string, snapshot domain.CodexCapacitySnapshot, trigger string) domain.CodexCapacitySnapshot {
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	changed := !reflect.DeepEqual(state.snapshot, snapshot)
	state.snapshot = snapshot
	state.invalidated = false
	state.failures = 0
	state.nextRetryAt = time.Time{}
	state.generation++
	c.mu.Unlock()
	if changed {
		c.publish(accountID, &snapshot)
		c.logger.Debug("Codex account capacity state changed", "account_id", accountID, "trigger", trigger, "outcome", snapshot.State)
	}
	return snapshot
}

func (c *codexCapacityCoordinator) invalidate(accountID string, clearSnapshot bool) {
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if clearSnapshot {
		state.snapshot = uncheckedCodexCapacity()
	}
	state.snapshot.Freshness = domain.AgentReadinessStale
	state.snapshot.ReasonCode = domain.CodexCapacityReasonInvalidated
	state.snapshot.Reason = "Codex capacity changed and will be checked again when needed."
	state.invalidated = true
	state.nextRetryAt = time.Time{}
	state.generation++
	snapshot := state.snapshot
	c.mu.Unlock()
	c.publish(accountID, &snapshot)
}

func (c *codexCapacityCoordinator) removeAccounts(accountIDs []string) {
	sort.Strings(accountIDs)
	for _, accountID := range accountIDs {
		c.mu.Lock()
		delete(c.accounts, accountID)
		c.mu.Unlock()
		c.publish(accountID, nil)
	}
}

func (c *codexCapacityCoordinator) scheduleResetInvalidation(accountID string, generation uint64, snapshot domain.CodexCapacitySnapshot) {
	reset := nearestCapacityReset(snapshot)
	if reset == nil {
		return
	}
	delay := reset.Sub(c.now())
	if delay <= 0 {
		delay = time.Millisecond
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-c.ctx.Done():
			return
		}
		c.mu.Lock()
		state := c.accounts[accountID]
		if state == nil || state.generation != generation {
			c.mu.Unlock()
			return
		}
		state.snapshot.Freshness = domain.AgentReadinessStale
		state.snapshot.ReasonCode = domain.CodexCapacityReasonInvalidated
		state.snapshot.Reason = "A reported Codex reset time passed; capacity should be checked again."
		state.invalidated = true
		state.generation++
		current := state.snapshot
		c.mu.Unlock()
		c.publish(accountID, &current)
	}()
}

func nearestCapacityReset(snapshot domain.CodexCapacitySnapshot) *time.Time {
	var nearest *time.Time
	visit := func(window *domain.CodexCapacityWindow) {
		if window == nil || window.ResetsAt == nil {
			return
		}
		if nearest == nil || window.ResetsAt.Before(*nearest) {
			value := *window.ResetsAt
			nearest = &value
		}
	}
	if snapshot.Overall != nil {
		visit(snapshot.Overall.Primary)
		visit(snapshot.Overall.Secondary)
	}
	for i := range snapshot.AdditionalBuckets {
		visit(snapshot.AdditionalBuckets[i].Primary)
		visit(snapshot.AdditionalBuckets[i].Secondary)
	}
	return nearest
}

func (c *codexCapacityCoordinator) publish(_ string, _ *domain.CodexCapacitySnapshot) {
	// Account management owns the public stream. Capacity is one field on that
	// shared snapshot, so publish the complete cached view after releasing the
	// capacity mutex (cached() reads it again).
	c.manager.publish()
}
