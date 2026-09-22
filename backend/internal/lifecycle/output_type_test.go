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

func TestReconcileSessionOutputType_PRRowOutranksArtifactFiles(t *testing.T) {
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
	if got := st.sessions["mer-1"].OutputType; got != domain.SessionOutputPR {
		t.Fatalf("outputType = %q, want %q", got, domain.SessionOutputPR)
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
