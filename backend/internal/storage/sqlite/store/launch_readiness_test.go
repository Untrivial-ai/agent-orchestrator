package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestLaunchReadinessSQLiteRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	want := domain.LaunchReadiness{
		State: domain.LaunchReadinessLaunchFailed, LaunchID: "launch-1", ConversationID: "native-1",
		Cause: "exit_code_23", Resume: true, UpdatedAt: now,
	}
	rec := sampleRecord("mer")
	rec.Harness = domain.HarnessCodex
	rec.Metadata.RuntimeLaunchID = want.LaunchID
	rec.Metadata.AgentSessionID = want.ConversationID
	rec.Metadata.AgentSessionIDLaunchID = want.LaunchID
	rec.Activity.State = domain.ActivityExited
	rec.LaunchReadiness = want
	rec, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	read := func() domain.SessionRecord {
		t.Helper()
		got, ok, err := s.GetSession(ctx, rec.ID)
		if err != nil || !ok {
			t.Fatalf("get session: found=%v err=%v", ok, err)
		}
		if want.UpdatedAt.IsZero() {
			if got.LaunchReadiness.UpdatedAt.IsZero() {
				t.Fatal("readiness transition has no timestamp")
			}
			want.UpdatedAt = got.LaunchReadiness.UpdatedAt
		}
		if got.LaunchReadiness != want {
			t.Fatalf("readiness=%+v want=%+v", got.LaunchReadiness, want)
		}
		for _, project := range []bool{false, true} {
			var rows []domain.SessionRecord
			if project {
				rows, err = s.ListSessions(ctx, "mer")
			} else {
				rows, err = s.ListAllSessions(ctx)
			}
			if err != nil || len(rows) != 1 || rows[0].LaunchReadiness != want {
				t.Fatalf("list readiness=%+v err=%v", rows, err)
			}
		}
		return got
	}
	read()
	m := lifecycle.New(s, nil)
	// A metadata-only callback must retain every failure field, including its cause.
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{LaunchID: "launch-1", AgentSessionID: "native-1", TranscriptPath: "/transcript"}); err != nil {
		t.Fatal(err)
	}
	read()
	zero := 0
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{Valid: true, State: domain.ActivityExited, LaunchID: "launch-1", ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	read()
	if err := m.MarkSpawned(ctx, rec.ID, domain.SessionMetadata{RuntimeLaunchID: "launch-2", AgentSessionID: "native-1", AgentSessionIDLaunchID: "launch-2"}); err != nil {
		t.Fatal(err)
	}
	want = domain.LaunchReadiness{State: domain.LaunchReadinessLaunching, LaunchID: "launch-2", ConversationID: "native-1", Resume: true}
	read()
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{Valid: true, State: domain.ActivityBlocked, LaunchID: "launch-2", AgentSessionID: "native-1"}); err != nil {
		t.Fatal(err)
	}
	want.State = domain.LaunchReadinessNeedsInput
	want.UpdatedAt = time.Time{}
	read()
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LaunchID: "launch-2", AgentSessionID: "native-1"}); err != nil {
		t.Fatal(err)
	}
	want.State = domain.LaunchReadinessReady
	want.UpdatedAt = time.Time{}
	read()
}

func TestLaunchReadinessSQLiteRejectsStaleRevision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec := sampleRecord("mer")
	rec.Metadata.RuntimeLaunchID = "launch"
	rec.LaunchReadiness = domain.LaunchReadiness{State: domain.LaunchReadinessLaunching, LaunchID: "launch"}
	rec, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := s.GetSession(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := before
	changed.LaunchReadiness.ConversationID = "new-conversation"
	if err := s.UpdateSession(ctx, changed); err != nil {
		t.Fatal(err)
	}
	before.LaunchReadiness.State = domain.LaunchReadinessReady
	if applied, err := s.UpdateSessionFromActivitySignal(ctx, before, before.Revision); err != nil || applied {
		t.Fatalf("stale write applied=%v err=%v", applied, err)
	}
	after, _, err := s.GetSession(ctx, rec.ID)
	if err != nil || after.LaunchReadiness != changed.LaunchReadiness {
		t.Fatalf("readiness overwritten: %+v err=%v", after.LaunchReadiness, err)
	}
}
