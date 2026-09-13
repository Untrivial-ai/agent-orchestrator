package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCodexCapacitySingleFlight(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	started, release := make(chan struct{}, 1), make(chan struct{})
	client := &fakeCodexAccountClient{
		capacityStarted: started, capacityRelease: release,
		capacity: ports.CodexCapacityObservation{ObservedAt: now, Overall: &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityNotReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 25}}},
	}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported, ReasonCode: domain.CodexCapabilityReasonSupported, Reason: "supported"}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{AccountRead: supported, CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := codexCapacityTestRecord(t.TempDir(), testAccountID, domain.CodexAccountSourceManaged, now)
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			_, _ = manager.capacity.ensureOne(context.Background(), record, factory.capabilities, false)
			done <- struct{}{}
		}()
	}
	<-started
	close(release)
	<-done
	<-done
	if factory.opens != 1 {
		t.Fatalf("opens = %d, want one", factory.opens)
	}
	if _, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, false); err != nil {
		t.Fatal(err)
	}
	if factory.opens != 1 {
		t.Fatalf("fresh cache opened %d clients, want one", factory.opens)
	}
	if got := manager.capacity.snapshot(record.Snapshot.ID); got.State != domain.CodexCapacityAvailable {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestCodexCapacityRequestCancellationDoesNotCancelSharedRead(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	started, release := make(chan struct{}, 1), make(chan struct{})
	client := &fakeCodexAccountClient{
		capacityStarted: started, capacityRelease: release,
		capacity: ports.CodexCapacityObservation{ObservedAt: now, Overall: &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityNotReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 35}}},
	}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := codexCapacityTestRecord(t.TempDir(), "existing", domain.CodexAccountSourceManaged, now)
	waitCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := manager.capacity.ensureOne(waitCtx, record, factory.capabilities, false)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context cancellation", err)
	}
	close(release)
	if _, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, false); err != nil {
		t.Fatalf("join shared read: %v", err)
	}
	if got := manager.capacity.snapshot(record.Snapshot.ID); got.State != domain.CodexCapacityAvailable {
		t.Fatalf("shared read was cancelled: %#v", got)
	}
}

func TestCodexCapacityReadsShareTwoProcessLimit(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	started, release := make(chan struct{}, 3), make(chan struct{})
	client := &fakeCodexAccountClient{
		capacityStarted: started, capacityRelease: release,
		capacity: ports.CodexCapacityObservation{ObservedAt: now, Overall: &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityNotReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 1}}},
	}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	done := make(chan struct{}, 3)
	for _, id := range []string{"one", "two", "three"} {
		record := codexCapacityTestRecord(t.TempDir(), id, domain.CodexAccountSourceManaged, now)
		go func() {
			_, _ = manager.capacity.ensureOne(context.Background(), record, factory.capabilities, false)
			done <- struct{}{}
		}()
	}
	<-started
	<-started
	select {
	case <-started:
		t.Fatal("third Codex app-server read started before a process slot was released")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done
	<-done
	<-done
}

func TestCodexCapacityFailurePreservesLastKnownStateAsStale(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	client := &fakeCodexAccountClient{capacityErr: errors.New("provider unavailable")}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := codexCapacityTestRecord(t.TempDir(), "existing", domain.CodexAccountSourceManaged, now)
	manager.capacity.updateFromEvent(record.Snapshot.ID, ports.CodexCapacityObservation{
		ObservedAt: now, Partial: true,
		Overall: &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityNotReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 55}},
	})
	manager.capacity.invalidate(record.Snapshot.ID, false)
	snapshot, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, true)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != domain.CodexCapacityAvailable || snapshot.Freshness != domain.AgentReadinessStale || snapshot.ReasonCode != domain.CodexCapacityReasonCheckFailed {
		t.Fatalf("failure did not preserve stale known state: %#v", snapshot)
	}
}

func TestCodexCapacityRevokedAccessTokenRefreshesAndRetriesOnce(t *testing.T) {
	now := time.Date(2026, 9, 11, 9, 30, 0, 0, time.UTC)
	email := "person@example.com"
	var capacityReads atomic.Int32
	var refreshReads atomic.Int32
	client := &fakeCodexAccountClient{
		readFn: func(_ context.Context, refresh bool) (ports.CodexAccountObservation, error) {
			if !refresh {
				return ports.CodexAccountObservation{}, errors.New("refresh was not requested")
			}
			refreshReads.Add(1)
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}, nil
		},
		capacityFn: func(context.Context) (ports.CodexCapacityObservation, error) {
			switch capacityReads.Add(1) {
			case 1:
				return ports.CodexCapacityObservation{}, ports.ErrCodexOAuthTokenRevoked
			case 2:
				return ports.CodexCapacityObservation{ObservedAt: now, Overall: &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityNotReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 25}}}, nil
			default:
				return ports.CodexCapacityObservation{}, errors.New("capacity was retried more than once")
			}
		},
	}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := commitAuthorizedCodexCapacityTestRecord(t, manager, "cf1012ed-4416-47c1-bc84-926069374db6", email, now)

	snapshot, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, true)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != domain.CodexCapacityAvailable || snapshot.Freshness != domain.AgentReadinessFresh {
		t.Fatalf("capacity after refresh = %#v", snapshot)
	}
	if capacityReads.Load() != 2 || refreshReads.Load() != 1 {
		t.Fatalf("capacity reads = %d, refresh reads = %d", capacityReads.Load(), refreshReads.Load())
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("successful refresh signed the account out: %#v", latest.Snapshot.Authentication)
	}
}

func TestCodexCapacityProtectedRetryOverridesInconclusiveRefreshRead(t *testing.T) {
	now := time.Date(2026, 9, 11, 9, 30, 0, 0, time.UTC)
	email := "person@example.com"
	var capacityReads atomic.Int32
	var refreshReads atomic.Int32
	client := &fakeCodexAccountClient{
		readFn: func(_ context.Context, refresh bool) (ports.CodexAccountObservation, error) {
			if !refresh {
				return ports.CodexAccountObservation{}, errors.New("refresh was not requested")
			}
			refreshReads.Add(1)
			// account/read can report no account after refresh even though the
			// protected endpoint accepts the resulting credential.
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}, nil
		},
		capacityFn: func(context.Context) (ports.CodexCapacityObservation, error) {
			if capacityReads.Add(1) == 1 {
				return ports.CodexCapacityObservation{}, ports.ErrCodexOAuthTokenRevoked
			}
			return ports.CodexCapacityObservation{ObservedAt: now}, nil
		},
	}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := commitAuthorizedCodexCapacityTestRecord(t, manager, "8b0d7f34-af76-482e-a4ca-6a2727e16b3d", email, now)

	if _, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, true); err != nil {
		t.Fatal(err)
	}
	if capacityReads.Load() != 2 || refreshReads.Load() != 1 {
		t.Fatalf("capacity reads = %d, refresh reads = %d", capacityReads.Load(), refreshReads.Load())
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("protected success did not win over account/read = %#v", latest.Snapshot.Authentication)
	}
	verified, reauthenticationRequired := manager.authenticationVerification(record.Snapshot.ID)
	if !verified || reauthenticationRequired {
		t.Fatalf("verification = %t, reauthentication required = %t", verified, reauthenticationRequired)
	}
}

func TestCodexCapacitySuccessRepairsStaleRefreshBasedSignedOutState(t *testing.T) {
	now := time.Date(2026, 9, 11, 9, 30, 0, 0, time.UTC)
	email := "person@example.com"
	client := &fakeCodexAccountClient{capacity: ports.CodexCapacityObservation{ObservedAt: now}}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := commitAuthorizedCodexCapacityTestRecord(t, manager, "72eef9ac-8f87-47bb-a8cc-e9380823688d", email, now)
	manager.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.CodexAccountSnapshot) {
		snapshot.Authentication = signedOutAuthentication(now, "stale refresh result")
	})
	manager.mu.Lock()
	manager.auth[record.Snapshot.ID] = &accountAuthState{launchVerified: true}
	manager.mu.Unlock()
	record, _ = manager.catalog.record(record.Snapshot.ID)

	if _, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, true); err != nil {
		t.Fatal(err)
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("protected success did not repair stale signed-out state = %#v", latest.Snapshot.Authentication)
	}
}

func TestCodexCapacityRevokedAfterRefreshRequiresSignInAgain(t *testing.T) {
	now := time.Date(2026, 9, 11, 9, 30, 0, 0, time.UTC)
	email := "person@example.com"
	var capacityReads atomic.Int32
	var refreshReads atomic.Int32
	client := &fakeCodexAccountClient{
		readFn: func(_ context.Context, refresh bool) (ports.CodexAccountObservation, error) {
			if !refresh {
				return ports.CodexAccountObservation{}, errors.New("refresh was not requested")
			}
			refreshReads.Add(1)
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}, nil
		},
		capacityFn: func(context.Context) (ports.CodexCapacityObservation, error) {
			capacityReads.Add(1)
			return ports.CodexCapacityObservation{}, ports.ErrCodexOAuthTokenRevoked
		},
	}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := commitAuthorizedCodexCapacityTestRecord(t, manager, "6661e336-c326-4885-b20f-719b5a073a14", email, now)

	snapshot, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, true)
	if err != nil {
		t.Fatal(err)
	}
	if capacityReads.Load() != 2 || refreshReads.Load() != 1 {
		t.Fatalf("capacity reads = %d, refresh reads = %d", capacityReads.Load(), refreshReads.Load())
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State != domain.AgentAuthenticationUnauthorized || latest.Snapshot.Authentication.ReasonCode != domain.AgentReadinessReasonUnauthorized {
		t.Fatalf("revoked account authentication = %#v", latest.Snapshot.Authentication)
	}
	if snapshot.State != domain.CodexCapacityUnknown || snapshot.ReasonCode != domain.CodexCapacityReasonSkippedSignedOut {
		t.Fatalf("revoked account capacity = %#v", snapshot)
	}
	manager.mu.Lock()
	reauthenticationRequired := manager.auth[record.Snapshot.ID].reauthenticationRequired
	manager.mu.Unlock()
	if !reauthenticationRequired {
		t.Fatal("revoked account was not marked for reauthentication")
	}
}

func TestCodexCapacityTransientRefreshFailureDoesNotSignAccountOut(t *testing.T) {
	now := time.Date(2026, 9, 11, 9, 30, 0, 0, time.UTC)
	email := "person@example.com"
	client := &fakeCodexAccountClient{
		readFn: func(context.Context, bool) (ports.CodexAccountObservation, error) {
			return ports.CodexAccountObservation{}, errors.New("temporary refresh transport failure with private details")
		},
		capacityErr: ports.ErrCodexOAuthTokenRevoked,
	}
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{capabilities: domain.CodexAccountCapabilities{CapacityRead: supported}, open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := commitAuthorizedCodexCapacityTestRecord(t, manager, "ff2e5860-7276-4843-92e9-07905cc763a6", email, now)

	snapshot, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, true)
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("transient failure signed the account out: %#v", latest.Snapshot.Authentication)
	}
	if snapshot.ReasonCode != domain.CodexCapacityReasonCheckFailed || snapshot.Reason == "temporary refresh transport failure with private details" {
		t.Fatalf("transient refresh failure = %#v", snapshot)
	}
}

func TestCodexCapacityFailureClassificationIsSpecificAndSafe(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		wantCode string
	}{
		{"timeout", context.DeadlineExceeded, domain.CodexCapacityReasonCheckTimeout},
		{"interrupted", context.Canceled, domain.CodexCapacityReasonCheckStopped},
		{"revoked OAuth token", ports.ErrCodexOAuthTokenRevoked, domain.CodexCapacityReasonSkippedSignedOut},
		{"provider rejected", ports.ErrCodexCapacityRequestRejected, domain.CodexCapacityReasonProviderRejected},
		{"provider unavailable", ports.ErrCodexCapacityProviderUnavailable, domain.CodexCapacityReasonProviderUnavailable},
		{"unknown", errors.New("private provider response with secret-token"), domain.CodexCapacityReasonCheckFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, reason := classifyCodexCapacityReadFailure(test.err)
			if code != test.wantCode {
				t.Fatalf("code = %q, want %q", code, test.wantCode)
			}
			if reason == "" || reason == test.err.Error() {
				t.Fatalf("reason was empty or retained the raw error: %q", reason)
			}
		})
	}
}

func TestCodexCapacityClientStartFailureDoesNotExposeRawError(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported}
	factory := &fakeCodexAccountFactory{
		capabilities: domain.CodexAccountCapabilities{CapacityRead: supported},
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return nil, errors.New("failed to start with secret-token")
		},
	}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.now = func() time.Time { return now }
	manager.capacity.now = manager.now
	record := codexCapacityTestRecord(t.TempDir(), "existing", domain.CodexAccountSourceManaged, now)

	snapshot, err := manager.capacity.ensureOne(context.Background(), record, factory.capabilities, true)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReasonCode != domain.CodexCapacityReasonClientStartFailed {
		t.Fatalf("reason code = %q, want %q", snapshot.ReasonCode, domain.CodexCapacityReasonClientStartFailed)
	}
	if snapshot.Reason == "" || snapshot.Reason == "failed to start with secret-token" {
		t.Fatalf("reason was empty or retained the raw error: %q", snapshot.Reason)
	}
}

func codexCapacityTestRecord(home, id string, source domain.CodexAccountSource, now time.Time) codexAccountRecord {
	return codexAccountRecord{Home: home, Snapshot: domain.CodexAccountSnapshot{
		ID: id, Source: source, Status: domain.CodexAccountStatusValid,
		Authentication: successfulAuthentication(now, domain.AgentAuthenticationAuthorized, domain.AgentReadinessReasonAuthorized, "authorized"),
		AuthMethod:     domain.CodexAuthMethodChatGPT,
	}}
}

func commitAuthorizedCodexCapacityTestRecord(t *testing.T, manager *codexAccountManager, operationID, email string, now time.Time) codexAccountRecord {
	t.Helper()
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, operationID, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	manager.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.CodexAccountSnapshot) {
		snapshot.Authentication = successfulAuthentication(now, domain.AgentAuthenticationAuthorized, domain.AgentReadinessReasonAuthorized, "authorized")
	})
	record, _ = manager.catalog.record(record.Snapshot.ID)
	return record
}

func TestCodexCapacityHeadlineMatrix(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		used    float64
		reached domain.CodexCapacityReachedState
		want    domain.CodexCapacityState
	}{
		{"available", 74.9, domain.CodexCapacityNotReached, domain.CodexCapacityAvailable},
		{"near threshold", 75, domain.CodexCapacityNotReached, domain.CodexCapacityNearLimit},
		{"one hundred", 100, domain.CodexCapacityNotReached, domain.CodexCapacityExhausted},
		{"provider reached", 12, domain.CodexCapacityReached, domain.CodexCapacityExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := ports.CodexCapacityObservation{ObservedAt: now, Overall: &domain.CodexCapacityBucket{
				LimitID: "codex", Reached: test.reached,
				Primary: &domain.CodexCapacityWindow{UsedPercent: test.used},
			}}
			got := capacitySnapshotFromObservation(observation, now, now)
			if got.State != test.want {
				t.Fatalf("state = %q, want %q", got.State, test.want)
			}
		})
	}
}

func TestSparseCodexCapacityEventPreservesAdditionalBuckets(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	current := capacitySnapshotFromObservation(ports.CodexCapacityObservation{
		ObservedAt:        now,
		Plan:              testStringPointer("pro"),
		Overall:           &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityNotReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 40}},
		AdditionalBuckets: []domain.CodexCapacityBucket{{LimitID: "spark", Reached: domain.CodexCapacityNotReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 10}}},
		ResetCredits:      &domain.CodexResetCreditsSummary{AvailableCount: 2},
	}, now, now)
	later := now.Add(time.Minute)
	merged := mergeCapacityObservation(current, ports.CodexCapacityObservation{
		ObservedAt: later, Partial: true,
		Overall:           &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityReachUnknown, Secondary: &domain.CodexCapacityWindow{UsedPercent: 81}},
		AdditionalBuckets: []domain.CodexCapacityBucket{{LimitID: "event-only", Reached: domain.CodexCapacityReached}},
	}, later)
	if merged.Overall == nil || merged.Overall.Primary == nil || merged.Overall.Secondary == nil {
		t.Fatalf("sparse merge erased an overall window: %#v", merged.Overall)
	}
	if len(merged.AdditionalBuckets) != 1 || merged.AdditionalBuckets[0].LimitID != "spark" {
		t.Fatalf("sparse merge erased additional buckets: %#v", merged.AdditionalBuckets)
	}
	if merged.ResetCredits == nil || merged.ResetCredits.AvailableCount != 2 {
		t.Fatalf("sparse merge erased reset credits: %#v", merged.ResetCredits)
	}
	if merged.State != domain.CodexCapacityNearLimit || merged.UsedPercent == nil || *merged.UsedPercent != 81 {
		t.Fatalf("merged headline = %#v", merged)
	}
}

func testStringPointer(value string) *string { return &value }
