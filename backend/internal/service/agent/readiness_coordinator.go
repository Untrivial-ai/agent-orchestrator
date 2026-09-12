package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	defaultDisplayReadinessTTL = 5 * time.Minute
	defaultLaunchReadinessTTL  = 30 * time.Second
	defaultInstallCheckTimeout = 2 * time.Second
	defaultAuthCheckTimeout    = 10 * time.Second
	defaultReadinessWorkers    = 4
)

var defaultReadinessRetryDelays = []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute}

type readinessInvalidation uint8

const (
	readinessInvalidateInstallation readinessInvalidation = 1 << iota
	readinessInvalidateAuthentication
	readinessInvalidateAll = readinessInvalidateInstallation | readinessInvalidateAuthentication
)

type readinessCoordinatorConfig struct {
	Agents              []agentregistry.HarnessAgent
	Factory             func() []agentregistry.HarnessAgent
	Context             context.Context
	Logger              *slog.Logger
	Now                 func() time.Time
	DisplayTTL          time.Duration
	LaunchTTL           time.Duration
	InstallTimeout      time.Duration
	AuthTimeout         time.Duration
	RetryDelays         []time.Duration
	Workers             int
	AuthenticationCheck func(context.Context, string, domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, bool)
}

type readinessEntry struct {
	snapshot        domain.AgentReadinessSnapshot
	invalidated     readinessInvalidation
	checking        readinessInvalidation
	installVersion  uint64
	authVersion     uint64
	failures        int
	nextRetryAt     time.Time
	retryGeneration uint64
}

type readinessCall struct {
	done           chan struct{}
	checks         readinessInvalidation
	installVersion uint64
	authVersion    uint64
}

type readinessCoordinator struct {
	ctx                 context.Context
	factory             func() []agentregistry.HarnessAgent
	logger              *slog.Logger
	now                 func() time.Time
	displayTTL          time.Duration
	launchTTL           time.Duration
	installTimeout      time.Duration
	authTimeout         time.Duration
	retryDelays         []time.Duration
	workers             int
	authenticationCheck func(context.Context, string, domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, bool)

	mu      sync.Mutex
	entries map[string]*readinessEntry
	calls   map[string]*readinessCall
}

type unsupportedAgentError struct{ id string }

func (e unsupportedAgentError) Error() string { return fmt.Sprintf("unsupported agent %q", e.id) }

func newReadinessCoordinator(cfg readinessCoordinatorConfig) *readinessCoordinator {
	if cfg.Context == nil {
		cfg.Context = context.Background()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.DisplayTTL <= 0 {
		cfg.DisplayTTL = defaultDisplayReadinessTTL
	}
	if cfg.LaunchTTL <= 0 {
		cfg.LaunchTTL = defaultLaunchReadinessTTL
	}
	if cfg.InstallTimeout <= 0 {
		cfg.InstallTimeout = defaultInstallCheckTimeout
	}
	if cfg.AuthTimeout <= 0 {
		cfg.AuthTimeout = defaultAuthCheckTimeout
	}
	if len(cfg.RetryDelays) == 0 {
		cfg.RetryDelays = append([]time.Duration(nil), defaultReadinessRetryDelays...)
	} else {
		cfg.RetryDelays = append([]time.Duration(nil), cfg.RetryDelays...)
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultReadinessWorkers
	}
	if cfg.Factory == nil {
		stable := append([]agentregistry.HarnessAgent(nil), cfg.Agents...)
		cfg.Factory = func() []agentregistry.HarnessAgent {
			return append([]agentregistry.HarnessAgent(nil), stable...)
		}
	}
	c := &readinessCoordinator{
		ctx: cfg.Context, factory: cfg.Factory, logger: cfg.Logger, now: cfg.Now,
		displayTTL: cfg.DisplayTTL, launchTTL: cfg.LaunchTTL,
		installTimeout: cfg.InstallTimeout, authTimeout: cfg.AuthTimeout,
		retryDelays: cfg.RetryDelays, workers: cfg.Workers,
		authenticationCheck: cfg.AuthenticationCheck,
		entries:             make(map[string]*readinessEntry, len(cfg.Agents)), calls: make(map[string]*readinessCall),
	}
	for _, item := range cfg.Agents {
		id := string(item.Harness)
		label := item.Manifest.Name
		if label == "" {
			label = id
		}
		c.entries[id] = &readinessEntry{snapshot: domain.AgentReadinessSnapshot{
			ID: id, Label: label,
			Installation: domain.AgentInstallationObservation{
				State: domain.AgentInstallationUnknown, Freshness: domain.AgentReadinessStale,
				ReasonCode: domain.AgentReadinessReasonNotChecked, Reason: "Installation has not been checked yet.",
			},
			Authentication: domain.AgentAuthenticationObservation{
				State: domain.AgentAuthenticationUnknown, Freshness: domain.AgentReadinessStale,
				ReasonCode: domain.AgentReadinessReasonNotChecked, Reason: "Authentication has not been checked yet.",
			},
		}}
	}
	return c
}

func (c *readinessCoordinator) Snapshot() []domain.AgentReadinessSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := c.sortedIDsLocked()
	out := make([]domain.AgentReadinessSnapshot, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.snapshotLocked(c.entries[id], domain.AgentReadinessPurposeDisplay))
	}
	return out
}

func (c *readinessCoordinator) Ensure(ctx context.Context, agentIDs []string, purpose domain.AgentReadinessPurpose) ([]domain.AgentReadinessSnapshot, error) {
	return c.ensure(ctx, agentIDs, purpose, readinessInvalidateAll)
}

func (c *readinessCoordinator) EnsureInstallation(ctx context.Context, agentIDs []string, purpose domain.AgentReadinessPurpose) ([]domain.AgentReadinessSnapshot, error) {
	return c.ensure(ctx, agentIDs, purpose, readinessInvalidateInstallation)
}

// Force invalidates the requested observations before ensuring them. It is
// reserved for explicit refresh actions; ordinary callers should use Ensure so
// they benefit from the coordinator's freshness policy.
func (c *readinessCoordinator) Force(ctx context.Context, agentIDs []string, purpose domain.AgentReadinessPurpose) ([]domain.AgentReadinessSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !purpose.Valid() {
		return nil, fmt.Errorf("invalid readiness purpose %q", purpose)
	}
	ids, err := c.normalizeIDs(agentIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		c.Invalidate(id, readinessInvalidateAll)
	}
	return c.ensure(ctx, ids, purpose, readinessInvalidateAll)
}

// FindInstalled returns as soon as one bounded installation-only ensure
// confirms a harness. Checks already in progress remain shared and continue
// under the daemon context after this caller stops waiting.
func (c *readinessCoordinator) FindInstalled(ctx context.Context, purpose domain.AgentReadinessPurpose) (domain.AgentReadinessSnapshot, bool) {
	if err := ctx.Err(); err != nil || !purpose.Valid() {
		return domain.AgentReadinessSnapshot{}, false
	}
	ids, err := c.normalizeIDs(nil)
	if err != nil || len(ids) == 0 {
		return domain.AgentReadinessSnapshot{}, false
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	matches := make(chan domain.AgentReadinessSnapshot, 1)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(len(ids))
	for _, id := range ids {
		go func(id string) {
			defer wg.Done()
			snapshot, ensureErr := c.ensureOne(waitCtx, id, purpose, readinessInvalidateInstallation, true)
			if ensureErr != nil || snapshot.Installation.State != domain.AgentInstallationInstalled {
				return
			}
			select {
			case matches <- snapshot:
				cancel()
			case <-waitCtx.Done():
			}
		}(id)
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case snapshot := <-matches:
		return snapshot, true
	case <-done:
		select {
		case snapshot := <-matches:
			return snapshot, true
		default:
			return domain.AgentReadinessSnapshot{}, false
		}
	case <-ctx.Done():
		return domain.AgentReadinessSnapshot{}, false
	}
}

func (c *readinessCoordinator) ensure(ctx context.Context, agentIDs []string, purpose domain.AgentReadinessPurpose, requested readinessInvalidation) ([]domain.AgentReadinessSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !purpose.Valid() {
		return nil, fmt.Errorf("invalid readiness purpose %q", purpose)
	}
	ids, err := c.normalizeIDs(agentIDs)
	if err != nil {
		return nil, err
	}
	type result struct {
		index int
		item  domain.AgentReadinessSnapshot
		err   error
	}
	results := make(chan result, len(ids))
	semaphore := make(chan struct{}, c.workers)
	for index, id := range ids {
		go func(index int, id string) {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- result{index: index, err: ctx.Err()}
				return
			}
			item, err := c.ensureOne(ctx, id, purpose, requested, false)
			results <- result{index: index, item: item, err: err}
		}(index, id)
	}
	out := make([]domain.AgentReadinessSnapshot, len(ids))
	for range ids {
		res := <-results
		if res.err != nil {
			return nil, res.err
		}
		out[res.index] = res.item
	}
	return out, nil
}

func (c *readinessCoordinator) ensureOne(ctx context.Context, id string, purpose domain.AgentReadinessPurpose, requested readinessInvalidation, presenceOnly bool) (domain.AgentReadinessSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.AgentReadinessSnapshot{}, err
	}
	c.mu.Lock()
	entry := c.entries[id]
	if entry == nil {
		c.mu.Unlock()
		return domain.AgentReadinessSnapshot{}, unsupportedAgentError{id: id}
	}
	needed := c.neededChecksLocked(entry, purpose) & requested
	if needed == 0 {
		snapshot := c.snapshotLocked(entry, purpose)
		c.mu.Unlock()
		c.logDecision(id, purpose, "cache_hit", 0, snapshot, "", time.Time{})
		return snapshot, nil
	}
	if purpose == domain.AgentReadinessPurposeDisplay && !entry.nextRetryAt.IsZero() && c.now().Before(entry.nextRetryAt) {
		snapshot := c.snapshotLocked(entry, purpose)
		nextRetry := entry.nextRetryAt
		c.mu.Unlock()
		c.logDecision(id, purpose, "retry_delayed", 0, snapshot, readinessFailureCategory(snapshot), nextRetry)
		return snapshot, nil
	}
	if call := c.calls[id]; call != nil {
		joinedAt := c.now()
		joinedChecks := call.checks
		joinedInstallVersion := call.installVersion
		joinedAuthVersion := call.authVersion
		wantedInstallVersion := entry.installVersion
		wantedAuthVersion := entry.authVersion
		c.mu.Unlock()
		select {
		case <-call.done:
			c.mu.Lock()
			entry = c.entries[id]
			snapshot := c.snapshotLocked(entry, purpose)
			nextRetry := entry.nextRetryAt
			remaining := c.neededChecksLocked(entry, purpose) & requested
			c.mu.Unlock()
			c.logDecision(id, purpose, "join", c.now().Sub(joinedAt), snapshot, readinessFailureCategory(snapshot), nextRetry)
			installMissed := remaining&readinessInvalidateInstallation != 0 && (joinedChecks&readinessInvalidateInstallation == 0 || joinedInstallVersion < wantedInstallVersion)
			authMissed := remaining&readinessInvalidateAuthentication != 0 && (joinedChecks&readinessInvalidateAuthentication == 0 || joinedAuthVersion < wantedAuthVersion)
			if installMissed || authMissed {
				return c.ensureOne(ctx, id, purpose, requested, presenceOnly)
			}
			return snapshot, nil
		case <-ctx.Done():
			return domain.AgentReadinessSnapshot{}, ctx.Err()
		}
	}
	call := &readinessCall{
		done:           make(chan struct{}),
		checks:         needed,
		installVersion: entry.installVersion,
		authVersion:    entry.authVersion,
	}
	c.calls[id] = call
	entry.checking = needed
	started := c.now()
	c.mu.Unlock()
	go c.runCheck(id, purpose, needed, presenceOnly, requested&readinessInvalidateAuthentication != 0, call, started)

	select {
	case <-call.done:
		c.mu.Lock()
		snapshot := c.snapshotLocked(c.entries[id], purpose)
		c.mu.Unlock()
		return snapshot, nil
	case <-ctx.Done():
		return domain.AgentReadinessSnapshot{}, ctx.Err()
	}
}

func readinessFailureCategory(snapshot domain.AgentReadinessSnapshot) string {
	if isReadinessFailureCode(snapshot.Installation.ReasonCode) {
		return snapshot.Installation.ReasonCode
	}
	if isReadinessFailureCode(snapshot.Authentication.ReasonCode) {
		return snapshot.Authentication.ReasonCode
	}
	return ""
}

func isReadinessFailureCode(code string) bool {
	switch code {
	case domain.AgentReadinessReasonInstallCheckTimeout,
		domain.AgentReadinessReasonInstallCheckFailed,
		domain.AgentReadinessReasonAuthCheckInconclusive,
		domain.AgentReadinessReasonAuthCheckTimeout,
		domain.AgentReadinessReasonAuthCheckFailed:
		return true
	default:
		return false
	}
}

func (c *readinessCoordinator) runCheck(id string, purpose domain.AgentReadinessPurpose, needed readinessInvalidation, presenceOnly, includeAuthentication bool, call *readinessCall, started time.Time) {
	item, ok := c.freshAgent(id)
	install := domain.AgentInstallationObservation{}
	auth := domain.AgentAuthenticationObservation{}
	installFailed := false
	authFailed := false
	previousInstallState := domain.AgentInstallationUnknown
	if needed&readinessInvalidateInstallation != 0 {
		c.mu.Lock()
		previousInstallState = c.entries[id].snapshot.Installation.State
		c.mu.Unlock()
	}
	if !ok {
		now := c.now()
		install = failedInstallation(now, domain.AgentReadinessReasonInstallCheckFailed, "Installation check failed.")
		installFailed = true
	} else {
		if needed&readinessInvalidateInstallation != 0 {
			install, installFailed = c.checkInstallation(item, presenceOnly)
			if includeAuthentication && !presenceOnly && !installFailed && install.State == domain.AgentInstallationInstalled && previousInstallState != domain.AgentInstallationInstalled {
				needed |= readinessInvalidateAuthentication
			}
		}
		if !installFailed {
			state := install.State
			if needed&readinessInvalidateInstallation == 0 {
				c.mu.Lock()
				state = c.entries[id].snapshot.Installation.State
				c.mu.Unlock()
			}
			switch state {
			case domain.AgentInstallationInstalled:
				if needed&readinessInvalidateAuthentication != 0 {
					auth, authFailed = c.checkAuthentication(item, purpose)
				}
			case domain.AgentInstallationNotInstalled:
				auth = skippedAuthentication(c.now())
			case domain.AgentInstallationUnknown:
				auth = inconclusiveAuthentication(c.now())
			}
		}
	}

	c.mu.Lock()
	entry := c.entries[id]
	if needed&readinessInvalidateInstallation != 0 {
		if installFailed {
			preserveInstallationFailure(&entry.snapshot.Installation, install)
		} else {
			entry.snapshot.Installation = install
			if entry.installVersion == call.installVersion {
				entry.invalidated &^= readinessInvalidateInstallation
			}
		}
	}
	if installFailed {
		entry.snapshot.Authentication.Freshness = domain.AgentReadinessStale
	} else if auth.ReasonCode != "" {
		if authFailed {
			preserveAuthenticationFailure(&entry.snapshot.Authentication, auth)
		} else {
			entry.snapshot.Authentication = auth
			if entry.authVersion == call.authVersion {
				entry.invalidated &^= readinessInvalidateAuthentication
			}
		}
	}
	entry.checking = 0
	failureCode := ""
	if installFailed || authFailed {
		entry.failures++
		delay := c.retryDelays[min(entry.failures-1, len(c.retryDelays)-1)]
		entry.nextRetryAt = c.now().Add(delay)
		entry.retryGeneration++
		generation := entry.retryGeneration
		if installFailed {
			failureCode = entry.snapshot.Installation.ReasonCode
		} else {
			failureCode = entry.snapshot.Authentication.ReasonCode
		}
		go c.scheduleRetry(id, generation, delay)
	} else {
		entry.failures = 0
		entry.nextRetryAt = time.Time{}
		entry.retryGeneration++
	}
	snapshot := c.snapshotLocked(entry, purpose)
	nextRetry := entry.nextRetryAt
	duration := c.now().Sub(started)
	delete(c.calls, id)
	close(call.done)
	c.mu.Unlock()
	c.logDecision(id, purpose, "new_check", duration, snapshot, failureCode, nextRetry)
}

func (c *readinessCoordinator) checkInstallation(item agentregistry.HarnessAgent, presenceOnly bool) (domain.AgentInstallationObservation, bool) {
	attempted := c.now()
	ctx, cancel := context.WithTimeout(c.ctx, c.installTimeout)
	defer cancel()
	var path string
	var err error
	if resolver, ok := item.Agent.(ports.AgentBinaryPresenceResolver); presenceOnly && ok {
		path, err = resolver.ResolveBinaryPresence(ctx)
	} else if resolver, ok := item.Agent.(ports.AgentBinaryResolver); ok {
		path, err = resolver.ResolveBinary(ctx)
	} else {
		return successfulInstallation(attempted, domain.AgentInstallationUnknown, domain.AgentReadinessReasonInstallCheckUnsupported, "Installation checks are not supported for this harness."), false
	}
	if err == nil && path != "" {
		return successfulInstallation(attempted, domain.AgentInstallationInstalled, domain.AgentReadinessReasonInstalled, item.Manifest.Name+" is installed."), false
	}
	if errors.Is(err, ports.ErrAgentBinaryNotFound) {
		return successfulInstallation(attempted, domain.AgentInstallationNotInstalled, domain.AgentReadinessReasonNotInstalled, item.Manifest.Name+" is not installed."), false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failedInstallation(attempted, domain.AgentReadinessReasonInstallCheckTimeout, "Installation check timed out."), true
	}
	return failedInstallation(attempted, domain.AgentReadinessReasonInstallCheckFailed, "Installation check failed."), true
}

func (c *readinessCoordinator) checkAuthentication(item agentregistry.HarnessAgent, purpose domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, bool) {
	attempted := c.now()
	if c.authenticationCheck != nil {
		ctx, cancel := context.WithTimeout(c.ctx, c.authTimeout)
		observation, handled := c.authenticationCheck(ctx, string(item.Harness), purpose)
		cancel()
		if handled {
			failed := isReadinessFailureCode(observation.ReasonCode)
			return observation, failed
		}
	}
	checker, ok := item.Agent.(ports.AgentAuthChecker)
	if !ok {
		return successfulAuthentication(attempted, domain.AgentAuthenticationUnknown, domain.AgentReadinessReasonAuthCheckUnsupported, "Authentication checks are not supported for this harness."), false
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.authTimeout)
	defer cancel()
	status, err := checker.AuthStatus(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckTimeout, "Authentication check timed out."), true
		}
		return failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."), true
	}
	switch status {
	case ports.AgentAuthStatusAuthorized:
		return successfulAuthentication(attempted, domain.AgentAuthenticationAuthorized, domain.AgentReadinessReasonAuthorized, item.Manifest.Name+" appears signed in."), false
	case ports.AgentAuthStatusUnauthorized:
		return successfulAuthentication(attempted, domain.AgentAuthenticationUnauthorized, domain.AgentReadinessReasonUnauthorized, item.Manifest.Name+" needs authentication."), false
	default:
		return failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication check was inconclusive."), true
	}
}

func (c *readinessCoordinator) Invalidate(agentID string, invalidation readinessInvalidation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[agentID]
	if entry == nil {
		return
	}
	entry.invalidated |= invalidation
	if invalidation&readinessInvalidateInstallation != 0 {
		entry.installVersion++
		entry.snapshot.Installation.Freshness = domain.AgentReadinessStale
	}
	if invalidation&readinessInvalidateAuthentication != 0 {
		entry.authVersion++
		entry.snapshot.Authentication.Freshness = domain.AgentReadinessStale
	}
	entry.nextRetryAt = time.Time{}
}

func (c *readinessCoordinator) Warm() {
	go func() {
		// Finish the bounded presence pass first so the desktop startup gate can
		// reuse installation work without waiting behind an authentication probe.
		_, _ = c.EnsureInstallation(c.ctx, nil, domain.AgentReadinessPurposeDisplay)
		_, _ = c.Ensure(c.ctx, nil, domain.AgentReadinessPurposeDisplay)
	}()
}

func (c *readinessCoordinator) normalizeIDs(ids []string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ids) == 0 {
		return c.sortedIDsLocked(), nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := c.entries[id]; !ok {
			return nil, unsupportedAgentError{id: id}
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (c *readinessCoordinator) sortedIDsLocked() []string {
	ids := make([]string, 0, len(c.entries))
	for id := range c.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c *readinessCoordinator) neededChecksLocked(entry *readinessEntry, purpose domain.AgentReadinessPurpose) readinessInvalidation {
	ttl := c.displayTTL
	if purpose == domain.AgentReadinessPurposeLaunch {
		ttl = c.launchTTL
	}
	now := c.now()
	needed := entry.invalidated
	if entry.snapshot.Installation.CheckedAt == nil || now.Sub(*entry.snapshot.Installation.CheckedAt) >= ttl {
		needed |= readinessInvalidateInstallation
	}
	if entry.snapshot.Authentication.CheckedAt == nil || now.Sub(*entry.snapshot.Authentication.CheckedAt) >= ttl {
		needed |= readinessInvalidateAuthentication
	}
	return needed
}

func (c *readinessCoordinator) snapshotLocked(entry *readinessEntry, purpose domain.AgentReadinessPurpose) domain.AgentReadinessSnapshot {
	snapshot := entry.snapshot
	needed := c.neededChecksLocked(entry, purpose)
	if needed&readinessInvalidateInstallation != 0 {
		snapshot.Installation.Freshness = domain.AgentReadinessStale
	}
	if needed&readinessInvalidateAuthentication != 0 {
		snapshot.Authentication.Freshness = domain.AgentReadinessStale
	}
	if entry.checking&readinessInvalidateInstallation != 0 {
		snapshot.Installation.Freshness = domain.AgentReadinessChecking
		snapshot.Installation.ReasonCode = domain.AgentReadinessReasonChecking
		snapshot.Installation.Reason = "Installation is being checked."
	}
	if entry.checking&readinessInvalidateAuthentication != 0 {
		snapshot.Authentication.Freshness = domain.AgentReadinessChecking
		snapshot.Authentication.ReasonCode = domain.AgentReadinessReasonChecking
		snapshot.Authentication.Reason = "Authentication is being checked."
	}
	snapshot.EffectiveReadiness = domain.EffectiveAgentReadiness(snapshot.Installation.State, snapshot.Authentication.State)
	return snapshot
}

func (c *readinessCoordinator) freshAgent(id string) (agentregistry.HarnessAgent, bool) {
	for _, item := range c.factory() {
		if string(item.Harness) == id {
			return item, true
		}
	}
	return agentregistry.HarnessAgent{}, false
}

func (c *readinessCoordinator) scheduleRetry(id string, generation uint64, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-c.ctx.Done():
		return
	}
	c.mu.Lock()
	entry := c.entries[id]
	valid := entry != nil && entry.retryGeneration == generation && !entry.nextRetryAt.IsZero()
	if valid {
		entry.nextRetryAt = time.Time{}
	}
	c.mu.Unlock()
	if valid {
		_, _ = c.Ensure(c.ctx, []string{id}, domain.AgentReadinessPurposeDisplay)
	}
}

func (c *readinessCoordinator) logDecision(id string, purpose domain.AgentReadinessPurpose, decision string, duration time.Duration, snapshot domain.AgentReadinessSnapshot, failureCategory string, nextRetry time.Time) {
	args := []any{
		"harness", id, "purpose", purpose, "decision", decision,
		"duration_ms", duration.Milliseconds(), "outcome", snapshot.EffectiveReadiness,
	}
	if failureCategory != "" {
		args = append(args, "failure_category", failureCategory)
	}
	if !nextRetry.IsZero() {
		args = append(args, "next_retry", nextRetry)
	}
	c.logger.Debug("agent readiness check", args...)
}

func successfulInstallation(at time.Time, state domain.AgentInstallationState, code, reason string) domain.AgentInstallationObservation {
	return domain.AgentInstallationObservation{State: state, Freshness: domain.AgentReadinessFresh, CheckedAt: timePtr(at), AttemptedAt: timePtr(at), ReasonCode: code, Reason: reason}
}

func failedInstallation(at time.Time, code, reason string) domain.AgentInstallationObservation {
	return domain.AgentInstallationObservation{State: domain.AgentInstallationUnknown, Freshness: domain.AgentReadinessStale, AttemptedAt: timePtr(at), ReasonCode: code, Reason: reason}
}

func successfulAuthentication(at time.Time, state domain.AgentAuthenticationState, code, reason string) domain.AgentAuthenticationObservation {
	return domain.AgentAuthenticationObservation{State: state, Freshness: domain.AgentReadinessFresh, CheckedAt: timePtr(at), AttemptedAt: timePtr(at), ReasonCode: code, Reason: reason}
}

func failedAuthentication(at time.Time, code, reason string) domain.AgentAuthenticationObservation {
	return domain.AgentAuthenticationObservation{State: domain.AgentAuthenticationUnknown, Freshness: domain.AgentReadinessStale, AttemptedAt: timePtr(at), ReasonCode: code, Reason: reason}
}

func skippedAuthentication(at time.Time) domain.AgentAuthenticationObservation {
	return successfulAuthentication(at, domain.AgentAuthenticationUnknown, domain.AgentReadinessReasonAuthSkippedNotInstalled, "Authentication was skipped because the harness is not installed.")
}

func inconclusiveAuthentication(at time.Time) domain.AgentAuthenticationObservation {
	return successfulAuthentication(at, domain.AgentAuthenticationUnknown, domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication could not be checked until installation is known.")
}

func preserveInstallationFailure(current *domain.AgentInstallationObservation, failure domain.AgentInstallationObservation) {
	state, checkedAt := current.State, current.CheckedAt
	*current = failure
	current.State = state
	current.CheckedAt = checkedAt
}

func preserveAuthenticationFailure(current *domain.AgentAuthenticationObservation, failure domain.AgentAuthenticationObservation) {
	state, checkedAt := current.State, current.CheckedAt
	*current = failure
	current.State = state
	current.CheckedAt = checkedAt
}

func timePtr(value time.Time) *time.Time {
	copied := value
	return &copied
}
