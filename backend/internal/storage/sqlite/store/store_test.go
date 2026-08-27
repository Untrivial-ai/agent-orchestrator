package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	return sqlitetest.MustOpen(t)
}

func seedProject(t *testing.T, s *sqlite.Store, id string) {
	t.Helper()
	if err := s.UpsertProject(context.Background(), domain.ProjectRecord{
		ID: id, Path: "/tmp/" + id, RegisteredAt: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatalf("seed project %s: %v", id, err)
	}
}

func sampleRecord(project string) domain.SessionRecord {
	now := time.Now().UTC().Truncate(time.Second)
	return domain.SessionRecord{
		ProjectID:        domain.ProjectID(project),
		Kind:             domain.KindWorker,
		Harness:          domain.HarnessClaudeCode,
		Activity:         domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata:         domain.SessionMetadata{Branch: "feat/x", WorkspacePath: "/ws"},
		AutoInjectReview: true,
		AutoInjectCI:     true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// Regression: the sessions.harness CHECK must allow the 'fake' harness (added
// in migration 0024) so fake-driven e2e sessions can be created.
func TestSessionCreateAllowsFakeHarness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec := sampleRecord("mer")
	rec.Harness = domain.HarnessFake
	if _, err := s.CreateSession(ctx, rec); err != nil {
		t.Fatalf("create fake-harness session: %v", err)
	}
}

func TestSessionCreateAllowsPrimeAgentHarness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec := sampleRecord("mer")
	rec.Harness = domain.HarnessPrimeAgent
	if _, err := s.CreateSession(ctx, rec); err != nil {
		t.Fatalf("create prime-agent-harness session: %v", err)
	}
}

func TestSessionPersistsReviewerHarness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetSessionReviewerHarness(ctx, rec.ID, domain.ReviewerCodex, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("set reviewer harness = %v, %v", ok, err)
	}
	got, ok, err := s.GetSession(ctx, rec.ID)
	if err != nil || !ok {
		t.Fatalf("get session = %v, %v", ok, err)
	}
	if got.ReviewerHarness != domain.ReviewerCodex {
		t.Fatalf("reviewer harness = %q, want %q", got.ReviewerHarness, domain.ReviewerCodex)
	}
}

func TestSessionPersistsDiffBaseMetadata(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec := sampleRecord("mer")
	rec.Metadata.DiffBaseSHA = "base-sha-1"
	rec.Metadata.DiffBaseRef = "main"

	created, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, ok, err := s.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if got.Metadata.DiffBaseSHA != "base-sha-1" || got.Metadata.DiffBaseRef != "main" {
		t.Fatalf("created diff base = sha:%q ref:%q", got.Metadata.DiffBaseSHA, got.Metadata.DiffBaseRef)
	}

	got.Metadata.DiffBaseSHA = "base-sha-2"
	got.Metadata.DiffBaseRef = "develop"
	if err := s.UpdateSession(ctx, got); err != nil {
		t.Fatalf("update session: %v", err)
	}
	updated, ok, err := s.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("get updated session: ok=%v err=%v", ok, err)
	}
	if updated.Metadata.DiffBaseSHA != "base-sha-2" || updated.Metadata.DiffBaseRef != "develop" {
		t.Fatalf("updated diff base = sha:%q ref:%q", updated.Metadata.DiffBaseSHA, updated.Metadata.DiffBaseRef)
	}
}

func TestSessionPersistsDeterministicHandoffInputs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "handoff-inputs")
	rec := sampleRecord("handoff-inputs")
	rec.Metadata.LatestUserPrompt = "Please finish the duplicate-listener test."
	rec.Metadata.LatestUserPromptAt = rec.CreatedAt.Add(time.Minute)
	rec.Metadata.LatestAssistantUpdate = "The generation fence is implemented; the test is unfinished."
	rec.Metadata.NativeTranscriptPath = "/ao/transcripts/claude/session.jsonl"
	rec.Metadata.AgentSessionID = "native-session-1"
	rec.Metadata.AgentSessionIDLaunchID = "launch-1"

	created, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, ok, err := s.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if got.Metadata.LatestUserPrompt != rec.Metadata.LatestUserPrompt ||
		!got.Metadata.LatestUserPromptAt.Equal(rec.Metadata.LatestUserPromptAt) ||
		got.Metadata.LatestAssistantUpdate != rec.Metadata.LatestAssistantUpdate ||
		got.Metadata.NativeTranscriptPath != rec.Metadata.NativeTranscriptPath ||
		got.Metadata.AgentSessionIDLaunchID != rec.Metadata.AgentSessionIDLaunchID {
		t.Fatalf("handoff inputs after create = %+v", got.Metadata)
	}

	got.Metadata.LatestUserPrompt = "Now run the focused tests."
	got.Metadata.LatestUserPromptAt = got.Metadata.LatestUserPromptAt.Add(time.Minute)
	got.Metadata.LatestAssistantUpdate = "The regression test has been added."
	got.Metadata.NativeTranscriptPath = "/ao/transcripts/codex/session.jsonl"
	got.Metadata.AgentSessionIDLaunchID = "launch-2"
	got.UpdatedAt = got.UpdatedAt.Add(time.Second)
	if err := s.UpdateSession(ctx, got); err != nil {
		t.Fatalf("update session: %v", err)
	}
	updated, ok, err := s.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("get updated session: ok=%v err=%v", ok, err)
	}
	if updated.Metadata.LatestUserPrompt != got.Metadata.LatestUserPrompt ||
		!updated.Metadata.LatestUserPromptAt.Equal(got.Metadata.LatestUserPromptAt) ||
		updated.Metadata.LatestAssistantUpdate != got.Metadata.LatestAssistantUpdate ||
		updated.Metadata.NativeTranscriptPath != got.Metadata.NativeTranscriptPath ||
		updated.Metadata.AgentSessionIDLaunchID != got.Metadata.AgentSessionIDLaunchID {
		t.Fatalf("handoff inputs after update = %+v", updated.Metadata)
	}
	listed, err := s.ListSessions(ctx, created.ProjectID)
	if err != nil || len(listed) != 1 || listed[0].Metadata.LatestUserPrompt != got.Metadata.LatestUserPrompt ||
		!listed[0].Metadata.LatestUserPromptAt.Equal(got.Metadata.LatestUserPromptAt) ||
		listed[0].Metadata.AgentSessionIDLaunchID != got.Metadata.AgentSessionIDLaunchID {
		t.Fatalf("listed handoff inputs = %+v err=%v", listed, err)
	}
}

func TestRecordSessionLatestUserPromptIsNarrowAndMonotonic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "prompt-fence")
	created, err := s.CreateSession(ctx, sampleRecord("prompt-fence"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	ownerAt := created.UpdatedAt.Add(2 * time.Second)
	created.Harness = domain.HarnessCodex
	created.Metadata.RuntimeLaunchID = "target-generation"
	created.Metadata.LatestAssistantUpdate = "target already owns this row"
	created.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: ownerAt}
	created.UpdatedAt = ownerAt
	if err := s.UpdateSession(ctx, created); err != nil {
		t.Fatalf("record newer target owner: %v", err)
	}

	if changed, err := s.RecordSessionLatestUserPrompt(ctx, created.ID, "stale source prompt", ownerAt.Add(-time.Second)); err != nil || changed {
		t.Fatalf("stale prompt write = changed %v, err %v", changed, err)
	}
	current, ok, err := s.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("get after stale write: ok=%v err=%v", ok, err)
	}
	if current.Harness != domain.HarnessCodex || current.Metadata.RuntimeLaunchID != "target-generation" || current.Metadata.LatestUserPrompt != "" {
		t.Fatalf("stale prompt changed durable owner: %+v", current)
	}

	promptAt := ownerAt.Add(time.Second)
	if changed, err := s.RecordSessionLatestUserPrompt(ctx, created.ID, "continue the target work", promptAt); err != nil || !changed {
		t.Fatalf("fresh prompt write = changed %v, err %v", changed, err)
	}
	current, _, _ = s.GetSession(ctx, created.ID)
	if current.Metadata.LatestUserPrompt != "continue the target work" || !current.Metadata.LatestUserPromptAt.Equal(promptAt) || current.Harness != domain.HarnessCodex ||
		current.Metadata.RuntimeLaunchID != "target-generation" || current.Metadata.LatestAssistantUpdate != "target already owns this row" {
		t.Fatalf("narrow prompt write changed unrelated facts: %+v", current)
	}

	current.IsTerminated = true
	current.UpdatedAt = promptAt.Add(time.Second)
	if err := s.UpdateSession(ctx, current); err != nil {
		t.Fatalf("terminate session: %v", err)
	}
	if changed, err := s.RecordSessionLatestUserPrompt(ctx, created.ID, "must not resurrect", promptAt.Add(2*time.Second)); err != nil || changed {
		t.Fatalf("terminated prompt write = changed %v, err %v", changed, err)
	}
}

// Regression: the sessions.harness CHECK must allow the 'kimchi' harness (added
// in migration 0054) so Kimchi sessions can be created.
func TestSessionCreateAllowsKimchiHarness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec := sampleRecord("mer")
	rec.Harness = domain.HarnessKimchi
	if _, err := s.CreateSession(ctx, rec); err != nil {
		t.Fatalf("create kimchi-harness session: %v", err)
	}
}

func TestSessionPersistsBrowserCapabilityVerifier(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec := sampleRecord("mer")
	rec.Metadata.BrowserCapabilityVerifier = "one-way-verifier"

	created, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, ok, err := s.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if got.Metadata.BrowserCapabilityVerifier != "one-way-verifier" {
		t.Fatalf("created verifier = %q", got.Metadata.BrowserCapabilityVerifier)
	}

	got.Metadata.BrowserCapabilityVerifier = "rotated-verifier"
	if err := s.UpdateSession(ctx, got); err != nil {
		t.Fatalf("update session: %v", err)
	}
	updated, ok, err := s.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("get updated session: ok=%v err=%v", ok, err)
	}
	if updated.Metadata.BrowserCapabilityVerifier != "rotated-verifier" {
		t.Fatalf("updated verifier = %q", updated.Metadata.BrowserCapabilityVerifier)
	}
}

func TestProjectCRUDAndArchive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")

	got, ok, err := s.GetProject(ctx, "mer")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.ID != "mer" || got.Path != "/tmp/mer" {
		t.Fatalf("project = %+v", got)
	}
	if list, _ := s.ListProjects(ctx); len(list) != 1 {
		t.Fatalf("active list = %d, want 1", len(list))
	}
	// archive hides from the active list but still resolves by id.
	if ok, err := s.ArchiveProject(ctx, "mer", time.Now().UTC()); err != nil || !ok {
		t.Fatalf("archive: ok=%v err=%v", ok, err)
	}
	if list, _ := s.ListProjects(ctx); len(list) != 0 {
		t.Fatalf("after archive, active list = %d, want 0", len(list))
	}
	if _, ok, _ := s.GetProject(ctx, "mer"); !ok {
		t.Fatal("archived project must still resolve by id")
	}
}

func TestImportWorkspaceProjectConflictsWithArchivedSameID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archivedAt := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertWorkspaceProject(ctx, domain.ProjectRecord{
		ID: "alpha", Path: "/tmp/target", RegisteredAt: time.Now().UTC().Truncate(time.Second),
	}, nil); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if ok, err := s.ArchiveProject(ctx, "alpha", archivedAt); err != nil || !ok {
		t.Fatalf("archive: ok=%v err=%v", ok, err)
	}

	err := s.ImportWorkspaceProject(ctx, domain.ProjectRecord{
		ID: "alpha", Path: "/tmp/source", RegisteredAt: time.Now().UTC().Truncate(time.Second),
	}, nil)
	var conflict *domain.ProjectImportConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want project import conflict", err)
	}
	if conflict.Conflict.Reason != domain.ProjectImportConflictSameIDArchivedTarget || conflict.Conflict.TargetPath != "/tmp/target" {
		t.Fatalf("conflict = %#v", conflict.Conflict)
	}
	got, ok, err := s.GetProject(ctx, "alpha")
	if err != nil || !ok {
		t.Fatalf("get project: ok=%v err=%v", ok, err)
	}
	if got.ArchivedAt.IsZero() || got.Path != "/tmp/target" {
		t.Fatalf("project = %+v, want archived target preserved", got)
	}
}

func TestProjectScratchKindAndArchivedCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	count, err := s.CountProjectsIncludingArchived(ctx)
	if err != nil {
		t.Fatalf("count initial: %v", err)
	}
	if count != 0 {
		t.Fatalf("initial project count = %d, want 0", count)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertProject(ctx, domain.ProjectRecord{
		ID:           "scratch",
		DisplayName:  "Scratch",
		Path:         "/ao/scratch/default",
		Kind:         domain.ProjectKindScratch,
		RegisteredAt: now,
	}); err != nil {
		t.Fatalf("upsert scratch: %v", err)
	}

	got, ok, err := s.GetProject(ctx, "scratch")
	if err != nil || !ok {
		t.Fatalf("get scratch: ok=%v err=%v", ok, err)
	}
	if got.Kind != domain.ProjectKindScratch {
		t.Fatalf("kind = %q, want scratch", got.Kind)
	}

	if ok, err := s.ArchiveProject(ctx, "scratch", now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("archive scratch: ok=%v err=%v", ok, err)
	}
	if list, err := s.ListProjects(ctx); err != nil || len(list) != 0 {
		t.Fatalf("active projects = %#v, %v; want empty", list, err)
	}
	count, err = s.CountProjectsIncludingArchived(ctx)
	if err != nil {
		t.Fatalf("count after archive: %v", err)
	}
	if count != 1 {
		t.Fatalf("project count including archived = %d, want 1", count)
	}
}

func TestProjectConfigRoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// A config with mixed field kinds (scalar, map, list, nested) survives the
	// JSON round trip.
	cfg := domain.ProjectConfig{
		DefaultBranch:     "develop",
		Env:               map[string]string{"FOO": "bar"},
		Symlinks:          []string{".env"},
		PostCreate:        []string{"echo hi"},
		AgentRules:        "Run focused tests.",
		AgentRulesFile:    "docs/agent-rules.md",
		OrchestratorRules: "Keep workers unblocked.",
		AgentConfig:       domain.AgentConfig{Model: "claude-opus-4-5", Permissions: domain.PermissionModeAcceptEdits},
		Worker:            domain.RoleOverride{Harness: domain.HarnessCodex},
	}
	if err := s.UpsertProject(ctx, domain.ProjectRecord{
		ID: "cfg", Path: "/tmp/cfg", RegisteredAt: now, Config: cfg,
	}); err != nil {
		t.Fatalf("upsert with config: %v", err)
	}
	got, ok, err := s.GetProject(ctx, "cfg")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got.Config, cfg) {
		t.Fatalf("config = %#v, want %#v", got.Config, cfg)
	}

	// An unset config round-trips back to a zero value rather than an empty object.
	seedProject(t, s, "nocfg")
	got, _, _ = s.GetProject(ctx, "nocfg")
	if !got.Config.IsZero() {
		t.Fatalf("unset config = %#v, want zero", got.Config)
	}

	// Clearing replaces a previously-set config with a zero value.
	if err := s.UpsertProject(ctx, domain.ProjectRecord{
		ID: "cfg", Path: "/tmp/cfg", RegisteredAt: now, Config: domain.ProjectConfig{},
	}); err != nil {
		t.Fatalf("clear config: %v", err)
	}
	if got, _, _ := s.GetProject(ctx, "cfg"); !got.Config.IsZero() {
		t.Fatalf("cleared config = %#v, want zero", got.Config)
	}
}

func TestSessionCreateAssignsPerProjectID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	seedProject(t, s, "ao")

	r1, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := s.CreateSession(ctx, sampleRecord("mer"))
	r3, _ := s.CreateSession(ctx, sampleRecord("ao"))
	if r1.ID != "mer-1" || r2.ID != "mer-2" || r3.ID != "ao-1" {
		t.Fatalf("ids = %s, %s, %s; want mer-1, mer-2, ao-1", r1.ID, r2.ID, r3.ID)
	}
	got, ok, err := s.GetSession(ctx, "mer-1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Activity.State != domain.ActivityActive || got.IsTerminated ||
		got.Harness != domain.HarnessClaudeCode || got.Metadata.Branch != "feat/x" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if list, _ := s.ListSessions(ctx, "mer"); len(list) != 2 {
		t.Fatalf("list mer = %d, want 2", len(list))
	}
	if all, _ := s.ListAllSessions(ctx); len(all) != 3 {
		t.Fatalf("list all = %d, want 3", len(all))
	}
}

// TestDeleteSessionOnlyRemovesSeedRows covers Bug 4's storage-layer guarantee:
// DeleteSession removes a session row only when the row is still in seed state
// (no workspace, no runtime handle, no agent session id, no prompt, not
// terminated). Rows that already carry spawn output are immutable so the
// no-resurrection guarantee for live sessions still holds.
func TestDeleteSessionOnlyRemovesSeedRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")

	now := time.Now().UTC().Truncate(time.Second)
	seed := domain.SessionRecord{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessClaudeCode,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	}
	tests := []struct {
		name        string
		record      domain.SessionRecord
		prepare     func(*domain.SessionRecord)
		wantDeleted bool
		wantPresent bool
	}{
		{
			name:        "seed row",
			record:      seed,
			wantDeleted: true,
		},
		{
			name:        "spawn output",
			record:      sampleRecord("mer"),
			wantPresent: true,
		},
		{
			name:   "terminated row",
			record: seed,
			prepare: func(rec *domain.SessionRecord) {
				rec.IsTerminated = true
			},
			wantPresent: true,
		},
		{
			name:   "recorded user interaction",
			record: sampleRecord("mer"),
			prepare: func(rec *domain.SessionRecord) {
				// Keep the legacy seed predicates empty; the durable interaction
				// fact alone is observable progress and must prevent deletion.
				rec.Metadata.WorkspacePath = ""
				rec.Metadata.LatestUserPrompt = "Continue the task."
			},
			wantPresent: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := s.CreateSession(ctx, tt.record)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			if tt.prepare != nil {
				tt.prepare(&created)
				if err := s.UpdateSession(ctx, created); err != nil {
					t.Fatalf("prepare session: %v", err)
				}
			}
			deleted, err := s.DeleteSession(ctx, created.ID)
			if err != nil {
				t.Fatalf("delete session: %v", err)
			}
			if deleted != tt.wantDeleted {
				t.Fatalf("DeleteSession deleted=%v, want %v", deleted, tt.wantDeleted)
			}
			if _, ok, err := s.GetSession(ctx, created.ID); err != nil || ok != tt.wantPresent {
				t.Fatalf("session after DeleteSession: ok=%v err=%v, want present=%v", ok, err, tt.wantPresent)
			}
		})
	}
}

func TestSessionRenameUpdatesDisplayName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	renamedAt := r.UpdatedAt.Add(time.Minute)
	ok, err := s.RenameSession(ctx, r.ID, "Fix flaky tests", renamedAt)
	if err != nil || !ok {
		t.Fatalf("rename: ok=%v err=%v", ok, err)
	}
	got, _, _ := s.GetSession(ctx, r.ID)
	if got.DisplayName != "Fix flaky tests" || !got.UpdatedAt.Equal(renamedAt) {
		t.Fatalf("rename not persisted: %+v", got)
	}

	ok, err = s.RenameSession(ctx, "mer-missing", "Missing", renamedAt)
	if err != nil {
		t.Fatalf("rename missing: %v", err)
	}
	if ok {
		t.Fatal("rename missing ok=true, want false")
	}
}

func TestSessionTerminateOnPRMergePolicyRoundTripAndCDC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	base, _ := s.LatestSeq(ctx)
	updatedAt := r.UpdatedAt.Add(time.Minute)
	ok, err := s.SetSessionTerminateOnPRMerge(ctx, r.ID, true, updatedAt)
	if err != nil || !ok {
		t.Fatalf("enable terminate-on-merge: ok=%v err=%v", ok, err)
	}
	got, found, err := s.GetSession(ctx, r.ID)
	if err != nil || !found {
		t.Fatalf("get session: found=%v err=%v", found, err)
	}
	if !got.TerminateOnPRMerge || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("policy not persisted: %+v", got)
	}

	evs, err := s.EventsAfter(ctx, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || string(evs[0].Type) != "session_updated" {
		t.Fatalf("policy change events = %+v, want one session_updated", evs)
	}
	var payload map[string]any
	if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := payload["terminateOnPrMerge"].(bool); !ok || !enabled {
		t.Fatalf("terminateOnPrMerge payload = %#v, want true", payload["terminateOnPrMerge"])
	}

	ok, err = s.SetSessionTerminateOnPRMerge(ctx, "mer-missing", true, updatedAt)
	if err != nil || ok {
		t.Fatalf("missing policy update: ok=%v err=%v", ok, err)
	}
}

func TestSessionAutoInjectReviewPolicyRoundTripAndCDC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	base, _ := s.LatestSeq(ctx)
	updatedAt := r.UpdatedAt.Add(time.Minute)
	ok, err := s.SetSessionAutoInjectReview(ctx, r.ID, false, updatedAt)
	if err != nil || !ok {
		t.Fatalf("disable automatic review injection: ok=%v err=%v", ok, err)
	}
	got, found, err := s.GetSession(ctx, r.ID)
	if err != nil || !found {
		t.Fatalf("get session: found=%v err=%v", found, err)
	}
	if got.AutoInjectReview || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("policy not persisted: %+v", got)
	}

	evs, err := s.EventsAfter(ctx, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || string(evs[0].Type) != "session_updated" {
		t.Fatalf("policy change events = %+v, want one session_updated", evs)
	}
	var payload map[string]any
	if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := payload["autoInjectReview"].(bool); !ok || enabled {
		t.Fatalf("autoInjectReview payload = %#v, want false", payload["autoInjectReview"])
	}

	ok, err = s.SetSessionAutoInjectReview(ctx, "mer-missing", false, updatedAt)
	if err != nil || ok {
		t.Fatalf("missing policy update: ok=%v err=%v", ok, err)
	}
}

func TestSessionAutoReviewPolicyRoundTripAndCDC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	base, _ := s.LatestSeq(ctx)
	updatedAt := r.UpdatedAt.Add(time.Minute)
	ok, err := s.SetSessionAutoReview(ctx, r.ID, true, updatedAt)
	if err != nil || !ok {
		t.Fatalf("enable auto review: ok=%v err=%v", ok, err)
	}
	got, found, err := s.GetSession(ctx, r.ID)
	if err != nil || !found {
		t.Fatalf("get session: found=%v err=%v", found, err)
	}
	if !got.AutoReviewEnabled || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("auto review policy not persisted: %+v", got)
	}

	evs, err := s.EventsAfter(ctx, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || string(evs[0].Type) != "session_updated" {
		t.Fatalf("auto review policy events = %+v, want one session_updated", evs)
	}
	var payload map[string]any
	if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := payload["autoReviewEnabled"].(bool); !ok || !enabled {
		t.Fatalf("autoReviewEnabled payload = %#v, want true", payload["autoReviewEnabled"])
	}
}

func TestSessionRuntimeLaunchIDRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec := sampleRecord("mer")
	rec.Metadata.RuntimeHandleID = "tmux-1"
	rec.Metadata.RuntimeLaunchID = "launch-1"

	created, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if created.Metadata.RuntimeLaunchID != "launch-1" {
		t.Fatalf("created launch id = %q", created.Metadata.RuntimeLaunchID)
	}
	created.Metadata.RuntimeLaunchID = "launch-2"
	if err := s.UpdateSession(ctx, created); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetSession(ctx, created.ID)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.Metadata.RuntimeLaunchID != "launch-2" {
		t.Fatalf("updated launch id = %q", got.Metadata.RuntimeLaunchID)
	}
	listed, err := s.ListSessions(ctx, "mer")
	if err != nil || len(listed) != 1 || listed[0].Metadata.RuntimeLaunchID != "launch-2" {
		t.Fatalf("listed sessions = %+v err=%v", listed, err)
	}
}

func TestSessionUpdateActivityAndTermination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	r.Activity = domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: r.CreatedAt}
	r.IsTerminated = true
	if err := s.UpdateSession(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetSession(ctx, r.ID)
	if got.Activity.State != domain.ActivityWaitingInput || !got.IsTerminated {
		t.Fatalf("update not persisted: %+v", got)
	}

	got.IsTerminated = false
	got.Activity.State = domain.ActivityActive
	_ = s.UpdateSession(ctx, got)
	again, _, _ := s.GetSession(ctx, r.ID)
	if again.IsTerminated || again.Activity.State != domain.ActivityActive {
		t.Fatalf("activity/termination should update, got %+v", again)
	}
}

func TestSessionFirstSignalRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	// Fresh sessions have no signal receipt: NULL round-trips as zero time.
	got, _, _ := s.GetSession(ctx, r.ID)
	if !got.FirstSignalAt.IsZero() {
		t.Fatalf("fresh session has receipt: %v", got.FirstSignalAt)
	}

	stamp := time.Now().UTC().Truncate(time.Second)
	got.FirstSignalAt = stamp
	if err := s.UpdateSession(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _, _ := s.GetSession(ctx, r.ID)
	if !again.FirstSignalAt.Equal(stamp) {
		t.Fatalf("receipt not persisted: got %v want %v", again.FirstSignalAt, stamp)
	}

	// Clearing it (spawn/restore re-proves the hook pipeline) round-trips too.
	again.FirstSignalAt = time.Time{}
	if err := s.UpdateSession(ctx, again); err != nil {
		t.Fatal(err)
	}
	final, _, _ := s.GetSession(ctx, r.ID)
	if !final.FirstSignalAt.IsZero() {
		t.Fatalf("receipt not cleared: %v", final.FirstSignalAt)
	}
}

func TestPRCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	pr := domain.PullRequest{
		URL: "https://gh/pr/1", SessionID: r.ID, Number: 1,
		Review: domain.ReviewRequired, CI: domain.CIFailing, Mergeability: domain.MergeBlocked, UpdatedAt: now, StateChangedAt: now,
		AutoInjectCI: true,
	}
	if err := s.WritePR(ctx, pr, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetPR(ctx, pr.URL)
	if err != nil || !ok || got != pr {
		t.Fatalf("get pr: ok=%v err=%v got=%+v", ok, err, got)
	}
	if list, _ := s.ListPRsBySession(ctx, r.ID); len(list) != 1 {
		t.Fatalf("list prs = %d, want 1", len(list))
	}
}

func TestPRAutoInjectCITracksSessionPolicyChanges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	owner, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)
	first := domain.PullRequest{URL: "https://github.com/o/r/pull/1", SessionID: owner.ID, Number: 1, CI: domain.CIFailing, UpdatedAt: now}

	if err := s.WritePR(ctx, first, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetPR(ctx, first.URL)
	if err != nil || !found || !got.AutoInjectCI {
		t.Fatalf("new PR policy = found:%v err:%v value:%v, want enabled", found, err, got.AutoInjectCI)
	}

	base, _ := s.LatestSeq(ctx)
	changedAt := now.Add(time.Minute)
	ok, err := s.SetSessionAutoInjectCI(ctx, owner.ID, false, changedAt)
	if err != nil || !ok {
		t.Fatalf("disable session CI policy: ok=%v err=%v", ok, err)
	}
	session, found, err := s.GetSession(ctx, owner.ID)
	if err != nil || !found || session.AutoInjectCI {
		t.Fatalf("disabled session policy = found:%v err:%v session:%+v", found, err, session)
	}

	events, err := s.EventsAfter(ctx, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("policy change events = %+v, want session and PR updates", events)
	}
	var sessionPayload []byte
	for i := range events {
		if string(events[i].Type) == "session_updated" {
			sessionPayload = events[i].Payload
			break
		}
	}
	if sessionPayload == nil {
		t.Fatalf("policy change events = %+v, want session_updated", events)
	}
	var payload map[string]any
	if err := json.Unmarshal(sessionPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := payload["autoInjectCI"].(bool); !ok || enabled {
		t.Fatalf("autoInjectCI payload = %#v, want false", payload["autoInjectCI"])
	}

	// Re-observing the first PR after changing the session policy must preserve
	// the currently selected policy.
	first.UpdatedAt = changedAt.Add(time.Minute)
	if err := s.WritePR(ctx, first, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetPR(ctx, first.URL)
	if got.AutoInjectCI {
		t.Fatal("session toggle did not rewrite the first PR's enabled policy")
	}

	second := domain.PullRequest{URL: "https://github.com/o/r/pull/2", SessionID: owner.ID, Number: 2, UpdatedAt: changedAt}
	if err := s.WritePR(ctx, second, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetPR(ctx, second.URL)
	if got.AutoInjectCI {
		t.Fatal("PR created while disabled did not inherit the disabled session policy")
	}

	if ok, err := s.SetSessionAutoInjectCI(ctx, owner.ID, true, changedAt.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("enable session CI policy: ok=%v err=%v", ok, err)
	}
	second.UpdatedAt = changedAt.Add(2 * time.Minute)
	if err := s.WritePR(ctx, second, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetPR(ctx, second.URL)
	if !got.AutoInjectCI {
		t.Fatal("later session enable did not rewrite the second PR's disabled policy")
	}
}

func TestWritePRRejectsSessionReassignment(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	first, _ := s.CreateSession(ctx, sampleRecord("mer"))
	second, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	pr := domain.PullRequest{URL: "https://gh/pr/1", SessionID: first.ID, Number: 1, UpdatedAt: now}
	if err := s.WritePR(ctx, pr, nil, nil); err != nil {
		t.Fatal(err)
	}
	pr.SessionID = second.ID
	if err := s.WritePR(ctx, pr, nil, nil); err == nil {
		t.Fatal("expected reassignment to fail")
	}
	got, ok, err := s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("get pr: ok=%v err=%v", ok, err)
	}
	if got.SessionID != first.ID {
		t.Fatalf("pr moved to %s, want %s", got.SessionID, first.ID)
	}
}

func TestDisplayPRFactsPrefersActivePR(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.WritePR(ctx, domain.PullRequest{URL: "closed", SessionID: r.ID, Number: 1, Closed: true, UpdatedAt: now.Add(time.Minute)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePR(ctx, domain.PullRequest{URL: "open", SessionID: r.ID, Number: 2, CI: domain.CIFailing, UpdatedAt: now}, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetDisplayPRFactsForSession(ctx, r.ID)
	if err != nil || !ok {
		t.Fatalf("display pr: ok=%v err=%v", ok, err)
	}
	if got.URL != "open" || got.CI != domain.CIFailing {
		t.Fatalf("display pr = %+v", got)
	}
}

func TestPRCommentsReplace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)
	_ = s.WritePR(ctx, domain.PullRequest{URL: "pr1", SessionID: r.ID, UpdatedAt: now}, nil, []domain.PullRequestComment{
		{ID: "c1", Author: "a", File: "a.go", Line: 1, Body: "nit", CreatedAt: now},
		{ID: "c2", Author: "b", File: "b.go", Line: 2, Body: "bug", Resolved: true, CreatedAt: now.Add(time.Second)},
	})
	if list, _ := s.ListPRComments(ctx, "pr1"); len(list) != 2 {
		t.Fatalf("comments = %d, want 2", len(list))
	}
	// replace with a smaller set drops the rest.
	_ = s.WritePR(ctx, domain.PullRequest{URL: "pr1", SessionID: r.ID, UpdatedAt: now}, nil, []domain.PullRequestComment{{ID: "c1", Body: "x", CreatedAt: now}})
	if list, _ := s.ListPRComments(ctx, "pr1"); len(list) != 1 {
		t.Fatalf("after replace, comments = %d, want 1", len(list))
	}
}

func TestMarkPRCommentResolved(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)
	pr := domain.PullRequest{URL: "pr1", SessionID: r.ID, UpdatedAt: now}
	if err := s.WritePR(ctx, pr, nil, []domain.PullRequestComment{
		{ID: "c1", Author: "a", Body: "nit", URL: "comment-1", CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := s.MarkPRCommentResolved(ctx, "pr1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("MarkPRCommentResolved updated = false, want true")
	}
	comments, err := s.ListPRComments(ctx, "pr1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !comments[0].Resolved {
		t.Fatalf("comments = %+v, want c1 resolved", comments)
	}

	updated, err = s.MarkPRCommentResolved(ctx, "pr1", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("MarkPRCommentResolved missing updated = true, want false")
	}
}

func TestWriteSCMObservationPersistsMetadataChecksReviewsAndComments(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	pr := domain.PullRequest{
		URL: "https://github.com/o/r/pull/1", SessionID: r.ID, Number: 1,
		Provider: "github", Host: "github.com", Repo: "o/r",
		SourceBranch: "feat/75", TargetBranch: "main", HeadSHA: "h1",
		Title: "SCM observer", Additions: 10, Deletions: 2, ChangedFiles: 3,
		Author: "dev", BaseSHA: "b1", MergeCommitSHA: "m1",
		ProviderState: "OPEN", ProviderMergeable: "MERGEABLE", ProviderMergeStateStatus: "CLEAN",
		HTMLURL: "https://github.com/o/r/pull/1",
		CI:      domain.CIFailing, Review: domain.ReviewChangesRequest, Mergeability: domain.MergeBlocked,
		MetadataHash: "mh", CIHash: "ch", ReviewHash: "rh",
		UpdatedAt: now, ObservedAt: now, CIObservedAt: now, ReviewObservedAt: now,
	}
	checks := []domain.PullRequestCheck{{Name: "build", CommitHash: "h1", Status: domain.PRCheckFailed, Conclusion: "failure", URL: "ci", Details: "99", LogTail: "boom", CreatedAt: now}}
	reviews := []domain.PullRequestReview{{ID: "review-1", Author: "reviewer", State: domain.ReviewChangesRequest, URL: "https://github.com/o/r/pull/1#pullrequestreview-1", Body: "please fix the nil check", TargetSHA: "h1", SubmittedAt: now, AutoInjectReview: false}}
	threads := []domain.PullRequestReviewThread{{ThreadID: "t1", Path: "main.go", Line: 7, SemanticHash: "th", UpdatedAt: now}}
	comments := []domain.PullRequestComment{{ThreadID: "t1", ReviewID: "4876751117", ID: "c1", Author: "reviewer", File: "main.go", Line: 7, Body: "fix", URL: "comment", CreatedAt: now, AutoInjectReview: false}}

	if err := s.WriteSCMObservation(ctx, pr, checks, reviews, threads, comments, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("get pr: ok=%v err=%v", ok, err)
	}
	if got.Provider != "github" || got.HeadSHA != "h1" || got.MetadataHash != "mh" || got.CIHash != "ch" || got.ReviewHash != "rh" {
		t.Fatalf("SCM metadata not persisted: %+v", got)
	}
	gotChecks, _ := s.ListChecks(ctx, pr.URL)
	if len(gotChecks) != 1 || gotChecks[0].Conclusion != "failure" || gotChecks[0].Details != "99" || gotChecks[0].LogTail != "boom" {
		t.Fatalf("checks not persisted: %+v", gotChecks)
	}
	gotThreads, _ := s.ListPRReviewThreads(ctx, pr.URL)
	if len(gotThreads) != 1 || gotThreads[0].ThreadID != "t1" || gotThreads[0].SemanticHash != "th" {
		t.Fatalf("threads not persisted: %+v", gotThreads)
	}
	gotReviews, _ := s.ListPRReviews(ctx, pr.URL)
	if len(gotReviews) != 1 || gotReviews[0].ID != "review-1" || gotReviews[0].URL != "https://github.com/o/r/pull/1#pullrequestreview-1" || gotReviews[0].Body != "please fix the nil check" || gotReviews[0].TargetSHA != "h1" || gotReviews[0].AutoInjectReview {
		t.Fatalf("reviews not persisted: %+v", gotReviews)
	}
	gotComments, _ := s.ListPRComments(ctx, pr.URL)
	if len(gotComments) != 1 || gotComments[0].ThreadID != "t1" || gotComments[0].ReviewID != "4876751117" || gotComments[0].URL != "comment" || gotComments[0].AutoInjectReview {
		t.Fatalf("comments not persisted: %+v", gotComments)
	}
}

func TestUpsertPRReviewUpdatesBody(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	pr := domain.PullRequest{
		URL: "https://github.com/o/r/pull/9", SessionID: r.ID, Number: 9,
		Provider: "github", Host: "github.com", Repo: "o/r",
		SourceBranch: "feat/body", TargetBranch: "main", HeadSHA: "h1",
		Title: "review body", UpdatedAt: now, ObservedAt: now,
	}
	review := domain.PullRequestReview{ID: "review-1", Author: "reviewer", State: domain.ReviewChangesRequest, URL: "https://github.com/o/r/pull/9#pullrequestreview-1", Body: "first pass", SubmittedAt: now, AutoInjectReview: false}

	if err := s.WriteSCMObservation(ctx, pr, nil, []domain.PullRequestReview{review}, nil, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListPRReviews(ctx, pr.URL); len(got) != 1 || got[0].Body != "first pass" {
		t.Fatalf("initial body not persisted: %+v", got)
	}

	// A re-observation of the same review id must overwrite the stored body.
	review.State = domain.ReviewApproved
	review.Body = "resolved, approving"
	review.AutoInjectReview = true
	if err := s.WriteSCMObservation(ctx, pr, nil, []domain.PullRequestReview{review}, nil, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListPRReviews(ctx, pr.URL)
	if len(got) != 1 || got[0].Body != "resolved, approving" || got[0].State != domain.ReviewApproved || got[0].AutoInjectReview {
		t.Fatalf("body/state not updated on upsert: %+v", got)
	}
}

func TestUpsertPRCommentPreservesOriginalInjectionDecisionPerComment(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)
	pr := domain.PullRequest{URL: "https://github.com/o/r/pull/10", SessionID: r.ID, Number: 10, UpdatedAt: now}
	threads := []domain.PullRequestReviewThread{{ThreadID: "t1", UpdatedAt: now}}

	first := domain.PullRequestComment{ThreadID: "t1", ID: "c1", Author: "alice", Body: "first", CreatedAt: now, AutoInjectReview: false}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, threads, []domain.PullRequestComment{first}, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}

	first.Body = "edited"
	first.AutoInjectReview = true
	second := domain.PullRequestComment{ThreadID: "t1", ID: "c2", Author: "alice", Body: "second", CreatedAt: now.Add(time.Second), AutoInjectReview: true}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, threads, []domain.PullRequestComment{first, second}, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}

	comments, err := s.ListPRComments(ctx, pr.URL)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.PullRequestComment{}
	for _, comment := range comments {
		byID[comment.ID] = comment
	}
	if len(comments) != 2 || byID["c1"].AutoInjectReview || !byID["c2"].AutoInjectReview {
		t.Fatalf("comments = %+v, want c1=false preserved and c2=true", comments)
	}
	if byID["c1"].Body != "edited" {
		t.Fatalf("existing comment body was not refreshed: %+v", byID["c1"])
	}
}

func TestWriteSCMObservationMergeUpdatesFetchedReviewWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)
	pr := domain.PullRequest{URL: "https://github.com/o/r/pull/1", SessionID: r.ID, Number: 1, UpdatedAt: now}

	initialThreads := []domain.PullRequestReviewThread{
		{ThreadID: "older", Path: "old.go", Line: 1, Resolved: false, SemanticHash: "old", UpdatedAt: now},
		{ThreadID: "latest", Path: "main.go", Line: 7, Resolved: false, SemanticHash: "latest-v1", UpdatedAt: now},
	}
	initialComments := []domain.PullRequestComment{
		{ThreadID: "older", ID: "older-c1", Author: "ann", Body: "old", CreatedAt: now},
		{ThreadID: "latest", ID: "latest-c1", Author: "bob", Body: "before", CreatedAt: now},
	}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, initialThreads, initialComments, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}

	mergedThreads := []domain.PullRequestReviewThread{
		{ThreadID: "latest", Path: "main.go", Line: 8, Resolved: true, SemanticHash: "latest-v2", UpdatedAt: now.Add(time.Second)},
		{ThreadID: "new", Path: "new.go", Line: 2, Resolved: false, SemanticHash: "new", UpdatedAt: now.Add(time.Second)},
	}
	mergedComments := []domain.PullRequestComment{
		{ThreadID: "latest", ID: "latest-c2", Author: "bob", Body: "after", CreatedAt: now.Add(time.Second)},
		{ThreadID: "new", ID: "new-c1", Author: "cat", Body: "new", CreatedAt: now.Add(time.Second)},
	}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, mergedThreads, mergedComments, ports.ReviewWriteMerge); err != nil {
		t.Fatal(err)
	}

	gotThreads, err := s.ListPRReviewThreads(ctx, pr.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotThreads) != 3 {
		t.Fatalf("threads = %#v, want older preserved plus latest/new", gotThreads)
	}
	byThread := map[string]domain.PullRequestReviewThread{}
	for _, th := range gotThreads {
		byThread[th.ThreadID] = th
	}
	if byThread["older"].SemanticHash != "old" {
		t.Fatalf("older thread not preserved: %#v", byThread["older"])
	}
	if byThread["latest"].SemanticHash != "latest-v2" || !byThread["latest"].Resolved || byThread["latest"].Line != 8 {
		t.Fatalf("latest thread not updated: %#v", byThread["latest"])
	}
	if byThread["new"].SemanticHash != "new" {
		t.Fatalf("new thread not inserted: %#v", byThread["new"])
	}

	gotComments, err := s.ListPRComments(ctx, pr.URL)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, c := range gotComments {
		ids[c.ID] = true
	}
	if !ids["older-c1"] || !ids["latest-c2"] || !ids["new-c1"] {
		t.Fatalf("comments after merge = %#v, want older preserved and fetched threads replaced", gotComments)
	}
	if ids["latest-c1"] {
		t.Fatalf("stale fetched-thread comment was preserved: %#v", gotComments)
	}
}

func TestWritePRPreservesSCMReviewThreads(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)
	observedAt := now.Add(-time.Minute)
	pr := domain.PullRequest{
		URL: "https://github.com/o/r/pull/1", SessionID: r.ID, Number: 1, UpdatedAt: now,
		Provider: "github", Host: "github.com", Repo: "o/r", HeadSHA: "head-1",
		MetadataHash: "metadata-v1", CIHash: "ci-v1", ReviewHash: "review-v1",
		ObservedAt: observedAt, CIObservedAt: observedAt, ReviewObservedAt: observedAt,
	}
	threads := []domain.PullRequestReviewThread{{ThreadID: "t1", Path: "main.go", Line: 7, SemanticHash: "thread-v1", UpdatedAt: now}}
	comments := []domain.PullRequestComment{{ThreadID: "t1", ID: "c1", Author: "reviewer", Body: "scm", URL: "https://example/comment/c1", CreatedAt: now}}

	if err := s.WriteSCMObservation(ctx, pr, nil, nil, threads, comments, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	legacyComments := []domain.PullRequestComment{
		{ID: "c1", Author: "legacy", Body: "duplicate legacy row must not clear thread metadata", CreatedAt: now.Add(time.Second)},
		{ID: "legacy-only", Author: "legacy", Body: "legacy", CreatedAt: now.Add(time.Second)},
	}
	if err := s.WritePR(ctx, domain.PullRequest{URL: pr.URL, SessionID: r.ID, Number: 1, CI: domain.CIPassing, UpdatedAt: now.Add(time.Second)}, nil, legacyComments); err != nil {
		t.Fatal(err)
	}

	gotPR, ok, err := s.GetPR(ctx, pr.URL)
	if err != nil || !ok {
		t.Fatalf("get pr: ok=%v err=%v", ok, err)
	}
	if gotPR.Provider != "github" || gotPR.Host != "github.com" || gotPR.Repo != "o/r" || gotPR.HeadSHA != "head-1" ||
		gotPR.MetadataHash != "metadata-v1" || gotPR.CIHash != "ci-v1" || gotPR.ReviewHash != "review-v1" {
		t.Fatalf("legacy WritePR must preserve SCM-owned metadata and hashes, got %+v", gotPR)
	}
	if !gotPR.ObservedAt.Equal(observedAt) || !gotPR.CIObservedAt.Equal(observedAt) || !gotPR.ReviewObservedAt.Equal(observedAt) {
		t.Fatalf("legacy WritePR must preserve SCM observation timestamps, got observed=%s ci=%s review=%s", gotPR.ObservedAt, gotPR.CIObservedAt, gotPR.ReviewObservedAt)
	}
	if gotPR.CI != domain.CIPassing {
		t.Fatalf("legacy WritePR should still update legacy scalar CI state, got %s", gotPR.CI)
	}
	gotThreads, err := s.ListPRReviewThreads(ctx, pr.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotThreads) != 1 || gotThreads[0].ThreadID != "t1" || gotThreads[0].SemanticHash != "thread-v1" {
		t.Fatalf("legacy WritePR must preserve SCM-owned review threads, got %+v", gotThreads)
	}
	gotComments, err := s.ListPRComments(ctx, pr.URL)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.PullRequestComment{}
	for _, c := range gotComments {
		byID[c.ID] = c
	}
	scmComment, ok := byID["c1"]
	if !ok || scmComment.ThreadID != "t1" || scmComment.URL != "https://example/comment/c1" || scmComment.Body != "scm" {
		t.Fatalf("legacy WritePR must not clear SCM comment metadata, got %+v", scmComment)
	}
	legacyOnly, ok := byID["legacy-only"]
	if !ok || legacyOnly.ThreadID != "" {
		t.Fatalf("legacy-only comment should remain unthreaded, got %+v", legacyOnly)
	}

	mergedThreads := []domain.PullRequestReviewThread{{ThreadID: "t1", Path: "main.go", Line: 8, Resolved: true, SemanticHash: "thread-v2", UpdatedAt: now.Add(2 * time.Second)}}
	mergedComments := []domain.PullRequestComment{{ThreadID: "t1", ID: "c2", Author: "reviewer", Body: "updated", URL: "https://example/comment/c2", CreatedAt: now.Add(2 * time.Second)}}
	if err := s.WriteSCMObservation(ctx, pr, nil, nil, mergedThreads, mergedComments, ports.ReviewWriteMerge); err != nil {
		t.Fatal(err)
	}
	gotComments, err = s.ListPRComments(ctx, pr.URL)
	if err != nil {
		t.Fatal(err)
	}
	byID = map[string]domain.PullRequestComment{}
	for _, c := range gotComments {
		byID[c.ID] = c
	}
	if _, ok := byID["c1"]; ok {
		t.Fatalf("SCM merge should delete stale fetched-thread comment c1, comments=%+v", gotComments)
	}
	replacement, ok := byID["c2"]
	if !ok || replacement.ThreadID != "t1" {
		t.Fatalf("SCM merge did not insert replacement threaded comment, comments=%+v", gotComments)
	}
}

func TestWritePRReplacesLegacyCommentBodies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)
	pr := domain.PullRequest{URL: "https://github.com/o/r/pull/2", SessionID: r.ID, Number: 2, UpdatedAt: now}

	if err := s.WritePR(ctx, pr, nil, []domain.PullRequestComment{{ID: "legacy", Author: "reviewer", Body: "before", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePR(ctx, pr, nil, []domain.PullRequestComment{{ID: "legacy", Author: "reviewer", Body: "after edit", CreatedAt: now.Add(time.Second)}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListPRComments(ctx, pr.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "after edit" || !got[0].CreatedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("legacy comment replacement did not persist edited row: %+v", got)
	}
}

func TestCDCTriggersPopulateChangeLog(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")

	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	// a real state change logs; a metadata-only change does not (WHEN guard).
	r.Activity.State = domain.ActivityIdle
	_ = s.UpdateSession(ctx, r)
	r.Metadata.Prompt = "only metadata changed"
	_ = s.UpdateSession(ctx, r)
	// a PR insert logs too.
	_ = s.WritePR(ctx, domain.PullRequest{URL: "pr1", SessionID: r.ID, UpdatedAt: r.UpdatedAt}, nil, nil)

	evs, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		if e.ProjectID != "mer" {
			t.Fatalf("event project = %s, want mer", e.ProjectID)
		}
		types = append(types, string(e.Type))
	}
	want := []string{"session_created", "session_updated", "pr_created"}
	if len(types) != 3 || types[0] != want[0] || types[1] != want[1] || types[2] != want[2] {
		t.Fatalf("change_log event types = %v, want %v (metadata-only update suppressed)", types, want)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(evs[0].Payload), &payload); err != nil {
		t.Fatalf("session payload JSON: %v", err)
	}
	if _, ok := payload["isTerminated"].(bool); !ok {
		t.Fatalf("isTerminated payload type = %T, want bool", payload["isTerminated"])
	}
	maxSeq, _ := s.LatestSeq(ctx)
	if maxSeq != int64(len(evs)) {
		t.Fatalf("max seq = %d, want %d", maxSeq, len(evs))
	}
}

func TestSetSessionPreviewURLBumpsRevisionAndFiresCDCOnSameURL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	base, _ := s.LatestSeq(ctx)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		ok, err := s.SetSessionPreviewURL(ctx, r.ID, "http://localhost:5173/", now.Add(time.Duration(i)*time.Second))
		if err != nil || !ok {
			t.Fatalf("set preview url (call %d): ok=%v err=%v", i, ok, err)
		}
	}

	got, found, err := s.GetSession(ctx, r.ID)
	if err != nil || !found {
		t.Fatalf("get session: found=%v err=%v", found, err)
	}
	if got.Metadata.PreviewURL != "http://localhost:5173/" {
		t.Fatalf("preview url = %q, want persisted target", got.Metadata.PreviewURL)
	}
	if got.Metadata.PreviewRevision != 2 {
		t.Fatalf("preview revision = %d, want 2 after two sets", got.Metadata.PreviewRevision)
	}

	// Both sets fire session_updated even though the URL never changed — the
	// revision bump is what trips the trigger, so a same-URL `ao preview` re-run
	// still reaches the browser panel.
	evs, err := s.EventsAfter(ctx, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, e := range evs {
		if string(e.Type) == "session_updated" {
			updates++
		}
	}
	if updates != 2 {
		t.Fatalf("session_updated events = %d, want 2 (one per same-URL set)", updates)
	}
}

func TestRenameSessionFiresCDCEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	base, _ := s.LatestSeq(ctx)
	renamedAt := r.UpdatedAt.Add(time.Minute)
	if ok, err := s.RenameSession(ctx, r.ID, "Fix flaky tests", renamedAt); err != nil || !ok {
		t.Fatalf("rename: ok=%v err=%v", ok, err)
	}

	// A rename must reach the live SSE stream: display_name is in the
	// sessions_cdc_update WHEN guard, so the rename writes exactly one
	// session_updated row. The event is an invalidation-only nudge (renderer
	// consumers refetch the workspace and read the new name from durable
	// state), so we assert the fan-out fired, not the payload contents — the
	// payload carries the shared {id,activity,isTerminated,preview*} shape
	// common to every session_updated emitter.
	evs, err := s.EventsAfter(ctx, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	var payloads []json.RawMessage
	for _, e := range evs {
		if string(e.Type) == "session_updated" {
			payloads = append(payloads, e.Payload)
		}
	}
	if len(payloads) != 1 {
		t.Fatalf("session_updated events = %d, want 1 after rename", len(payloads))
	}
	var payload map[string]any
	if err := json.Unmarshal(payloads[0], &payload); err != nil {
		t.Fatalf("session_updated payload JSON: %v", err)
	}
	if payload["id"] != string(r.ID) {
		t.Fatalf("payload id = %v, want %q", payload["id"], r.ID)
	}
	if _, carried := payload["displayName"]; carried {
		t.Fatalf("session_updated must stay invalidation-only; payload should not carry displayName: %v", payload)
	}
}

func TestSessionPinFiresCDCEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))

	base, _ := s.LatestSeq(ctx)
	pinnedAt := r.UpdatedAt.Add(time.Minute)
	if ok, err := s.SetSessionPinned(ctx, r.ID, true, &pinnedAt, pinnedAt); err != nil || !ok {
		t.Fatalf("pin: ok=%v err=%v", ok, err)
	}

	evs, err := s.EventsAfter(ctx, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	var payloads []json.RawMessage
	for _, e := range evs {
		if string(e.Type) == "session_updated" {
			payloads = append(payloads, e.Payload)
		}
	}
	if len(payloads) != 1 {
		t.Fatalf("session_updated events = %d, want 1 after pin", len(payloads))
	}
	var payload map[string]any
	if err := json.Unmarshal(payloads[0], &payload); err != nil {
		t.Fatalf("session_updated payload JSON: %v", err)
	}
	if payload["id"] != string(r.ID) {
		t.Fatalf("payload id = %v, want %q", payload["id"], r.ID)
	}
	if isPinned, ok := payload["isPinned"].(bool); !ok || !isPinned {
		t.Fatalf("payload isPinned = %v, want true", payload["isPinned"])
	}
}

func TestConcurrentSessionCreateAssignsUniqueNums(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")

	const n = 20
	var wg sync.WaitGroup
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.CreateSession(ctx, sampleRecord("mer"))
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			ids[i] = string(r.ID)
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("duplicate or empty id: %q in %v", id, ids)
		}
		seen[id] = true
	}
	if all, _ := s.ListAllSessions(ctx); len(all) != n {
		t.Fatalf("created %d sessions, want %d", len(all), n)
	}
}

func TestSessionWorktreesRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "ws")
	rec, err := s.CreateSession(ctx, sampleRecord("ws"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	rows := []domain.SessionWorktreeRecord{
		{SessionID: rec.ID, RepoName: domain.RootWorkspaceRepoName, Branch: "ao/ws-1", BaseSHA: "root-base", BaseRef: "refs/remotes/origin/trunk", WorktreePath: "/managed/ws/ws-1", State: "active"},
		{SessionID: rec.ID, RepoName: "api", Branch: "ao/ws-1", BaseSHA: "api-base", BaseRef: "refs/remotes/origin/dev", WorktreePath: "/managed/ws/ws-1/api", PreservedRef: "refs/ao/preserved/ws-1", State: "removed"},
	}
	for _, row := range rows {
		if err := s.UpsertSessionWorktree(ctx, row); err != nil {
			t.Fatalf("upsert worktree %s: %v", row.RepoName, err)
		}
	}
	got, err := s.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Fatalf("worktrees = %#v, want %#v", got, rows)
	}
	one, ok, err := s.GetSessionWorktree(ctx, rec.ID, "api")
	if err != nil || !ok || one.PreservedRef != "refs/ao/preserved/ws-1" {
		t.Fatalf("get api = %#v ok=%v err=%v", one, ok, err)
	}
	rows[1].State = "active"
	rows[1].PreservedRef = ""
	if err := s.UpsertSessionWorktree(ctx, rows[1]); err != nil {
		t.Fatalf("update api worktree: %v", err)
	}
	one, ok, err = s.GetSessionWorktree(ctx, rec.ID, "api")
	if err != nil || !ok || one.State != "active" || one.PreservedRef != "" {
		t.Fatalf("updated api = %#v ok=%v err=%v", one, ok, err)
	}
	if err := s.DeleteSessionWorktrees(ctx, rec.ID); err != nil {
		t.Fatalf("delete worktrees: %v", err)
	}
	got, err = s.ListSessionWorktrees(ctx, rec.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("after delete = %#v err=%v", got, err)
	}
}

// TestUpsertSessionWorktreeEmptyStateDefaultsToActive exercises the guard in
// UpsertSessionWorktree: when State is left at its zero value "", the store
// must default it to "active" so the SQLite CHECK constraint is satisfied.
// Without the guard, the generated upsert would insert "" and the CHECK would
// reject it. This test catches any regression that removes that guard.
func TestUpsertSessionWorktreeEmptyStateDefaultsToActive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "sw")
	rec, err := s.CreateSession(ctx, sampleRecord("sw"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// State is intentionally left at zero value "" to exercise the guard.
	row := domain.SessionWorktreeRecord{
		SessionID:    rec.ID,
		RepoName:     domain.RootWorkspaceRepoName,
		Branch:       "ao/sw-1",
		BaseSHA:      "abc123",
		WorktreePath: "/managed/sw/sw-1",
	}
	if err := s.UpsertSessionWorktree(ctx, row); err != nil {
		t.Fatalf("upsert with empty State: %v", err)
	}

	got, ok, err := s.GetSessionWorktree(ctx, rec.ID, domain.RootWorkspaceRepoName)
	if err != nil {
		t.Fatalf("get worktree: %v", err)
	}
	if !ok {
		t.Fatal("worktree row not found after upsert")
	}
	if got.State != "active" {
		t.Fatalf("State = %q, want %q", got.State, "active")
	}
}
