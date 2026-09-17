package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type readinessTestAgent struct {
	resolveCalls atomic.Int32
	authCalls    atomic.Int32
	resolve      func(context.Context) (string, error)
	auth         func(context.Context) (ports.AgentAuthStatus, error)
}

func (a *readinessTestAgent) ResolveBinary(ctx context.Context) (string, error) {
	a.resolveCalls.Add(1)
	return a.resolve(ctx)
}

func (a *readinessTestAgent) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	a.authCalls.Add(1)
	return a.auth(ctx)
}

func (a *readinessTestAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (a *readinessTestAgent) GetLaunchCommand(context.Context, ports.LaunchConfig) ([]string, error) {
	return nil, nil
}
func (a *readinessTestAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}
func (a *readinessTestAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error {
	return nil
}
func (a *readinessTestAgent) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error) {
	return nil, false, nil
}
func (a *readinessTestAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func readinessHarness(id, label string, a ports.Agent) agentregistry.HarnessAgent {
	return agentregistry.HarnessAgent{
		Harness: domain.AgentHarness(id),
		Manifest: adapters.Manifest{
			ID:   id,
			Name: label,
		},
		Agent: a,
	}
}

func TestReadinessCoordinatorStartsWithDeterministicUnknownSnapshots(t *testing.T) {
	t.Parallel()
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{
			readinessHarness("zeta", "Zeta", &readinessTestAgent{}),
			readinessHarness("alpha", "Alpha", &readinessTestAgent{}),
		},
	})

	got := coordinator.Snapshot()
	if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "zeta" {
		t.Fatalf("Snapshot() order = %#v, want alpha then zeta", got)
	}
	for _, item := range got {
		if item.Installation.State != domain.AgentInstallationUnknown || item.Authentication.State != domain.AgentAuthenticationUnknown {
			t.Fatalf("initial snapshot = %#v, want unknown observations", item)
		}
		if item.Installation.ReasonCode != domain.AgentReadinessReasonNotChecked || item.Authentication.ReasonCode != domain.AgentReadinessReasonNotChecked {
			t.Fatalf("initial reason codes = (%q, %q), want not_checked", item.Installation.ReasonCode, item.Authentication.ReasonCode)
		}
	}
}

func TestReadinessCoordinatorEnsureNormalizesInstalledAndAuthorized(t *testing.T) {
	t.Parallel()
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "/bin/codex", nil },
		auth:    func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusAuthorized, nil },
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
		Factory: func() []agentregistry.HarnessAgent {
			return []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)}
		},
	})

	got, err := coordinator.Ensure(context.Background(), []string{"codex", "codex"}, domain.AgentReadinessPurposeLaunch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Ensure() returned %d snapshots, want 1", len(got))
	}
	item := got[0]
	if item.Installation.State != domain.AgentInstallationInstalled || item.Installation.ReasonCode != domain.AgentReadinessReasonInstalled {
		t.Fatalf("installation = %#v", item.Installation)
	}
	if item.Authentication.State != domain.AgentAuthenticationAuthorized || item.Authentication.ReasonCode != domain.AgentReadinessReasonAuthorized {
		t.Fatalf("authentication = %#v", item.Authentication)
	}
	if item.EffectiveReadiness != domain.AgentReadinessReady {
		t.Fatalf("effective readiness = %q, want ready", item.EffectiveReadiness)
	}
	if agent.resolveCalls.Load() != 1 || agent.authCalls.Load() != 1 {
		t.Fatalf("probe calls = resolve %d auth %d, want one each", agent.resolveCalls.Load(), agent.authCalls.Load())
	}
}

func TestReadinessCoordinatorSingleFlightAndCallerCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	agent := &readinessTestAgent{
		resolve: func(ctx context.Context) (string, error) {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return "/bin/codex", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusAuthorized, nil },
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
		Factory: func() []agentregistry.HarnessAgent {
			return []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)}
		},
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch)
		firstDone <- err
	}()
	<-started
	checking := coordinator.Snapshot()[0]
	if checking.Installation.Freshness != domain.AgentReadinessChecking || checking.Installation.ReasonCode != domain.AgentReadinessReasonChecking {
		t.Fatalf("checking snapshot = %#v", checking.Installation)
	}

	waiterCtx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Ensure(waiterCtx, []string{"codex"}, domain.AgentReadinessPurposeLaunch)
		waiterDone <- err
	}()
	cancel()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("shared check failed after waiter canceled: %v", err)
	}
	if got := agent.resolveCalls.Load(); got != 1 {
		t.Fatalf("ResolveBinary calls = %d, want 1", got)
	}
}

func TestReadinessCoordinatorUsesPurposeSpecificFreshnessWindows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "/bin/codex", nil },
		auth:    func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusAuthorized, nil },
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents:     []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
		Now:        func() time.Time { return now },
		DisplayTTL: 5 * time.Minute,
		LaunchTTL:  30 * time.Second,
	})
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeDisplay); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Second)
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeLaunch); err != nil {
		t.Fatal(err)
	}
	if got := agent.resolveCalls.Load(); got != 2 {
		t.Fatalf("launch checks after 31s = %d, want 2 total", got)
	}
	now = now.Add(31 * time.Second)
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeDisplay); err != nil {
		t.Fatal(err)
	}
	if got := agent.resolveCalls.Load(); got != 2 {
		t.Fatalf("display checks inside 5m = %d, want cached result", got)
	}
	now = now.Add(5 * time.Minute)
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeDisplay); err != nil {
		t.Fatal(err)
	}
	if got := agent.resolveCalls.Load(); got != 3 {
		t.Fatalf("display checks after 5m = %d, want recheck", got)
	}
}

func TestReadinessCoordinatorFailurePreservesKnownStateAndLaunchBypassesRetry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	var fail atomic.Bool
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) {
			if fail.Load() {
				return "", errors.New("secret token must not leak")
			}
			return "/bin/codex", nil
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusAuthorized, nil },
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
		Factory: func() []agentregistry.HarnessAgent {
			return []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)}
		},
		Now:         func() time.Time { return now },
		DisplayTTL:  5 * time.Minute,
		LaunchTTL:   30 * time.Second,
		RetryDelays: []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute},
	})
	initial, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeLaunch)
	if err != nil {
		t.Fatal(err)
	}
	initialCheckedAt := initial[0].Installation.CheckedAt

	fail.Store(true)
	now = now.Add(31 * time.Second)
	failed, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch)
	if err != nil {
		t.Fatal(err)
	}
	if failed[0].Installation.State != domain.AgentInstallationInstalled || failed[0].Installation.Freshness != domain.AgentReadinessStale {
		t.Fatalf("failed recheck installation = %#v, want stale installed", failed[0].Installation)
	}
	if failed[0].Installation.ReasonCode != domain.AgentReadinessReasonInstallCheckFailed {
		t.Fatalf("failure reason code = %q", failed[0].Installation.ReasonCode)
	}
	if failed[0].Installation.CheckedAt == nil || initialCheckedAt == nil || !failed[0].Installation.CheckedAt.Equal(*initialCheckedAt) {
		t.Fatalf("failure checkedAt = %v, want last successful %v", failed[0].Installation.CheckedAt, initialCheckedAt)
	}
	if failed[0].Installation.AttemptedAt == nil || !failed[0].Installation.AttemptedAt.Equal(now) {
		t.Fatalf("failure attemptedAt = %v, want latest attempt %v", failed[0].Installation.AttemptedAt, now)
	}
	if got := failed[0].Installation.Reason; got == "" || got == "secret token must not leak" {
		t.Fatalf("failure reason is not safe: %q", got)
	}

	before := agent.resolveCalls.Load()
	if _, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeDisplay); err != nil {
		t.Fatal(err)
	}
	if agent.resolveCalls.Load() != before {
		t.Fatal("display ensure ignored retry delay")
	}
	if _, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch); err != nil {
		t.Fatal(err)
	}
	if agent.resolveCalls.Load() != before+1 {
		t.Fatal("launch ensure did not bypass retry delay")
	}
}

func TestReadinessCoordinatorUsesFreshAdapterInstances(t *testing.T) {
	t.Parallel()
	var generations atomic.Int32
	factory := func() []agentregistry.HarnessAgent {
		generations.Add(1)
		a := &readinessTestAgent{
			resolve: func(context.Context) (string, error) { return "/bin/codex", nil },
			auth:    func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusAuthorized, nil },
		}
		return []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", a)}
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents:  factory(),
		Factory: factory,
	})
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeLaunch); err != nil {
		t.Fatal(err)
	}
	coordinator.Invalidate("codex", readinessInvalidateInstallation)
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeLaunch); err != nil {
		t.Fatal(err)
	}
	if generations.Load() != 3 {
		t.Fatalf("factory calls = %d, want metadata plus one fresh instance per check", generations.Load())
	}
}

func TestReadinessCoordinatorBoundsBatchConcurrency(t *testing.T) {
	t.Parallel()
	const workerLimit = 2
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	agents := make([]agentregistry.HarnessAgent, 0, 6)
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		testAgent := &readinessTestAgent{
			resolve: func(context.Context) (string, error) {
				current := active.Add(1)
				defer active.Add(-1)
				for {
					previous := maximum.Load()
					if current <= previous || maximum.CompareAndSwap(previous, current) {
						break
					}
				}
				started <- struct{}{}
				<-release
				return "/bin/agent", nil
			},
			auth: func(context.Context) (ports.AgentAuthStatus, error) {
				return ports.AgentAuthStatusAuthorized, nil
			},
		}
		agents = append(agents, readinessHarness(id, id, testAgent))
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{Agents: agents, Workers: workerLimit})
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeDisplay)
		done <- err
	}()
	<-started
	<-started
	select {
	case <-started:
		t.Fatal("more checks started than the configured worker limit")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := maximum.Load(); got != workerLimit {
		t.Fatalf("maximum concurrent checks = %d, want %d", got, workerLimit)
	}
}

func TestReadinessCoordinatorClassifiesTimeouts(t *testing.T) {
	t.Parallel()
	agent := &readinessTestAgent{
		resolve: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusUnknown, nil },
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents:         []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
		InstallTimeout: 10 * time.Millisecond,
	})

	items, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0].Installation.ReasonCode; got != domain.AgentReadinessReasonInstallCheckTimeout {
		t.Fatalf("installation reason code = %q, want install_check_timeout", got)
	}
	if items[0].Installation.AttemptedAt == nil || items[0].Installation.CheckedAt != nil {
		t.Fatalf("timeout timestamps = %#v", items[0].Installation)
	}
	if got := agent.authCalls.Load(); got != 0 {
		t.Fatalf("authentication checks = %d, want none after installation timeout", got)
	}
}

func TestReadinessCoordinatorClassifiesAuthenticationTimeout(t *testing.T) {
	t.Parallel()
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "/bin/codex", nil },
		auth: func(ctx context.Context) (ports.AgentAuthStatus, error) {
			<-ctx.Done()
			return ports.AgentAuthStatusUnknown, ctx.Err()
		},
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents:      []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
		AuthTimeout: 10 * time.Millisecond,
	})

	items, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0].Authentication.ReasonCode; got != domain.AgentReadinessReasonAuthCheckTimeout {
		t.Fatalf("authentication reason code = %q, want auth_check_timeout", got)
	}
	if items[0].Authentication.State != domain.AgentAuthenticationUnknown || items[0].Authentication.Freshness != domain.AgentReadinessStale {
		t.Fatalf("authentication timeout snapshot = %#v", items[0].Authentication)
	}
}

func TestReadinessCoordinatorJoinCompletesChecksMissingFromInFlightWork(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) {
			close(started)
			<-release
			return "/bin/codex", nil
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusAuthorized, nil
		},
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
	})

	presenceDone := make(chan error, 1)
	go func() {
		_, err := coordinator.EnsureInstallation(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch)
		presenceDone <- err
	}()
	<-started

	fullDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch)
		fullDone <- err
	}()
	close(release)
	if err := <-presenceDone; err != nil {
		t.Fatal(err)
	}
	if err := <-fullDone; err != nil {
		t.Fatal(err)
	}
	if got := agent.resolveCalls.Load(); got != 1 {
		t.Fatalf("installation checks = %d, want shared check", got)
	}
	if got := agent.authCalls.Load(); got != 1 {
		t.Fatalf("authentication checks = %d, want follow-up auth check", got)
	}
}

func TestReadinessCoordinatorInvalidationDuringCheckRemainsStale(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			return "/bin/codex", nil
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusAuthorized, nil
		},
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
	})

	initialDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch)
		initialDone <- err
	}()
	<-started
	coordinator.Invalidate("codex", readinessInvalidateInstallation)
	recheckDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch)
		recheckDone <- err
	}()
	close(release)
	if err := <-initialDone; err != nil {
		t.Fatal(err)
	}
	if err := <-recheckDone; err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("installation checks = %d, want invalidation to trigger a second check", got)
	}
	if got := coordinator.Snapshot()[0].Installation.Freshness; got != domain.AgentReadinessFresh {
		t.Fatalf("installation freshness = %q, want fresh after recheck", got)
	}
}

func TestReadinessCoordinatorUsesConfiguredRetryBackoff(t *testing.T) {
	t.Parallel()
	daemonCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "", errors.New("probe failed") },
		auth:    func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusUnknown, nil },
	}
	delays := []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents:      []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
		Context:     daemonCtx,
		Now:         func() time.Time { return now },
		RetryDelays: delays,
	})

	for attempt, wantDelay := range []time.Duration{delays[0], delays[1], delays[2], delays[2]} {
		if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeLaunch); err != nil {
			t.Fatal(err)
		}
		coordinator.mu.Lock()
		gotDelay := coordinator.entries["codex"].nextRetryAt.Sub(now)
		coordinator.mu.Unlock()
		if gotDelay != wantDelay {
			t.Fatalf("attempt %d retry delay = %s, want %s", attempt+1, gotDelay, wantDelay)
		}
	}
}

func TestReadinessCoordinatorInvalidationTargetsOnlyRequiredObservation(t *testing.T) {
	t.Parallel()
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "/bin/codex", nil },
		auth:    func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusAuthorized, nil },
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
	})
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeLaunch); err != nil {
		t.Fatal(err)
	}

	coordinator.Invalidate("codex", readinessInvalidateAuthentication)
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurposeLaunch); err != nil {
		t.Fatal(err)
	}
	if got := agent.resolveCalls.Load(); got != 1 {
		t.Fatalf("installation checks = %d, want cached installation", got)
	}
	if got := agent.authCalls.Load(); got != 2 {
		t.Fatalf("authentication checks = %d, want targeted recheck", got)
	}
}

func TestReadinessCoordinatorRechecksAuthenticationAfterInstallTransition(t *testing.T) {
	t.Parallel()
	var installed atomic.Bool
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) {
			if !installed.Load() {
				return "", ports.ErrAgentBinaryNotFound
			}
			return "/bin/codex", nil
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusAuthorized, nil
		},
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
	})

	initial, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch)
	if err != nil {
		t.Fatal(err)
	}
	if got := initial[0].Installation.State; got != domain.AgentInstallationNotInstalled {
		t.Fatalf("initial installation state = %q, want not_installed", got)
	}
	if got := agent.authCalls.Load(); got != 0 {
		t.Fatalf("initial authentication checks = %d, want none", got)
	}

	installed.Store(true)
	coordinator.Invalidate("codex", readinessInvalidateInstallation)
	updated, err := coordinator.Ensure(context.Background(), []string{"codex"}, domain.AgentReadinessPurposeLaunch)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated[0].Authentication.State; got != domain.AgentAuthenticationAuthorized {
		t.Fatalf("authentication state after install = %q, want authorized", got)
	}
	if got := agent.authCalls.Load(); got != 1 {
		t.Fatalf("authentication checks after install = %d, want 1", got)
	}
}

func TestReadinessCoordinatorWarmDoesNotBlock(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) {
			close(started)
			<-release
			return "/bin/codex", nil
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusAuthorized, nil
		},
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
	})
	returned := make(chan struct{})
	go func() {
		coordinator.Warm()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Warm blocked on native readiness work")
	}
	<-started
	close(release)
}

func TestReadinessCoordinatorFindInstalledIsBoundedAndDoesNotWaitForAuth(t *testing.T) {
	t.Parallel()
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()
	slowRelease := make(chan struct{})
	installed := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "/bin/codex", nil },
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			<-slowRelease
			return ports.AgentAuthStatusAuthorized, nil
		},
	}
	slow := &readinessTestAgent{
		resolve: func(ctx context.Context) (string, error) {
			select {
			case <-slowRelease:
				return "", ports.ErrAgentBinaryNotFound
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) { return ports.AgentAuthStatusUnknown, nil },
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{
			readinessHarness("codex", "Codex", installed),
			readinessHarness("slow", "Slow", slow),
		},
		Context: daemonCtx,
		Workers: 2,
	})

	found, ok := coordinator.FindInstalled(context.Background(), domain.AgentReadinessPurposeLaunch)
	if !ok || found.ID != "codex" {
		t.Fatalf("FindInstalled() = (%#v, %v), want Codex", found, ok)
	}
	if got := installed.authCalls.Load(); got != 0 {
		t.Fatalf("authentication checks = %d, want none in startup presence path", got)
	}
	close(slowRelease)
}

func TestReadinessCoordinatorFindInstalledDoesNotStarveLaterHarnesses(t *testing.T) {
	t.Parallel()
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()
	release := make(chan struct{})
	defer close(release)
	agents := make([]agentregistry.HarnessAgent, 0, 5)
	for _, id := range []string{"a", "b", "c", "d"} {
		slow := &readinessTestAgent{
			resolve: func(ctx context.Context) (string, error) {
				select {
				case <-release:
					return "", ports.ErrAgentBinaryNotFound
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
			auth: func(context.Context) (ports.AgentAuthStatus, error) {
				return ports.AgentAuthStatusUnknown, nil
			},
		}
		agents = append(agents, readinessHarness(id, id, slow))
	}
	installed := &readinessTestAgent{
		resolve: func(context.Context) (string, error) { return "/bin/z", nil },
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			return ports.AgentAuthStatusAuthorized, nil
		},
	}
	agents = append(agents, readinessHarness("z", "Z", installed))
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: agents, Context: daemonCtx, Workers: 4,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	found, ok := coordinator.FindInstalled(ctx, domain.AgentReadinessPurposeLaunch)
	if !ok || found.ID != "z" {
		t.Fatalf("FindInstalled() = (%#v, %v), want Z", found, ok)
	}
}

func TestReadinessCoordinatorWarmFinishesPresenceBeforeAuthentication(t *testing.T) {
	t.Parallel()
	installDone := make(chan struct{})
	authStarted := make(chan struct{})
	authRelease := make(chan struct{})
	agent := &readinessTestAgent{
		resolve: func(context.Context) (string, error) {
			select {
			case <-installDone:
			default:
				close(installDone)
			}
			return "/bin/codex", nil
		},
		auth: func(context.Context) (ports.AgentAuthStatus, error) {
			close(authStarted)
			<-authRelease
			return ports.AgentAuthStatusAuthorized, nil
		},
	}
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", agent)},
	})
	coordinator.Warm()
	<-installDone
	<-authStarted

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	found, ok := coordinator.FindInstalled(ctx, domain.AgentReadinessPurposeLaunch)
	if !ok || found.ID != "codex" {
		t.Fatalf("FindInstalled() while auth is running = (%#v, %v), want cached Codex presence", found, ok)
	}
	close(authRelease)
}

func TestReadinessCoordinatorRejectsInvalidPurposeAndUnknownAgent(t *testing.T) {
	t.Parallel()
	coordinator := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", &readinessTestAgent{})},
	})
	if _, err := coordinator.Ensure(context.Background(), nil, domain.AgentReadinessPurpose("force")); err == nil {
		t.Fatal("invalid purpose error = nil")
	}
	if _, err := coordinator.Ensure(context.Background(), []string{"missing"}, domain.AgentReadinessPurposeDisplay); err == nil {
		t.Fatal("unknown agent error = nil")
	} else {
		var unsupported unsupportedAgentError
		if !errors.As(err, &unsupported) {
			t.Fatalf("unknown agent error = %T, want unsupportedAgentError", err)
		}
	}
}
