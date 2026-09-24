package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestApplyPRObservation_PersistsPROutputType(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = working("mer-1")
	st.prs["mer-1"] = []domain.PullRequest{{URL: "pr1"}}

	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{Fetched: true, URL: "pr1"}); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"].OutputType; got != domain.SessionOutputPR {
		t.Fatalf("outputType = %q, want %q", got, domain.SessionOutputPR)
	}
}

func TestReconcileSessionOutputType_ArtifactFilesPersistArtifactOutput(t *testing.T) {
	m, st, _ := newManager()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:       "mer-1",
		Metadata: domain.SessionMetadata{ArtifactDir: dir},
	}

	if err := m.ReconcileSessionOutputType(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"].OutputType; got != domain.SessionOutputArtifact {
		t.Fatalf("outputType = %q, want %q", got, domain.SessionOutputArtifact)
	}
}

func TestReconcileSessionOutputType_PRAndArtifactFilesCombine(t *testing.T) {
	m, st, _ := newManager()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:       "mer-1",
		Metadata: domain.SessionMetadata{ArtifactDir: dir},
	}
	st.prs["mer-1"] = []domain.PullRequest{{URL: "https://example.com/pr/1"}}

	if err := m.ReconcileSessionOutputType(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"].OutputType; got != domain.SessionOutputPRAndArtifact {
		t.Fatalf("outputType = %q, want %q", got, domain.SessionOutputPRAndArtifact)
	}
}

// TestReconcileSessionOutputType_BackfillsEmptyArtifactDir covers the
// critical-risk gap flagged in review: a session row created before
// artifact_dir existed carries it as ” (migration 0155's default), even
// though session_manager always prompts the agent to write into the
// deterministic dataDir/artifacts/<id> path regardless of what is stored.
// The very first reconcile after upgrade must derive and persist that path
// so the session stops silently under-reporting artifacts it actually has.
func TestReconcileSessionOutputType_BackfillsEmptyArtifactDir(t *testing.T) {
	dataDir := t.TempDir()
	m, st, _ := newManager(WithDataDir(dataDir))
	artifactDir := filepath.Join(dataDir, "artifacts", "mer-1")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "report.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:       "mer-1",
		Metadata: domain.SessionMetadata{ArtifactDir: ""},
	}

	if err := m.ReconcileSessionOutputType(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.Metadata.ArtifactDir != artifactDir {
		t.Fatalf("artifactDir = %q, want backfilled %q", got.Metadata.ArtifactDir, artifactDir)
	}
	if got.OutputType != domain.SessionOutputArtifact {
		t.Fatalf("outputType = %q, want %q", got.OutputType, domain.SessionOutputArtifact)
	}
}

// TestReconcileSessionOutputType_NoOpWithoutDataDirConfigured covers a nil
// WithDataDir wiring (e.g. a test manager built without it): the backfill
// must not panic or write a bogus empty-dataDir path, it must simply leave
// ArtifactDir empty and behave as before.
func TestReconcileSessionOutputType_NoOpWithoutDataDirConfigured(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:         "mer-1",
		OutputType: domain.SessionOutputNone,
	}

	if err := m.ReconcileSessionOutputType(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	got := st.sessions["mer-1"]
	if got.Metadata.ArtifactDir != "" {
		t.Fatalf("artifactDir = %q, want still empty without a configured dataDir", got.Metadata.ArtifactDir)
	}
}

func TestReconcileSessionOutputType_NoOpWhenUnchanged(t *testing.T) {
	m, st, _ := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:         "mer-1",
		OutputType: domain.SessionOutputNone,
	}

	if err := m.ReconcileSessionOutputType(ctx, "mer-1"); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"].OutputType; got != domain.SessionOutputNone {
		t.Fatalf("outputType = %q, want %q", got, domain.SessionOutputNone)
	}
}

func TestReconcileSessionOutputType_UnknownSessionIsNoOp(t *testing.T) {
	m, _, _ := newManager()
	if err := m.ReconcileSessionOutputType(ctx, "missing"); err != nil {
		t.Fatal(err)
	}
}
