package cli

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	reportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/report"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// TestReportRoundTrip pins the intentionally hand-mirrored CLI DTO to the real
// HTTP, service, and durable SQLite boundaries.
func TestReportRoundTrip(t *testing.T) {
	ctx := context.Background()
	cfg := setConfigEnv(t)
	dbDir := filepath.Join(t.TempDir(), "db")
	store, err := sqlite.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "ao", Path: "/tmp/ao", RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "ao", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_SESSION_ID", string(session.ID))

	service := reportsvc.New(reportsvc.Deps{
		Store: store,
		Now:   func() time.Time { return now },
		NewID: func() string { return "rpt_roundtrip" },
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Reports: service}, httpd.ControlDeps{})
	server := httptest.NewServer(router)
	writeRunFileFor(t, cfg, server)

	_, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"report", "--done", "--note", "real boundary",
		"--artifact", "artifact-one", "--artifact", "artifact-two",
		"--pr-reviewed", "https://github.com/aoagents/agent-orchestrator/pull/4488")
	server.Close()
	if err != nil {
		t.Fatalf("report: %v\nstderr=%s", err, stderr)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok, err := reopened.GetReport(ctx, "rpt_roundtrip")
	if err != nil || !ok {
		t.Fatalf("persisted report: ok=%v err=%v", ok, err)
	}
	if got.SessionID != session.ID || got.ProjectID != session.ProjectID || got.State != domain.ReportDone || got.Note != "real boundary" || got.DeliveryState != domain.ReportPending || !got.AvailableAt.Equal(now.Add(domain.ReportSettlementWindow)) || !got.SettlementDeadline.Equal(got.AvailableAt) || got.RepeatCount != 1 || len(got.Outputs) != 3 || got.Outputs[1].Reference != "artifact-two" || got.Outputs[2].Kind != domain.ReportOutputPRReviewed {
		t.Fatalf("persisted report = %+v", got)
	}
	persistedSession, ok, err := reopened.GetSession(ctx, session.ID)
	if err != nil || !ok || persistedSession.IsTerminated {
		t.Fatalf("session changed by done report: session=%+v ok=%v err=%v", persistedSession, ok, err)
	}
}
