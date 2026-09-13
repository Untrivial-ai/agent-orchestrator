package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDeviceSetupJobRoundTripAndRecovery(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	job := ports.DeviceSetupJobRecord{Platform: "android", State: "downloading", Stage: "tools", Progress: 31, LicenseAccepted: true, StartedAt: now, UpdatedAt: now}
	if err := store.UpsertDeviceSetupJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetDeviceSetupJob(context.Background(), "android")
	if err != nil || !ok || got.Progress != 31 || !got.LicenseAccepted {
		t.Fatalf("round trip = %#v, %v, %v", got, ok, err)
	}
	if err := store.InterruptActiveDeviceSetupJobs(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.GetDeviceSetupJob(context.Background(), "android")
	if got.State != "interrupted" || got.ErrorCode != "SETUP_INTERRUPTED" || got.FinishedAt == nil {
		t.Fatalf("recovered = %#v", got)
	}
}
