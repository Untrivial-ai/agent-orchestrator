package projectsummary

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeStore struct {
	sessions   []domain.SessionRecord
	summary    domain.ProjectSummary
	hasSummary bool
	writes     int
}

func TestGenerationFailureRetainsLastGoodSummaryAndWatermark(t *testing.T) {
	base := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	last := domain.ProjectSummary{ProjectID: "demo", Narrative: "Last good summary.", SourceWatermark: "old", GeneratedAt: base, NeedsAttention: []domain.ProjectAttentionItem{}}
	store := &fakeStore{summary: last, hasSummary: true, sessions: []domain.SessionRecord{{ID: "orchestrator", ProjectID: "demo", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}, {ID: "worker", ProjectID: "demo", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityActive}, UpdatedAt: base.Add(time.Hour)}}}
	svc := New(store, &fakeGenerator{err: errors.New("not authenticated")})
	got, err := svc.Get(context.Background(), "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Narrative != last.Narrative || got.SourceWatermark != last.SourceWatermark || got.GenerationError == "" {
		t.Fatalf("failure result = %#v, want retained summary with surfaced error", got)
	}
	if store.writes != 0 {
		t.Fatalf("writes = %d, want 0", store.writes)
	}
}

type fakeGenerator struct {
	result   string
	err      error
	calls    int
	requests []GenerationRequest
}

type fakeReportReader struct {
	reports []ReportFact
	calls   int
}

func (f *fakeReportReader) ListProject(context.Context, domain.ProjectID) ([]ReportFact, error) {
	f.calls++
	return f.reports, nil
}

func (f *fakeGenerator) Update(_ context.Context, request GenerationRequest) (string, error) {
	f.calls++
	f.requests = append(f.requests, request)
	return f.result, f.err
}

func (f *fakeStore) GetProject(context.Context, string) (domain.ProjectRecord, bool, error) {
	return domain.ProjectRecord{ID: "demo", Path: "/demo"}, true, nil
}
func (f *fakeStore) ListSessions(context.Context, domain.ProjectID) ([]domain.SessionRecord, error) {
	return f.sessions, nil
}
func (f *fakeStore) GetProjectSummary(context.Context, domain.ProjectID) (domain.ProjectSummary, bool, error) {
	return f.summary, f.hasSummary, nil
}
func (f *fakeStore) PutProjectSummary(_ context.Context, summary domain.ProjectSummary) error {
	f.summary, f.hasSummary, f.writes = summary, true, f.writes+1
	return nil
}

func TestRefreshIsStableUntilObservedFactsChange(t *testing.T) {
	base := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{sessions: []domain.SessionRecord{{ID: "orchestrator", ProjectID: "demo", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}, {ID: "demo-1", ProjectID: "demo", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityActive}, UpdatedAt: base}}}
	generator := &fakeGenerator{result: "Work is moving."}
	svc := New(store, generator)
	svc.clock = func() time.Time { return base }
	first, err := svc.Get(context.Background(), "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	svc.clock = func() time.Time { return base.Add(time.Hour) }
	second, err := svc.Get(context.Background(), "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 {
		t.Fatalf("writes = %d, want 1", store.writes)
	}
	if !second.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatalf("no-op refresh changed generatedAt")
	}
	if generator.calls != 1 {
		t.Fatalf("generator calls = %d, want 1 for no-op refresh", generator.calls)
	}
	store.sessions[1].Activity.State = domain.ActivityWaitingInput
	store.sessions[1].Metadata.LatestAssistantUpdate = "Choose the API shape."
	store.sessions[1].UpdatedAt = base.Add(2 * time.Hour)
	third, err := svc.Get(context.Background(), "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if store.writes != 2 || len(third.NeedsAttention) != 1 {
		t.Fatalf("changed refresh = writes %d, attention %d", store.writes, len(third.NeedsAttention))
	}
}

func TestAttentionClearsFromLiveProjectionWhenLatestActivityNoLongerNeedsInput(t *testing.T) {
	base := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{sessions: []domain.SessionRecord{{ID: "orchestrator", ProjectID: "demo", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}, {ID: "demo-1", ProjectID: "demo", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityWaitingInput}, UpdatedAt: base}}}
	svc := New(store, &fakeGenerator{result: "A decision is pending."})
	svc.clock = func() time.Time { return base }
	if got, _ := svc.Get(context.Background(), "demo", true); len(got.NeedsAttention) != 1 {
		t.Fatalf("attention = %d, want 1", len(got.NeedsAttention))
	}
	store.sessions[1].Activity.State = domain.ActivityActive
	store.sessions[1].UpdatedAt = base.Add(time.Minute)
	if got, _ := svc.Get(context.Background(), "demo", false); len(got.NeedsAttention) != 0 {
		t.Fatalf("attention = %d, want cleared", len(got.NeedsAttention))
	}
	if store.writes != 1 {
		t.Fatalf("writes = %d, want narrative projection unchanged", store.writes)
	}
}

func TestAttentionClearsWhenWorkerTerminates(t *testing.T) {
	base := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{sessions: []domain.SessionRecord{{ID: "orchestrator", ProjectID: "demo", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}, {ID: "demo-1", ProjectID: "demo", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityWaitingInput}, UpdatedAt: base}}}
	svc := New(store, &fakeGenerator{result: "A decision is pending."})
	if got, _ := svc.Get(context.Background(), "demo", true); len(got.NeedsAttention) != 1 {
		t.Fatalf("attention = %d, want 1", len(got.NeedsAttention))
	}
	store.sessions[1].IsTerminated = true
	store.sessions[1].UpdatedAt = base.Add(time.Minute)
	if got, _ := svc.Get(context.Background(), "demo", false); len(got.NeedsAttention) != 0 {
		t.Fatalf("attention = %d, want cleared for terminated worker", len(got.NeedsAttention))
	}
}

func TestRefreshConsumesReadOnlyReportFactsAsNarrativeContext(t *testing.T) {
	base := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{sessions: []domain.SessionRecord{{ID: "orchestrator", ProjectID: "demo", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}, {ID: "worker", ProjectID: "demo", Kind: domain.KindWorker, DisplayName: "Worker", Activity: domain.Activity{State: domain.ActivityWaitingInput}, UpdatedAt: base}}}
	reports := &fakeReportReader{reports: []ReportFact{{ID: "rpt-1", SessionID: "worker", State: "needs_input", Note: "Choose the API shape.", CreatedAt: base, RepeatCount: 1, Outputs: []ReportOutputFact{{Kind: "artifact", Reference: "opaque-output", Label: "Design"}}}}}
	generator := &fakeGenerator{result: "The API decision is pending."}
	svc := New(store, generator, reports)
	first, err := svc.Get(context.Background(), "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if reports.calls != 1 || len(first.NeedsAttention) != 1 || len(generator.requests) != 1 || len(generator.requests[0].Reports) != 1 || len(generator.requests[0].Reports[0].Outputs) != 1 || generator.requests[0].Reports[0].Outputs[0].Reference != "opaque-output" {
		t.Fatalf("summary did not consume report projection: %#v", first)
	}
	watermark := first.SourceWatermark
	reports.reports[0].Note = "Choose the final API shape."
	second, err := svc.Get(context.Background(), "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if second.SourceWatermark == watermark || generator.calls != 2 {
		t.Fatalf("report change did not regenerate: watermark=%q calls=%d", second.SourceWatermark, generator.calls)
	}
}

func TestNeedsInputReportDoesNotOutliveLatestWorkerActivity(t *testing.T) {
	base := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{sessions: []domain.SessionRecord{{ID: "orchestrator", ProjectID: "demo", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}, {ID: "worker", ProjectID: "demo", Kind: domain.KindWorker, Activity: domain.Activity{State: domain.ActivityActive}, UpdatedAt: base}}}
	reports := &fakeReportReader{reports: []ReportFact{{ID: "rpt-1", SessionID: "worker", State: "needs_input", Note: "This question was dismissed.", CreatedAt: base}}}

	got, err := New(store, &fakeGenerator{result: "Work resumed."}, reports).Get(context.Background(), "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.NeedsAttention) != 0 {
		t.Fatalf("attention = %d, want stale report hidden by latest active state", len(got.NeedsAttention))
	}
}

func TestRefreshPassesCheckpointTextToNarrativeGenerator(t *testing.T) {
	base := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{sessions: []domain.SessionRecord{{ID: "orchestrator", ProjectID: "demo", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}, {ID: "worker", ProjectID: "demo", Kind: domain.KindWorker, DisplayName: "Worker", Activity: domain.Activity{State: domain.ActivityActive}, UpdatedAt: base}}}
	reports := &fakeReportReader{reports: []ReportFact{{ID: "rpt-1", SessionID: "worker", ProjectID: "demo", State: "checkpoint", Note: "Implemented the report-backed summary adapter.", CreatedAt: base}}}
	generator := &fakeGenerator{result: "The adapter is implemented."}

	if _, err := New(store, generator, reports).Get(context.Background(), "demo", true); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(generator.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "Implemented the report-backed summary adapter.") {
		t.Fatalf("generation request omitted checkpoint text: %s", payload)
	}
}
