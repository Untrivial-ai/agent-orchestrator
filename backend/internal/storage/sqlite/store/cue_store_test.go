package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func sampleCue(id, project, name string, typ domain.CueType) domain.Cue {
	now := time.Now().UTC().Truncate(time.Second)
	cue := domain.Cue{
		ID:        domain.CueID(id),
		ProjectID: domain.ProjectID(project),
		Name:      name,
		Type:      typ,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if typ == domain.CueTypeCommand {
		cue.Command = "pnpm test"
		cue.Description = "Run the test suite"
	} else {
		cue.Prompt = "Run the test suite, investigate failures, and fix them."
	}
	return cue
}

// TestCueInsertAndSelectRoundTrip pins command and agent cue round-trips: every
// field survives the SQLite write, including the typed enums and ids.
func TestCueInsertAndSelectRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")

	for _, cue := range []domain.Cue{
		sampleCue("cue-1", "mer", "Test", domain.CueTypeCommand),
		sampleCue("cue-2", "mer", "Fix Tests", domain.CueTypeAgent),
	} {
		if err := s.InsertCue(ctx, cue); err != nil {
			t.Fatalf("insert %s: %v", cue.Name, err)
		}
		got, ok, err := s.SelectCueByID(ctx, cue.ID)
		if err != nil || !ok {
			t.Fatalf("select %s: ok=%v err=%v", cue.ID, ok, err)
		}
		if got.ID != cue.ID || got.ProjectID != cue.ProjectID || got.Name != cue.Name ||
			got.Description != cue.Description || got.Type != cue.Type ||
			got.Command != cue.Command || got.Prompt != cue.Prompt ||
			!got.CreatedAt.Equal(cue.CreatedAt) || !got.UpdatedAt.Equal(cue.UpdatedAt) {
			t.Fatalf("round-trip mismatch: got %+v want %+v", got, cue)
		}
	}
}

// TestCueListByProject returns cues in name order and only for the requested
// project.
func TestCueListByProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	seedProject(t, s, "ao")

	if err := s.InsertCue(ctx, sampleCue("cue-1", "mer", "Zebra", domain.CueTypeCommand)); err != nil {
		t.Fatalf("insert zebra: %v", err)
	}
	if err := s.InsertCue(ctx, sampleCue("cue-2", "mer", "Alpha", domain.CueTypeAgent)); err != nil {
		t.Fatalf("insert alpha: %v", err)
	}
	if err := s.InsertCue(ctx, sampleCue("cue-3", "ao", "Other", domain.CueTypeCommand)); err != nil {
		t.Fatalf("insert other: %v", err)
	}

	listed, err := s.SelectCuesByProject(ctx, "mer")
	if err != nil {
		t.Fatalf("list mer: %v", err)
	}
	if len(listed) != 2 || listed[0].Name != "Alpha" || listed[1].Name != "Zebra" {
		t.Fatalf("mer cues = %+v, want Alpha then Zebra in name order", listed)
	}

	empty, err := s.SelectCuesByProject(ctx, "unseen")
	if err != nil || len(empty) != 0 {
		t.Fatalf("unseen project cues = %+v, err=%v; want empty", empty, err)
	}
}

// TestCueDuplicateNameFailsInProject pins unique (project_id, name) enforcement:
// the same name may not repeat within a project but may be reused across projects.
func TestCueDuplicateNameFailsInProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	seedProject(t, s, "ao")

	if err := s.InsertCue(ctx, sampleCue("cue-1", "mer", "Test", domain.CueTypeCommand)); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := s.InsertCue(ctx, sampleCue("cue-2", "mer", "Test", domain.CueTypeAgent))
	if !errors.Is(err, domain.ErrCueNameExists) {
		t.Fatalf("duplicate insert err = %v, want %v", err, domain.ErrCueNameExists)
	}

	if err := s.InsertCue(ctx, sampleCue("cue-3", "ao", "Test", domain.CueTypeCommand)); err != nil {
		t.Fatalf("same name in another project err = %v, want nil", err)
	}
}

// TestCueUpdateReplacesDefinition pins that update overwrites the whole
// definition (type switch included) and reports missing rows.
func TestCueUpdateReplacesDefinition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	if err := s.InsertCue(ctx, sampleCue("cue-1", "mer", "Test", domain.CueTypeCommand)); err != nil {
		t.Fatalf("insert: %v", err)
	}

	updated := sampleCue("cue-1", "mer", "Run Tests", domain.CueTypeAgent)
	updated.Prompt = "Run tests in watch mode."
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Minute)

	got, ok, err := s.UpdateCue(ctx, updated)
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if got.Name != "Run Tests" || got.Type != domain.CueTypeAgent ||
		got.Command != "" || got.Prompt != "Run tests in watch mode." ||
		!got.UpdatedAt.Equal(updated.UpdatedAt) || !got.CreatedAt.Equal(updated.CreatedAt) {
		t.Fatalf("updated cue = %+v", got)
	}

	if missing, ok, err := s.UpdateCue(ctx, sampleCue("cue-missing", "mer", "Missing", domain.CueTypeCommand)); err != nil || ok {
		t.Fatalf("update missing: ok=%v err=%v", ok, err)
	} else if missing.ID != "" {
		t.Fatalf("update missing returned row %+v", missing)
	}
}

// TestCueUpdateDuplicateNameFails pins rename collisions surfacing as
// domain.ErrCueNameExists.
func TestCueUpdateDuplicateNameFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	if err := s.InsertCue(ctx, sampleCue("cue-1", "mer", "Test", domain.CueTypeCommand)); err != nil {
		t.Fatalf("insert test: %v", err)
	}
	if err := s.InsertCue(ctx, sampleCue("cue-2", "mer", "Ship", domain.CueTypeAgent)); err != nil {
		t.Fatalf("insert ship: %v", err)
	}

	renamed := sampleCue("cue-2", "mer", "Test", domain.CueTypeAgent)
	_, _, err := s.UpdateCue(ctx, renamed)
	if !errors.Is(err, domain.ErrCueNameExists) {
		t.Fatalf("rename collision err = %v, want %v", err, domain.ErrCueNameExists)
	}
}

// TestCueDelete pins delete-returns-existed semantics.
func TestCueDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	if err := s.InsertCue(ctx, sampleCue("cue-1", "mer", "Test", domain.CueTypeCommand)); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if ok, err := s.DeleteCueByID(ctx, "cue-missing"); err != nil || ok {
		t.Fatalf("delete missing: ok=%v err=%v", ok, err)
	}
	if ok, err := s.DeleteCueByID(ctx, "cue-1"); err != nil || !ok {
		t.Fatalf("delete existing: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.SelectCueByID(ctx, "cue-1"); err != nil || ok {
		t.Fatalf("deleted cue resolves: ok=%v err=%v", ok, err)
	}
}

// TestCueRejectsUnknownProject pins the migration's project FK: a cue can never
// outlive or precede its project.
func TestCueRejectsUnknownProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.InsertCue(ctx, sampleCue("cue-1", "ghost", "Test", domain.CueTypeCommand))
	if !errors.Is(err, domain.ErrProjectUnknown) {
		t.Fatalf("insert for unknown project err = %v, want %v", err, domain.ErrProjectUnknown)
	}
}

// TestCueRejectsUnknownType pins the migration's CHECK constraint: an
// unsupported cue type never reaches storage.
func TestCueRejectsUnknownType(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")

	cue := sampleCue("cue-1", "mer", "Test", domain.CueTypeCommand)
	cue.Type = domain.CueType("prompt")
	if err := s.InsertCue(ctx, cue); err == nil {
		t.Fatal("insert invalid cue type succeeded, want constraint error")
	}
}
