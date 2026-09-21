package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestAgentInstallJobStoreRoundTripAndList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	started := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	want := ports.AgentInstallJobRecord{
		Target: "codex", Status: "failed", Method: "npm", Command: "npm install -g @openai/codex",
		ExpectedDestination: "/Users/test/.npm/bin/codex", Output: "permission denied", Error: "exit status 1",
		StartedAt: started, FinishedAt: &finished, UpdatedAt: finished,
	}

	if err := store.UpsertAgentInstallJob(ctx, want); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	got, ok, err := store.GetAgentInstallJob(ctx, "codex")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if !ok {
		t.Fatal("get job: missing record")
	}
	assertAgentInstallJobRecord(t, got, want)

	jobs, err := store.ListAgentInstallJobs(ctx)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("list jobs length = %d, want 1", len(jobs))
	}
	assertAgentInstallJobRecord(t, jobs[0], want)
}

func TestInterruptActiveAgentInstallJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	started := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	for _, record := range []ports.AgentInstallJobRecord{
		{Target: "codex", Status: "installing", Method: "npm", StartedAt: started, UpdatedAt: started},
		{Target: "claude-code", Status: "verifying", Method: "homebrew", StartedAt: started, UpdatedAt: started},
		{Target: "cursor", Status: "succeeded", Method: "manual", StartedAt: started, UpdatedAt: started},
	} {
		if err := store.UpsertAgentInstallJob(ctx, record); err != nil {
			t.Fatalf("seed %s: %v", record.Target, err)
		}
	}

	interruptedAt := started.Add(2 * time.Minute)
	if err := store.InterruptActiveAgentInstallJobs(ctx, interruptedAt); err != nil {
		t.Fatalf("interrupt active jobs: %v", err)
	}

	jobs, err := store.ListAgentInstallJobs(ctx)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	byTarget := make(map[string]ports.AgentInstallJobRecord, len(jobs))
	for _, job := range jobs {
		byTarget[job.Target] = job
	}
	for _, target := range []string{"codex", "claude-code"} {
		job := byTarget[target]
		if job.Status != "interrupted" {
			t.Errorf("%s status = %q, want interrupted", target, job.Status)
		}
		if job.FinishedAt == nil || !job.FinishedAt.Equal(interruptedAt) {
			t.Errorf("%s finishedAt = %v, want %v", target, job.FinishedAt, interruptedAt)
		}
		if job.Error == "" {
			t.Errorf("%s error is empty", target)
		}
	}
	if got := byTarget["cursor"].Status; got != "succeeded" {
		t.Errorf("cursor status = %q, want succeeded", got)
	}
}

func assertAgentInstallJobRecord(t *testing.T, got, want ports.AgentInstallJobRecord) {
	t.Helper()
	if got.Target != want.Target || got.Status != want.Status || got.Method != want.Method || got.Command != want.Command ||
		got.ExpectedDestination != want.ExpectedDestination || got.Output != want.Output || got.Error != want.Error ||
		!got.StartedAt.Equal(want.StartedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("job = %#v, want %#v", got, want)
	}
	if (got.FinishedAt == nil) != (want.FinishedAt == nil) {
		t.Fatalf("finishedAt = %v, want %v", got.FinishedAt, want.FinishedAt)
	}
	if got.FinishedAt != nil && !got.FinishedAt.Equal(*want.FinishedAt) {
		t.Fatalf("finishedAt = %v, want %v", got.FinishedAt, want.FinishedAt)
	}
}
