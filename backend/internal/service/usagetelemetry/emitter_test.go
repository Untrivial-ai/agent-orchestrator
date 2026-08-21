package usagetelemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeInner struct {
	finalizeCalls   int
	reactivateCalls int
	finalizeErr     error
}

func (f *fakeInner) FinalizeSession(context.Context, domain.SessionID, string, time.Time) error {
	f.finalizeCalls++
	return f.finalizeErr
}
func (f *fakeInner) ReactivateSession(context.Context, domain.SessionID, string) error {
	f.reactivateCalls++
	return nil
}

type fakeSummary struct {
	summary domain.SessionUsageSummary
	err     error
}

func (f fakeSummary) Get(context.Context, domain.SessionID) (domain.SessionUsageSummary, error) {
	return f.summary, f.err
}

type fakeStore struct {
	rec     domain.SessionRecord
	recOK   bool
	project domain.ProjectRecord
	projOK  bool
}

func (f fakeStore) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return f.rec, f.recOK, nil
}
func (f fakeStore) GetProject(context.Context, string) (domain.ProjectRecord, bool, error) {
	return f.project, f.projOK, nil
}

type fakeSCM struct{ repo ports.SCMRepo }

func (f fakeSCM) ParseRepository(remote string) (ports.SCMRepo, bool) {
	if remote == "" {
		return ports.SCMRepo{}, false
	}
	return f.repo, true
}

type fakeSink struct{ events []ports.TelemetryEvent }

func (f *fakeSink) Emit(_ context.Context, ev ports.TelemetryEvent) { f.events = append(f.events, ev) }
func (f *fakeSink) Close(context.Context) error                     { return nil }

func opusSummary(incomplete bool) domain.SessionUsageSummary {
	return domain.SessionUsageSummary{
		SessionID:  "ao-1",
		Incomplete: incomplete,
		Totals: domain.UsageMetricTotals{
			InputTokens: i64(100), OutputTokens: i64(50), CacheReadTokens: i64(10), CacheWriteTokens: i64(5),
		},
		Harnesses: []domain.HarnessUsageSummary{{
			Harness: domain.HarnessClaudeCode,
			Models: []domain.ModelUsageSummary{{
				ModelID: "claude-opus-4-8",
				Totals: domain.UsageMetricTotals{
					InputTokens: i64(100), OutputTokens: i64(50), CacheReadTokens: i64(10), CacheWriteTokens: i64(5),
				},
			}},
		}},
	}
}

func newFixture(summary domain.SessionUsageSummary) (*EmittingFinalizer, *fakeInner, *fakeSink) {
	inner := &fakeInner{}
	sink := &fakeSink{}
	store := fakeStore{
		rec:     domain.SessionRecord{ID: "ao-1", ProjectID: "ao", Harness: domain.HarnessClaudeCode},
		recOK:   true,
		project: domain.ProjectRecord{RepoOriginURL: "https://github.com/aoagents/agent-orchestrator.git"},
		projOK:  true,
	}
	scm := fakeSCM{repo: ports.SCMRepo{Provider: "github", Owner: "aoagents"}}
	e := NewEmittingFinalizer(inner, fakeSummary{summary: summary}, store, scm,
		sink, func() time.Time { return time.Unix(1700000000, 0).UTC() })
	return e, inner, sink
}

func TestFinalizeSessionForwardsAndEmits(t *testing.T) {
	e, inner, sink := newFixture(opusSummary(false))
	if err := e.FinalizeSession(context.Background(), "ao-1", "launch-1", time.Unix(0, 0)); err != nil {
		t.Fatalf("FinalizeSession: %v", err)
	}
	if inner.finalizeCalls != 1 {
		t.Fatalf("inner FinalizeSession calls = %d, want 1", inner.finalizeCalls)
	}
	if len(sink.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Name != "ao.session.token_usage" {
		t.Fatalf("event name = %q", ev.Name)
	}
	p := ev.Payload
	if p["github_org"] != "aoagents" {
		t.Fatalf("github_org = %v", p["github_org"])
	}
	if p["model"] != "claude-opus-4-8" || p["harness"] != string(domain.HarnessClaudeCode) {
		t.Fatalf("model/harness = %v / %v", p["model"], p["harness"])
	}
	if p["total_tokens"] != int64(165) {
		t.Fatalf("total_tokens = %v, want 165", p["total_tokens"])
	}
	if p["incomplete"] != false {
		t.Fatalf("incomplete = %v, want false", p["incomplete"])
	}
	// cost = modelCost(opus, 100 in / 50 out / 10 cacheRead / 5 cacheWrite)
	wantCost := round2(modelCost("claude-opus-4-8", opusSummary(false).Harnesses[0].Models[0].Totals))
	if p["est_cost_usd"] != wantCost || wantCost <= 0 {
		t.Fatalf("est_cost_usd = %v, want %v", p["est_cost_usd"], wantCost)
	}
	if ev.SessionID == nil || *ev.SessionID != "ao-1" {
		t.Fatalf("session id = %v", ev.SessionID)
	}
}

func TestFinalizeSessionCarriesIncompleteFlag(t *testing.T) {
	_, _, sink := func() (*EmittingFinalizer, *fakeInner, *fakeSink) {
		e, inner, sink := newFixture(opusSummary(true))
		_ = e.FinalizeSession(context.Background(), "ao-1", "l", time.Unix(0, 0))
		return e, inner, sink
	}()
	if sink.events[0].Payload["incomplete"] != true {
		t.Fatalf("incomplete = %v, want true", sink.events[0].Payload["incomplete"])
	}
}

func TestFinalizeSessionEmptyUsageDoesNotEmit(t *testing.T) {
	e, _, sink := newFixture(domain.SessionUsageSummary{SessionID: "ao-1"})
	if err := e.FinalizeSession(context.Background(), "ao-1", "l", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("empty usage emitted %d events, want 0", len(sink.events))
	}
}

func TestFinalizeSessionReturnsInnerErrorButStillBestEffort(t *testing.T) {
	e, inner, sink := newFixture(opusSummary(false))
	inner.finalizeErr = errors.New("boom")
	err := e.FinalizeSession(context.Background(), "ao-1", "l", time.Unix(0, 0))
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
	// Emission is best-effort and still happens even when finalize errored.
	if len(sink.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(sink.events))
	}
}

func TestReactivateSessionForwards(t *testing.T) {
	e, inner, _ := newFixture(opusSummary(false))
	if err := e.ReactivateSession(context.Background(), "ao-1", "l"); err != nil {
		t.Fatal(err)
	}
	if inner.reactivateCalls != 1 {
		t.Fatalf("inner ReactivateSession calls = %d, want 1", inner.reactivateCalls)
	}
}

func TestGithubOrgOmittedForNonGitHubRemote(t *testing.T) {
	inner := &fakeInner{}
	sink := &fakeSink{}
	store := fakeStore{
		rec:     domain.SessionRecord{ID: "ao-1", ProjectID: "ao", Harness: domain.HarnessClaudeCode},
		recOK:   true,
		project: domain.ProjectRecord{RepoOriginURL: ""},
		projOK:  true,
	}
	e := NewEmittingFinalizer(inner, fakeSummary{summary: opusSummary(false)}, store, fakeSCM{},
		sink, func() time.Time { return time.Unix(1700000000, 0).UTC() })
	_ = e.FinalizeSession(context.Background(), "ao-1", "l", time.Unix(0, 0))
	if _, ok := sink.events[0].Payload["github_org"]; ok {
		t.Fatalf("github_org should be absent for a non-GitHub remote")
	}
}
