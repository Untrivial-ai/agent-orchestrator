package usagetelemetry

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SummaryReader reads a session's aggregated usage. Satisfied by
// *usage.SummaryReader.
type SummaryReader interface {
	Get(ctx context.Context, id domain.SessionID) (domain.SessionUsageSummary, error)
}

// SessionStore resolves the session's harness and its project's remote so a
// usage event can be attributed to a GitHub owner.
type SessionStore interface {
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
}

// SCMParser extracts the owner from a project's remote. Satisfied by the SCM
// adapter's ParseRepository.
type SCMParser interface {
	ParseRepository(remote string) (ports.SCMRepo, bool)
}

// Finalizer is the lifecycle usage-finalizer contract this package decorates.
// It mirrors the interfaces lifecycle.Manager type-asserts for, so the
// decorated value can be passed straight to SetUsageFinalizer.
type Finalizer interface {
	FinalizeSession(ctx context.Context, id domain.SessionID, expectedRuntimeLaunchID string, expectedSessionRevision time.Time) error
	ReactivateSession(ctx context.Context, id domain.SessionID, expectedRuntimeLaunchID string) error
}

// EmittingFinalizer decorates the usage finalizer so that, immediately after a
// session's usage is finalized at termination, a single ao.session.token_usage
// event is emitted with the session's token totals, an estimated dollar cost,
// and the owning GitHub organisation/account. Emission is best-effort: it never
// changes the finalizer's result or blocks the lifecycle transition.
type EmittingFinalizer struct {
	inner     Finalizer
	summary   SummaryReader
	store     SessionStore
	scm       SCMParser
	telemetry ports.EventSink
	now       func() time.Time
}

// NewEmittingFinalizer wraps inner. If any dependency needed to emit is nil the
// decorator still forwards finalizer calls; it simply does not emit.
func NewEmittingFinalizer(inner Finalizer, summary SummaryReader, store SessionStore, scm SCMParser, telemetry ports.EventSink, clock func() time.Time) *EmittingFinalizer {
	if clock == nil {
		clock = time.Now
	}
	return &EmittingFinalizer{inner: inner, summary: summary, store: store, scm: scm, telemetry: telemetry, now: clock}
}

// FinalizeSession forwards to the wrapped finalizer, then emits usage telemetry
// once the totals are as complete as they will be at termination.
func (e *EmittingFinalizer) FinalizeSession(ctx context.Context, id domain.SessionID, expectedRuntimeLaunchID string, expectedSessionRevision time.Time) error {
	err := e.inner.FinalizeSession(ctx, id, expectedRuntimeLaunchID, expectedSessionRevision)
	e.emit(ctx, id)
	return err
}

// ReactivateSession forwards unchanged; reactivation is not a usage boundary.
func (e *EmittingFinalizer) ReactivateSession(ctx context.Context, id domain.SessionID, expectedRuntimeLaunchID string) error {
	return e.inner.ReactivateSession(ctx, id, expectedRuntimeLaunchID)
}

func (e *EmittingFinalizer) emit(ctx context.Context, id domain.SessionID) {
	if e.telemetry == nil || e.summary == nil {
		return
	}
	summary, err := e.summary.Get(ctx, id)
	if err != nil {
		return
	}
	input := deref(summary.Totals.InputTokens)
	output := deref(summary.Totals.OutputTokens)
	cacheRead := deref(summary.Totals.CacheReadTokens)
	cacheWrite := deref(summary.Totals.CacheWriteTokens)
	total := input + output + cacheRead + cacheWrite
	if total == 0 {
		return // nothing ingested for this session; not worth an event.
	}

	model, harness := dominant(summary)
	cost := round2(summaryCost(summary))

	payload := map[string]any{
		"harness":            harness,
		"model":              model,
		"input_tokens":       input,
		"output_tokens":      output,
		"cache_read_tokens":  cacheRead,
		"cache_write_tokens": cacheWrite,
		"total_tokens":       total,
		"est_cost_usd":       cost,
		"incomplete":         summary.Incomplete,
	}
	if org := e.githubOrg(ctx, id); org != "" {
		payload["github_org"] = org
	}

	sessionID := id
	ev := ports.TelemetryEvent{
		Name:       "ao.session.token_usage",
		Source:     "usage_telemetry",
		OccurredAt: e.now().UTC(),
		Level:      ports.TelemetryLevelInfo,
		SessionID:  &sessionID,
	}
	if pid := e.projectID(ctx, id); pid != "" {
		p := domain.ProjectID(pid)
		ev.ProjectID = &p
	}
	ev.Payload = payload
	e.telemetry.Emit(context.Background(), ev)
}

// summaryCost sums the per-model cost across every harness in the summary, so a
// session that switched models or harnesses is priced with each model's own
// rate rather than a single blended one.
func summaryCost(summary domain.SessionUsageSummary) float64 {
	var cost float64
	for _, h := range summary.Harnesses {
		for _, m := range h.Models {
			cost += modelCost(m.ModelID, m.Totals)
		}
	}
	return cost
}

// dominant picks the model (and its harness) that produced the most output
// tokens, used to label the event. Ties break on model id for stability.
func dominant(summary domain.SessionUsageSummary) (model, harness string) {
	var best int64 = -1
	for _, h := range summary.Harnesses {
		for _, m := range h.Models {
			out := deref(m.Totals.OutputTokens)
			if out > best || (out == best && m.ModelID < model) {
				best = out
				model = m.ModelID
				harness = string(h.Harness)
			}
		}
	}
	return model, harness
}

func (e *EmittingFinalizer) projectID(ctx context.Context, id domain.SessionID) string {
	if e.store == nil {
		return ""
	}
	rec, ok, err := e.store.GetSession(ctx, id)
	if err != nil || !ok {
		return ""
	}
	return string(rec.ProjectID)
}

// githubOrg resolves the GitHub owner/org of the session's project remote, or
// "" when unknown or not a GitHub remote. Only the owner segment is used.
func (e *EmittingFinalizer) githubOrg(ctx context.Context, id domain.SessionID) string {
	if e.store == nil || e.scm == nil {
		return ""
	}
	rec, ok, err := e.store.GetSession(ctx, id)
	if err != nil || !ok {
		return ""
	}
	project, ok, err := e.store.GetProject(ctx, string(rec.ProjectID))
	if err != nil || !ok {
		return ""
	}
	repo, ok := e.scm.ParseRepository(project.RepoOriginURL)
	if !ok || repo.Provider != "github" {
		return ""
	}
	return repo.Owner
}
