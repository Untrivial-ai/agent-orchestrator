package store_test

import (
	"context"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"os"
	"path/filepath"
	"testing"
)

func TestImportedLookupBoundsAndSourceRoots(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "imports")
	root := t.TempDir()
	for _, other := range []bool{true, false} {
		r := sampleRecord("imports")
		r.Harness = domain.HarnessClaudeCode
		r.Metadata.ProviderConversationID = "native"
		r.Metadata.NativeTranscriptPath = filepath.Join(root, "projects", "repo", "native.jsonl")
		if other {
			r.Metadata.NativeTranscriptPath = filepath.Join(root, "nested", "projects", "repo", "native.jsonl")
		}
		if _, err := s.CreateSession(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.FindImportedSessions(ctx, []ports.ImportIdentity{{Provider: domain.HarnessClaudeCode, NativeSessionID: "native", ConfigDir: root}})
	if err != nil || len(rows) != 1 || rows[0].ID != "imports-2" {
		t.Fatal(rows, err)
	}
	if _, err = s.FindImportedSessions(ctx, make([]ports.ImportIdentity, 101)); err == nil {
		t.Fatal("unbounded lookup accepted")
	}
}

func TestImportedLookupRootAliasWithMissingTranscript(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "alias")
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "provider")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	r := sampleRecord("alias")
	r.Harness = domain.HarnessClaudeCode
	r.Metadata.ProviderConversationID = "native"
	r.Metadata.NativeTranscriptPath = filepath.Join(alias, "projects", "deleted", "native.jsonl")
	if _, err := s.CreateSession(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err := s.FindImportedSessions(ctx, []ports.ImportIdentity{{Provider: domain.HarnessClaudeCode, NativeSessionID: "native", ConfigDir: root}})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	rows, err = s.FindImportedSessions(ctx, []ports.ImportIdentity{{Provider: domain.HarnessClaudeCode, NativeSessionID: "native", ConfigDir: t.TempDir()}})
	if err != nil || len(rows) != 0 {
		t.Fatal("distinct root matched", rows, err)
	}
}
