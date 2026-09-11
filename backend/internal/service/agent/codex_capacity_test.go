package agent

import (
	"context"
	"errors"
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

func codexCapacityTestRecord(home, id string, source domain.CodexAccountSource, now time.Time) codexAccountRecord {
	return codexAccountRecord{Home: home, Snapshot: domain.CodexAccountSnapshot{
		ID: id, Source: source, Status: domain.CodexAccountStatusValid,
		Authentication: successfulAuthentication(now, domain.AgentAuthenticationAuthorized, domain.AgentReadinessReasonAuthorized, "authorized"),
		AuthMethod:     domain.CodexAuthMethodChatGPT,
	}}
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
