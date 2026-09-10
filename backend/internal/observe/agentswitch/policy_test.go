package agentswitch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/telemetry/policyauthority"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestHeadlessAuthorityTruthTable(t *testing.T) {
	tests := []struct {
		name, file         string
		explicit, envOn    bool
		wantEnabled        bool
		wantFileGeneration bool
		wantUnavailable    bool
	}{
		{name: "valid off always off", file: "off", explicit: true, envOn: true},
		{name: "valid on and explicit environment on", file: "on", explicit: true, envOn: true, wantEnabled: true, wantFileGeneration: true},
		{name: "valid on without explicit environment is off", file: "on", envOn: true},
		{name: "valid on and explicit environment off is off", file: "on", explicit: true},
		{name: "missing and explicit on uses boot token", explicit: true, envOn: true, wantEnabled: true},
		{name: "missing without explicit on is off"},
		{name: "malformed is off even with explicit on", file: "malformed", explicit: true, envOn: true, wantUnavailable: true},
		{name: "unsafe is off even with explicit on", file: "unsafe", explicit: true, envOn: true, wantUnavailable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			generation := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
			path := filepath.Join(dir, PolicyFileName)
			switch tc.file {
			case "on", "off":
				writePolicy(t, path, tc.file == "on", generation, 0o600)
			case "malformed":
				if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unsafe":
				writePolicy(t, path, true, generation, 0o644)
			}
			store := &policyStoreFake{}
			coordinator := NewPolicyCoordinator(store, PolicyOptions{
				AuthorityReader: policyauthority.New(path), TelemetryEvents: tc.envOn, TelemetryEventsExplicit: tc.explicit,
				DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true),
				Metadata: validMetadata(), NewBootToken: func() string { return "boot-token" },
			})
			if err := coordinator.ForceDisabled(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Synchronize(context.Background()); errors.Is(err, ErrPolicyUnavailable) != tc.wantUnavailable {
				t.Fatalf("synchronize error = %v, want unavailable=%v", err, tc.wantUnavailable)
			}
			got := coordinator.Authorization()
			if got.Enabled != tc.wantEnabled {
				t.Fatalf("Enabled = %v, want %v", got.Enabled, tc.wantEnabled)
			}
			if tc.wantFileGeneration && got.ConsentGeneration != generation {
				t.Fatalf("generation = %q", got.ConsentGeneration)
			}
			if tc.wantEnabled && !tc.wantFileGeneration && got.ConsentGeneration != "boot-token" {
				t.Fatalf("boot generation = %q", got.ConsentGeneration)
			}
		})
	}
}

func TestApplyPolicyTreatsBodyAsHintAndCannotForgeEnablement(t *testing.T) {
	dir := t.TempDir()
	generation := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
	writePolicy(t, filepath.Join(dir, PolicyFileName), false, generation, 0o600)
	store := &policyStoreFake{}
	coordinator := NewPolicyCoordinator(store, PolicyOptions{AuthorityReader: policyauthority.New(filepath.Join(dir, PolicyFileName)), TelemetryEvents: true, TelemetryEventsExplicit: true, DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata()})
	if _, err := coordinator.ApplyPolicy(context.Background(), generation, true); err == nil {
		t.Fatal("forged enabled hint was accepted for an off authority file")
	}
	if got := coordinator.Authorization(); got.Enabled {
		t.Fatal("forged hint opened the gate")
	}
}

func TestPrepareDisableLatchesGateUntilDisabledGenerationIsPurged(t *testing.T) {
	dir := t.TempDir()
	onGeneration := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
	offGeneration := "19549322-5832-4d4e-9206-7268e0228db3"
	path := filepath.Join(dir, PolicyFileName)
	writePolicy(t, path, true, onGeneration, 0o600)
	store := &policyStoreFake{}
	coordinator := NewPolicyCoordinator(store, PolicyOptions{
		AuthorityReader: policyauthority.New(path), TelemetryEvents: true, TelemetryEventsExplicit: true,
		DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata(),
	})
	if err := coordinator.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	initialApplies := len(store.applied)
	prepared, err := coordinator.PrepareDisable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.GateDrained || prepared.PurgeConfirmed || prepared.Authorization.Enabled {
		t.Fatalf("prepare acknowledgement = %+v", prepared)
	}
	if prepared.Authorization.ConsentGeneration != onGeneration {
		t.Fatalf("prepare generation = %q, want %q", prepared.Authorization.ConsentGeneration, onGeneration)
	}
	if _, err := coordinator.ApplyPolicy(context.Background(), onGeneration, true); !errors.Is(err, ErrPolicyCleanupPending) {
		t.Fatalf("enabled apply after prepare error = %v, want ErrPolicyCleanupPending", err)
	}
	if err := coordinator.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if coordinator.Authorization().Enabled {
		t.Fatal("watcher synchronization reopened a prepared disable from the old on authority")
	}
	if len(store.applied) != initialApplies {
		t.Fatalf("prepare synchronization mutated policy: applies=%d want=%d", len(store.applied), initialApplies)
	}

	writePolicy(t, path, false, offGeneration, 0o600)
	applied, err := coordinator.ApplyPolicy(context.Background(), offGeneration, false)
	if err != nil {
		t.Fatal(err)
	}
	wantAuthorization := domain.AgentSwitchReportingAuthorization{ConsentGeneration: offGeneration}
	if applied.Authorization != wantAuthorization || !applied.GateDrained || !applied.PurgeConfirmed {
		t.Fatalf("disabled acknowledgement = %+v, want authorization=%+v gateDrained=true purgeConfirmed=true", applied, wantAuthorization)
	}
}

func TestDisabledApplyFailureDoesNotCachePurgeProof(t *testing.T) {
	dir := t.TempDir()
	generation := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
	writePolicy(t, filepath.Join(dir, PolicyFileName), false, generation, 0o600)
	store := &policyStoreFake{applyErr: errors.New("purge failed")}
	coordinator := NewPolicyCoordinator(store, PolicyOptions{
		AuthorityReader: policyauthority.New(filepath.Join(dir, PolicyFileName)), TelemetryEvents: true, TelemetryEventsExplicit: true,
		DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata(),
	})
	failed, err := coordinator.ApplyPolicy(context.Background(), generation, false)
	if err == nil {
		t.Fatal("disabled apply acknowledged a failed atomic purge")
	}
	if failed.PurgeConfirmed || failed.GateDrained {
		t.Fatalf("failed acknowledgement = %+v", failed)
	}
	if coordinator.Authorization().Enabled {
		t.Fatal("failed purge reopened the delivery gate")
	}

	store.applyErr = nil
	applied, err := coordinator.ApplyPolicy(context.Background(), generation, false)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.GateDrained || !applied.PurgeConfirmed || applied.Authorization.ConsentGeneration != generation {
		t.Fatalf("retry acknowledgement = %+v", applied)
	}
	if len(store.applied) != 2 {
		t.Fatalf("atomic policy applies = %d, want 2", len(store.applied))
	}
}

func TestDisabledApplyAcknowledgesOnlyAfterAtomicStoreCommit(t *testing.T) {
	dir := t.TempDir()
	generation := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
	writePolicy(t, filepath.Join(dir, PolicyFileName), false, generation, 0o600)
	applyStarted := make(chan struct{}, 1)
	releaseApply := make(chan struct{})
	store := &policyStoreFake{applyStarted: applyStarted, releaseApply: releaseApply}
	coordinator := NewPolicyCoordinator(store, PolicyOptions{
		AuthorityReader: policyauthority.New(filepath.Join(dir, PolicyFileName)), TelemetryEvents: true, TelemetryEventsExplicit: true,
		DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata(),
	})
	type result struct {
		ack ports.AgentSwitchFailurePolicyAcknowledgement
		err error
	}
	completed := make(chan result, 1)
	go func() {
		ack, err := coordinator.ApplyPolicy(context.Background(), generation, false)
		completed <- result{ack: ack, err: err}
	}()
	select {
	case <-applyStarted:
	case <-time.After(time.Second):
		t.Fatal("atomic policy apply did not start")
	}
	select {
	case result := <-completed:
		t.Fatalf("disabled apply acknowledged before store commit: %+v", result)
	default:
	}
	close(releaseApply)
	select {
	case result := <-completed:
		if result.err != nil || !result.ack.GateDrained || !result.ack.PurgeConfirmed {
			t.Fatalf("disabled apply result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("disabled apply did not acknowledge committed cleanup")
	}
}

func TestDisabledApplyAcknowledgesOnlyAfterProviderDrain(t *testing.T) {
	dir := t.TempDir()
	generation := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
	path := filepath.Join(dir, PolicyFileName)
	writePolicy(t, path, false, generation, 0o600)
	drainStarted := make(chan struct{}, 1)
	releaseDrain := make(chan struct{})
	coordinator := NewPolicyCoordinator(&policyStoreFake{}, PolicyOptions{
		AuthorityReader: policyauthority.New(path), TelemetryEvents: true, TelemetryEventsExplicit: true,
		DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata(),
		ProviderDrain: func(ctx context.Context) error {
			drainStarted <- struct{}{}
			select {
			case <-releaseDrain:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	type result struct {
		ack ports.AgentSwitchFailurePolicyAcknowledgement
		err error
	}
	done := make(chan result, 1)
	go func() {
		ack, err := coordinator.ApplyPolicy(context.Background(), generation, false)
		done <- result{ack: ack, err: err}
	}()
	select {
	case <-drainStarted:
	case <-time.After(time.Second):
		t.Fatal("provider drain did not start")
	}
	select {
	case got := <-done:
		t.Fatalf("opt-out acknowledged before provider drain: %+v", got)
	default:
	}
	close(releaseDrain)
	got := <-done
	if got.err != nil || !got.ack.GateDrained || !got.ack.PurgeConfirmed {
		t.Fatalf("disabled apply result = %+v", got)
	}
}

func TestPolicyOperationsDoNotOvertakeAtomicApply(t *testing.T) {
	dir := t.TempDir()
	generation := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
	writePolicy(t, filepath.Join(dir, PolicyFileName), false, generation, 0o600)
	applyStarted := make(chan struct{}, 1)
	releaseApply := make(chan struct{})
	store := &policyStoreFake{applyStarted: applyStarted, releaseApply: releaseApply}
	coordinator := NewPolicyCoordinator(store, PolicyOptions{
		AuthorityReader: policyauthority.New(filepath.Join(dir, PolicyFileName)), TelemetryEvents: true, TelemetryEventsExplicit: true,
		DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata(),
	})
	applyDone := make(chan error, 1)
	go func() {
		_, err := coordinator.ApplyPolicy(context.Background(), generation, false)
		applyDone <- err
	}()
	select {
	case <-applyStarted:
	case <-time.After(time.Second):
		t.Fatal("atomic policy apply did not start")
	}
	prepareAttempted := make(chan struct{})
	prepareDone := make(chan error, 1)
	go func() {
		close(prepareAttempted)
		_, err := coordinator.PrepareDisable(context.Background())
		prepareDone <- err
	}()
	<-prepareAttempted
	select {
	case err := <-prepareDone:
		t.Fatalf("prepare overtook in-progress atomic apply: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseApply)
	if err := <-applyDone; err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if err := <-prepareDone; err != nil {
		t.Fatalf("serialized prepare failed: %v", err)
	}
}

func TestInvalidAuthorityPurgesBeforeReportingUnavailable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PolicyFileName), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &policyStoreFake{}
	coordinator := NewPolicyCoordinator(store, PolicyOptions{
		AuthorityReader: policyauthority.New(filepath.Join(dir, PolicyFileName)), TelemetryEvents: true, TelemetryEventsExplicit: true,
		DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata(),
	})
	if err := coordinator.ForceDisabled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Synchronize(context.Background()); !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("synchronize error = %v, want ErrPolicyUnavailable", err)
	}
	if len(store.applied) != 1 || store.applied[0].Authorization.Enabled {
		t.Fatalf("invalid authority applies = %+v, want one disabled atomic purge", store.applied)
	}
}

func TestEnrollmentFailureFallsBackToDisabledAtomicPurge(t *testing.T) {
	dir := t.TempDir()
	generation := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
	writePolicy(t, filepath.Join(dir, PolicyFileName), true, generation, 0o600)
	store := &policyStoreFake{enrollErr: errors.New("enrollment failed")}
	coordinator := NewPolicyCoordinator(store, PolicyOptions{
		AuthorityReader: policyauthority.New(filepath.Join(dir, PolicyFileName)), TelemetryEvents: true, TelemetryEventsExplicit: true,
		DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata(),
	})
	if err := coordinator.Synchronize(context.Background()); err == nil {
		t.Fatal("enabled synchronization acknowledged failed recovery enrollment")
	}
	if len(store.applied) != 2 || !store.applied[0].Authorization.Enabled || store.applied[1].Authorization.Enabled {
		t.Fatalf("policy fallback applies = %+v", store.applied)
	}
	if store.applied[1].Authorization.ConsentGeneration != generation {
		t.Fatalf("fallback generation = %q, want %q", store.applied[1].Authorization.ConsentGeneration, generation)
	}
	if coordinator.Authorization().Enabled {
		t.Fatal("enrollment failure left delivery authorized")
	}
}

func TestGateChangeCancelsAndAwaitsRegisteredDeliveryWithoutInventingGeneration(t *testing.T) {
	dir := t.TempDir()
	generation := "7f80c8a9-ec67-4a16-a067-a444ffcc5cca"
	writePolicy(t, filepath.Join(dir, PolicyFileName), true, generation, 0o600)
	store := &policyStoreFake{}
	coordinator := NewPolicyCoordinator(store, PolicyOptions{AuthorityReader: policyauthority.New(filepath.Join(dir, PolicyFileName)), TelemetryEvents: true, TelemetryEventsExplicit: true, DestinationFingerprint: "destination", ProductionEnabled: boolPtr(true), Metadata: validMetadata()})
	if err := coordinator.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	epoch := coordinator.DeliveryEpoch()
	callContext, release, ok := coordinator.EnterDelivery(context.Background(), generation, epoch)
	if !ok {
		t.Fatal("delivery gate rejected matching authority")
	}
	done := make(chan struct{})
	go func() { <-callContext.Done(); release(); close(done) }()
	writePolicy(t, filepath.Join(dir, PolicyFileName), false, "19549322-5832-4d4e-9206-7268e0228db3", 0o600)
	if err := coordinator.Synchronize(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gate close did not await the registered call")
	}
	if coordinator.DeliveryEpoch() != epoch+1 {
		t.Fatalf("delivery epoch did not advance exactly once")
	}
	if got := coordinator.Authorization().ConsentGeneration; got != "19549322-5832-4d4e-9206-7268e0228db3" {
		t.Fatalf("generation = %q", got)
	}
}

type policyStoreFake struct {
	forced       int
	applied      []ports.AgentSwitchFailurePolicy
	metadata     bool
	applyErr     error
	enrollErr    error
	applyStarted chan<- struct{}
	releaseApply <-chan struct{}
}

func (s *policyStoreFake) ConfigureAgentSwitchFailureEventMetadata(_ context.Context, metadata domain.AgentSwitchEventMetadata) error {
	s.metadata = domain.ValidateAgentSwitchEventMetadata(metadata) == nil
	return domain.ValidateAgentSwitchEventMetadata(metadata)
}
func (s *policyStoreFake) ForceDisableAgentSwitchFailurePolicy(context.Context, time.Time) error {
	s.forced++
	return nil
}
func (s *policyStoreFake) ApplyAgentSwitchFailurePolicy(_ context.Context, policy ports.AgentSwitchFailurePolicy) error {
	s.applied = append(s.applied, policy)
	if s.applyStarted != nil {
		s.applyStarted <- struct{}{}
	}
	if s.releaseApply != nil {
		<-s.releaseApply
	}
	return s.applyErr
}
func (s *policyStoreFake) PurgeAgentSwitchFailurePayloads(context.Context) (int64, error) {
	return 0, nil
}
func (s *policyStoreFake) EnrollCurrentAgentSwitchRecoveryMarkers(context.Context, ports.AgentSwitchFailureRecoveryEnrollment) (int64, error) {
	return 0, s.enrollErr
}

func validMetadata() domain.AgentSwitchEventMetadata {
	return domain.AgentSwitchEventMetadata{Release: "1.2.3", Environment: domain.AgentSwitchEnvironmentStable, Channel: domain.AgentSwitchChannelStable, Platform: domain.AgentSwitchPlatformDaemon, OS: domain.AgentSwitchOSLinux, ElapsedTimeBucket: domain.AgentSwitchElapsedNotApplicable}
}
func boolPtr(value bool) *bool { return &value }
func writePolicy(t *testing.T, path string, enabled bool, generation string, mode os.FileMode) {
	t.Helper()
	record := map[string]any{
		"schema_version":     1,
		"events_enabled":     enabled,
		"consent_generation": generation,
		"updated_at":         "2026-08-28T10:15:30.000Z",
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
