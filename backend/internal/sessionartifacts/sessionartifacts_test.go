package sessionartifacts

import (
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDir_JoinsDataDirArtifactsAndID(t *testing.T) {
	got := Dir("/data", domain.SessionID("mer-1"))
	want := filepath.Join("/data", "artifacts", "mer-1")
	if got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestDir_EmptyDataDirReturnsEmpty(t *testing.T) {
	if got := Dir("", domain.SessionID("mer-1")); got != "" {
		t.Fatalf("Dir with empty dataDir = %q, want empty", got)
	}
	if got := Dir("   ", domain.SessionID("mer-1")); got != "" {
		t.Fatalf("Dir with whitespace-only dataDir = %q, want empty", got)
	}
}
