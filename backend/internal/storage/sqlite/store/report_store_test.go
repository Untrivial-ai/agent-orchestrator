package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestReportStorePersistsOutputsAndDeadlinesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedProject(t, s, "ao")
	sess, err := s.CreateSession(ctx, sampleRecord("ao"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	want := domain.ReportRecord{
		ID: "rpt_1", SessionID: sess.ID, ProjectID: sess.ProjectID,
		Outputs: []domain.ReportOutput{
			{Kind: domain.ReportOutputArtifact, Reference: "opaque-ref", Label: "Result"},
			{Kind: domain.ReportOutputArtifact, Reference: "second"},
			{Kind: domain.ReportOutputPRCreated, Reference: "https://github.com/o/r/pull/1"},
		},
		CreatedAt: now, DeliveryState: domain.ReportPending,
		AvailableAt: now.Add(domain.ReportBatchFallback), RepeatCount: 1,
	}
	if _, err = s.CreateReport(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, ok, err := s.GetReport(ctx, want.ID)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.SessionID != sess.ID || got.ProjectID != sess.ProjectID || len(got.Outputs) != 3 || got.Outputs[0].Label != "Result" || got.Outputs[1].Reference != "second" || got.Outputs[2].Kind != domain.ReportOutputPRCreated || !got.AvailableAt.Equal(want.AvailableAt) {
		t.Fatalf("got=%+v", got)
	}
	if pending, err := s.ListPendingReports(ctx, now, 10); err != nil || len(pending) != 0 {
		t.Fatalf("early pending=%+v err=%v", pending, err)
	}
	pending, err := s.ListPendingReports(ctx, want.AvailableAt, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != want.ID || len(pending[0].Outputs) != 3 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestReportStoreRejectsInvalidRecord(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateReport(context.Background(), domain.ReportRecord{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestReportStoreClaimAcknowledgeAndTokenFence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "ao")
	sess, err := s.CreateSession(ctx, sampleRecord("ao"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.ReportRecord{ID: "rpt_lease", SessionID: sess.ID, ProjectID: sess.ProjectID, State: domain.ReportDone, Note: "done", CreatedAt: now.Add(-domain.ReportSettlementWindow), DeliveryState: domain.ReportPending, AvailableAt: now, SettlementDeadline: now, RepeatCount: 1}
	if _, err = s.CreateReport(ctx, rec); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimReport(ctx, rec.ID, "lease-1", now)
	if err != nil || !ok || claimed.DeliveryAttempts != 1 || claimed.DeliveryState != domain.ReportClaimed {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if _, ok, err = s.ClaimReport(ctx, rec.ID, "lease-2", now); err != nil || ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
	if _, ok, err = s.AcknowledgeReport(ctx, rec.ID, "wrong", now.Add(time.Second)); err != nil || ok {
		t.Fatalf("wrong ack ok=%v err=%v", ok, err)
	}
	acked, ok, err := s.AcknowledgeReport(ctx, rec.ID, "lease-1", now.Add(time.Second))
	if err != nil || !ok || acked.DeliveryState != domain.ReportAcknowledged {
		t.Fatalf("acked=%+v ok=%v err=%v", acked, ok, err)
	}
}

func TestReportStoreRequeuesInterruptedClaimOnRestartAndFencesOldToken(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedProject(t, s, "ao")
	sess, err := s.CreateSession(ctx, sampleRecord("ao"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.ReportRecord{ID: "rpt_restart", SessionID: sess.ID, ProjectID: sess.ProjectID, Message: "information", CreatedAt: now.Add(-time.Hour), DeliveryState: domain.ReportPending, AvailableAt: now, RepeatCount: 1}
	if _, err = s.CreateReport(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimReport(ctx, rec.ID, "old-token", now); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if count, err := s.RequeueClaimedReports(ctx); err != nil || count != 1 {
		t.Fatalf("restart recovery count=%d err=%v", count, err)
	}
	recovered, ok, err := s.GetReport(ctx, rec.ID)
	if err != nil || !ok || recovered.DeliveryState != domain.ReportPending || recovered.ClaimToken != "" || !recovered.ClaimedAt.IsZero() || recovered.DeliveryAttempts != 1 || !strings.Contains(recovered.LastError, "restart") {
		t.Fatalf("recovered=%+v ok=%v err=%v", recovered, ok, err)
	}
	if _, ok, err := s.AcknowledgeReport(ctx, rec.ID, "old-token", now.Add(time.Second)); err != nil || ok {
		t.Fatalf("stale acknowledge ok=%v err=%v", ok, err)
	}
	claimed, ok, err := s.ClaimReport(ctx, rec.ID, "new-token", now.Add(2*time.Second))
	if err != nil || !ok || claimed.DeliveryAttempts != 2 {
		t.Fatalf("reclaim=%+v ok=%v err=%v", claimed, ok, err)
	}
	if _, ok, err := s.AcknowledgeReport(ctx, rec.ID, "old-token", now.Add(3*time.Second)); err != nil || ok {
		t.Fatalf("old token crossed fence ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.AcknowledgeReport(ctx, rec.ID, "new-token", now.Add(4*time.Second)); err != nil || !ok {
		t.Fatalf("new acknowledge ok=%v err=%v", ok, err)
	}
}
