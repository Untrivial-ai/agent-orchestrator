package domain

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestCloudSessionStatusDoesNotReportNoSignalDuringStartup(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	old := now.Add(-4 * time.Minute)
	for _, observed := range []string{"requested", "provisioning", "bootstrapping"} {
		t.Run(observed, func(t *testing.T) {
			session := Session{
				ActivityState: contract.ActivityIdle,
				DesiredState:  "running",
				ObservedState: observed,
				RuntimeState:  observed,
				UpdatedAt:     old,
			}
			if got := session.Status(now, nil); got != contract.StatusIdle {
				t.Fatalf("status = %q, want idle while the worker has not reported activity", got)
			}
		})
	}
}

func TestCloudSessionStatusReportsNoSignalAfterStartupDeadline(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	session := Session{
		ActivityState:   contract.ActivityIdle,
		DesiredState:    "running",
		ObservedState:   "bootstrapping",
		RuntimeState:    "bootstrapping",
		StartupAttempts: 1,
		UpdatedAt:       now.Add(-4 * time.Minute),
	}
	if got := session.Status(now, nil); got != contract.StatusNoSignal {
		t.Fatalf("status = %q, want no signal after startup repair begins", got)
	}
}

func TestCloudSessionStatusKeepsIdleAfterWorkerHasCheckedIn(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-30 * time.Second)
	session := Session{
		ActivityState:    contract.ActivityIdle,
		DesiredState:     "running",
		ObservedState:    "bootstrapping",
		WorkerLastSeenAt: &lastSeen,
		UpdatedAt:        now.Add(-4 * time.Minute),
	}
	if got := session.Status(now, nil); got != contract.StatusIdle {
		t.Fatalf("status = %q, want idle for a checked-in idle worker", got)
	}
}

func TestCloudSessionStatusDoesNotReportNoSignalWhileStopped(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	session := Session{
		ActivityState: contract.ActivityIdle,
		DesiredState:  "paused",
		ObservedState: "stopped",
		RuntimeState:  "stopped",
		UpdatedAt:     now.Add(-4 * time.Minute),
	}
	if got := session.Status(now, nil); got != contract.StatusIdle {
		t.Fatalf("status = %q, want idle while stopped", got)
	}
}

func TestCloudSessionStatusUsesWorkerHeartbeatInsteadOfSessionUpdate(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		lastSeen time.Time
		want     contract.SessionStatus
	}{
		{name: "recent heartbeat", lastSeen: now.Add(-30 * time.Second), want: contract.StatusIdle},
		{name: "stale heartbeat", lastSeen: now.Add(-3 * time.Minute), want: contract.StatusNoSignal},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := Session{
				ActivityState:    contract.ActivityIdle,
				DesiredState:     "running",
				ObservedState:    "running",
				RuntimeState:     "running",
				RuntimeConnected: false,
				WorkerLastSeenAt: &test.lastSeen,
				UpdatedAt:        now.Add(-4 * time.Minute),
			}
			if got := session.Status(now, nil); got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}
